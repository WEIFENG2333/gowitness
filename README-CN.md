# GoWitness Enhanced (增强版)

这是 [gowitness](https://github.com/sensepost/gowitness) 的增强版本，添加了中文支持和额外功能。

## 新增功能 ✨

### 1. 中文注释翻译 🇨🇳

- 将 `pkg/runner` 包的所有注释翻译成中文
- 将 `pkg/runner/drivers` 包的注释翻译成中文
- 更好地支持中文用户理解和修改代码

### 2. CSS 选择器支持 🎯

- 新增 `Selector` 参数，可以截取页面中的特定元素
- 例如：`#page-block-container` 只截取该元素
- 支持所有标准 CSS 选择器

### 3. JavaScript 执行支持 💻

- 在截图前执行自定义 JavaScript 代码
- 可用于关闭弹窗、修改页面状态等
- 例如：`document.querySelector('.cookie-banner').remove()`

### 4. 自定义请求头支持 📋

- 支持添加自定义 HTTP 请求头
- 适用于需要认证或特殊头部的场景
- 格式：`["Authorization: Bearer token", "Custom-Header: value"]`

### 5. 全页截图选项 📸

- 新增 `FullPage` 布尔参数
- 控制是否截取整个页面（包括滚动区域）
- 默认开启

### 6. Web UI 改进 🎨

- 默认展开所有选项（Probe Options 和 Advanced Options）
- 更新默认值：
  - Screenshot Delay: 15秒（原5秒）
  - CSS Selector: `#page-block-container`
  - Capture Full Page: 默认开启

## API 增强

### `/api/submit` 和 `/api/submit/single` 端点

新增参数支持：

```json
{
  "url": "https://example.com",
  "options": {
    "format": "jpeg",
    "timeout": 60,
    "delay": 15,
    "user_agent": "Mozilla/5.0...",
    "window_x": 1920,
    "window_y": 1080,
    "javascript": "console.log('Hello');",
    "headers": ["Custom-Header: value"],
    "selector": "#main-content",
    "full_page": true
  }
}
```

## 使用示例

### 命令行示例

```bash
# 编译项目
go build

# 运行 Web 服务器
./gowitness server --address 127.0.0.1:7171

# 截取特定元素
curl -X POST 'http://127.0.0.1:7171/api/submit/single' \
  -H 'Content-Type: application/json' \
  -d '{
    "url":"https://example.com",
    "options":{
      "selector":"#page-block-container",
      "full_page":true,
      "javascript":"console.log(\"Page loaded\");",
      "headers":["User-Agent: Custom Bot"]
    }
  }'
```

### 程序化使用

```go
// 创建带选择器的截图选项
options := runner.Options{
    Scan: runner.Scan{
        Selector: "#main-content",
        ScreenshotFullPage: true,
        JavaScript: "document.querySelector('.popup').remove();",
    },
    Chrome: runner.Chrome{
        Headers: []string{"Authorization: Bearer token"},
    },
}
```

## 安装和运行

1. 克隆仓库

```bash
git clone https://github.com/WEIFENG2333/gowitness-enhanced.git
cd gowitness-enhanced
```

2. 安装依赖

```bash
go mod download
```

3. 构建前端

```bash
cd web/ui
npm install
npm run build
cd ../..
```

4. 编译项目

```bash
go build
```

5. 运行服务器

```bash
./gowitness server
```

## 原始项目

本项目基于 [sensepost/gowitness](https://github.com/sensepost/gowitness) 开发，保留所有原始功能并添加增强特性。

## 许可证

遵循原项目的 GPL-3.0 许可证。
