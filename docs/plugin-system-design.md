# QuickDock v3 插件系统架构设计

## 一、核心问题：Go 能动态注入吗？

| 方案 | Windows | 多语言 | 热加载 | 隔离性 | 结论 |
|---|---|---|---|---|---|
| Go `plugin` 包 | 不支持 | 仅 Go | 不支持 | 差 | 排除 |
| Yaegi（Go 解释器）| 支持 | 仅 Go | 支持 | 差（同进程）| 仅内部脚本 |
| Extism（Wasm）| 支持 | 多语言 | 支持 | 极好 | 高级插件 |
| **子进程 + JSON-RPC** | **支持** | **任意语言** | **支持** | **好（进程隔离）** | **推荐主方案** |
| HashiCorp go-plugin | 支持 | 主要 Go | 支持 | 好 | 偏重 |
| 脚本执行 | 支持 | 多语言 | 支持 | 差 | 太简陋 |

**推荐：子进程 JSON-RPC 为主 + Extism Wasm 为高级选项**

理由：子进程方案是 LSP（Language Server Protocol）验证过的成熟模式，Terraform、VSCode、Neovim 都用这种架构。插件崩溃不影响主程序，支持任意语言开发插件，热加载只需杀掉旧进程重启新进程。

---

## 二、整体架构

```
┌─────────────────────────────────────────────────────┐
│                   QuickDock 主程序                    │
│                                                      │
│  ┌──────────┐  ┌──────────┐  ┌───────────────────┐  │
│  │  Wails    │  │  Plugin   │  │   Plugin Frontend │  │
│  │  前端     │←→│  Manager  │←→│   (iframe/Shadow  │  │
│  │  (Vue 3)  │  │  (Go)    │  │    DOM 渲染)       │  │
│  └──────────┘  └────┬─────┘  └───────────────────┘  │
│                      │                               │
│               JSON-RPC 2.0 (stdin/stdout)            │
│                      │                               │
├──────────────────────┼───────────────────────────────┤
│                      │                               │
│  ┌───────────┐  ┌────┴──────┐  ┌──────────────────┐ │
│  │  Plugin A  │  │  Plugin B  │  │    Plugin C      │ │
│  │  (Go)     │  │  (Node.js) │  │    (Python)      │ │
│  │           │  │           │  │                  │ │
│  │ 后端逻辑  │  │ 后端逻辑   │  │   后端逻辑       │ │
│  │ + 前端资源 │  │ + 前端资源 │  │  + 前端资源      │ │
│  └───────────┘  └───────────┘  └──────────────────┘ │
│                                                      │
│         ~/.quickdock/plugins/                        │
│         ├── translator/plugin.json + dist/           │
│         ├── code-runner/plugin.json + dist/          │
│         └── pomodoro/plugin.json + dist/             │
└─────────────────────────────────────────────────────┘
```

---

## 三、插件目录结构

每个插件是一个独立文件夹，放在 `~/.quickdock/plugins/` 下：

```
my-plugin/
├── plugin.json          # 插件清单（必须）
├── main                 # 后端入口（可执行文件或脚本）
├── frontend/            # 前端资源（可选）
│   ├── index.html       # 插件 UI 入口
│   ├── style.css
│   └── app.js
└── README.md
```

### plugin.json 清单格式

```json
{
  "id": "com.quickdock.translator",
  "name": "翻译助手",
  "version": "1.0.0",
  "description": "选中文字一键翻译",
  "author": "Your Name",
  "icon": "icon.png",

  "backend": {
    "runtime": "native",
    "entry": "main",
    "args": []
  },

  "frontend": {
    "enabled": true,
    "entry": "frontend/index.html",
    "width": 400,
    "height": 300
  },

  "capabilities": [
    "command",
    "context-menu",
    "clipboard-watch"
  ],

  "permissions": {
    "network": true,
    "filesystem": false,
    "clipboard": true
  },

  "commands": [
    {
      "id": "translate",
      "title": "翻译选中文字",
      "hotkey": "Ctrl+Shift+T"
    }
  ]
}
```

**runtime 支持的值：**

| runtime | entry 含义 | 示例 |
|---|---|---|
| `native` | 可执行文件路径 | `"entry": "main"` (Go/Rust 编译产物) |
| `node` | Node.js 脚本 | `"entry": "index.js"` |
| `python` | Python 脚本 | `"entry": "main.py"` |
| `powershell` | PowerShell 脚本 | `"entry": "plugin.ps1"` |
| `wasm` | Wasm 文件 | `"entry": "plugin.wasm"` (高级) |

---

## 四、通信协议：JSON-RPC 2.0

主程序与插件通过 **stdin/stdout** 传输 JSON-RPC 2.0 消息，每行一个 JSON 对象。

### 4.1 主程序 → 插件（请求）

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "plugin.execute",
  "params": {
    "command": "translate",
    "input": {
      "text": "Hello World",
      "sourceLang": "en",
      "targetLang": "zh"
    }
  }
}
```

### 4.2 插件 → 主程序（响应）

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "text": "你好世界",
    "success": true
  }
}
```

### 4.3 插件 → 主程序（回调请求）

插件可以反向调用主程序的能力：

```json
{
  "jsonrpc": "2.0",
  "id": 100,
  "method": "host.clipboard.write",
  "params": {
    "text": "你好世界"
  }
}
```

### 4.4 主程序提供的 Host Methods（插件可调用的 API）

| 方法 | 说明 |
|---|---|
| `host.clipboard.read` | 读取剪贴板 |
| `host.clipboard.write` | 写入剪贴板 |
| `host.notify` | 弹出系统通知 |
| `host.dialog.open` | 打开文件对话框 |
| `host.dialog.save` | 保存文件对话框 |
| `http.get` / `http.post` | HTTP 请求（受 permissions.network 控制）|
| `db.get` / `db.set` | 读写插件专属存储（plugin_data 表）|
| `ui.show` | 显示插件前端面板 |
| `ui.hide` | 隐藏插件前端面板 |
| `log.info` / `log.error` | 日志 |

---

## 五、插件生命周期

```
安装                    加载                     运行
  │                      │                       │
  ▼                      ▼                       ▼
┌──────┐  验证  ┌──────────┐  spawn  ┌───────────────┐
│ 解压  │──────→│ 校验清单  │────────→│ 启动子进程     │
│ 到目录│       │ 检查权限  │         │ 发 initialize │
└──────┘       └──────────┘         └───────┬───────┘
                                            │
                                            ▼
                                    ┌───────────────┐
                                    │  plugin ready  │
                                    │  等待请求      │◄──┐
                                    └───────┬───────┘   │
                                            │           │
                                    收到请求 ▼           │
                                    ┌───────────────┐   │
                                    │  处理 + 响应   │───┘
                                    └───────────────┘
                                            │
                                    卸载/关闭 ▼
                                    ┌───────────────┐
                                    │ 发 shutdown   │
                                    │ kill 进程     │
                                    └───────────────┘
```

### Go 代码核心实现

```go
// internal/plugin/manager.go

package plugin

import (
    "bufio"
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "sync"
)

// PluginManifest 插件清单
type PluginManifest struct {
    ID          string   `json:"id"`
    Name        string   `json:"name"`
    Version     string   `json:"version"`
    Description string   `json:"description"`
    Author      string   `json:"author"`
    Icon        string   `json:"icon"`
    Backend     Backend  `json:"backend"`
    Frontend    Frontend `json:"frontend"`
    Capabilities []string `json:"capabilities"`
    Permissions  Permissions `json:"permissions"`
    Commands    []Command `json:"commands"`
}

type Backend struct {
    Runtime string   `json:"runtime"` // native | node | python | powershell | wasm
    Entry   string   `json:"entry"`
    Args    []string `json:"args"`
}

type Frontend struct {
    Enabled bool   `json:"enabled"`
    Entry   string `json:"entry"`
    Width   int    `json:"width"`
    Height  int    `json:"height"`
}

type Permissions struct {
    Network    bool `json:"network"`
    Filesystem bool `json:"filesystem"`
    Clipboard  bool `json:"clipboard"`
}

type Command struct {
    ID     string `json:"id"`
    Title  string `json:"title"`
    Hotkey string `json:"hotkey"`
}

// PluginInstance 运行中的插件实例
type PluginInstance struct {
    Manifest PluginManifest
    Cmd      *exec.Cmd
    Stdin    io.WriteCloser
    Stdout   io.ReadCloser
    Mu       sync.Mutex
    NextID   int64
    Pending  map[int64]chan *RPCResponse
    Dir      string // 插件目录
    Status   string // running | stopped | error
}

// Manager 插件管理器
type Manager struct {
    plugins  map[string]*PluginInstance
    mu       sync.RWMutex
    pluginsDir string
    db       *db.Database
}

// NewManager 创建插件管理器
func NewManager(pluginsDir string, database *db.Database) *Manager {
    return &Manager{
        plugins:    make(map[string]*PluginInstance),
        pluginsDir: pluginsDir,
        db:         database,
    }
}

// DiscoverAndLoad 扫描插件目录，加载所有已安装插件
func (m *Manager) DiscoverAndLoad() error {
    entries, err := os.ReadDir(m.pluginsDir)
    if err != nil {
        return err
    }
    for _, entry := range entries {
        if !entry.IsDir() {
            continue
        }
        manifestPath := filepath.Join(m.pluginsDir, entry.Name(), "plugin.json")
        manifest, err := LoadManifest(manifestPath)
        if err != nil {
            fmt.Printf("插件 %s 清单加载失败: %v\n", entry.Name(), err)
            continue
        }
        if err := m.LoadPlugin(manifest, filepath.Join(m.pluginsDir, entry.Name())); err != nil {
            fmt.Printf("插件 %s 启动失败: %v\n", manifest.ID, err)
        }
    }
    return nil
}

// LoadPlugin 启动一个插件的子进程
func (m *Manager) LoadPlugin(manifest PluginManifest, dir string) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    // 如果已在运行，先卸载
    if inst, ok := m.plugins[manifest.ID]; ok {
        m.stopPlugin(inst)
    }

    // 根据 runtime 构建启动命令
    var cmd *exec.Cmd
    entryPath := filepath.Join(dir, manifest.Backend.Entry)

    switch manifest.Backend.Runtime {
    case "native":
        cmd = exec.Command(entryPath, manifest.Backend.Args...)
    case "node":
        cmd = exec.Command("node", entryPath)
    case "python":
        cmd = exec.Command("python", entryPath)
    case "powershell":
        cmd = exec.Command("powershell", "-File", entryPath)
    default:
        return fmt.Errorf("不支持的 runtime: %s", manifest.Backend.Runtime)
    }

    cmd.Dir = dir

    stdin, err := cmd.StdinPipe()
    if err != nil {
        return err
    }
    stdout, err := cmd.StdoutPipe()
    if err != nil {
        return err
    }

    // stderr 打到日志
    cmd.Stderr = os.Stderr

    if err := cmd.Start(); err != nil {
        return fmt.Errorf("插件进程启动失败: %w", err)
    }

    inst := &PluginInstance{
        Manifest: manifest,
        Cmd:      cmd,
        Stdin:    stdin,
        Stdout:   stdout,
        Pending:  make(map[int64]chan *RPCResponse),
        Dir:      dir,
        Status:   "running",
    }

    m.plugins[manifest.ID] = inst

    // 后台读取 stdout
    go inst.readLoop()

    // 发送 initialize
    _, err = inst.Call("initialize", map[string]interface{}{
        "hostVersion": "3.0.0",
        "pluginDir":   dir,
    })
    if err != nil {
        m.stopPlugin(inst)
        return fmt.Errorf("插件初始化失败: %w", err)
    }

    return nil
}

// UnloadPlugin 卸载插件
func (m *Manager) UnloadPlugin(id string) {
    m.mu.Lock()
    defer m.mu.Unlock()
    if inst, ok := m.plugins[id]; ok {
        m.stopPlugin(inst)
        delete(m.plugins, id)
    }
}

// stopPlugin 停止插件子进程
func (m *Manager) stopPlugin(inst *PluginInstance) {
    inst.Call("shutdown", nil)
    inst.Status = "stopped"
    inst.Stdin.Close()
    inst.Cmd.Process.Kill()
    inst.Cmd.Wait()
}

// ExecuteCommand 执行插件命令（前端调用）
func (m *Manager) ExecuteCommand(pluginID, commandID string, input map[string]interface{}) (json.RawMessage, error) {
    m.mu.RLock()
    inst, ok := m.plugins[pluginID]
    m.mu.RUnlock()
    if !ok {
        return nil, fmt.Errorf("插件未加载: %s", pluginID)
    }
    return inst.Call("plugin.execute", map[string]interface{}{
        "command": commandID,
        "input":   input,
    })
}

// GetFrontendPath 获取插件前端资源路径（供 Wails 静态文件服务）
func (m *Manager) GetFrontendPath(pluginID string) (string, error) {
    m.mu.RLock()
    inst, ok := m.plugins[pluginID]
    m.mu.RUnlock()
    if !ok {
        return "", fmt.Errorf("插件未加载: %s", pluginID)
    }
    if !inst.Manifest.Frontend.Enabled {
        return "", fmt.Errorf("插件无前端: %s", pluginID)
    }
    return filepath.Join(inst.Dir, inst.Manifest.Frontend.Entry), nil
}

// ListPlugins 列出所有插件（暴露给前端）
func (m *Manager) ListPlugins() []PluginInfo {
    m.mu.RLock()
    defer m.mu.RUnlock()
    var result []PluginInfo
    for _, inst := range m.plugins {
        result = append(result, PluginInfo{
            ID:          inst.Manifest.ID,
            Name:        inst.Manifest.Name,
            Version:     inst.Manifest.Version,
            Description: inst.Manifest.Description,
            Status:      inst.Status,
            HasFrontend: inst.Manifest.Frontend.Enabled,
            Commands:    inst.Manifest.Commands,
        })
    }
    return result
}
```

### JSON-RPC 通信层

```go
// internal/plugin/rpc.go

type RPCRequest struct {
    JSONRPC string      `json:"jsonrpc"`
    ID      int64       `json:"id,omitempty"`
    Method  string      `json:"method"`
    Params  interface{} `json:"params,omitempty"`
}

type RPCResponse struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      int64           `json:"id,omitempty"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
}

// Call 发送 JSON-RPC 请求并等待响应
func (inst *PluginInstance) Call(method string, params interface{}) (json.RawMessage, error) {
    inst.Mu.Lock()
    inst.NextID++
    id := inst.NextID
    ch := make(chan *RPCResponse, 1)
    inst.Pending[id] = ch
    inst.Mu.Unlock()

    req := RPCRequest{
        JSONRPC: "2.0",
        ID:      id,
        Method:  method,
        Params:  params,
    }

    data, _ := json.Marshal(req)
    data = append(data, '\n')
    if _, err := inst.Stdin.Write(data); err != nil {
        inst.Mu.Lock()
        delete(inst.Pending, id)
        inst.Mu.Unlock()
        return nil, err
    }

    // 等待响应（带超时）
    select {
    case resp := <-ch:
        if resp.Error != nil {
            return nil, fmt.Errorf("插件错误 [%d]: %s", resp.Error.Code, resp.Error.Message)
        }
        return resp.Result, nil
    case <-time.After(30 * time.Second):
        inst.Mu.Lock()
        delete(inst.Pending, id)
        inst.Mu.Unlock()
        return nil, fmt.Errorf("插件响应超时")
    }
}

// readLoop 后台循环读取插件 stdout
func (inst *PluginInstance) readLoop() {
    scanner := bufio.NewScanner(inst.Stdout)
    scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
    for scanner.Scan() {
        var resp RPCResponse
        if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
            continue
        }

        // 匹配 pending request
        inst.Mu.Lock()
        if ch, ok := inst.Pending[resp.ID]; ok {
            ch <- &resp
            delete(inst.Pending, resp.ID)
        }
        inst.Mu.Unlock()
    }
}
```

---

## 六、前端集成

### 6.1 插件 UI 渲染方式

插件的前端资源通过 **iframe** 隔离渲染，避免污染主应用 DOM：

```vue
<!-- PluginPanel.vue -->
<template>
  <div class="plugin-panel">
    <iframe
      :src="pluginUrl"
      :style="{ width: width + 'px', height: height + 'px' }"
      sandbox="allow-scripts allow-same-origin"
      ref="iframeRef"
    />
  </div>
</template>

<script setup>
// pluginUrl 指向 Wails 静态文件服务提供的插件前端入口
// 例如: http://wails.localhost/plugins/translator/frontend/index.html
</script>
```

### 6.2 插件与前端通信（postMessage）

iframe 内的插件前端通过 `postMessage` 与主应用通信：

```javascript
// 插件前端代码 (plugin frontend/index.html)
// 调用后端
function callBackend(command, input) {
  window.parent.postMessage({
    type: 'plugin:call',
    pluginId: 'com.quickdock.translator',
    command: command,
    input: input
  }, '*')
}

// 监听响应
window.addEventListener('message', (event) => {
  if (event.data.type === 'plugin:result') {
    // 处理结果
    document.getElementById('result').textContent = event.data.result
  }
})
```

```javascript
// 主应用 PluginPanel.vue
window.addEventListener('message', async (event) => {
  if (event.data.type === 'plugin:call') {
    const result = await PluginManager.ExecuteCommand(
      event.data.pluginId,
      event.data.command,
      event.data.input
    )
    iframeRef.value.contentWindow.postMessage({
      type: 'plugin:result',
      result: result
    }, '*')
  }
})
```

---

## 七、数据库变更

在现有 schema 基础上新增 `plugins` 表：

```sql
CREATE TABLE IF NOT EXISTS plugins (
    id TEXT PRIMARY KEY,              -- com.quickdock.translator
    name TEXT NOT NULL,               -- 翻译助手
    version TEXT NOT NULL,            -- 1.0.0
    author TEXT DEFAULT '',
    description TEXT DEFAULT '',
    enabled INTEGER DEFAULT 1,        -- 用户是否启用
    config TEXT DEFAULT '{}',         -- 用户配置 JSON
    installed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- plugin_data 表（已有，用于插件专属键值存储）
-- 每个插件只能读写自己的 key
-- CREATE TABLE IF NOT EXISTS plugin_data (
--     plugin_id TEXT NOT NULL,
--     key TEXT NOT NULL,
--     value TEXT,
--     PRIMARY KEY (plugin_id, key)
-- );
```

---

## 八、插件开发示例

### 8.1 最简单的 Go 插件（翻译助手）

```go
// main.go
package main

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "net/url"
)

type RPCRequest struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      int64           `json:"id,omitempty"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
    JSONRPC string      `json:"jsonrpc"`
    ID      int64       `json:"id,omitempty"`
    Result  interface{} `json:"result,omitempty"`
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
            var params struct {
                Command string `json:"command"`
                Input   struct {
                    Text string `json:"text"`
                } `json:"input"`
            }
            json.Unmarshal(req.Params, &params)

            if params.Command == "translate" {
                result := translate(params.Input.Text)
                respond(req.ID, map[string]string{"text": result})
            }

        case "shutdown":
            respond(req.ID, map[string]string{"status": "bye"})
            os.Exit(0)
        }
    }
}

func translate(text string) string {
    resp, err := http.Get("https://api.example.com/translate?text=" + url.QueryEscape(text))
    if err != nil {
        return "翻译失败: " + err.Error()
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    return string(body)
}

func respond(id int64, result interface{}) {
    resp := RPCResponse{JSONRPC: "2.0", ID: id, Result: result}
    data, _ := json.Marshal(resp)
    fmt.Fprintln(os.Stdout, string(data))
}
```

### 8.2 Node.js 插件（更简单）

```javascript
// index.js
const readline = require('readline')
const https = require('https')

const rl = readline.createInterface({ input: process.stdin })

rl.on('line', (line) => {
    const req = JSON.parse(line)

    switch (req.method) {
        case 'initialize':
            respond(req.id, { status: 'ready' })
            break

        case 'plugin.execute':
            const { command, input } = req.params
            if (command === 'translate') {
                // 调用翻译 API...
                respond(req.id, { text: '翻译结果: ' + input.text })
            }
            break

        case 'shutdown':
            respond(req.id, { status: 'bye' })
            process.exit(0)
    }
})

function respond(id, result) {
    process.stdout.write(JSON.stringify({
        jsonrpc: '2.0', id, result
    }) + '\n')
}
```

### 8.3 Python 插件

```python
# main.py
import sys, json

for line in sys.stdin:
    req = json.loads(line.strip())

    if req['method'] == 'initialize':
        respond(req['id'], {'status': 'ready'})

    elif req['method'] == 'plugin.execute':
        params = req['params']
        if params['command'] == 'translate':
            respond(req['id'], {'text': f"翻译: {params['input']['text']}"})

    elif req['method'] == 'shutdown':
        respond(req['id'], {'status': 'bye'})
        sys.exit(0)

def respond(id, result):
    print(json.dumps({'jsonrpc': '2.0', 'id': id, 'result': result}), flush=True)
```

---

## 九、安全模型

```
┌────────────────────────────────────┐
│            权限控制层               │
│                                     │
│  plugin.json 声明 permissions:       │
│    network: true/false              │
│    filesystem: true/false           │
│    clipboard: true/false            │
│                                     │
│  Host Methods 在执行前检查权限:      │
│    http.get → 需要 network          │
│    host.clipboard → 需要 clipboard  │
│    host.dialog → 需要 filesystem    │
└────────────────────────────────────┘
```

1. **进程隔离**：插件运行在独立进程，崩溃不影响主程序
2. **权限声明**：plugin.json 声明所需权限，主程序在 Host Method 层校验
3. **存储隔离**：每个插件只能读写 `plugin_data` 表中自己 `plugin_id` 前缀的数据
4. **网络限制**：无 `network` 权限的插件，Host 拒绝 `http.*` 调用
5. **前端沙箱**：iframe 的 `sandbox` 属性限制脚本权限

---

## 十、Go 端暴露给 Wails 的 API

```go
// app.go 新增方法

// ListPlugins 列出所有已安装插件
func (a *AppService) ListPlugins() ([]plugin.PluginInfo, error) {
    return a.pluginMgr.ListPlugins(), nil
}

// InstallPlugin 从目录安装插件
func (a *AppService) InstallPlugin(dir string) error {
    return a.pluginMgr.InstallPlugin(dir)
}

// UninstallPlugin 卸载插件
func (a *AppService) UninstallPlugin(id string) error {
    return a.pluginMgr.UninstallPlugin(id)
}

// EnablePlugin 启用插件
func (a *AppService) EnablePlugin(id string) error {
    return a.pluginMgr.EnablePlugin(id)
}

// DisablePlugin 禁用插件
func (a *AppService) DisablePlugin(id string) error {
    return a.pluginMgr.DisablePlugin(id)
}

// ExecutePluginCommand 执行插件命令
func (a *AppService) ExecutePluginCommand(pluginID, commandID string, input map[string]interface{}) (string, error) {
    result, err := a.pluginMgr.ExecuteCommand(pluginID, commandID, input)
    return string(result), err
}

// GetPluginFrontendURL 获取插件前端 URL
func (a *AppService) GetPluginFrontendURL(pluginID string) (string, error) {
    return a.pluginMgr.GetFrontendURL(pluginID)
}
```

---

## 十一、实施路线图

### Phase 1：核心骨架（2-3天）
- 创建 `internal/plugin/` 包（manager.go + rpc.go + manifest.go）
- 实现插件发现、加载、JSON-RPC 通信、生命周期管理
- 新增 `plugins` 数据库表
- app.go 新增 ListPlugins / ExecutePluginCommand 等方法

### Phase 2：前端集成（1-2天）
- 创建 PluginPanel.vue（iframe 渲染）
- 设置页新增"插件管理"面板（列表、安装、启用/禁用）
- 实现 postMessage 通信桥

### Phase 3：插件开发工具（1天）
- 提供 `quickdock-plugin-init` 模板项目（Go / Node / Python 各一个）
- 编写插件开发文档
- 创建 2-3 个内置插件作为示例（翻译、取色、计算）

### Phase 4：高级特性（可选）
- Wasm 插件支持（Extism 集成）
- 插件市场（远程 JSON 索引）
- 插件热更新（文件监控自动重载）
- 插件间通信

---

## 十二、与现有代码的衔接

| 现有代码 | 衔接方式 |
|---|---|
| `collections.plugin_id` | 集合关联插件，`plugin_id` 匹配 `plugins.id` |
| `items.plugin_data` | 项存储插件自定义数据（JSON 字符串） |
| `plugin_data` 表 | 插件专属键值存储（每个插件隔离读写） |
| `validColumns` 中的预留字段 | `version`/`capability`/`permissions` 等 → 用于 `plugins` 表 |
| 命令面板窗口（paletteWindow）| 插件命令注册到命令面板的搜索结果中 |
| 全局热键系统（tray.go）| 插件声明的 `commands[].hotkey` 注册到全局热键 |
