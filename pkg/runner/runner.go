package runner

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"sync"

	wappalyzer "github.com/projectdiscovery/wappalyzergo"
	"github.com/sensepost/gowitness/internal/islazy"
	"github.com/sensepost/gowitness/pkg/models"
	"github.com/sensepost/gowitness/pkg/writers"
)

// Runner 是使用驱动程序探测 Web 目标的运行器
type Runner struct {
	Driver     Driver
	Wappalyzer *wappalyzer.Wappalyze

	// Runner 需要考虑的选项配置
	options Options
	// 要使用的结果写入器
	writers []writers.Writer
	// 日志处理器
	log *slog.Logger

	// 要扫描的目标。
	// 这通常由 gowitness/pkg/reader 提供。
	Targets chan string

	// 用于需要退出的情况
	ctx    context.Context
	cancel context.CancelFunc
}

// NewRunner 创建一个新的 Runner 准备进行探测。
// 调用者负责在使用完后调用 Close() 方法
func NewRunner(logger *slog.Logger, driver Driver, opts Options, writers []writers.Writer) (*Runner, error) {
	if !opts.Scan.ScreenshotSkipSave {
		screenshotPath, err := islazy.CreateDir(opts.Scan.ScreenshotPath)
		if err != nil {
			return nil, err
		}
		opts.Scan.ScreenshotPath = screenshotPath
		logger.Debug("final screenshot path", "screenshot-path", opts.Scan.ScreenshotPath)
	} else {
		logger.Debug("not saving screenshots to disk")
	}

	// 截图格式检查
	if !islazy.SliceHasStr([]string{"jpeg", "png"}, opts.Scan.ScreenshotFormat) {
		return nil, errors.New("invalid screenshot format")
	}

	// 包含要在每个页面上执行的 JavaScript 的文件。
	// 直接读取并将值设置到 Scan.JavaScript。
	if opts.Scan.JavaScriptFile != "" {
		javascript, err := os.ReadFile(opts.Scan.JavaScriptFile)
		if err != nil {
			return nil, err
		}

		opts.Scan.JavaScript = string(javascript)
	}

	// 获取 wappalyzer 实例
	wap, err := wappalyzer.New()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Runner{
		Driver:     driver,
		Wappalyzer: wap,
		options:    opts,
		writers:    writers,
		Targets:    make(chan string),
		log:        logger,
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// runWriters 获取结果并将其传递给写入器
func (run *Runner) runWriters(result *models.Result) error {
	for _, writer := range run.writers {
		if err := writer.Write(result); err != nil {
			return err
		}
	}

	return nil
}

// checkUrl 确保 URL 有效
func (run *Runner) checkUrl(target string) error {
	url, err := url.ParseRequestURI(target)
	if err != nil {
		return err
	}

	if !islazy.SliceHasStr(run.options.Scan.UriFilter, url.Scheme) {
		return errors.New("url contains invalid scheme")
	}

	return nil
}

// Run 执行运行器，处理从 Targets 通道接收到的目标
func (run *Runner) Run() {
	wg := sync.WaitGroup{}

	// 将生成 Scan.Threads 数量的 "工作线程" 作为 goroutines
	for w := 0; w < run.options.Scan.Threads; w++ {
		wg.Add(1)

		// 启动一个工作线程
		go func() {
			defer wg.Done()
			for {
				select {
				case <-run.ctx.Done():
					return
				case target, ok := <-run.Targets:
					if !ok {
						return
					}

					// 验证目标
					if err := run.checkUrl(target); err != nil {
						if run.options.Logging.LogScanErrors {
							run.log.Error("invalid target to scan", "target", target, "err", err)
						}
						continue
					}

					result, err := run.Driver.Witness(target, run)
					if err != nil {
						// 这是 Chrome 未找到错误吗？
						var chromeErr *ChromeNotFoundError
						if errors.As(err, &chromeErr) {
							run.log.Error("no valid chrome intallation found", "err", err)
							run.cancel()
							return
						}

						if run.options.Logging.LogScanErrors {
							run.log.Error("failed to witness target", "target", target, "err", err)
						}
						continue
					}

					// 假设状态码 0 表示没有信息，所以
					// 不向写入器发送任何内容。
					if result.ResponseCode == 0 {
						if run.options.Logging.LogScanErrors {
							run.log.Error("failed to witness target, status code was 0", "target", target)
						}
						continue
					}

					if err := run.runWriters(result); err != nil {
						run.log.Error("failed to write result for target", "target", target, "err", err)
					}

					run.log.Info("result 🤖", "target", target, "status-code", result.ResponseCode,
						"title", result.Title, "have-screenshot", !result.Failed)

				}
			}

		}()
	}

	wg.Wait()
}

func (run *Runner) Close() {
	// 关闭驱动
	run.Driver.Close()
}
