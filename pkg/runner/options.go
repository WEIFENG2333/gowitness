package runner

// Options 是 gowitness 的全局选项
type Options struct {
	// Logging 是日志相关选项
	Logging Logging
	// Chrome 是 Chrome 相关选项
	Chrome Chrome
	// Writer 是输出选项
	Writer Writer
	// Scan 是扫描相关选项
	Scan Scan
}

// Logging 是日志相关选项
type Logging struct {
	// Debug 显示调试级别日志
	Debug bool
	// LogScanErrors 记录与扫描相关的错误
	LogScanErrors bool
	// Silence 禁用所有日志
	Silence bool
}

// Chrome 是 Google Chrome 相关选项
type Chrome struct {
	// Path 是 Chrome 二进制文件的路径。空值表示
	// go-rod 将自动下载适合当前平台的二进制文件
	// 来使用。
	Path string
	// WSS 是 websocket URL。设置此值将阻止 gowitness
	// 启动 Chrome，而是使用远程实例。
	WSS string
	// Proxy 要使用的代理服务器
	Proxy string
	// UserAgent 是要为 Chrome 设置的 user-agent 字符串
	UserAgent string
	// Headers 是要添加到每个请求的头部
	Headers []string
	// WindowSize，以像素为单位。例如；X=1920,Y=1080
	WindowX int
	WindowY int
}

// Writer 选项
type Writer struct {
	Db        bool
	DbURI     string
	DbDebug   bool // 启用详细的数据库日志
	Csv       bool
	CsvFile   string
	Jsonl     bool
	JsonlFile string
	Stdout    bool
	None      bool
}

// Scan 是扫描相关选项
type Scan struct {
	// Driver 是要使用的扫描驱动。可以是 [gorod, chromedp] 之一
	Driver string
	// Threads（并非真正的线程）是要使用的 goroutines 数量。
	// 更确切地说，这是我们将使用的 go-rod 页面池。
	Threads int
	// Timeout 是页面加载超时前的最长等待时间。
	Timeout int
	// Delay 是导航和截图之间的延迟秒数
	Delay int
	// UriFilter 是可以处理的 URI。通常应该
	// 是 http 和 https
	UriFilter []string
	// SkipHTML 不写入 HTML 响应内容
	SkipHTML bool
	// ScreenshotPath 是存储截图图像的路径。
	// 空值表示驱动程序不会将截图写入磁盘。在
	// 这种情况下，你需要指定写入器保存。
	ScreenshotPath string
	// ScreenshotFormat 保存的截图格式
	ScreenshotFormat string
	// ScreenshotFullPage 保存完整的、滚动后的网页
	ScreenshotFullPage bool
	// ScreenshotToWriter 将截图作为模型属性传递给写入器
	ScreenshotToWriter bool
	// ScreenshotSkipSave 跳过将截图保存到磁盘
	ScreenshotSkipSave bool
	// JavaScript 是要在每个页面上执行的 JavaScript
	JavaScript     string
	JavaScriptFile string
	// SaveContent 存储网络请求的内容（警告）这
	// 可能会使写入的文件变得非常巨大
	SaveContent bool
	// Selector 是要截图的 CSS 选择器，为空时截取整个页面
	Selector string
}

// NewDefaultOptions 返回带有一些默认值的 Options
func NewDefaultOptions() *Options {
	return &Options{
		Chrome: Chrome{
			UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
			WindowX:   1920,
			WindowY:   1080,
		},
		Scan: Scan{
			Driver:           "chromedp",
			Threads:          6,
			Timeout:          60,
			UriFilter:        []string{"http", "https"},
			ScreenshotFormat: "jpeg",
		},
		Logging: Logging{
			Debug:         true,
			LogScanErrors: true,
		},
	}
}
