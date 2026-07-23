# QuickDock v3 插件开发指南

## 概述

QuickDock v3 支持三种插件运行时，**均无需外部依赖**：

| 运行时 | 说明 | 适用场景 |
|--------|------|---------|
| `none` | 纯前端插件，无后端进程 | 计算稿纸、翻译面板、JSON 格式化等 UI 型插件 |
| `goja` | 内嵌 JS 引擎（Goja），进程中执行 | 需要后端逻辑但不需要子进程的插件 |
| `native` | 独立可执行文件（.exe） | 需要独立进程、系统 API 或高计算量的插件 |

> ❌ **Python / Node.js / PowerShell 运行时已不再支持**。所有插件运行时均嵌入 QuickDock 内部，用户无需安装任何外部环境。

---

## 目录结构

一个插件是一个文件夹，放在 `~/.quickdock/plugins/<plugin-id>/` 下：

```
my-plugin/
├── plugin.json            # 插件清单（必须）
├── main.js                # Goja 后端脚本（goja runtime）
├── main.exe               # 可执行文件（native runtime）
├── frontend/              # 前端资源（none/goja/native 均可选）
│   ├── index.html
│   ├── style.css
│   └── app.js
```

---

## plugin.json 清单格式

```json
{
  "id": "com.quickdock.my-plugin",
  "name": "我的插件",
  "version": "0.1.0",
  "description": "插件功能描述",
  "author": "Your Name",

  "backend": {
    "runtime": "goja",
    "entry": "main.js"
  },

  "frontend": {
    "enabled": false,
    "entry": "frontend/index.html",
    "width": 400,
    "height": 300
  },

  "capabilities": ["command"],

  "permissions": {
    "network": false,
    "filesystem": false,
    "clipboard": true
  },

  "commands": [
    {
      "id": "hello",
      "title": "Hello World",
      "keywords": ["hw", "greet"]
    }
  ]
}
```

### 字段说明

| 字段 | 说明 |
|---|---|
| `id` | 唯一标识，格式 `com.quickdock.xxx`（至少一个点号）|
| `name` | 插件显示名称 |
| `version` | 语义化版本号 |
| `backend.runtime` | 运行环境：`none` / `goja` / `native` |
| `backend.entry` | 入口文件名（`none` runtime 不需要）|
| `permissions` | 权限声明，影响插件能调用的 Host API |
| `commands` | 注册到命令面板的命令列表 |
| `commands[].keywords` | 搜索别名数组，用户输入这些词也能匹配到该命令 |

### commands 字段

每个命令对象支持的字段：

| 字段 | 说明 |
|---|---|
| `id` | 命令唯一 ID（插件内唯一） |
| `title` | 命令显示名称 |
| `hotkey` | 全局热键（如 `Ctrl+Shift+T`），可选 |
| `keywords` | 搜索别名数组，用户输入这些词也能匹配到该命令 |
| `aliases` | 中文别名数组（如 `["计算器","jsq"]`），扩展中文搜索覆盖 |
| `prefix` | Slash 前缀（如 `/tr`），命令面板输入 `/tr` 时仅该命令激活 |
| `matchPattern` | 正则匹配模式，命令面板输入文本命中该正则时该命令会被推荐 |
| `acceptsInput` | **声明该命令接收命令面板传入的参数**，详见「从命令面板接收输入」 |

> ⚠️ **`matchPattern` / `prefix` 只负责「让命令被推荐/激活」，并不代表参数会自动传入插件。** 若要让命令面板输入框中的文本（如 `500`、`192.168.1.1`、`*/5 * * * *`）真正带进插件并执行，必须在命令上声明 `"acceptsInput": true`。

### Runtime 说明

| runtime | entry 示例 | 说明 |
|---------|-----------|------|
| `none` | 无 | 纯前端插件，没有后端进程。所有逻辑在 iframe 的 JS 中执行 |
| `goja` | `main.js` | 内嵌 JS 引擎。插件 JS 在 QuickDock 进程内执行，无需安装 Node.js |
| `native` | `main.exe` | 独立可执行文件。QuickDock 会启动为子进程，通过 stdin/stdout JSON-RPC 通信 |

---

## 三种运行时详解

### none runtime（纯前端）

适用于不需要后端逻辑的 UI 插件。插件只是一个 HTML 页面，在独立窗口中通过 iframe 加载。

**plugin.json 示例：**
```json
{
  "backend": { "runtime": "none" },
  "frontend": { "enabled": true, "entry": "frontend/index.html" }
}
```

**特点：**
- 不启动子进程，零资源开销
- 所有逻辑在浏览器 JS 中执行
- 通过 `parent.postMessage` 与主程序通信（经由 PluginPage 中转）
- 数据持久化使用 `localStorage`

---

### goja runtime（内嵌 JS 引擎）

适用于需要后端逻辑但不需要子进程的插件。JS 代码在 QuickDock 进程内直接执行。

**plugin.json 示例：**
```json
{
  "backend": { "runtime": "goja", "entry": "main.js" }
}
```

**main.js 模板：**
```javascript
function handleInitialize(params) {
    api.log('插件初始化完成')
    return { status: 'ready', version: '0.1.0' }
}

function handleExecute(params) {
    var command = params.command || ''
    var input = params.input || {}

    if (command === 'hello') {
        var name = input.text || 'World'
        return { result: 'Hello, ' + name + '!' }
    }

    throw new Error('未知命令: ' + command)
}
```

**特点：**
- 无需安装 Node.js，内嵌 Goja 引擎（纯 Go，无 CGO）
- 支持 ES5.1 + 大部分 ES6 特性
- 通过 `api.log()` 输出日志
- 导出 `handleInitialize()` 和 `handleExecute()` 函数供主程序调用

---

### native runtime（独立可执行文件）

适用于需要独立进程、系统 API 或高计算量的插件。

**通信方式：stdin/stdout JSON-RPC 2.0**

插件通过 stdin 接收请求，通过 stdout 发送响应。每行一个完整的 JSON 对象。

#### 生命周期

**1. initialize（主程序 → 插件）**

```json
// 主程序发送
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"hostVersion":"3.0.0","pluginDir":"..."}}

// 插件响应（15 秒内）
{"jsonrpc":"2.0","id":1,"result":{"status":"ready","pluginId":"com.quickdock.my-plugin"}}
```

**2. plugin.execute（主程序 → 插件）**

用户在命令面板执行插件命令时触发。

```json
// 主程序发送
{"jsonrpc":"2.0","id":2,"method":"plugin.execute","params":{"command":"hello","input":{"name":"World"}}}

// 插件响应（10 秒内）
{"jsonrpc":"2.0","id":2,"result":{"message":"Hello, World!"}}
```

**3. shutdown（主程序 → 插件）**

插件被卸载/禁用/主程序退出时触发。

```json
// 主程序发送（通知，无需响应）
{"jsonrpc":"2.0","method":"shutdown","params":null}
```

#### Host Methods（插件可调用的主程序 API）

native 插件在收到请求后，可以通过 stdout 向主程序发起回调请求：

| 方法 | 说明 | 所需权限 |
|---|---|---|
| `log.info` | 记录日志 | 无需权限 |
| `log.error` | 记录错误日志 | 无需权限 |
| `host.notify` | 弹出系统通知 | 无需权限 |
| `host.clipboard.read` | 读取剪贴板文本 | `clipboard: true` |
| `host.clipboard.write` | 写入剪贴板文本 | `clipboard: true` |

```json
// 插件 → 主程序（回调请求）
{"jsonrpc":"2.0","id":101,"method":"host.clipboard.write","params":{"text":"剪贴板内容"}}

// 主程序 → 插件（响应）
{"jsonrpc":"2.0","id":101,"result":{"success":true}}
```

---

## 权限声明

插件在 `plugin.json` 中声明所需权限：

```json
"permissions": {
  "network": false,    // 能否发起 HTTP 请求
  "filesystem": false, // 能否访问文件对话框
  "clipboard": true    // 能否读写剪贴板
}
```

---

## 前端开发

插件前端是一个标准的 HTML 页面，在独立窗口中通过 iframe 加载。

### 与主程序通信

通过 `window.parent.postMessage` 与主程序通信（`javascript:void(0)`）：

```javascript
// 插件前端 → 主程序
window.parent.postMessage(
  { type: 'plugin:execute', id: 1, command: 'hello', input: { name: 'World' } },
  '*'
)
```

### 从命令面板接收输入（acceptsInput）

当用户在命令面板选中某个插件命令，且输入框里有文本时，这些文本**默认不会**传给插件。只有命令在 `plugin.json` 中声明了 `"acceptsInput": true`，宿主才会把文本注入插件。

典型场景：端口检查（输入 `8080`）、HTTP 状态码（输入 `500`）、时间戳转换（输入 `1700000000`）、Cron 解释（输入 `*/5 * * * *`）等「单一数据主体」类命令。

#### 投递路径

宿主按插件是否带前端分两条路径投递：

**路径 A：插件带前端（none / goja / native 且配了 frontend）**

宿主调用 `SetPendingPluginInit(text, commandID)` 暂存参数并打开插件窗口 / 内联 iframe，加载完成后向 iframe 发送 `plugin:init` 消息：

```javascript
// 宿主发送（plugin:init）:
// { type:'plugin:init', data: { text: '<用户输入>', command: '<命令ID>', theme:'dark', locale:'zh' } }

window.addEventListener('message', (e) => {
  if (e.data?.type === 'plugin:init') {
    const { text, command } = e.data.data || {}
    if (text) {
      // 1. 把 text 填入插件输入框
      // 2. 调用插件自身的转换/执行函数（如 showDetail(code) / convert()）
    }
  }
})
```

> 内置插件使用 Nonce 握手安全机制，`plugin:init` 由 PluginPage.vue 在 iframe `onload` 后自动发送，插件只需监听 `message` 事件即可。

**路径 B：插件无前端（纯后端命令）**

宿主直接调用 `ExecutePluginCommand(pluginID, commandID, { text })`，把文本作为 `input.text` 传给后端：

```javascript
// goja 后端 main.js
function handleExecute(params) {
    var command = params.command || ''
    var input = params.input || {}
    var text = input.text || ''   // ← 命令面板传入的文本
    // ...
}

// native 后端（JSON-RPC plugin.execute）
// params: { "command":"hello", "input": { "text": "用户输入" } }
```

#### 声明示例

```json
{
  "commands": [
    {
      "id": "lookup-status",
      "title": "HTTP 状态码查询",
      "prefix": "/http",
      "matchPattern": "^[1-5][0-9]{2}$",
      "acceptsInput": true
    }
  ]
}
```

---

## 安装与调试

### 安装插件

1. 将插件打包为 `.zip` 文件（`plugin.json` 必须在根目录）
2. 打开 QuickDock → 插件管理页面
3. 拖入 zip 文件或点击「安装插件」选择文件
4. 安装成功后插件自动启动

### 查看日志

- `goja` 插件：使用 `api.log('message')` 输出日志
- `native` 插件：stderr 输出会显示在主程序日志中
- 所有插件均可通过 `log.info` / `log.error` Host Method 输出日志

### 调试建议

1. 先用模板创建项目，确认基础通信正常
2. 新插件建议使用 `goja` runtime（无需编译，修改 JS 后重启插件即可）
3. 用 `api.log` 输出调试信息
4. 检查 `~/.quickdock/plugins/` 目录确认插件已安装

---

## 注意事项

1. **native 插件 stdout 专用于 JSON-RPC**: 不要用 `fmt.Println` / `console.log` 输出非 JSON 内容到 stdout，这会破坏通信协议
2. **错误处理**: 始终用 JSON-RPC 错误响应返回错误
3. **热键冲突**: 如果多个插件声明相同热键，后安装的插件注册会失败
4. **存储隔离**: 插件的 `db.*` Host Method 只能读写自己 `plugin_id` 的数据

---

## 完整示例

参见 `plugins/templates/goja/` 目录下的 Goja 模板项目。
以及 `plugins/builtin/calcsheet/` 目录下的内置计算稿纸插件（`none` runtime）。
