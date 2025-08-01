package driver

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/corona10/goimagehash"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sensepost/gowitness/internal/islazy"
	"github.com/sensepost/gowitness/pkg/log"
	"github.com/sensepost/gowitness/pkg/models"
	"github.com/sensepost/gowitness/pkg/runner"
	"github.com/ysmood/gson"
)

// Gorod 是使用 go-rod 探测 Web 目标的驱动程序
type Gorod struct {
	// browser 是 go-rod 浏览器实例
	browser *rod.Browser
	// 用户数据目录
	userData string
	// Runner 需要考虑的选项
	options runner.Options
	// 日志记录器
	log *slog.Logger
}

// NewGorod 创建一个准备进行探测的新 Runner。
// 调用者负责在实例上调用 Close()。
func NewGorod(logger *slog.Logger, opts runner.Options) (*Gorod, error) {
	var (
		url      string
		userData string
		err      error
	)

	if opts.Chrome.WSS == "" {
		userData, err = os.MkdirTemp("", "gowitness-v3-gorod-*")
		if err != nil {
			return nil, err
		}

		// 准备 chrome
		chrmLauncher := launcher.New().
			// https://github.com/GoogleChrome/chrome-launcher/blob/main/docs/chrome-flags-for-tools.md
			Set("user-data-dir", userData).
			Set("disable-features", "MediaRouter").
			Set("disable-client-side-phishing-detection").
			Set("explicitly-allowed-ports", restrictedPorts()).
			Set("disable-default-apps").
			Set("hide-scrollbars").
			Set("mute-audio").
			Set("no-default-browser-check").
			Set("no-first-run").
			Set("deny-permission-prompts")

		log.Debug("go-rod chrome args", "args", chrmLauncher.FormatArgs())

		// 用户指定的 Chrome
		if opts.Chrome.Path != "" {
			chrmLauncher.Bin(opts.Chrome.Path)
		}

		// 代理
		if opts.Chrome.Proxy != "" {
			chrmLauncher.Proxy(opts.Chrome.Proxy)
		}

		url, err = chrmLauncher.Launch()
		if err != nil {
			return nil, err
		}
		logger.Debug("got a browser up", "control-url", url)
	} else {
		url = opts.Chrome.WSS
		logger.Debug("using a user specified WSS url", "control-url", url)
	}

	// 连接到控制 URL
	browser := rod.New().ControlURL(url)
	if err := browser.Connect(); err != nil {
		return nil, err
	}

	// 忽略证书错误
	if err := browser.IgnoreCertErrors(true); err != nil {
		return nil, err
	}

	return &Gorod{
		browser:  browser,
		userData: userData,
		options:  opts,
		log:      logger,
	}, nil
}

// witness 执行探测 URL 的工作。
// 就 runner 而言，这是所有工作汇聚的地方。
func (run *Gorod) Witness(target string, runner *runner.Runner) (*models.Result, error) {
	logger := run.log.With("target", target)
	logger.Debug("witnessing 👀")

	page, err := run.browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("could not get a page: %w", err)
	}
	defer page.Close()

	// 配置视口大小
	if run.options.Chrome.WindowX > 0 && run.options.Chrome.WindowY > 0 {
		if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width:  run.options.Chrome.WindowX,
			Height: run.options.Chrome.WindowY,
		}); err != nil {
			return nil, fmt.Errorf("unable to set viewport: %w", err)
		}
	}

	// 配置超时
	duration := time.Duration(run.options.Scan.Timeout) * time.Second
	page = page.Timeout(duration)

	// 设置用户代理
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: run.options.Chrome.UserAgent,
	}); err != nil {
		return nil, fmt.Errorf("unable to set user-agent string: %w", err)
	}

	// 设置额外的头部（如果有）
	if len(run.options.Chrome.Headers) > 0 {
		var headers []string
		for _, header := range run.options.Chrome.Headers {
			kv := strings.SplitN(header, ":", 2)
			if len(kv) != 2 {
				logger.Warn("custom header did not parse correctly", "header", header)
				continue
			}

			headers = append(headers, strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1]))
		}
		_, err := page.SetExtraHeaders(headers)
		if err != nil {
			return nil, fmt.Errorf("could not set extra headers for page: %s", err)
		}
	}

	// 使用页面事件来获取有关目标的信息。这是我们
	// 了解第一个请求结果的方式，以便将其保存为
	// 输出写入器的整体 URL 结果。
	var (
		first  *proto.NetworkRequestWillBeSent
		result = &models.Result{
			URL:      target,
			ProbedAt: time.Now(),
		}
		resultMutex   = sync.Mutex{}
		netlog        = make(map[string]models.NetworkLog)
		dismissEvents = false // 设置为 true 以停止 EachEvent 回调
	)

	go page.EachEvent(
		// 关闭任何 JavaScript 对话框
		func(e *proto.PageJavascriptDialogOpening) bool {
			_ = proto.PageHandleJavaScriptDialog{Accept: true}.Call(page)
			return dismissEvents
		},

		// 记录 console.* 调用
		func(e *proto.RuntimeConsoleAPICalled) bool {
			v := ""
			for _, arg := range e.Args {
				if !arg.Value.Nil() {
					v += arg.Value.String()
				}
			}

			if v == "" {
				return dismissEvents
			}

			resultMutex.Lock()
			result.Console = append(result.Console, models.ConsoleLog{
				Type:  "console." + string(e.Type),
				Value: strings.TrimSpace(v),
			})
			resultMutex.Unlock()

			return dismissEvents
		},

		// 网络相关事件
		// 将请求写入网络请求映射
		func(e *proto.NetworkRequestWillBeSent) bool {
			// 记录第一个请求的请求 ID。我们稍后会回到
			// 这里来提取有关探测的信息。
			if first == nil {
				first = e
			}

			// 记录新请求
			netlog[string(e.RequestID)] = models.NetworkLog{
				Time:        e.WallTime.Time(),
				RequestType: models.HTTP,
				URL:         e.Request.URL,
			}

			return dismissEvents
		},

		// 将响应写入网络请求映射
		func(e *proto.NetworkResponseReceived) bool {
			// 获取现有的 requestid，并添加响应信息
			if entry, ok := netlog[string(e.RequestID)]; ok {
				// 更新第一个请求的详情（头部、TLS 等）
				if first != nil && first.RequestID == e.RequestID {
					resultMutex.Lock()
					result.FinalURL = e.Response.URL
					result.ResponseCode = e.Response.Status
					result.ResponseReason = e.Response.StatusText
					result.Protocol = e.Response.Protocol
					result.ContentLength = int64(e.Response.EncodedDataLength)

					// 写入头部
					for k, v := range e.Response.Headers {
						result.Headers = append(result.Headers, models.Header{
							Key:   k,
							Value: v.Str(),
						})
					}

					// 获取可用的安全详情
					if e.Response.SecurityDetails != nil {
						var sanlist []models.TLSSanList
						for _, san := range e.Response.SecurityDetails.SanList {
							sanlist = append(sanlist, models.TLSSanList{
								Value: san,
							})
						}

						result.TLS = models.TLS{
							Protocol:                 e.Response.SecurityDetails.Protocol,
							KeyExchange:              e.Response.SecurityDetails.KeyExchange,
							Cipher:                   e.Response.SecurityDetails.Cipher,
							SubjectName:              e.Response.SecurityDetails.SubjectName,
							SanList:                  sanlist,
							Issuer:                   e.Response.SecurityDetails.Issuer,
							ValidFrom:                islazy.Float64ToTime(float64(e.Response.SecurityDetails.ValidFrom)),
							ValidTo:                  islazy.Float64ToTime(float64(e.Response.SecurityDetails.ValidTo)),
							ServerSignatureAlgorithm: int64(*e.Response.SecurityDetails.ServerSignatureAlgorithm),
							EncryptedClientHello:     e.Response.SecurityDetails.EncryptedClientHello,
						}
					}
					resultMutex.Unlock()
				}

				entry.StatusCode = int64(e.Response.Status)
				entry.URL = e.Response.URL
				entry.RemoteIP = e.Response.RemoteIPAddress
				entry.MIMEType = e.Response.MIMEType
				entry.Time = e.Response.ResponseTime.Time()

				// 写入网络日志
				resultMutex.Lock()
				entryIndex := len(result.Network)
				result.Network = append(result.Network, entry)
				resultMutex.Unlock()

				// 如果我们需要写入响应体，就这样做
				if run.options.Scan.SaveContent {
					go func(index int) {
						body, err := proto.NetworkGetResponseBody{RequestID: e.RequestID}.Call(page)
						if err != nil {
							if run.options.Logging.LogScanErrors {
								if run.options.Logging.LogScanErrors {
									run.log.Error("could not get network request response body", "url", e.Response.URL, "err", err)
								}
								return
							}
						}

						resultMutex.Lock()
						result.Network[index].Content = []byte(body.Body)
						resultMutex.Unlock()
					}(entryIndex)
				}
			}

			return dismissEvents
		},

		// 将请求标记为失败
		func(e *proto.NetworkLoadingFailed) bool {
			// 获取现有的 requestid 并添加失败信息
			if entry, ok := netlog[string(e.RequestID)]; ok {
				resultMutex.Lock()

				// 更新第一个请求的详情
				if first != nil && first.RequestID == e.RequestID {
					result.Failed = true
					result.FailedReason = e.ErrorText
				} else {
					entry.Error = e.ErrorText

					// 写入网络日志
					result.Network = append(result.Network, entry)
				}

				resultMutex.Unlock()
			}

			return dismissEvents
		},

		// TODO: wss
	)()

	// 最后，导航到目标
	if err := page.Navigate(target); err != nil {
		return nil, fmt.Errorf("could not navigate to target: %s", err)
	}

	// 等待配置的延追
	if run.options.Scan.Delay > 0 {
		time.Sleep(time.Duration(run.options.Scan.Delay) * time.Second)
	}

	// 运行我们有的任何 JavaScript
	if run.options.Scan.JavaScript != "" {
		_, err := page.Eval(run.options.Scan.JavaScript)
		if err != nil {
			logger.Warn("failed to evaluate user-provided javascript", "err", err)
		}
	}

	// 获取 cookies
	cookies, err := page.Cookies([]string{})
	if err != nil {
		if run.options.Logging.LogScanErrors {
			logger.Error("could not get cookies", "err", err)
		}
	} else {
		for _, cookie := range cookies {
			result.Cookies = append(result.Cookies, models.Cookie{
				Name:         cookie.Name,
				Value:        cookie.Value,
				Domain:       cookie.Domain,
				Path:         cookie.Path,
				Expires:      cookie.Expires.Time(),
				Size:         int64(cookie.Size),
				HTTPOnly:     cookie.HTTPOnly,
				Secure:       cookie.Secure,
				Session:      cookie.Session,
				Priority:     string(cookie.Priority),
				SourceScheme: string(cookie.SourceScheme),
				SourcePort:   int64(cookie.SourcePort),
			})
		}
	}

	// 在触发之前获取并设置最后的结果信息
	info, err := page.Info()
	if err != nil {
		if run.options.Logging.LogScanErrors {
			logger.Error("could not get page info", "err", err)
		}
	} else {
		result.Title = info.Title
	}

	if !run.options.Scan.SkipHTML {
		html, err := page.HTML()
		if err != nil {
			if run.options.Logging.LogScanErrors {
				logger.Error("could not get page html", "err", err)
			}
		} else {
			result.HTML = html
		}
	}

	// 停止事件处理程序
	dismissEvents = true

	// 在第一个响应中识别技术指纹
	if fingerprints := runner.Wappalyzer.Fingerprint(result.HeaderMap(), []byte(result.HTML)); fingerprints != nil {
		for tech := range fingerprints {
			result.Technologies = append(result.Technologies, models.Technology{
				Value: tech,
			})
		}
	}

	// 进行截图。能到这里通常意味着页面已响应且我们有
	// 一些信息。但有时，我不确定为什么，page.Screenshot()
	// 会因为超时而失败。在这种情况下，至少记录我们所拥有的，但将
	// 截图标记为失败。这样我们至少不会失去所有的工作。
	logger.Debug("taking a screenshot 🔎")
	var screenshotOptions = &proto.PageCaptureScreenshot{}
	switch run.options.Scan.ScreenshotFormat {
	case "jpeg":
		screenshotOptions.Format = proto.PageCaptureScreenshotFormatJpeg
		screenshotOptions.Quality = gson.Int(80)
	case "png":
		screenshotOptions.Format = proto.PageCaptureScreenshotFormatPng
	}

	img, err := page.Screenshot(run.options.Scan.ScreenshotFullPage, screenshotOptions)
	if err != nil {
		if run.options.Logging.LogScanErrors {
			logger.Error("could not grab screenshot", "err", err)
		}

		result.Failed = true
		result.FailedReason = err.Error()
	} else {
		// 给写入器一个截图来处理
		if run.options.Scan.ScreenshotToWriter {
			result.Screenshot = base64.StdEncoding.EncodeToString(img)
		}

		// 如果我们有路径，将截图写入磁盘
		if !run.options.Scan.ScreenshotSkipSave {
			result.Filename = islazy.SafeFileName(target) + "." + run.options.Scan.ScreenshotFormat
			result.Filename = islazy.LeftTrucate(result.Filename, 200)
			if err := os.WriteFile(
				filepath.Join(run.options.Scan.ScreenshotPath, result.Filename),
				img, os.FileMode(0664),
			); err != nil {
				return nil, fmt.Errorf("could not write screenshot to disk: %w", err)
			}
		}

		// 计算并设置感知哈希
		decoded, _, err := image.Decode(bytes.NewReader(img))
		if err != nil {
			return nil, fmt.Errorf("failed to decode screenshot image: %w", err)
		}

		hash, err := goimagehash.PerceptionHash(decoded)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate image perception hash: %w", err)
		}
		result.PerceptionHash = hash.ToString()
	}

	return result, nil
}

// Close 清理 Browser 运行器。调用者需要
// 关闭 Targets 通道
func (run *Gorod) Close() {
	run.log.Debug("closing the browser instance")

	if err := run.browser.Close(); err != nil {
		log.Error("could not close the browser", "err", err)
		return
	}

	// 清理用户数据
	if run.userData != "" {
		// 等待一秒让浏览器进程退出
		time.Sleep(time.Second * 1)

		run.log.Debug("cleaning user data directory", "directory", run.userData)
		if err := os.RemoveAll(run.userData); err != nil {
			run.log.Error("could not cleanup temporary user data dir", "dir", run.userData, "err", err)
		}
	}
}
