# QuickDock v3 插件系统 — 技术方案与可行性分析

> 基于 `docs/plugin-system-design.md` 的架构设计，对技术方案进行可行性验证，识别关键风险并给出修正式实施方案。

---

## 一、方案概述

### 1.1 目标
为 QuickDock 构建一个**多语言、进程隔离、热加载**的插件系统，允许第三方开发者用 Go/Node.js/Python 等语言扩展功能。

### 1.2 核心选型

| 维度 | 选择 | 理由 |
|---|---|---|
| 通信协议 | JSON-RPC 2.0 over stdin/stdout | LSP 验证过的模式，任意语言都有 JSON 库 |
| 进程模型 | 子进程（每个插件一个独立进程） | 崩溃隔离，操作系统级安全边界 |
| 前端渲染 | iframe + postMessage | DOM 隔离，不影响主应用 |
| 热加载 | kill + restart | 简单可靠，无需复杂状态迁移 |

### 1.3 总体架构

```
主程序 (Go)
  ├── PluginManager ──→ 子进程 JSON-RPC ──→ Plugin A (Go)
  │                                          Plugin B (Node.js)
  │                                          Plugin C (Python)
  ├── Plugin DB (plugins 表 + plugin_data)
  └── Wails 前端
       ├── PluginPanel (iframe)
       ├── 插件管理页面 (设置页)
       └── 命令面板集成 (插件命令)
```

---

## 二、技术方案详述

### 2.1 包结构

```
internal/plugin/
├── manager.go        # 插件管理器：发现/加载/卸载/生命周期
├── rpc.go            # JSON-RPC 通信层（请求/响应/回调）
├── manifest.go       # plugin.json 解析与校验
├── host.go           # Host Method 分发器 + 权限校验
├── types.go          # 公共类型定义
└── errors.go         # 插件相关错误码
```

### 2.2 关键技术实现

#### 2.2.1 JSON-RPC 通信层（rpc.go）

```go
// 核心数据结构
type PluginInstance struct {
    Manifest PluginManifest
    Cmd      *exec.Cmd
    Stdin    io.WriteCloser
    Stdout   io.ReadCloser

    sendMu   sync.Mutex        // 写入 stdin 的串行锁 ← P0 修复
    readMu   sync.Mutex
    NextID   int64
    Pending  map[int64]chan *RPCResponse

    readyCh  chan struct{}      // readLoop 就绪信号 ← P0 修复
    doneCh   chan struct{}      // 进程退出信号
    Dir      string
    Status   string
}

// 发送请求（带串行锁）
func (inst *PluginInstance) Call(method string, params interface{}, timeout time.Duration) (json.RawMessage, error)
// 后台读取 stdout
func (inst *PluginInstance) readLoop()
// 处理插件发起的回调请求
func (inst *PluginInstance) handleCallback(req *RPCRequest)
```

#### 2.2.2 插件管理器（manager.go）

```go
type Manager struct {
    plugins     map[string]*PluginInstance
    mu          sync.RWMutex
    pluginsDir  string
    db          *db.Database
    hostMethods map[string]HostMethod // 注册的 Host Method
}

// 核心流程
func (m *Manager) DiscoverAndLoad() error  // 扫描 plugins 目录
func (m *Manager) LoadPlugin(manifest, dir)  // 启动子进程
func (m *Manager) UnloadPlugin(id string)     // 停止子进程
func (m *Manager) ExecuteCommand(pluginID, cmd, input)  // 执行插件命令
func (m *Manager) handleHostCall(pluginID, method, params) // 处理回调
```

#### 2.2.3 Host Method 分发器（host.go）

```go
// 插件 → 主程序的回调统一入口
func (m *Manager) handleHostCall(pluginID string, req *RPCRequest) (interface{}, *RPCError) {
    // 1. 权限检查（permissions.network / clipboard / filesystem）
    // 2. 分发到对应 handler
    // 3. 返回结果或错误
}
```

#### 2.2.4 权限校验层

```go
func (m *Manager) checkPermission(pluginID, method string) error {
    inst := m.plugins[pluginID]
    switch method {
    case "http.get", "http.post":
        if !inst.Manifest.Permissions.Network {
            return fmt.Errorf("permission denied: network")
        }
    case "host.clipboard.read", "host.clipboard.write":
        if !inst.Manifest.Permissions.Clipboard {
            return fmt.Errorf("permission denied: clipboard")
        }
    }
    return nil
}
```

### 2.3 数据库变更

#### plugins 表（新增）

```sql
CREATE TABLE IF NOT EXISTS plugins (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    author TEXT DEFAULT '',
    description TEXT DEFAULT '',
    enabled INTEGER DEFAULT 1,
    capabilities TEXT DEFAULT '[]',
    permissions TEXT DEFAULT '{}',
    config TEXT DEFAULT '{}',
    installed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

#### plugin_data 表（已有，强制隔离）

```sql
-- 读写时必须强制 plugin_id = 当前插件，不允许插件自行指定
func (d *Database) GetPluginData(pluginID, key string) (string, error)
func (d *Database) SetPluginData(pluginID, key, value string) error
```

### 2.4 前端架构

#### 插件管理页面

- 复现当前 Sidebar 导航 + 内容区路由模式
- 导航项：`插件`（`navPlugins`）
- 内容区：插件列表（卡片风格，显示名称/版本/状态/作者/描述）
- 操作：启用/禁用/卸载/配置

#### 插件 UI 面板

- 主窗口内渲染：Shadow DOM 嵌入插件 HTML（优先）
- 独立浮层渲染：iframe（fallback，用于弹窗式插件）
- 命令面板集成：插件注册的 commands 出现在搜索列表中

#### postMessage 通信桥

```
插件 iframe → window.parent.postMessage → 前端 PluginPanel
    → 调用 ExecutePluginCommand → Go PluginManager → 子进程 JSON-RPC
    → 响应返回 → PluginPanel → iframe.contentWindow.postMessage
```

### 2.5 热键注册

```go
// PluginManager 启动时收集所有插件 commands
// 调用 tray.go 的 RegisterHotkey(hotkey, callback)
// 冲突检测：已注册的热键不允许重复注册
type HotkeyRegistry struct {
    mu       sync.Mutex
    bindings map[string]string // "Ctrl+Shift+T" → "translator.translate"
}
```

### 2.6 插件安装流程

1. 用户选择 `.zip` 包（通过文件对话框）
2. 解压前做 Zip Slip 防护（检测 `../` 路径穿越）
3. 校验 `plugin.json` 格式完整性
4. 检查 `plugins.id` 是否已存在（提示覆盖/升级）
5. 复制到 `~/.quickdock/plugins/<id>/`
6. 写入 `plugins` 数据库表
7. 调用 `LoadPlugin()` 启动

---

## 三、关键风险与修复方案

### 3.1 🔴 P0 风险（必须开工前修掉）

| 风险 | 描述 | 修复方案 | 影响 |
|---|---|---|---|
| **写入竞态** | 多个 goroutine 同时写 stdin 导致 JSON 行交错 | 加 `sendMu sync.Mutex`，`Call()` 中写入前加锁 | 不改则 100% 触发数据错乱 |
| **readLoop 启动时序** | 子进程响应到达时 readLoop 尚未开始监听 | 用 `readyCh` 同步：readLoop 开始 scan 后 close(channel)，`LoadPlugin` 等确认后再发 initialize | 不改则间歇性初始化超时 |

### 3.2 🟡 P1 风险（Phase 1 内修掉）

| 风险 | 描述 | 修复方案 |
|---|---|---|
| **子进程崩溃无感知** | 插件 exit(1) 后 Status 仍显示 "running" | 添加 `doneCh` + `cmd.Wait()` 监控 goroutine |
| **30s 超时太粗** | 通用超时不适合所有场景 | `Call()` 支持按方法设定超时：initialize 15s，execute 5s，shutdown 3s |
| **iframe sandbox 安全** | `allow-same-origin` 泄露同源策略 | 去掉 `allow-same-origin`，仅保留 `allow-scripts`，全量走 postMessage |

### 3.3 🟢 P2 风险（Phase 2 处理）

| 风险 | 描述 | 修复方案 |
|---|---|---|
| **热键冲突** | 多插件注册同一快捷键 | HotkeyRegistry 检测冲突后返回错误 |
| **Zip Slip** | 恶意插件解压时逃逸到 plugins 目录外 | `filepath.Clean` + `strings.Contains("..")` 检查 |
| **DB 隔离不强制** | 插件可伪造 plugin_id 读写其他插件数据 | Host handler 强制注入当前 plugin_id |
| **插件升级数据迁移** | 版本更新后旧数据格式不兼容 | plugins 表加 `schema_version` 字段 |

### 3.4 🔵 P3 风险（长期维护）

| 风险 | 描述 | 修复方案 |
|---|---|---|
| **孤儿进程** | 主程序强制退出后子进程残留 | 启动时记录 PID 文件，启动时清理 |
| **运行时环境缺失** | node/python 不在 PATH | 允许 plugin.json 中指定 runtime 路径，或 `exec.LookPath` fallback |

---

## 四、可行性分析

### 4.1 技术可行性 ✅

| 维度 | 评估 | 依据 |
|---|---|---|
| 通信协议 | ✅ 成熟 | JSON-RPC 2.0 是标准协议，所有语言都有实现 |
| 进程隔离 | ✅ 可靠 | OS 级进程隔离，子进程崩溃不影响主程序 |
| 多语言支持 | ✅ 已验证 | Node.js/Python/PowerShell 都能用 stdin/stdout 交互 |
| 热加载 | ✅ 可行 | kill + restart 模式，插件无状态设计则无缝 |
| 前端隔离 | ✅ 可行 | iframe/postMessage 是浏览器标准能力 |
| Windows 兼容 | ✅ 符合 | 都是标准 API，无 Linux-only 依赖 |
| 性能 | ✅ 可接受 | JSON 解析 + 子进程通信延迟约 1-5ms，交互类场景足够 |
| 安全性 | ⚠️ 需加固 | 核心权限校验 + zip slip + iframe sandbox 三个点要打好 |

### 4.2 实施可行性 ✅

| 维度 | 评估 | 依据 |
|---|---|---|
| 团队能力 | ✅ 匹配 | Go 后端 + Vue 前端，完全在现有技术栈内 |
| 现有代码衔接 | ✅ 良好 | `collections.plugin_id`、`items.plugin_data` 已预留 |
| 与 Wails v3 兼容 | ⚠️ 需验证 | iframe 加载本地 HTML 需要 Wails 静态文件服务支持 |
| 与多窗口架构兼容 | ✅ 良好 | 插件 UI 可嵌入主窗口内容区，也可开独立弹窗 |
| 增量交付 | ✅ 可行 | Phase 1-4 可独立上线，互不阻塞 |

### 4.3 风险量化

```
影响程度
 高 │ 写入竞态   子进程崩溃
    │ 时序问题   
 中 │ 30s 超时   iframe 安全
    │ 热键冲突   Zip Slip
 低 │ DB 隔离    孤儿进程
    │ 环境缺失
    └──────────────────────────
      低    中    高    发生概率
```

- **红线右侧**（P0）：写入竞态 + 时序问题 → 必须在 Phase 1 修复
- **黄线区域**（P1）：子进程崩溃 + 超时 + iframe 安全 → Phase 1 内修复
- **绿线区域**（P2-P3）：热键冲突 + Zip Slip + DB 隔离 → Phase 2 覆盖

### 4.4 不推荐的替代方案

| 方案 | 排除理由 |
|---|---|
| 用 WebSocket 代替 stdin/stdout | 增加了端口管理和连接建立的复杂度，stdin/stdout 更简单可靠 |
| 用 gRPC 代替 JSON-RPC | 太重量级，需要 protobuf 编译，增加插件开发者的门槛 |
| 插件内嵌 Wasm 运行时 | 性能好但开发门槛高，调试困难，适合高级场景（Phase 4） |
| 插件用共享库（.dll/.so） | 不支持热加载，崩溃连带主程序，Go plugin 在 Windows 不可用 |

---

## 五、修正式实施路线图

### Phase 0：安全基线（1 天）← 新增

> 在写任何业务代码之前，先打好安全基础

1. 修复写入竞态（`sendMu`）
2. 修复 readLoop 时序（`readyCh`）
3. 设计 Host Method 权限校验框架
4. 编写单元测试：并发写入测试、时序测试

### Phase 1：核心骨架（2-3 天）

1. 创建 `internal/plugin/` 包（按 2.1 节结构）
2. 实现 JSON-RPC 通信层（rpc.go）
3. 实现插件管理器（manager.go）
4. 实现 Host Method 分发器（host.go）
5. 添加子进程退出监控（`cmd.Wait()` goroutine）
6. 添加 `plugins` 表 + `plugin_data` 强制隔离
7. AppService 暴露 ListPlugins / ExecutePluginCommand
8. 前端：插件管理页面（列表 + 启用/禁用 + 卸载）

### Phase 2：前端集成（1-2 天）

1. 实现 Shadow DOM 插件渲染
2. 实现 postMessage 通信桥
3. 插件命令注册到命令面板
4. 插件热键注册 + 冲突检测
5. iframe fallback 渲染方案

### Phase 3：插件工具链（1-2 天）

1. 插件安装（Zip 解压 + 校验 + Zip Slip 防护）
2. 插件模板项目（Go / Node / Python 各一）
3. 插件开发文档
4. 2-3 个内置示例插件

### Phase 4：高级特性（可选）

1. Wasm 插件（Extism 集成）
2. 插件市场（远程 JSON 索引）
3. 文件监控自动热重载
4. 孤儿进程清理（启动时）

---

## 六、与现有代码衔接总表

| 现有文件/模块 | 需要做什么 |
|---|---|
| `internal/db/schema.go` | 新增 `plugins` 表迁移 |
| `internal/db/plugin_data.go` | 新增 `GetPluginData` / `SetPluginData` / `DeletePluginData`（强制 plugin_id 参数） |
| `services/service.go` | 注入 `*plugin.Manager` 实例 |
| `services/tool.go` | 新增 `ListPlugins` / `ExecutePluginCommand` / `InstallPlugin` / `UninstallPlugin` |
| `tray.go` | 新增 `HotkeyRegistry`，插件注册热键时检测冲突 |
| `frontend/src/App.vue` | 新增 `currentPage === 'plugins'` 分支，渲染插件管理页面 |
| `frontend/src/components/Sidebar.vue` | 导航菜单「插件」已占位 |
| `frontend/src/i18n/zh-CN.ts` / `en-US.ts` | 新增插件相关翻译键 |
| `frontend/src/components/CommandPalette.vue` | 新增插件命令搜索源（从 PluginManager 获取） |
| `frontend/src/components/SettingsModal.vue` | 可选：设置页增加插件配置入口 |

---

## 七、总结

**结论：方案可行，可以开始编码。**

共 9 项风险中，3 项 P0/P1（写入竞态、时序问题、子进程崩溃）影响核心可靠性，必须在 Phase 1 修掉。其余 6 项可以在渐进迭代中覆盖。整体工期预计 5-7 天完成 Phase 0-3（一个可用的插件系统），Phase 4 视需求决定是否实施。
