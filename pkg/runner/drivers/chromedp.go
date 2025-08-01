package driver

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/corona10/goimagehash"
	"github.com/sensepost/gowitness/internal/islazy"
	"github.com/sensepost/gowitness/pkg/models"
	"github.com/sensepost/gowitness/pkg/runner"
)

// Chromedp 是使用 chromedp 探测 Web 目标的驱动程序
// 实现参考：https://github.com/chromedp/examples/blob/master/multi/main.go
type Chromedp struct {
	// Runner 需要考虑的选项
	options runner.Options
	// 日志记录器
	log *slog.Logger
}

// browserInstance 是 Witness 一次运行使用的实例
type browserInstance struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
	userData    string
}

// Close 关闭分配器，并清理用户目录。
func (b *browserInstance) Close() {
	b.allocCancel()
	<-b.allocCtx.Done()

	// 清理用户数据目录
	os.RemoveAll(b.userData)
}

// getChromedpAllocator 是获取 chrome 分配上下文的辅助函数。
//
// 查看 Witness 以了解为什么我们明确不使用标签页
// （要做到这一点，我们将在 NewChromedp 函数中分配并确保
// 使用 chromedp.Run(browserCtx) 启动浏览器）。
func getChromedpAllocator(opts runner.Options) (*browserInstance, error) {
	var (
		allocCtx    context.Context
		allocCancel context.CancelFunc
		userData    string
		err         error
	)

	if opts.Chrome.WSS == "" {
		userData, err = os.MkdirTemp("", "gowitness-v3-chromedp-*")
		if err != nil {
			return nil, err
		}

		// 设置 chrome 上下文和启动选项
		allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.IgnoreCertErrors,
			chromedp.UserAgent(opts.Chrome.UserAgent),
			chromedp.Flag("headless", false),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-dev-shm-usage", true),
			chromedp.Flag("disable-features", "MediaRouter"),
			chromedp.Flag("mute-audio", true),
			chromedp.Flag("disable-background-timer-throttling", true),
			chromedp.Flag("disable-backgrounding-occluded-windows", true),
			chromedp.Flag("disable-renderer-backgrounding", true),
			chromedp.Flag("deny-permission-prompts", true),
			chromedp.Flag("explicitly-allowed-ports", restrictedPorts()),
			chromedp.WindowSize(opts.Chrome.WindowX, opts.Chrome.WindowY),
			chromedp.UserDataDir(userData),
		)

		// 如果指定了代理，则设置代理
		if opts.Chrome.Proxy != "" {
			allocOpts = append(allocOpts, chromedp.ProxyServer(opts.Chrome.Proxy))
		}

		// 如果提供了特定的 Chrome 二进制文件，则使用它
		if opts.Chrome.Path != "" {
			allocOpts = append(allocOpts, chromedp.ExecPath(opts.Chrome.Path))
		}

		allocCtx, allocCancel = chromedp.NewExecAllocator(context.Background(), allocOpts...)

	} else {
		allocCtx, allocCancel = chromedp.NewRemoteAllocator(context.Background(), opts.Chrome.WSS)
	}

	return &browserInstance{
		allocCtx:    allocCtx,
		allocCancel: allocCancel,
		userData:    userData,
	}, nil
}

// NewChromedp 返回一个新的 Chromedp 实例
func NewChromedp(logger *slog.Logger, opts runner.Options) (*Chromedp, error) {
	return &Chromedp{
		options: opts,
		log:     logger,
	}, nil
}

// witness 执行探测 URL 的工作。
// 就 runner 而言，这是所有工作汇聚的地方。
func (run *Chromedp) Witness(target string, thisRunner *runner.Runner) (*models.Result, error) {
	logger := run.log.With("target", target)
	logger.Debug("witnessing 👀")

	// 这可能看起来很奇怪，但在对大量列表进行截图时，使用
	// 标签页意味着截图失败的几率非常高。可能是
	// 父浏览器进程的资源问题？所以，现在使用这个
	// 驱动程序意味着资源使用量将更高，但你的准确性
	// 也会非常惊人。
	allocator, err := getChromedpAllocator(run.options)
	if err != nil {
		return nil, err
	}
	defer allocator.Close()
	browserCtx, cancel := chromedp.NewContext(allocator.allocCtx)
	defer cancel()

	// 获取一个标签页
	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()

	// 获取用于导航的超时上下文
	navigationCtx, navigationCancel := context.WithTimeout(tabCtx, time.Duration(run.options.Scan.Timeout)*time.Second)
	defer navigationCancel()

	if err := chromedp.Run(navigationCtx, network.Enable()); err != nil {
		// 检查错误是否与 Chrome 未找到相关，如果是，
		// 我们将返回一个特殊的错误类型。
		//
		// 这可能看起来是一个奇怪的地方来做这件事，但请记住
		// 这只是我们第一次真正 *运行* Chrome 的地方。
		var execErr *exec.Error
		if errors.As(err, &execErr) && execErr.Err == exec.ErrNotFound {
			return nil, &runner.ChromeNotFoundError{Err: err}
		}

		return nil, fmt.Errorf("error enabling network tracking: %w", err)
	}

	// 设置额外的头部（如果有）
	if len(run.options.Chrome.Headers) > 0 {
		headers := make(network.Headers)
		for _, header := range run.options.Chrome.Headers {
			kv := strings.SplitN(header, ":", 2)
			if len(kv) != 2 {
				logger.Warn("custom header did not parse correctly", "header", header)
				continue
			}

			headers[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}

		if err := chromedp.Run(navigationCtx, network.SetExtraHTTPHeaders((headers))); err != nil {
			return nil, fmt.Errorf("could not set extra http headers: %w", err)
		}
	}

	// 使用页面事件来获取有关目标的信息。这是我们
	// 了解第一个请求结果的方式，以便将其保存为
	// 输出写入器的整体 URL 结果。
	var (
		result = &models.Result{
			URL:      target,
			ProbedAt: time.Now(),
		}
		resultMutex sync.Mutex
		first       *network.EventRequestWillBeSent
		netlog      = make(map[string]models.NetworkLog)
	)

	go chromedp.ListenTarget(navigationCtx, func(ev interface{}) {
		switch e := ev.(type) {
		// 关闭任何 JavaScript 对话框
		case *page.EventJavascriptDialogOpening:
			if err := chromedp.Run(navigationCtx, page.HandleJavaScriptDialog(true)); err != nil {
				logger.Error("failed to handle a javascript dialog", "err", err)
			}
		// 记录 console.* 调用
		case *runtime.EventConsoleAPICalled:
			v := ""
			for _, arg := range e.Args {
				v += string(arg.Value)
			}

			if v == "" {
				return
			}

			resultMutex.Lock()
			result.Console = append(result.Console, models.ConsoleLog{
				Type:  "console." + string(e.Type),
				Value: strings.TrimSpace(v),
			})
			resultMutex.Unlock()

		// 网络相关事件
		// 将请求写入网络请求映射
		case *network.EventRequestWillBeSent:
			if first == nil {
				first = e
			}
			netlog[string(e.RequestID)] = models.NetworkLog{
				Time:        e.WallTime.Time(),
				RequestType: models.HTTP,
				URL:         e.Request.URL,
			}
		case *network.EventResponseReceived:
			if entry, ok := netlog[string(e.RequestID)]; ok {
				if first != nil && first.RequestID == e.RequestID {
					resultMutex.Lock()
					result.FinalURL = e.Response.URL
					result.ResponseCode = int(e.Response.Status)
					result.ResponseReason = e.Response.StatusText
					result.Protocol = e.Response.Protocol
					result.ContentLength = int64(e.Response.EncodedDataLength)

					// 写入头部
					for k, v := range e.Response.Headers {
						result.Headers = append(result.Headers, models.Header{
							Key:   k,
							Value: v.(string),
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

						// 啊，痛苦。
						var validFromTime, validToTime time.Time
						if e.Response.SecurityDetails.ValidFrom != nil {
							validFromTime = e.Response.SecurityDetails.ValidFrom.Time()
						}
						if e.Response.SecurityDetails.ValidTo != nil {
							validToTime = e.Response.SecurityDetails.ValidTo.Time()
						}

						result.TLS = models.TLS{
							Protocol:                 e.Response.SecurityDetails.Protocol,
							KeyExchange:              e.Response.SecurityDetails.KeyExchange,
							Cipher:                   e.Response.SecurityDetails.Cipher,
							SubjectName:              e.Response.SecurityDetails.SubjectName,
							SanList:                  sanlist,
							Issuer:                   e.Response.SecurityDetails.Issuer,
							ValidFrom:                validFromTime,
							ValidTo:                  validToTime,
							ServerSignatureAlgorithm: e.Response.SecurityDetails.ServerSignatureAlgorithm,
							EncryptedClientHello:     e.Response.SecurityDetails.EncryptedClientHello,
						}
					}
					resultMutex.Unlock()
				}

				entry.StatusCode = e.Response.Status
				entry.URL = e.Response.URL
				entry.RemoteIP = e.Response.RemoteIPAddress
				entry.MIMEType = e.Response.MimeType
				if e.Response.ResponseTime != nil {
					entry.Time = e.Response.ResponseTime.Time()
				}

				// 写入网络日志
				resultMutex.Lock()
				entryIndex := len(result.Network)
				result.Network = append(result.Network, entry)
				resultMutex.Unlock()

				// 如果我们需要写入响应体，就这样做
				// https://github.com/chromedp/chromedp/issues/543
				if run.options.Scan.SaveContent {
					go func(index int) {
						c := chromedp.FromContext(navigationCtx)
						p := network.GetResponseBody(e.RequestID)
						body, err := p.Do(cdp.WithExecutor(navigationCtx, c.Target))
						if err != nil {
							if run.options.Logging.LogScanErrors {
								run.log.Error("could not get network request response body", "url", e.Response.URL, "err", err)
								return
							}
						}

						resultMutex.Lock()
						result.Network[index].Content = body
						resultMutex.Unlock()

					}(entryIndex)
				}
			}
		// 将请求标记为失败
		case *network.EventLoadingFailed:
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
		}

		// TODO: wss
	})

	// 导航到目标
	if err := chromedp.Run(
		navigationCtx, chromedp.Navigate(target),
	); err != nil && err != context.DeadlineExceeded {
		return nil, fmt.Errorf("could not navigate to target: %w", err)
	}

	// 如果有延迟，就等待
	if run.options.Scan.Delay > 0 {
		time.Sleep(time.Duration(run.options.Scan.Delay) * time.Second)
	}

	// 运行我们有的任何 JavaScript
	if run.options.Scan.JavaScript != "" {
		if err := chromedp.Run(navigationCtx, chromedp.Evaluate(run.options.Scan.JavaScript, nil)); err != nil {
			return nil, fmt.Errorf("failed to evaluate user-provided javascript: %w", err)
		}
	}

	// 获取 cookies
	var cookies []*network.Cookie
	if err := chromedp.Run(navigationCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		cookies, err = storage.GetCookies().Do(ctx)
		return err
	})); err != nil {
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
				Expires:      islazy.Float64ToTime(cookie.Expires),
				Size:         cookie.Size,
				HTTPOnly:     cookie.HTTPOnly,
				Secure:       cookie.Secure,
				Session:      cookie.Session,
				Priority:     cookie.Priority.String(),
				SourceScheme: cookie.SourceScheme.String(),
				SourcePort:   cookie.SourcePort,
			})
		}
	}

	// 获取标题
	if err := chromedp.Run(navigationCtx, chromedp.Title(&result.Title)); err != nil {
		if run.options.Logging.LogScanErrors {
			logger.Error("could not get page title", "err", err)
		}
	}

	// 获取 HTML
	if !run.options.Scan.SkipHTML {
		if err := chromedp.Run(navigationCtx, chromedp.OuterHTML(":root", &result.HTML, chromedp.ByQueryAll)); err != nil {
			if run.options.Logging.LogScanErrors {
				logger.Error("could not get page html", "err", err)
			}
		}
	}

	// 在第一个响应中识别技术指纹
	if fingerprints := thisRunner.Wappalyzer.Fingerprint(result.HeaderMap(), []byte(result.HTML)); fingerprints != nil {
		for tech := range fingerprints {
			result.Technologies = append(result.Technologies, models.Technology{
				Value: tech,
			})
		}
	}

	// 获取截图
	var img []byte

	// 如果指定了选择器，截取特定元素
	if run.options.Scan.Selector != "" {
		err = chromedp.Run(navigationCtx,
			// 等待元素可见
			chromedp.WaitVisible(run.options.Scan.Selector, chromedp.ByQuery),
			chromedp.ActionFunc(func(ctx context.Context) error {
				// 获取元素的高度
				var scrollHeight float64
				err := chromedp.Evaluate(fmt.Sprintf(`
					document.querySelector('%s').scrollHeight
				`, run.options.Scan.Selector), &scrollHeight).Do(ctx)
				if err != nil {
					return err
				}

				// 设置视口高度为元素的完整高度（如果需要的话）
				fmt.Println("scrollHeight", scrollHeight)
				fmt.Println("run.options.Chrome.WindowY", run.options.Chrome.WindowY)
				if run.options.Scan.ScreenshotFullPage && scrollHeight > float64(run.options.Chrome.WindowY) {
					fmt.Println("scrollHeight", scrollHeight)
					return emulation.SetDeviceMetricsOverride(
						int64(run.options.Chrome.WindowX),
						int64(scrollHeight),
						1.0,
						false,
					).Do(ctx)
				}

				// 滚动到元素位置
				return chromedp.ScrollIntoView(run.options.Scan.Selector, chromedp.ByQuery).Do(ctx)
			}),
			// 等待一下让页面稳定
			// chromedp.Sleep(1*time.Second),
			// 截取指定元素
			chromedp.Screenshot(run.options.Scan.Selector, &img, chromedp.NodeVisible, chromedp.ByQuery),
		)
	} else {
		// 原来的全页截图逻辑
		err = chromedp.Run(navigationCtx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				var err error
				params := page.CaptureScreenshot().
					WithQuality(80).
					WithFormat(page.CaptureScreenshotFormat(run.options.Scan.ScreenshotFormat))

				// 如果是全页
				if run.options.Scan.ScreenshotFullPage {
					params = params.WithCaptureBeyondViewport(true)
				}

				img, err = params.Do(ctx)
				return err
			}),
		)
	}

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

func (run *Chromedp) Close() {
	run.log.Debug("closing browser allocation context")
}
