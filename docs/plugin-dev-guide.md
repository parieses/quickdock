# QuickDock v3 插件开发指南

## 概述

QuickDock v3 支持通过**子进程 + JSON-RPC 2.0** 协议扩展功能。插件运行在独立进程中，崩溃不影响主程序，支持 Go / Node.js / Python / PowerShell 等多种语言开发。

---

## 目录结构

一个插件是一个文件夹，放在 `~/.quickdock/plugins/<plugin-id>/` 下：

```
my-plugin/
├── plugin.json      # 插件清单（必须）
├── main.exe         # 后端入口（native runtime，Go/Rust 编译产物）
├── index.js         # 或 Node.js 脚本
├── main.py          # 或 Python 脚本
├── frontend/        # 前端资源（可选）
│   ├── index.html
│   ├── style.css
│   └── app.js
└── README.md
```

---

## plugin.json 清单格式

```json
{
  "id": "com.quickdock.my-plugin",
  "name": "我的插件",
  "version": "1.0.0",
  "description": "插件功能描述",
  "author": "Your Name",
  "icon": "icon.png",

  "backend": {
    "runtime": "native",
    "entry": "main.exe",
    "args": []
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
      "hotkey": ""
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
| `backend.runtime` | 运行环境：`native` / `node` / `python` / `powershell` |
| `backend.entry` | 入口文件名（相对插件根目录）|
| `permissions` | 权限声明，影响插件能调用的 Host API |
| `commands` | 注册到命令面板的命令列表 |

### Runtime 说明

| runtime | entry 示例 | 说明 |
|---|---|---|
| `native` | `main.exe` | 可执行文件（Go/Rust 编译产物）|
| `node` | `index.js` | Node.js 脚本 |
| `python` | `main.py` | Python 脚本 |
| `powershell` | `plugin.ps1` | PowerShell 脚本 |

---

## 通信协议：JSON-RPC 2.0

插件通过 stdin 接收请求，通过 stdout 发送响应。每行一个完整的 JSON 对象。

### 主程序 → 插件（请求）

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"hostVersion":"3.0.0","pluginDir":"/path/to/plugin"}}
```

### 插件 → 主程序（响应）

```json
{"jsonrpc":"2.0","id":1,"result":{"status":"ready"}}
```

### 插件 → 主程序（回调请求）

插件可以主动调用主程序的能力：

```json
{"jsonrpc":"2.0","id":100,"method":"host.clipboard.write","params":{"text":"Hello"}}
```

---

## 生命周期

### 1. initialize（主程序 → 插件）

插件启动后，主程序立即发送 `initialize` 请求。插件必须在**15 秒内**返回 `{"status": "ready"}`。

```json
// 请求
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"hostVersion":"3.0.0","pluginDir":"..."}}

// 响应
{"jsonrpc":"2.0","id":1,"result":{"status":"ready","pluginId":"com.quickdock.my-plugin"}}
```

### 2. plugin.execute（主程序 → 插件）

用户在命令面板执行插件命令时触发。

```json
// 请求
{"jsonrpc":"2.0","id":2,"method":"plugin.execute","params":{"command":"hello","input":{"name":"World"}}}

// 响应
{"jsonrpc":"2.0","id":2,"result":{"message":"Hello, World!"}}
```

### 3. shutdown（主程序 → 插件）

插件被卸载/禁用/主程序退出时触发。插件应在 **3 秒内**退出。

```json
// 请求（通知，无需响应）
{"jsonrpc":"2.0","method":"shutdown","params":null}
```

---

## Host Methods（插件可调用的主程序 API）

插件在收到请求后，可以通过 stdout 向主程序发起回调请求。

### 支持的 Host Methods

| 方法 | 说明 | 所需权限 |
|---|---|---|
| `log.info` | 记录日志 | 无需权限 |
| `log.error` | 记录错误日志 | 无需权限 |
| `host.notify` | 弹出系统通知 | 无需权限 |
| `host.clipboard.read` | 读取剪贴板文本 | `clipboard: true` |
| `host.clipboard.write` | 写入剪贴板文本 | `clipboard: true` |
| `db.get` | 读取插件专属存储 | 无需权限 |
| `db.set` | 写入插件专属存储 | 无需权限 |

### 调用示例

```json
// 插件发送请求到 stdout
{"jsonrpc":"2.0","id":101,"method":"host.clipboard.write","params":{"text":"剪贴板内容"}}

// 主程序返回结果到 stdin
{"jsonrpc":"2.0","id":101,"result":{"success":true}}
```

---

## 权限声明

插件在 `plugin.json` 中声明所需权限，主程序会在 Host Method 调用前校验：

```json
"permissions": {
  "network": false,    // 能否发起 HTTP 请求
  "filesystem": false, // 能否访问文件对话框
  "clipboard": true    // 能否读写剪贴板
}
```

---

## 开发语言模板

### Go 插件

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req RPCRequest
		json.Unmarshal(scanner.Bytes(), &req)

		switch req.Method {
		case "initialize":
			respond(req.ID, map[string]string{"status": "ready"})
		case "plugin.execute":
			// 处理命令
			respond(req.ID, map[string]string{"result": "ok"})
		case "shutdown":
			os.Exit(0)
		}
	}
}

func respond(id int64, result interface{}) {
	data, _ := json.Marshal(RPCResponse{JSONRPC: "2.0", ID: id, Result: result})
	fmt.Fprintln(os.Stdout, string(data))
}
```

**编译**: `go build -o main.exe main.go`

### Node.js 插件

```javascript
const readline = require('readline')
const rl = readline.createInterface({ input: process.stdin })

rl.on('line', (line) => {
  const req = JSON.parse(line)
  switch (req.method) {
    case 'initialize':
      respond(req.id, { status: 'ready' })
      break
    case 'plugin.execute':
      respond(req.id, { result: 'ok' })
      break
    case 'shutdown':
      process.exit(0)
  }
})

function respond(id, result) {
  process.stdout.write(JSON.stringify({jsonrpc:'2.0',id,result})+'\n')
}
```

### Python 插件

```python
import sys, json

for line in sys.stdin:
    req = json.loads(line.strip())
    if req['method'] == 'initialize':
        respond(req['id'], {'status': 'ready'})
    elif req['method'] == 'plugin.execute':
        respond(req['id'], {'result': 'ok'})
    elif req['method'] == 'shutdown':
        sys.exit(0)

def respond(id, result):
    print(json.dumps({'jsonrpc':'2.0','id':id,'result':result}), flush=True)
```

---

## 安装与调试

### 安装插件

1. 将插件打包为 `.zip` 文件（`plugin.json` 必须在根目录）
2. 打开 QuickDock → 插件管理页面
3. 拖入 zip 文件或点击「安装插件」选择文件
4. 安装成功后插件自动启动

### 查看日志

插件 stdout 用于 JSON-RPC 通信，stderr 输出会显示在主程序日志中。
插件可以通过 `log.info` / `log.error` Host Method 向主程序输出日志。

### 调试建议

1. 先用模板创建项目，确认基础通信正常
2. 插件首次开发建议使用 `node` 或 `python` runtime（无需编译）
3. 用 `log.info` 输出调试信息
4. 检查 `~/.quickdock/plugins/` 目录确认插件已安装

---

## 注意事项

1. **stdout 专用于 JSON-RPC**: 不要用 `fmt.Println` / `console.log` / `print` 输出非 JSON 内容到 stdout，这会破坏通信协议
2. **错误处理**: 始终用 JSON-RPC 错误响应（而非 stdout 文本）返回错误
3. **热键冲突**: 如果多个插件声明相同热键，后安装的插件注册会失败
4. **存储隔离**: 插件的 `db.*` Host Method 只能读写自己 `plugin_id` 的数据
5. **不要写死路径**: 使用 `initialize` 请求中的 `pluginDir` 参数获取插件目录路径

---

## 完整示例

参见 `plugins/templates/` 目录下的三个模板项目（Go / Node / Python）。

以及 `plugins/builtin/` 目录下的内置示例插件。
