# QuickDock v3 插件系统 — 开发计划

> 基于 `docs/plugin-system-design.md`（架构设计）和 `docs/plugin-technical-proposal.md`（技术方案）制定的详细开发计划。
> 工期估算：**6-8 天**（Phase 0-3），Phase 4 可选。

---

## Phase 0：安全基线（1 天）

> **先打好安全基础再写业务代码**，避免后续 debug 成本指数级增长。

### 任务 0.1 — 创建 internal/plugin/ 包骨架

**文件**：`internal/plugin/types.go`、`internal/plugin/errors.go`

- [ ] 定义 `PluginManifest`、`Backend`、`Frontend`、`Permissions`、`Command` 结构体（从设计文档搬运）
- [ ] 定义 `PluginInstance` 结构体（含 `sendMu`、`readyCh`、`doneCh`）
- [ ] 定义 `RPCRequest`、`RPCResponse`、`RPCError` 结构体
- [ ] 定义插件错误码常量

**验收**：`go build ./internal/plugin/...` 通过

### 任务 0.2 — 实现 JSON-RPC 通信层（含竞态修复）

**文件**：`internal/plugin/rpc.go`

- [ ] 实现 `Call(method, params, timeout)` 方法
  - [ ] `sendMu.Lock()` 串行化 stdin 写入  ← **P0 修复**
  - [ ] ID 递增 + pending map 注册
  - [ ] 带超时的 select 等待响应
  - [ ] 超时后清理 pending channel
- [ ] 实现 `readLoop()` 方法
  - [ ] 启动后 `close(inst.readyCh)` 通知就绪  ← **P0 修复**
  - [ ] bufio.Scanner 循环读取 stdout
  - [ ] 匹配 pending 响应 → 写入 channel
  - [ ] 区分**响应**（有 ID）和**回调请求**（无 ID 或特定 method 前缀）
- [ ] 实现 `handleCallback(req)` — 将插件发起的回调请求转给 Manager

**验收**：
- [ ] 写单元测试：并发 10 个 Call 不产生 JSON 交错
- [ ] 写单元测试：readLoop 就绪后才发请求

### 任务 0.3 — 实现 manifest 解析

**文件**：`internal/plugin/manifest.go`

- [ ] `LoadManifest(path string) (*PluginManifest, error)` — 读取并校验 plugin.json
- [ ] 校验字段完整性：id、name、version、backend.runtime、backend.entry
- [ ] 校验 runtime 值是否合法（native/node/python/powershell）
- [ ] 校验 ID 格式：`com.quickdock.xxx`

**验收**：能 `go build` 通过，能解析合法的 plugin.json

---

## Phase 1：核心骨架（3 天）

### 任务 1.1 — 实现插件管理器

**文件**：`internal/plugin/manager.go`

- [ ] `NewManager(pluginsDir, db)` — 构造函数
- [ ] `DiscoverAndLoad()` — 扫描 plugins 目录，加载所有插件
- [ ] `LoadPlugin(manifest, dir)` — 启动子进程核心流程
  - [ ] 根据 runtime 构建 exec.Cmd
  - [ ] 建立 stdin/stdout pipe
  - [ ] 启动进程
  - [ ] 启动 readLoop goroutine
  - [ ] 等待 readyCh 就绪
  - [ ] 发送 `initialize` 请求
  - [ ] 初始化失败则 stopPlugin 并返回错误
- [ ] `stopPlugin(inst)` — 停止插件
  - [ ] 发送 `shutdown` 请求
  - [ ] 设置 Status = "stopped"
  - [ ] close stdin
  - [ ] Process.Kill()
  - [ ] cmd.Wait()
- [ ] `UnloadPlugin(id)` — 从 Manager 移除
- [ ] `ExecuteCommand(pluginID, commandID, input)` — 执行命令
- [ ] `ListPlugins()` — 返回所有插件状态列表

**子进程退出监控**（P1 修复）：

- [ ] readLoop goroutine 中监听 `cmd.Wait()`
- [ ] 进程退出时设置 Status = "crashed" / "exited"
- [ ] 通过 channel 通知 Manager

**验收**：能手动启动一个简单的 Go 插件子进程并通信

### 任务 1.2 — 实现 Host Method 分发器

**文件**：`internal/plugin/host.go`

- [ ] Host method 注册表（map[string]HandlerFunc）
- [ ] `RegisterHostMethod(name, handler)` — 注册方法
- [ ] `handleHostCall(pluginID, method, params)` — 分发入口
  - [ ] 权限检查（permissions 校验）
  - [ ] 方法分发
  - [ ] 结果返回
- [ ] 实现基础 Host Method
  - [ ] `log.info` / `log.error`
  - [ ] `host.notify`
  - [ ] `host.clipboard.read` / `host.clipboard.write`
  - [ ] `db.get` / `db.set`
- [ ] 权限校验：在分发前检查 plugin.json 的 permissions

**超时优化**（P1 修复）：

- [ ] `initialize` 超时 15s
- [ ] `plugin.execute` 超时 5s（可配置）
- [ ] `shutdown` 超时 3s

**验收**：插件能通过 host 方法写剪贴板（需权限声明）

### 任务 1.3 — 数据库变更

**文件**：`internal/db/schema.go`、`internal/db/plugin_data.go`

- [ ] schema.go 新增 `plugins` 表迁移
- [ ] `db/plugin_data.go` 新增方法：
  - [ ] `GetPluginData(pluginID, key)` — 强制绑定 pluginID
  - [ ] `SetPluginData(pluginID, key, value)` — 强制绑定 pluginID
  - [ ] `DeletePluginData(pluginID, key)`
  - [ ] `ListPluginData(pluginID)`

**验收**：`go build` 通过，确认表迁移 SQL 正确

### 任务 1.4 — 暴露 Wails API

**文件**：`services/service.go`、`services/plugin.go`

- [ ] `service.go` 注入 `*plugin.Manager`
- [ ] 新建 `services/plugin.go`，暴露 Wails 绑定方法：
  - [ ] `ListPlugins()`
  - [ ] `ExecutePluginCommand(pluginID, commandID, input)`
  - [ ] `InstallPlugin(zipPath)`（留空，Phase 3 实现）
  - [ ] `UninstallPlugin(id)`
  - [ ] `EnablePlugin(id)` / `DisablePlugin(id)`
- [ ] `main.go` 中初始化 PluginManager 并传给 AppService

**插件命令注册到命令面板**：

- [ ] `palette.go` 中新增插件命令搜索源
- [ ] 插件命令显示在命令面板搜索结果中

**验收**：前端能调用 `ListPlugins()` 返回数据（即使为空列表）

---

## Phase 2：前端集成（2 天）

### 任务 2.1 — 插件管理页面

**文件**：`frontend/src/components/PluginManagerPage.vue`

- [ ] 插件列表（卡片式布局）
  - [ ] 显示：图标、名称、版本、作者、描述
  - [ ] 状态指示器（运行中/已停止/崩溃）
  - [ ] 启用/禁用开关
  - [ ] 卸载按钮
- [ ] 插件详情（点击展开或弹窗）
  - [ ] 权限声明展示
  - [ ] 注册的命令列表
  - [ ] 配置项（从 plugin.json config 读取）
- [ ] 空状态（暂无插件，引导安装）

**与 App.vue 对接**：

- [ ] 注册 `currentPage === 'plugins'` 分支
- [ ] 导航「插件」已就绪（之前加的 `navPlugins`）
- [ ] 加载插件列表数据

**验收**：前端能看到插件列表，能启用/禁用

### 任务 2.2 — 插件 UI 渲染核心

**文件**：`frontend/src/components/PluginPanel.vue`、`frontend/src/composables/usePluginBridge.ts`

- [ ] `PluginPanel.vue` — Shadow DOM 模式
  - [ ] 获取插件 HTML 资源
  - [ ] 挂载到 Shadow DOM
  - [ ] 注入通信桥 JS
- [ ] `usePluginBridge` composable
  - [ ] postMessage 监听
  - [ ] 解析插件调用请求
  - [ ] 调用 `ExecutePluginCommand`
  - [ ] 返回结果给插件
- [ ] 如果 Shadow DOM 不可行，fallback 到 iframe
  - [ ] sandbox：仅 `allow-scripts`，无 `allow-same-origin` ← **P1 修复**

**验收**：插件能显示 UI 并能与后端交互

### 任务 2.3 — 插件热键注册

**文件**：`tray.go`

- [ ] `HotkeyRegistry` 结构体（map 存储已注册热键）
- [ ] `RegisterPluginHotkey(hotkey, pluginID, commandID)` — 注册
  - [ ] 冲突检测：已存在则返回错误
- [ ] `UnregisterPluginHotkey(hotkey)` — 卸载时清理
- [ ] 插件启用时注册所有 commands[].hotkey
- [ ] 插件禁用/卸载时清理对应热键

**验收**：插件声明的快捷键能触发命令执行

### 任务 2.4 — i18n 翻译

**文件**：`frontend/src/i18n/zh-CN.ts`、`en-US.ts`

- [ ] 插件管理页面相关翻译
- [ ] 插件状态翻译（running/stopped/crashed）
- [ ] 权限名称翻译
- [ ] 操作按钮翻译

**验收**：中英文切换正确

---

## Phase 3：插件工具链（1-2 天）

### 任务 3.1 — 插件安装

**文件**：`internal/plugin/installer.go`

- [ ] `InstallFromZip(zipPath)` — 从 zip 包安装
  - [ ] Zip Slip 防护：检查所有条目路径 ← **P2 修复**
  - [ ] 解压到 `~/.quickdock/plugins/<id>/`
  - [ ] 校验 plugin.json
  - [ ] 检查 ID 冲突（已有则备份旧版本）
  - [ ] 写入 plugins 表
  - [ ] 调用 LoadPlugin 启动
- [ ] `UninstallPlugin(id)` — 卸载
  - [ ] 调用 UnloadPlugin 停止
  - [ ] 删除插件目录
  - [ ] 删除 plugins 表记录
  - [ ] 清理 plugin_data
- [ ] `EnablePlugin(id)` / `DisablePlugin(id)`
  - [ ] 更新数据库中 enabled 状态
  - [ ] 启动/停止插件进程

**验收**：能拖入一个 zip 包安装并自动启动

### 任务 3.2 — 插件模板项目

**文件**：`docs/plugin-dev-guide.md`

- [ ] Go 插件模板：`template-go/`（基于设计文档的示例）
- [ ] Node.js 插件模板：`template-node/`
- [ ] Python 插件模板：`template-python/`
- [ ] 模板脚手架脚本：`scripts/plugin-init.sh`

**验收**：能用模板 5 分钟创建一个可运行插件

### 任务 3.3 — 内置示例插件（2-3 个）

**文件**：`plugins/builtin/` 目录

- [ ] **翻译助手**（Go，调翻译 API）
- [ ] **取色器**（Node.js，截图取色）
- [ ] **快速计算**（Python，命令面板表达式求值）

**验收**：内置插件随主程序一起分发，安装即用

### 任务 3.4 — 孤儿进程清理

**文件**：`internal/plugin/manager.go`

- [ ] 启动时扫描是否有残留子进程
- [ ] 根据 PID 文件清理孤儿 ← **P3 修复**
- [ ] 正常关闭时写入 PID 快照

**验收**：主程序强制退出后，下次启动自动清理

---

## Phase 4：高级特性（可选）

| 任务 | 文件 | 工作量 |
|---|---|---|
| Wasm 插件支持（Extism）| `internal/plugin/runtime_wasm.go` | 2 天 |
| 插件市场（远程 JSON 索引）| `internal/plugin/marketplace.go` | 2 天 |
| 文件监控自动热重载 | `internal/plugin/watcher.go` | 1 天 |
| 插件间通信 | `internal/plugin/ipc.go` | 1 天 |

---

## 依赖关系图

```
Phase 0（安全基线）
  └─→ Phase 1（核心骨架）
        ├─→ Phase 2.1（插件管理页面） ← 可并行
        ├─→ Phase 2.2（PluginPanel）   ← 需 Phase 1.4
        ├─→ Phase 2.3（热键注册）      ← 需 Phase 1.4
        └─→ Phase 3（工具链）          ← 需 Phase 2.1
              └─→ Phase 4（高级特性）   ← 可选
```

**并行说明**：
- Phase 2.1（插件管理页面）可以和 Phase 1 并行开发，先用 Mock 数据
- Phase 2.2 和 2.3 需要 Phase 1.4（Wails API）就绪联调

---

## 各文件累计修改清单

| 文件 | Phase | 操作 | 预估代码行 |
|---|---|---|---|
| `internal/plugin/types.go` | 0 | 新建 | ~80 |
| `internal/plugin/errors.go` | 0 | 新建 | ~30 |
| `internal/plugin/rpc.go` | 0 | 新建 | ~200 |
| `internal/plugin/manifest.go` | 0 | 新建 | ~100 |
| `internal/plugin/manager.go` | 1 | 新建 | ~400 |
| `internal/plugin/host.go` | 1 | 新建 | ~250 |
| `internal/plugin/installer.go` | 3 | 新建 | ~200 |
| `internal/db/schema.go` | 1 | 修改（+ 插件表迁移） | ~30 |
| `internal/db/plugin_data.go` | 1 | 新建 | ~100 |
| `services/service.go` | 1 | 修改（注入 Manager） | ~10 |
| `services/plugin.go` | 1 | 新建 | ~150 |
| `main.go` | 1 | 修改（初始化 PluginManager）| ~20 |
| `tray.go` | 2 | 修改（+ HotkeyRegistry）| ~100 |
| `frontend/.../PluginManagerPage.vue` | 2 | 新建 | ~350 |
| `frontend/.../PluginPanel.vue` | 2 | 新建 | ~150 |
| `frontend/.../usePluginBridge.ts` | 2 | 新建 | ~100 |
| `frontend/.../Sidebar.vue` | 2 | 无需改动（已占位）| 0 |
| `frontend/.../App.vue` | 2 | 修改（+ plugins 分支）| ~20 |
| `frontend/.../i18n/*.ts` | 2 | 修改（+ 插件翻译）| ~50 |
| `frontend/.../CommandPalette.vue` | 2 | 修改（+ 插件命令源）| ~30 |
| `docs/plugin-dev-guide.md` | 3 | 新建 | ~200 |

**累计新增：~2,200 行 Go + ~650 行 Vue/TS = ~2,850 行**

---

## 每日开发建议

```
Day 1 （Phase 0）     ：types.go + errors.go + rpc.go + manifest.go
Day 2 （Phase 1.1-1.2）：manager.go + host.go
Day 3 （Phase 1.3-1.4）：DB + services + main.go + 联调
Day 4 （Phase 2.1）    ：PluginManagerPage.vue（与 Day 5 并行）
Day 5 （Phase 2.2-2.3）：PluginPanel.vue + hotkey + 联调（与 Day 4 并行）
Day 6 （Phase 2.4 + 3.1）：i18n + installer.go
Day 7 （Phase 3.2-3.4）：模板 + 内置示例 + 清理
Day 8 （缓冲区）       ：bug fix + 测试 + 文档完善
```
