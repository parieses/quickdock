# Go 后端目录与代码风格规范

适用范围：根目录 `*.go`、`services/**`、`internal/**`。总纲见根目录 `AGENTS.md`。

## 1. 分层与依赖方向

```
package main (main.go / tray.go / windows.go / *_windows.go / *_unix.go / elevated_cli.go)
        │  进程入口：注册服务、托盘、浮窗、窗口位置记忆
        ▼
services/**       面向前端的「门面」：参数校验 + 组装 ApiResult + 调 internal
        ▼
internal/**       引擎 / 基础设施：DB、平台 API、外部协议、算法，不感知 UI
```

- **单向依赖**：`internal/**` 不得 import `quickdock/services`（当前 0 命中，必须保持）。
  `services/<domain>/` 允许 import `quickdock/services`（取 `ApiResult`、`AppService`）。
- `package main` 只放进程装配与窗口/托盘相关逻辑，不放业务逻辑。

## 2. 目录放置规则

| 场景 | 位置 | 形态 |
|---|---|---|
| 领域引擎（可复用、与 OS/外部系统交互） | `internal/<domain>/` | 包内按职责拆文件，无强制分层 |
| Wails 门面（暴露给前端） | `services/<domain>/service.go` | `type XxxService struct{ App *services.AppService }` + `NewXxxService(app)` |
| 宿主单例状态 / 常驻循环 | `services/*.go`（根包 `package services`） | 必须在文件头注释说明"为何留在宿主" |
| 常量、DB 模型 | `internal/db/*.go` | 模型由 Wails 生成到前端 `models.ts`，**不手写 `models.go`** |
| 平台差异实现 | `<name>_<os>.go` | 见第 5 节 |

**门面方法要保持薄**：校验参数 → 调 `internal` → `services.Ok(data)`。业务逻辑不要写在门面里。

## 3. Wails 绑定规范

- 注册入口 `main.go:304-325`（`application.NewService(...)`，当前 22 个）。
- 注册服务的**所有导出方法**都会暴露给前端，因此：
  - 返回值统一 `*services.ApiResult`（`Code=0` 成功、`1` 失败）；
  - 参数用扁平基本类型，复杂更新用 `map[string]interface{}`；
  - 方法名用 PascalCase（前端生成同名 TS 函数）。
- 构造器：`services.Ok(data)` / `OkMsg(data, msg)` / `Fail(err)` / `FailMsg(msg)`；
  泛型 `services.Wrap[T]` / `wrap[T]` 用于消除重复的"取数据 + 判空 + 包壳"模板代码。
  `Fail` 内部已调用 `logger.E`，**不要**在调用点重复打日志。
- 新增服务后：注册到 `main.go` → 重新生成绑定（`wails3 generate bindings`）→ 前端调用。
- 未注册的内部包（如 `services/scheduler`）不受 ApiResult 约束，但**不得**被前端直接调用。

## 4. 错误处理与日志

- 错误包装一律 `fmt.Errorf("xxx: %w", err)`；需要判定用 `errors.Is` / `errors.As`（当前仅 4 处，偏少，新增判定场景应使用）。
- 哨兵错误与错误码并存时（如 `internal/plugin/errors.go` 的 `Err*` + `ErrCode*`），
  **优先用哨兵错误做判定**，错误码只用于跨进程/插件边界。
- 日志统一 `internal/logger`：`logger.I/W/E`、`logger.Logf(level, ...)`；插件上下文用 `PluginI/W/E`。
- **禁止静默吞错**：不得出现空 `catch`（前端）与 `if err != nil {}`（后端）。确需忽略时，写注释说明为什么可以忽略。
- panic：业务代码不用 `panic`；必须兜底时用 `RecoverToLog` / `RecoverPanic`，不要裸 `recover()` 后丢弃。

## 5. 平台差异文件约定

- 文件名后缀 + `//go:build` **同时写**（全仓 89 个平台文件均如此，保持一致）：
  ```go
  //go:build windows
  //go:build !windows      // _unix.go 统一用这一种，不再新增 darwin || linux
  //go:build darwin        // _darwin.go
  ```
- 不使用 `clipboard_sys_windows.go` 这类中缀（`_sys_`），直接 `<name>_windows.go`。
- 非平台维度的条件编译用 build tag + 语义后缀，如 `datadir_production.go` / `datadir_dev.go`。
- **剪贴板临界区一律经 `platform.WithClipboard(hwnd, fn)` 进出，禁止裸调 `OpenClipboard`/`CloseClipboard`**：
  Win32 把剪贴板锁绑定到调用线程，Go 的异步抢占会让 goroutine 在临界区中间切到别的 OS 线程，
  Open/Close 一旦跨线程，`CloseClipboard` 静默失败、剪贴板被无限期锁死——所有程序的复制全部失效，
  只能重启进程恢复。`fn` 内只允许内存拷贝，解析/入库/查窗口标题放到 `fn` 外。

## 6. 命名与注释

- 包名：全小写、无下划线（`sysutil` / `webdavsrv` / `winmgr`）。
- 文件名：**100% snake_case**（当前零违规），平台后缀小写。
- 类型 / 函数：`XxxService` + `NewXxxService`（19/20 子包一致）。
- 常量：外部可见/枚举用 PascalCase 导出；对外协议常量（如 Windows VK）用 SCREAMING_SNAKE；包内私有用 lowerCamel。
- import 别名：优先使用包原名；重名时统一用 `<domain>engine` 或 `<domain>mgr`，同一包在仓库内**只用一个别名**
  （`internal/env` 统一用 `envengine`，`internal/plugin` 统一用 `pluginmgr`；同一包在仓库内只保留一个别名）。
- 注释：**中文为主**（占比约 90%），保持；每个包至少一个 `// Package xxx ...` 包注释，
  放在该包**主文件**（`service.go` 或同名文件），不要散落在次要文件（反例：`services/env` 的包注释写在 `ollama.go`）。
- 根散落文件用 `// ===== xxx =====` 分区注释时，分区标题要与实际内容一致，失效注释须随代码更新
  （反例：`tray.go:9` 注释指向已迁移到 `internal/platform/` 的剪贴板监听文件）。

## 7. 格式化与校验

```bash
gofmt -l internal services *.go      # 必须输出为空
go vet ./internal/... ./services/...
```

- 提交代码**必须** `gofmt`（CI 目前没有 gofmt/vet 步骤，靠人工把关）。
- `go build ./...` 依赖 `frontend/dist`（`main.go:148` embed），只改后端时用
  `go build ./internal/... ./services/...` 验证。
- 测试：`internal/**` 测试覆盖尚可，`services/**` 20 个子包仅 4 个有测试。
  **新增领域逻辑须带单测**，尤其 `services/<domain>/` 门面层。
- 不要提交调试用测试（反例：`internal/env/zz_debug_path_test.go` 含 `fmt.Println`）。
