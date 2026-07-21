# QuickDock v3 插件系统 — 实际实现文档

> 本文档反映 v3 当前代码库的实际实现状态，非设计蓝图。

---

## 一、核心架构：三种 Runtime

| Runtime | 说明 | 隔离性 | 使用场景 |
|---|---|---|---|
| `none` | 纯前端插件，无后端进程 | — | 只展示 UI 的工具（emoji-search、http-status） |
| `goja` | Go 内嵌 JS 引擎执行 | 同进程，但沙箱隔离 | 内置轻量插件（json2ts、cron-explainer） |
| `native` | 独立子进程 + JSON-RPC | 进程隔离，最佳 | 外置插件、需要系统权限的工具 |

**设计决策：** Go `plugin` 包不支持 Windows 且无热加载，排除。Extism Wasm 保留为未来选项。

---

## 二、整体架构

```
┌─────────────────────────────────────────────────────────┐
│                    QuickDock 主程序                       │
│                                                          │
│  ┌──────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │  Wails    │  │  AppService   │  │  PluginManager    │  │
│  │  前端     │←→│  (services/)  │←→│  (internal/plugin)│  │
│  │  (Vue 3)  │  │  18 个绑定方法 │  │  Manager          │  │
│  └──────────┘  └──────┬───────┘  │  PluginInstance[]  │  │
│                        │          │  goja runtime       │  │
│                        │          │  PluginWindowMgr   │  │
│                        │          │  PluginHotkeyReg   │  │
│                        │          └────────┬──────────┘  │
│                        │                   │              │
│                  JSON-RPC 2.0 (stdin/stdout)              │
│                        │                   │              │
│                        │          ┌────────┴──────────┐  │
│                        │          │  native 子进程     │  │
│                        │          │  (任意语言)        │  │
│                        │          └───────────────────┘  │
│                        │                                  │
│                 ┌──────┴───────┐                          │
│                 │  独立 WebviewWindow  (/#/plugin/{id})   │
│                 │  PluginPage.vue  iframe + Blob URL     │
│                 │  Nonce 握手安全                         │
│                 └──────────────┘                          │
│                                                          │
│  plugins/builtin/          ~/.quickdock/plugins/         │
│  ├── json2ts/              ├── my-plugin/                │
│  ├── cron-explainer/       │   ├── plugin.json           │
│  ├── port-scanner/         │   ├── main (可执行文件)      │
│  ├── ... (共 19 个内置)     │   └── frontend/             │
│  └── common.css/.js        └── ...                       │
└─────────────────────────────────────────────────────────┘
```

---

## 三、插件清单 (plugin.json)

```json
{
  "id": "com.quickdock.translator",
  "name": "翻译助手",
  "version": "1.0.0",
  "description": "选中文字一键翻译",
  "author": "Your Name",
  "icon": "icon.svg",
  "category": "tools",

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

  "capabilities": ["command", "context-menu"],

  "permissions": {
    "network": true,
    "filesystem": false,
    "clipboard": true
  },

  "commands": [
    {
      "id": "translate",
      "title": "翻译选中文字",
      "hotkey": "Ctrl+Shift+T",
      "keywords": ["翻译", "translate", "翻"],
      "aliases": ["fy", "翻"],
      "prefix": "/tr",
      "matchPattern": "^[a-zA-Z].*"
    }
  ]
}
```

### runtime 支持的值

| runtime | entry 含义 | 示例 |
|---|---|---|
| `none` | 无后端 | 纯前端插件，entry 可省略 |
| `goja` | JavaScript 文件 | `"entry": "main.js"`（Go 内嵌引擎执行） |
| `native` | 可执行文件 | `"entry": "main"` (Go/Rust 编译产物) |
| `node` | (预留) | — |
| `python` | (预留) | — |
| `wasm` | (预留) | — |

### Command 字段

| 字段 | 说明 |
|---|---|
| `keywords` | 中文拼音搜索关键词（命令面板查找用） |
| `aliases` | 短别名（如 "fy", "翻"） |
| `prefix` | Slash 前缀触发（如 `/tr` 在命令面板输入 `/tr` 激活） |
| `matchPattern` | 正则匹配模式（命令面板输入文本与该正则匹配时激活） |

---

## 四、PluginManager

### Manager 结构

`internal/plugin/manager.go` — 核心管理器，负责插件生命周期。

```go
type Manager struct {
    plugins           map[string]*PluginInstance
    mu                sync.RWMutex
    pluginsDir        string
    hostMethods       map[string]HostMethod     // 回调注册表
    pidFilePath       string                     // 孤儿进程清理
    pidMu             sync.Mutex
    healthCheckStopCh chan struct{}
    healthCheckWg     sync.WaitGroup
}
```

### 主要方法

| 方法 | 说明 |
|---|---|
| `NewManager(pluginsDir)` | 创建管理器，注册默认 HostMethod，清理孤儿进程，启动健康检查 |
| `DiscoverAndLoad()` | 遍历 pluginsDir，加载所有含 plugin.json 的插件 |
| `LoadPlugin(manifest, dir)` | 按 runtime 分支：none→就绪，goja→执行 JS，native→启动子进程+initialize |
| `UnloadPlugin(id)` | 从内存移除（stop + delete） |
| `StopPlugin(id)` | 停止但保留在列表中（禁用时调用） |
| `ExecuteCommand(pluginID, commandID, input)` | 执行命令，按 runtime 分派 |
| `ListPlugins()` | 列出所有插件信息 |
| `GetFrontendPath(pluginID)` | 获取前端入口路径 |
| `InstallFromZip(zipPath)` | 从 ZIP 安装（含安全校验）|
| `UninstallPlugin(id)` | 删除插件目录 |
| `ShutdownAll()` | 停止所有插件 + 健康检查 + 清理 PID 文件 |

### PluginInstance 运行时结构

```go
type PluginInstance struct {
    Manifest PluginManifest
    Cmd      *exec.Cmd          // native 子进程
    Stdin    io.WriteCloser
    Stdout   io.ReadCloser
    DB       *sql.DB            // goja 插件专用 SQLite
    sendMu   sync.Mutex         // 串行化 stdin 写入
    readMu   sync.Mutex
    NextID   int64
    Pending  map[int64]chan *RPCResponse
    readyCh  chan struct{}       // readLoop 启动信号
    doneCh   chan struct{}       // 关闭信号
    closeOnce sync.Once
    stopped  atomic.Bool
    Dir      string
    Status   string              // running | stopped | crashed | unresponsive
    MissedPings    int
    UnresponsiveAt time.Time
    VM       *goja.Runtime       // goja JS 引擎
}
```

### 关键特性

- **自动重启（watchPlugin）：** native 插件崩溃后自动重启，最多 3 次，指数退避 2s/4s/6s
- **健康检查（startHealthCheck）：** 后台 30s ticker，ping 所有 running 插件，连续 3 次无响应标记 unresponsive
- **孤儿进程清理（cleanupOrphans）：** 启动时检查上次残留 PID，通过 tasklist 验证并终止
- **安全退出：** closeOnce 确保 doneCh 只关闭一次，防止重复 close panic

---

## 五、JSON-RPC 通信协议

主程序与 native 插件通过 **stdin/stdout** 传输 JSON-RPC 2.0 消息，每行一个 JSON 对象。

### 请求格式

```json
{"jsonrpc":"2.0","id":1,"method":"plugin.execute","params":{"command":"translate","input":{"text":"Hello"}}}
```

### 响应格式

```json
{"jsonrpc":"2.0","id":1,"result":{"text":"你好"}}
```

### 超时策略

| 场景 | 超时 |
|---|---|
| 默认 Call | 30s |
| `ExecuteCommand` | 20s |
| `initialize` | 15s |
| `health.ping` | 5s |

### readLoop 实现要点

- `sendMu` 互斥锁防止多协程写入 stdin 交错
- `readyCh` 关闭信号确保 readLoop 已开始监听再发送 initialize
- 1MB buffer 应对大响应
- 先尝试解析为请求（含 Method），再解析为响应

---

## 六、Goja JS 引擎插件

v3 内置 goja（纯 Go JavaScript 引擎），无需 Node.js 即可运行 JS 插件。

### 注入的 API

| 对象 | 方法 | 说明 |
|---|---|---|
| `api.log` | `info(msg)`, `error(msg)` | 日志 |
| `api.db` | `get(key)`, `set(key, value)` | 插件专属 KV 存储 |
| `api.crypto` | `md5()`, `sha256()`, `base64Encode()`, `base64Decode()`, `urlEncode()`, `urlDecode()`, `htmlEncode()`, `htmlDecode()` | 加解密工具 |

### Goja 插件的优缺点

**优点：** 零依赖、启动快（无需子进程）、内存共享（直接调 Go 函数）
**缺点：** 同进程无隔离、JS 能力受限（无网络请求、无文件系统 API）、CPU 密集型任务阻塞主协程

---

## 七、Host Methods（插件回调主程序）

插件（native 或 goja）可反向调用主程序能力。Manager 注册的默认 Host Method 共 13 个：

| 方法 | 说明 | 实现状态 |
|---|---|---|
| `log.info` / `log.error` | 日志 | ✅ 已实现 |
| `host.notify` | 系统通知 | ✅ 已实现（目前仅打日志） |
| `host.ping` | 健康检查回执 | ✅ 已实现 |
| `host.clipboard.read` / `host.clipboard.write` | 剪贴板读写 | ⏳ 占位 |
| `host.dialog.open` / `host.dialog.save` | 文件对话框 | ⏳ 占位 |
| `http.get` / `http.post` | HTTP 请求 | ⏳ 占位 |
| `db.get` / `db.set` | 插件专属存储 | ⏳ 占位 |
| `ui.show` / `ui.hide` | 显示/隐藏插件窗口 | ⏳ 占位 |

### 权限校验

Host Methods 在执行前检查 plugin.json 声明的 Permissions：

- `host.clipboard.*` → 需要 `permissions.clipboard = true`
- `http.*` → 需要 `permissions.network = true`
- `host.dialog.*` → 需要 `permissions.filesystem = true`

### 扩展点

`Manager.InjectHostMethod(name, handler)` — 供 services 层覆盖默认实现。

---

## 八、插件窗口管理（PluginWindowManager）

v3 不再使用 iframe 嵌入主窗口，每个插件拥有独立的 Wails `WebviewWindow`。

```go
type PluginWindowManager struct {
    mu         sync.Mutex
    windows    map[string]*application.WebviewWindow
    app        *application.App
    baseWidth  int    // 800
    baseHeight int    // 600
}
```

| 方法 | 说明 |
|---|---|
| `Show(pluginID, title, showInTaskbar)` | 显示/创建插件窗口 |
| `ShowInPanel(pluginID, title)` | 在内嵌面板窗口显示 |
| `ShowAsWindow(pluginID, title)` | 在独立窗口显示 |
| `Hide(pluginID)` | 隐藏窗口 |
| `CloseAll()` | 关闭所有插件窗口 |
| `InjectInitText(pluginID, text, command)` | 跨窗口传递初始文本（命令面板→插件窗口） |

窗口 URL 为 `/#/plugin/{id}`，路由到 PluginPage.vue 渲染。

### 前端通信安全（Nonce 握手）

PluginPage.vue 与插件 iframe 之间使用 nonce 机制防止跨源消息伪造：

1. 后端通过 `GetPluginFrontendPage(pluginId)` 返回服务端处理过的 HTML（内联 CSS/JS）
2. HTML 转为 Blob URL 加载到 iframe
3. 生成随机 `pluginNonce`（`Math.random().toString(36).slice(2, 12)`）
4. `onIframeLoad` → postMessage 发送 `plugin:theme` + `plugin:init`，均携带 nonce
5. `messageHandler` 验证 nonce 后才处理 `plugin:execute` 消息
6. 回复 `plugin:result` 也携带 nonce

---

## 九、数据库

### plugins 表

```sql
CREATE TABLE plugins (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    author TEXT DEFAULT '',
    description TEXT DEFAULT '',
    category TEXT DEFAULT '',
    icon TEXT DEFAULT '',
    enabled INTEGER DEFAULT 1,
    usage_count INTEGER DEFAULT 0,
    capabilities TEXT DEFAULT '[]',
    permissions TEXT DEFAULT '{}',
    config TEXT DEFAULT '{}',
    installed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

### plugin_data 表（KV 存储）

```sql
CREATE TABLE plugin_data (
    plugin_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT,
    PRIMARY KEY (plugin_id, key)
);
```

### plugin_exec_logs 表（执行日志）

```sql
CREATE TABLE plugin_exec_logs (
    id TEXT PRIMARY KEY,
    plugin_id TEXT NOT NULL,
    command_id TEXT NOT NULL,
    executed_at TEXT DEFAULT '',
    executed_ts INTEGER NOT NULL DEFAULT 0,
    success INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    result TEXT DEFAULT '',
    error TEXT DEFAULT '',
    trigger TEXT DEFAULT 'manual'
);
```

最大保留 500 条记录，超限时删除最旧记录。

### 使用计数

`RecordUsage("plugin:{id}.{cmdID}")` — 通过 frecency 表记录插件命令使用频次，`GetAllPluginUsageCounts()` 一条 SQL 聚合所有次数。

---

## 十、前端集成

### 10.1 AppService 暴露的绑定方法（共 18 个）

| 方法 | 说明 |
|---|---|
| `ListPlugins()` | 列出所有插件 + 补充 UsageCount |
| `InstallPlugin(zipPath)` | 从 ZIP 路径安装 |
| `SelectAndInstallPlugin()` | 原生文件对话框选 ZIP |
| `InstallPluginFromBytes(fileName, fileData)` | 拖拽安装 |
| `EnablePlugin(id)` / `DisablePlugin(id)` | 启用/禁用 + 热键注册/注销 |
| `UninstallPlugin(id)` | 卸载 + 清理热键 + 清理 DB |
| `ExecutePluginCommand(pluginID, commandID, input)` | 执行命令 + 记录日志 + 使用次数 |
| `GetPluginFrontendURL(pluginID)` | 获取前端路径 |
| `GetPluginIcon(pluginID)` | 获取图标 base64（DB 缓存） |
| `GetPluginFrontendPage(pluginID)` | 获取内联 HTML（mtime 缓存） |
| `ShowPluginWindow(pluginID)` / `HidePluginWindow(pluginID)` | 窗口控制 |
| `MinimizePluginWindow(pluginID)` / `ToggleMaximizePluginWindow(pluginID)` | 窗口操作 |
| `SetPendingPluginInit(text, command)` / `GetAndClearPendingPluginInit()` | 跨窗口传参 |
| `ListPluginExecLogs(limit)` | 执行日志查询 |

### 10.2 命令面板集成（CommandPalette.vue）

插件命令深度集成到命令面板：

- **索引构建：** 遍历 `PluginInfo.commands`，按 title/keywords/aliases 建立搜索索引
- **评分算法：** `calcPluginScore()` 按匹配程度给分，最高 100
- **Slash 前缀：** 输入 `/xxx` 直接激活匹配插件
- **内联模式：** 命令面板内嵌入 iframe 显示插件 UI（`inlinePluginId`）
- **分离模式：** `detachPlugin()` 从内联转为独立窗口
- **执行缓存：** `pluginResultCache` 显示最近一次执行结果

### 10.3 热键注册（PluginHotkeyRegistry）

```go
type PluginHotkeyRegistry struct {
    mu       sync.Mutex
    accelMap map[string]string    // "Ctrl+Shift+T" → "pluginID.commandID"
    byPlugin map[string][]string  // pluginID → []accel
}
```

- `Register(accel, pluginID, commandID)` — 冲突检测，已占用则返回错误
- `UnregisterAll(pluginID)` — 禁用/卸载时批量注销
- Windows 全局热键通过 tray.go 的 `RegisterHotKey` 实现

---

## 十一、内置插件（19 个）

### Goja 插件（有后端逻辑）

| 插件 ID | 功能 | 前端 |
|---|---|---|
| json2ts | JSON ↔ TypeScript 类型 | index.html + app.js |
| case-converter | 大小写/命名风格转换 | index.html |
| code-formatter | 代码格式化 | index.html |
| cron-explainer | Cron 表达式解析 | index.html |
| sql-formatter | SQL 格式化 | index.html |
| text-encoder | 文本编码转换 | index.html |
| data-converter | 数据格式转换 | index.html |
| text-diff | 文本差异对比 | index.html + app.js |
| regex-extractor | 正则提取 | index.html + app.js |
| file-compare | 文件对比 | index.html + app.js |
| time-converter | 时间戳/时区转换 | index.html + app.js |
| calcsheet | 计算表格 | index.html + app.js |

### Pure Frontend 插件（runtime: none）

| 插件 ID | 功能 | 依赖 |
|---|---|---|
| emoji-search | Emoji 搜索 | — |
| http-status | HTTP 状态码查询 | — |
| jwt-decoder | JWT 解码 | — |
| markdown-preview | Markdown 预览 | — |
| hosts-manager | hosts 文件管理 | system-tools.exe |
| port-scanner | 端口扫描 | system-tools.exe |
| wifi-manager | WiFi 管理 | system-tools.exe |

### 共享资源

`plugins/builtin/common.css` — 主题变量、布局类（`.p-app`/`.p-toolbar`/`.p-btn`）、浅色深色适配
`plugins/builtin/common.js` — 挂载到 `window.QD`：`escapeHtml`、`copyText`、`fallbackCopy`

---

## 十二、安全模型

| 层级 | 防护措施 |
|---|---|
| **进程隔离** | native 插件运行在独立子进程，崩溃不影响主程序 |
| **JS 沙箱** | goja 引擎纯 Go 实现，无文件系统/网络能力，仅暴露受限 API |
| **权限声明** | plugin.json 声明所需权限，Host Method 层运行时校验 |
| **Nonce 握手** | iframe postMessage 携带随机 nonce，防止跨源消息伪造 |
| **存储隔离** | 每个插件只能读写 `plugin_data` 中自己 plugin_id 的数据 |
| **ZIP 安全** | Zip Slip 路径穿越防护、100MB 解压上限、50MB 单文件上限、回滚机制 |
| **前端沙箱** | iframe `sandbox="allow-scripts allow-same-origin allow-modals"` |
| **孤儿清理** | PID 文件 + tasklist 验证，防止上次崩溃残留进程 |
| **崩溃恢复** | watchPlugin 最多重启 3 次 + 指数退避，防止无限重启 |
| **执行日志** | 记录每次执行的命令、耗时、结果、错误，便于审计 |

---

## 十三、与现有代码的衔接

| 现有代码 | 衔接方式 |
|---|---|
| `collections.plugin_id` | 集合关联插件 |
| `items.plugin_data` | 项存储插件自定义数据（JSON 字符串） |
| 命令面板窗口 | 插件命令注册到命令面板搜索索引，支持 slash 前缀 |
| 全局热键系统 (tray.go) | PluginHotkeyRegistry 管理插件热键注册/注销 |
| 内置插件 (plugins/builtin/) | `go:embed` 编译期嵌入，首次启动自动安装 |
| system-tools.exe | 被 hosts-manager/port-scanner/wifi-manager 共享的系统工具子进程 |

---

## 十四、实施状态

| 模块 | 状态 | 备注 |
|---|---|---|
| Manager 核心（发现/加载/卸载） | ✅ 已完成 | 含 watchPlugin 自动重启 |
| JSON-RPC 通信 | ✅ 已完成 | 含超时/P0 锁修复/readyCh |
| Goja JS 引擎集成 | ✅ 已完成 | 含 crypto/db/log API |
| Host Methods | ✅ 已完成 | 13 个已注册（部分占位）|
| 插件窗口管理 | ✅ 已完成 | 独立 WebviewWindow |
| 安全（Nonce/ZIP/权限） | ✅ 已完成 | 全链路安全 |
| 数据库（plugins/plugin_data/logs） | ✅ 已完成 | 含自动迁移 |
| 前端管理页面 | ✅ 已完成 | PluginManagerPage.vue |
| 前端命令面板集成 | ✅ 已完成 | 评分/内联/分离模式 |
| 热键注册 | ✅ 已完成 | PluginHotkeyRegistry |
| 19 个内置插件 | ✅ 已完成 | goja + pure frontend |
| 健康检查 | ✅ 已完成 | 30s ticker + ping |
| 孤儿进程清理 | ✅ 已完成 | PID 文件 + tasklist |
| 执行日志追踪 | ✅ 已完成 | 500 条上限 + 自动裁剪 |
| Wasm 插件支持 | ❌ 未实现 | Extism 预留 |
| 插件市场 | ❌ 未实现 | 远程 JSON 索引 |
| 插件间通信 | ❌ 未实现 | 未来考虑 |
