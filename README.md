# 快启坞 QuickDock

> 面向 Windows 开发者的效率工具 —— 资源集合、快速启动与工作空间管理

快启坞（QuickDock）是一款专为 Windows 开发者打造的桌面效率工具，融合了 **Raycast 的快速启动** 和 **VS Code 的开发者体验**。它帮助你统一管理项目、目录、网页链接、常用命令和应用，搭配剪贴板历史、代码片段、命令面板和丰富的内置插件，让开发工作流更高效。

![主界面截图](https://img.shields.io/badge/Platform-Windows-blue)
![Go](https://img.shields.io/badge/Go-1.25-blue)
![Vue](https://img.shields.io/badge/Vue-3-brightgreen)
![License](https://img.shields.io/badge/License-MIT-green)
![Version](https://img.shields.io/badge/Version-0.2.0-orange)

---

## 目录

- [功能特性](#功能特性)
- [截图一览](#截图一览)
- [快速开始](#快速开始)
- [开发指南](#开发指南)
- [技术栈](#技术栈)
- [项目结构](#项目结构)
- [数据模型](#数据模型)
- [全局热键](#全局热键)
- [设计哲学](#设计哲学)
- [内置插件](#内置插件)
- [第三方插件开发](#第三方插件开发)
  - [插件目录结构](#插件目录结构)
  - [plugin.json 完整字段](#pluginjson-完整字段)
  - [三种运行时](#三种运行时)
  - [纯前端插件（runtime: none）](#快速开始纯前端插件-runtime-none)
  - [Goja 插件（runtime: goja）](#快速开始-goja-插件-runtime-goja)
  - [原生插件（runtime: native）](#快速开始原生插件-runtime-native)
  - [标准 UI 样式](#标准-ui-样式commoncss)
  - [前端通信协议](#前端通信协议postmessage)
  - [安装与测试](#安装与测试)
  - [分发与发布](#分发与发布)
- [插件架构参考](#插件架构参考)
- [构建与打包](#构建与打包)
- [许可协议](#许可协议)

---

## 功能特性

### 📦 工作空间与资源管理

- **工作空间（Workspace）** — 顶级容器，隔离不同项目上下文
- **场景（Scene）** — 工作空间下的视图分组，快速切换关注点
- **集合（Collection）** — 资源的逻辑分组，可按项目或类型归类
- **项目（Item）** — 支持 6 种类型：
  - `directory` — 目录，用系统/终端打开
  - `file` — 文件，用系统默认程序打开
  - `url` — 网页链接，用浏览器打开
  - `command` — 终端命令，在终端中执行
  - `app` — 应用程序路径
  - `quicklink` — 快速链接，带参数的快捷方式

### 📋 剪贴板历史

- 自动监听并记录文本、图片、文件剪贴板内容
- 支持固定、搜索、复制粘贴、批量删除
- 浮动窗口，失焦自动隐藏，`Ctrl+`` ` 一键唤出

### 🔍 命令面板

- 全局搜索：工作空间 / 场景 / 集合 / 项目统一搜索
- 快速执行：选中即操作，支持键盘导航
- 浮动窗口，`Ctrl+K` 即时唤出

### 📝 文本片段（Snippets）

- 预定义的常用文本模板
- 一键复制或粘贴到当前活动窗口

### 🔌 插件系统

- 19 个开箱即用的内置插件（计算表格、JSON2TS、JWT 解码、正则提取、Markdown 预览……）
- 支持三种运行时：纯前端（none）、内嵌 JS 引擎（goja）、独立子进程（native）
- 基于 JSON-RPC 2.0 的前端 ↔ 后端通信协议
- 支持运行时安装 / 卸载 / 启用 / 禁用 / 热键绑定
- [开放第三方插件开发](#第三方插件开发)，打包为 ZIP 即可分发

### ☁️ WebDAV 云同步

- 全量 JSON 备份 / 恢复
- 多版本管理
- 任意 WebDAV 服务器（自建 / 第三方）

### 📸 快照备份

- 一键导出全部数据为 JSON 文件
- 导入恢复，迁移无忧

### 🔧 全局热键（可自定义）

- 所有热键均可在设置页面自定义
- 运行时动态重注册
- 捕获新热键时自动暂停全局监听以避免冲突

### 🖥️ 系统命令

- 锁屏、关机、重启、睡眠、清空回收站

---

## 快速开始

### 系统要求

- **操作系统**：Windows 10 1809+ 或 Windows 11
- **运行时**：WebView2 Runtime（Windows 自动带）
- **磁盘**：~100MB

### 下载安装

1. 从 [Releases](https://github.com/parieses/quickdock-v3/releases) 下载最新版本
2. 解压到任意目录（推荐 `%LOCALAPPDATA%\QuickDock`）
3. 运行 `QuickDock.exe`
4. 任务栏托盘出现 QuickDock 图标即启动成功

### 首次使用

启动后按 `Ctrl+Space` 唤出主窗口，跟随引导页完成初始设置即可开始使用。

---

## 开发指南

### 前置条件

- Go 1.25+
- Node.js 22+
- Wails3 CLI

```bash
# 安装 Wails3 CLI
go install github.com/wailsapp/wails/v3/cmd/wails3@latest

# 安装前端依赖
cd frontend && pnpm install
```

### 常用命令

| 命令 | 说明 |
|------|------|
| `task dev` | 开发模式（前后端热重载，端口 9245） |
| `wails3 dev` | 同上，直接调用 Wails3 |
| `wails3 build` | 生产构建 |
| `task build` | 通过 Taskfile 构建 |
| `task package` | 打包生产版本 |
| `task run` | 直接运行已构建的应用 |

### 数据库

- SQLite 数据库文件：`~/.quickdock/quickdock.db`
- 剪贴板图片：`~/.quickdock/images/`
- 应用配置：`%APPDATA%/QuickDock/`

---

## 技术栈

### 后端

| 技术 | 说明 |
|------|------|
| **Go 1.25** | 主语言 |
| **Wails3 v3.0.0-alpha2** | 桌面应用框架 |
| **modernc.org/sqlite** | 纯 Go SQLite（无 CGO） |
| **golang.org/x/sys** | Windows 系统 API 调用 |
| **dop251/goja** | JavaScript 沙箱（插件执行） |

### 前端

| 技术 | 说明 |
|------|------|
| **Vue 3 + TypeScript** | UI 框架 |
| **Vite 8** | 构建工具 |
| **Pinia 3** | 状态管理 |
| **vue-i18n** | 国际化（简体中文 / English） |
| **Lucide Vue** | 图标库 |
| **pinyin-pro** | 拼音搜索支持 |
| **@wailsio/runtime** | Wails 前端运行时绑定 |

---

## 项目结构

```
quickdock-v3/
├── main.go              # 入口：三窗口创建 + 应用配置
├── tray.go              # 系统托盘 + 全局热键 + 剪贴板监听
├── windows.go           # 窗口辅助函数
├── services/            # Wails 服务层（前端绑定方法）
│   ├── service.go       # AppService 核心
│   ├── lifecycle.go     # 生命周期管理
│   ├── workspace.go     # 工作空间 CRUD
│   ├── scene.go         # 场景 CRUD
│   ├── collection.go    # 集合 CRUD
│   ├── item.go          # 项目 CRUD
│   ├── clipboard.go     # 剪贴板历史
│   ├── clipboard_sys.go # 系统剪贴板操作
│   ├── palette.go       # 命令面板搜索
│   ├── snippet.go       # 文本片段
│   ├── hotkey.go        # 热键配置管理
│   ├── plugin.go        # 插件管理
│   ├── snapshot.go      # 快照备份
│   ├── webdav.go        # WebDAV 同步
│   ├── system.go        # 系统命令
│   ├── app_launcher.go  # 应用启动
│   ├── frecency.go      # 频率排序算法
│   ├── tool.go          # 打开工具管理
│   ├── autostart.go     # 开机自启
│   ├── api_result.go    # 统一 API 返回
│   └── types.go         # 配置类型定义
├── internal/
│   ├── db/              # SQLite 数据层（含安全白名单）
│   ├── platform/        # Windows API 封装
│   │   ├── clipboard.go # 剪贴板读写
│   │   ├── commands.go  # 系统命令
│   │   └── monitor.go   # 多显示器定位
│   ├── plugin/          # 插件管理器
│   └── webdav/          # WebDAV HTTP 客户端
├── frontend/
│   ├── src/
│   │   ├── components/  # 19 个 Vue 组件
│   │   ├── stores/      # Pinia 状态管理
│   │   ├── types/       # TypeScript 类型
│   │   ├── utils/       # 工具函数
│   │   ├── i18n/        # 国际化
│   │   └── assets/      # 字体、图片
│   └── vite.config.ts
├── plugins/builtin/     # 19 个内置插件
├── plugins/templates/   # 插件开发模板（none / goja / native）
├── build/               # 多平台构建配置
├── docs/                # 设计文档
├── DESIGN.md            # 设计系统规范
├── Taskfile.yml         # 构建任务定义
└── go.mod
```

---

## 数据模型

```
Workspace（工作空间）
  └── Scene（场景）
       └── Collection（集合）
            └── Item（项目）
                  ├── directory   — 目录
                  ├── file        — 文件
                  ├── url         — 网页链接
                  ├── command     — 终端命令
                  ├── app         — 应用程序
                  └── quicklink   — 快速链接
```

- Item 通过 `tool_id` 关联 **OpenTool**（系统 / 浏览器 / 终端 等打开方式）
- **ClipboardEntry**（剪贴板条目）和 **Snippet**（文本片段）独立于层级
- 数据库表通过白名单机制防止 SQL 注入

---

## 全局热键

| 功能 | 默认快捷键 | 说明 |
|------|-----------|------|
| 切换主窗口 | `Ctrl+Space` | 显示 / 隐藏主界面 |
| 剪贴板历史 | `Ctrl+`` `（反引号） | 显示 / 隐藏剪贴板浮动窗口 |
| 命令面板 | `Ctrl+K` | 显示 / 隐藏命令面板浮动窗口 |

> 所有热键均可在「设置 > 热键」页面自定义。

---

## 设计哲学

快启坞遵循 **精准暗色极简主义（Precision Dark Minimalism）**：

- **暗色主题为主** — 层次化灰色调，非纯黑，通过明度对比创造深度
- **强调色 `#4a9eff`** — 仅用于功能性交互元素，不作装饰
- **三面板布局** — 侧边栏(210px) | 集合列表(300px) | 项目列表(flex-1)
- **8px 基准间距** — 4px 递增，从 2px 到 48px
- **系统字体栈** — 无自定义字体，基字大小 13px
- **150ms 过渡** — 少动效，仅状态变化时使用动画
- **键盘优先** — 所有交互均支持键鼠操作
- **Shadow-border 技术** — `box-shadow` 替代 CSS `border`，消除布局偏移

详细设计规范请参阅 [DESIGN.md](./DESIGN.md)。

---

## 内置插件

QuickDock 内置 19 个即开即用的开发小工具：

| 插件 | 功能 |
|------|------|
| `calcsheet` | 计算表格 |
| `case-converter` | 大小写转换 |
| `code-formatter` | 代码格式化 |
| `cron-explainer` | Cron 表达式解析 |
| `data-converter` | 数据格式转换 |
| `emoji-search` | Emoji 搜索 |
| `file-compare` | 文件对比 |
| `hosts-manager` | Hosts 文件管理 |
| `http-status` | HTTP 状态码查询 |
| `json2ts` | JSON 转 TypeScript 类型 |
| `jwt-decoder` | JWT 解码 |
| `markdown-preview` | Markdown 预览 |
| `port-scanner` | 端口扫描 |
| `regex-extractor` | 正则提取 |
| `sql-formatter` | SQL 格式化 |
| `text-diff` | 文本差异对比 |
| `text-encoder` | 文本编码转换 |
| `time-converter` | 时间戳转换 |
| `wifi-manager` | WiFi 管理 |

插件启动时自动从 `plugins/builtin/` 安装至 `~/.quickdock/plugins/`。

---

## 第三方插件开发

QuickDock 提供开放的插件系统，任何人都可以为它开发第三方插件。插件本质上是包含 `plugin.json` 清单文件的一个目录，支持三种运行时模式。

### 插件目录结构

```
my-plugin/
├── plugin.json           # 插件清单（必须）
├── main.js               # Goja 后端脚本（goja 运行时必须）
├── my-plugin.exe         # 原生可执行文件（native 运行时必须）
├── frontend/
│   ├── index.html        # 前端 UI 入口（可选）
│   ├── style.css         # 专属样式（可选）
│   └── app.js            # 专属脚本（可选）
├── icon.svg              # 插件图标（可选，支持 svg/png/ico/jpg）
└── README.md             # 说明文档（推荐）
```

### plugin.json 完整字段

```json
{
  "id": "com.example.myplugin",
  "name": "我的插件",
  "version": "1.0.0",
  "description": "插件功能描述",
  "author": "YourName",
  "icon": "icon.svg",
  "category": "开发者日常",

  "backend": {
    "runtime": "goja",
    "entry": "main.js",
    "args": []
  },

  "frontend": {
    "enabled": true,
    "entry": "frontend/index.html",
    "width": 520,
    "height": 460
  },

  "commands": [
    {
      "id": "hello",
      "title": "Hello World",
      "hotkey": "Ctrl+Shift+H",
      "keywords": ["hello", "hi"],
      "aliases": ["打招呼", "测试"],
      "prefix": "/hello",
      "matchPattern": "^[a-zA-Z]+$"
    }
  ],

  "capabilities": ["command", "frontend"],
  "permissions": {
    "network": false,
    "filesystem": false,
    "clipboard": true
  }
}
```

| 字段 | 说明 |
|------|------|
| `id` | **必须**。反向域名格式，必须包含至少一个点号，如 `com.example.myplugin` |
| `name` | **必须**。插件显示名称 |
| `version` | **必须**。语义化版本号 |
| `backend.runtime` | **必须**。运行模式：`none` / `goja` / `native` |
| `backend.entry` | `none` 之外必须。入口文件路径 |
| `frontend.enabled` | 是否启用前端面板。`true` 时需指定 `entry` |
| `commands` | 注册到命令面板的命令列表 |
| `capabilities` | 能力声明：`command`（支持命令面板）/ `frontend`（有 UI 面板）|
| `permissions` | 权限声明：`network` / `filesystem` / `clipboard`，默认全 `false` |

### 三种运行时

| 运行时 | 适用场景 | 进程模型 | 开发语言 |
|--------|---------|---------|---------|
| `none` | 纯前端工具（计算器、编码转换） | 无后端进程，全部在 iframe 中运行 | HTML + CSS + JS |
| `goja` | 轻量逻辑（数据转换、文本处理） | 同进程内嵌 JS 引擎，零外部依赖 | JavaScript（ES5） |
| `native` | 系统级操作（文件管理、网络请求） | 独立子进程，JSON-RPC stdin/stdout 通信 | Go / Rust / Python 等任意语言 |

---

#### 快速开始：纯前端插件（runtime: none）

适合无需后端能力的工具类插件，如编码转换、正则测试、Markdown 预览等。

**1. 创建项目结构**

```
my-tool/
├── plugin.json
├── frontend/
│   └── index.html
└── icon.svg
```

**2. plugin.json**

```json
{
  "id": "com.example.my-tool",
  "name": "我的工具",
  "version": "1.0.0",
  "description": "一个纯前端小工具",
  "author": "YourName",
  "icon": "icon.svg",
  "category": "开发者日常",
  "backend": { "runtime": "none" },
  "frontend": {
    "enabled": true,
    "entry": "frontend/index.html"
  },
  "capabilities": ["frontend"]
}
```

**3. 前端页面**使用 **common.css** 标准样式类构建 UI：

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1.0">
  <link rel="stylesheet" href="../common.css">
</head>
<body>
<div class="p-app">
  <div class="p-toolbar">
    <span class="p-label">我的工具</span>
    <div class="p-spacer"></div>
    <span id="statusLabel" class="p-muted">就绪</span>
  </div>
  <div class="p-body" style="padding:10px;flex-direction:column;gap:8px">
    <input id="input" class="p-input" placeholder="输入内容…">
    <div id="output" class="p-card p-output">等待输入…</div>
  </div>
</div>
<script>
  // 后端通信不是必须的，纯前端可以直接操作 DOM
  document.getElementById('input').addEventListener('input', function() {
    var val = this.value
    document.getElementById('output').textContent = val ? '你输入了: ' + val : '等待输入…'
  })

  // 接收主题和语言传递
  window.addEventListener('message', function(e) {
    if (e.data && e.data.type === 'plugin:init' && e.data.data) {
      if (e.data.data.theme) document.documentElement.setAttribute('data-theme', e.data.data.theme)
      if (e.data.data.locale) document.documentElement.setAttribute('lang', e.data.data.locale)
    }
  })
</script>
</body>
</html>
```

> 完整模板见 `plugins/templates/none/`。

---

#### 快速开始：Goja 插件（runtime: goja）

适合需要轻量后端的插件，内嵌 JavaScript 引擎，无需外部进程。

**1. plugin.json**（如上，`runtime` 设为 `goja`）

**2. 后端 main.js** — 必须导出 `handleInitialize` 和 `handleExecute`：

```javascript
// handleInitialize — 插件启动时调用（可选）
function handleInitialize(params) {
  api.log('插件初始化完成')
  return { status: 'ready', version: '1.0.0' }
}

// handleExecute — 处理命令执行（必须）
function handleExecute(params) {
  var command = params.command || ''
  var input = params.input || {}
  var text = input.text || ''

  switch (command) {
    case 'hello':
      return { text: 'Hello, ' + text + '!', display: 'Hello, ' + text + '!' }
    default:
      return { error: '未知命令: ' + command }
  }
}
```

**可用 Goja API：**

| API | 说明 | 所需权限 |
|-----|------|---------|
| `api.log(msg)` | 写日志到后端 | — |
| `api.readFile(path)` | 读取文件 | `filesystem` |
| `api.writeFile(path, data)` | 写入文件 | `filesystem` |
| `api.httpGet(url)` | HTTP GET 请求 | `network` |
| `api.httpPost(url, body)` | HTTP POST 请求 | `network` |
| `api.db.exec(sql)` | 执行 SQL（插件专属 SQLite 数据库） | — |
| `api.db.query(sql)` | 查询 SQL，返回数组 | — |

**3. 前端与后端通信**：通过 `window.parent.postMessage` 发起 `plugin:execute`：

```javascript
function pluginExec(command, data) {
  return new Promise(function(resolve, reject) {
    var id = Date.now() + '_' + Math.random().toString(36).slice(2, 6)
    var timeout = setTimeout(function() { reject(new Error('响应超时')) }, 10000)

    var handler = function(e) {
      if (e.data && e.data.type === 'plugin:result') {
        window.removeEventListener('message', handler)
        clearTimeout(timeout)
        if (e.data.error) reject(new Error(e.data.error))
        else resolve(e.data.data)
      }
    }
    window.addEventListener('message', handler)
    window.parent.postMessage({ type: 'plugin:execute', id: id, command: command, input: { text: data } }, '*')
  })
}

// 调用后端
pluginExec('hello', 'World').then(function(data) {
  console.log('结果:', data)
})
```

> 完整模板见 `plugins/templates/goja/`。

---

#### 快速开始：原生插件（runtime: native）

适合系统级操作，可以用任意语言编写独立子进程，通过 JSON-RPC 2.0 over stdin/stdout 通信。

**1. plugin.json**（`runtime` 设为 `native`，`entry` 指向可执行文件）

**2. 后端（Go 示例）**

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal([]byte(scanner.Text()), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Println(`{"jsonrpc":"2.0","id":` + fmt.Sprint(req.ID) + `,"result":{"status":"ready"}}`)
		case "host.ping":
			fmt.Println(`{"jsonrpc":"2.0","id":` + fmt.Sprint(req.ID) + `,"result":{"pong":true}}`)
		case "plugin.execute":
			fmt.Println(`{"jsonrpc":"2.0","id":` + fmt.Sprint(req.ID) + `,"result":{"text":"Hello from native plugin!","success":true}}`)
		}
	}
}
```

**JSON-RPC 协议：**

| 方向 | 方法 | 说明 |
|------|------|------|
| 宿主→插件 | `initialize` | 插件初始化，返回 `{status: "ready"}` |
| 宿主→插件 | `host.ping` | 健康检查，返回 `{pong: true}`（每 30 秒） |
| 宿主→插件 | `plugin.execute` | 执行命令，`params = {command, input}` |
| 插件→宿主 | `host.shutdown` | 插件请求退出 |

**插件可调用的宿主方法（回调请求）：**

| 方法 | 说明 | 所需权限 |
|------|------|---------|
| `host.clipboard.read` / `write` | 读写剪贴板 | `clipboard` |
| `host.dialog.open` / `save` | 文件对话框 | `filesystem` |
| `http.get` / `http.post` | HTTP 请求 | `network` |
| `db.get` / `db.set` | 插件专属存储 | — |
| `log.info` / `log.error` | 日志 | — |
| `ui.show` / `ui.hide` | 显示/隐藏前端面板 | — |
| `host.notify` | 系统通知 | — |

> 完整原生 Go 模板见 `plugins/templates/native/`，包含完整的请求分发和主机方法调用示例。

---

### 标准 UI 样式（common.css）

所有插件前端自动注入 `common.css`，提供统一的设计语言。插件开发时直接使用以下 CSS 变量和样式类：

**CSS 变量：**

```
--bg-primary: #1a1a1a   --bg-secondary: #1e1e1e   --bg-tertiary: #242424
--text-primary: #e8e8e8  --text-secondary: #aaa    --text-muted: #999
--accent: #4a9eff        --success: #28c864        --danger: #e24b4a
--warning: #f0a030       --radius: 6px             --font: 系统字体栈
--font-mono: 等宽字体     --transition: 0.1s        --border: #2a2a2a
```

**布局类：** `.p-app`（全屏容器）/ `.p-toolbar`（工具栏）/ `.p-body`（内容区）/ `.p-pane`（面板）/ `.p-statusbar`（状态栏）

**按钮类：** `.p-btn` / `.p-btn-primary`（蓝色）/ `.p-btn-sm` / `.p-btn-group`

**表单类：** `.p-input` / `.p-input-mono` / `.p-select` / `.p-textarea`

**列表卡片类：** `.p-list` / `.p-item` / `.p-item-label` / `.p-item-desc` / `.p-card`

**文本类：** `.p-output`（代码输出区）/ `.p-label` / `.p-muted` / `.p-empty` / `.p-error` / `.p-spacer` / `.p-sep`

> 插件会自动适配 QuickDock 的明暗主题：通过 `plugin:init` 和 `plugin:theme` 消息传递 `data-theme` 属性，`common.css` 内置 `html[data-theme="light"]` 浅色适配规则。

---

### 前端通信协议（postMessage）

插件前端页面运行在 iframe 沙箱中，与主应用通过 `postMessage` 通信。

| type | 方向 | 说明 |
|------|------|------|
| `plugin:init` | 主应用→插件 | 初始化通知，携带 `theme`、`locale`、`text`（剪贴板内容） |
| `plugin:theme` | 主应用→插件 | 主题/语言变更通知 |
| `plugin:execute` | 插件→主应用 | 向后端发送执行命令请求 |
| `plugin:result` | 主应用→插件 | 命令执行结果响应 |

**通信示例：**

```javascript
// 插件前端 → 主应用：执行命令
window.parent.postMessage({
  type: 'plugin:execute',
  id: 'req_001',
  command: 'hello',
  input: { text: 'World' }
}, '*')

// 接收结果
window.addEventListener('message', function(e) {
  if (e.data && e.data.type === 'plugin:result') {
    // e.data.id → 请求标识
    // e.data.data → 结果数据
    // e.data.error → 错误信息
  }
  // 初始化（主题/语言）
  if (e.data && e.data.type === 'plugin:init' && e.data.data) {
    if (e.data.data.theme) document.documentElement.setAttribute('data-theme', e.data.data.theme)
    if (e.data.data.locale) document.documentElement.setAttribute('lang', e.data.data.locale)
  }
})
```

---

### 安装与测试

1. **打包插件**：将插件目录打包为 ZIP 文件，文件名不限
2. **安装插件**：
   - 打开 QuickDock，进入 **插件管理** 页面
   - 点击 **从文件安装**，选择 ZIP 包
   - 或直接将 ZIP 拖拽到插件管理页面
3. **验证**：安装成功后，插件出现在管理页面列表中，状态应为 `running`
4. **调试**：
   - `none` / 前端部分：使用浏览器 DevTools 调试 iframe
   - `goja`：查看 QuickDock 后端日志
   - `native`：插件 stdout/stderr 会被记录到 QuickDock 日志

### 分发与发布

1. 将插件目录打包为 `{your-plugin-id}.zip`
2. 分发 ZIP 文件，用户安装即可使用
3. （可选）在 GitHub 上发布插件，供社区下载

> **安全提示**：`permissions` 字段声明了插件的权限需求，用户安装时可见。请按最小权限原则声明，如无需网络功能则不声明 `network`。

---

## 插件架构参考

### 核心源码文件

| 文件 | 作用 |
|------|------|
| `internal/plugin/types.go` | PluginManifest、PluginInfo 等类型定义 |
| `internal/plugin/manifest.go` | plugin.json 加载与校验 |
| `internal/plugin/manager.go` | 插件管理器（加载/卸载/健康检查/重启） |
| `internal/plugin/rpc.go` | JSON-RPC 2.0 通信层 |
| `internal/plugin/host.go` | 宿主方法注册与权限校验 |
| `internal/plugin/installer.go` | ZIP 安装/校验/回滚 |
| `internal/plugin/window_manager.go` | 插件独立窗口管理 |
| `services/plugin.go` | Wails 前端绑定（安装/启用/禁用/卸载/执行） |
| `plugins/templates/` | 三种运行时的完整开发模板 |

### 生命周期

```text
安装 → 加载 plugin.json → 校验字段
     → runtime=none:  就绪，无后端进程
     → runtime=goja:  启动 goja VM，执行 main.js，调用 handleInitialize
     → runtime=native: spawn 子进程，发送 initialize 请求
     → 等待请求 → 处理命令 → 响应
     → 卸载时: 停止进程/VM，清理热键，删除目录
```

- **崩溃自动重启**：native 子进程崩溃后自动重启，最多 3 次（指数退避 2s → 4s → 6s）
- **健康检查**：每 30 秒 ping 一次，连续 3 次无响应标记为 `unresponsive`
- **安全防护**：ZIP 安装时校验路径穿越攻击（Zip Slip），限制解压上限 100MB，单文件上限 50MB

---

## 构建与打包

### 本地构建

```bash
# 开发模式（热重载）
task dev

# 生产构建
wails3 build

# 通过 Taskfile
task build

# 打包
task package
```

### 平台支持

> **当前仅支持 Windows**。macOS 和 Linux 的构建配置已预留，尚未适配。

---

## 架构亮点

- **三窗口架构**：主窗口 (1100×700) + 剪贴板浮动窗口 (480×420) + 命令面板浮动窗口 (680×460)
- **窗口即隐藏**：关闭主窗口时隐藏到系统托盘而非退出，通过 `atomic.Bool` 标志区分真实退出
- **多显示器支持**：浮动窗口自动定位到鼠标所在屏幕
- **纯 Go SQLite**：使用 modernc.org/sqlite，零 CGO 依赖，简化交叉编译
- **回调注入解耦**：热键函数通过注入方式避免 main 和 services 包之间的循环依赖
- **SQL 白名单**：表名和列名校验防止 SQL 注入

---

## 许可协议

本项目采用 **MIT License** 开源许可证。

Copyright (c) 2025-2026 王亮亮

```
MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

---

## 致谢

- [Wails](https://wails.io/) — 强大的 Go 桌面应用框架
- [Vue.js](https://vuejs.org/) — 渐进式前端框架
- [Lucide](https://lucide.dev/) — 优雅的开源图标库
- [modernc.org/sqlite](https://modernc.org/sqlite) — 纯 Go SQLite 实现
