# QuickDock 工程约束（AI 与开发者必读）

本文件是项目**唯一事实源**。任何代码改动前必须先读本文件与 `docs/conventions/` 下对应细则；
与本文档冲突的旧写法，一律以本文档为准（旧代码按"已知债务"渐进修复，但**不得新增**）。

## 0. 项目速览

| 项 | 值 |
|---|---|
| 类型 | Wails v3 桌面应用（Go 后端 + Vue3 前端，单进程嵌入式 WebView） |
| 模块名 | `quickdock`（Go 1.26，见 `go.mod`） |
| 前端 | Vue 3 `<script setup>` + TypeScript，Vite 构建，产物 `frontend/dist` 被 `main.go` embed |
| 绑定 | `frontend/bindings/**` 由 `wails3 generate bindings` **自动生成，禁止手改** |
| 任务入口 | `Taskfile.yml`（build / package / run / dev），CI 见 `.github/workflows/release.yml` |

## 1. 最高优先级约束（MUST）

### 通用
- **R1** 不改动自动生成物：`frontend/bindings/**`、`frontend/dist/**`、`plugins/templates` 生成结果。
- **R2** 单一职责提交：一次改动只解决一个问题；不与无关重构（格式化、重命名）混在一起。
- **R3** 提交前必须通过"第 4 节自检清单"，否则不视为完成。

### 前端
- **R4** 所有颜色/间距/圆角/阴影**只能**用 `frontend/src/style.css` 的设计 token（`--color-*`、`--space-*`、`--radius-*`、`--shadow-*`）。
  禁止硬编码色值（`#4a9eff`、`rgba(...)`），禁止引用未在 `style.css` 定义的变量名（如 `--border-color`、`--surface-2`、`--color-hover`）。
- **R5** 需要新 token 时：**先**在 `style.css` 的两套主题（`:root` / `[data-theme="light"]`）里同时定义，**再**使用。两套主题的变量集合必须一一对应。
- **R6** 新增设置类/表单类 UI 必须复用已有公共类体系（`.section` / `.section-title` / `.section-desc` / `.setting-row` / `.setting-label` / `.setting-control` / `.btn .btn-primary` / `.btn .btn-secondary` / `.action-row` / `.result-hint` / `.field*`）。
  **禁止**再造第二套同类体系（当前 `.modal/.modal-input/.btn-ghost`、`.switch/.knob` 属历史债务，不得新增使用）。
- **R7** 组件样式一律写在 `<style scoped>`；禁止 `style="..."` / `:style` 内联样式（Canvas 绘图、动态缩进等运行时计算值除外，需就近注释说明）。
- **R8** UI 文案一律走 i18n（`t('key')`），`zh-CN.ts` 与 `en-US.ts` **必须同步新增**；禁止模板/脚本里硬编码中文。
- **R9** 调用后端统一模式：`unwrap()` 解包 → `try/catch` → `toast.error(getErrorMessage(e))` + `logErr(scope, e)`。禁止空 `catch {}`。

### 后端
- **R10** 依赖方向单向：`main` → `services` → `internal`。
  `internal/**` **禁止** import `quickdock/services`；`services` 子包可 import `quickdock/services`（取 `ApiResult`/`AppService`）。
- **R11** 所有暴露给前端的 Wails service 方法**统一返回 `*services.ApiResult`**（`Code=0` 成功），
  构造器用 `services.Ok/OkMsg/Fail/FailMsg`（`Fail` 内部已记日志）。唯一例外是未注册的内部包。
- **R12** 平台差异文件：文件名后缀（`_windows.go` / `_unix.go` / `_darwin.go`）与 `//go:build` **同时写**；
  `_unix.go` 统一用 `//go:build !windows`（与 `main_unix.go` 对齐），不再新增 `darwin || linux` 写法。

## 2. 目录职责（放置新代码的决策依据）

```
main.go / tray.go / windows.go + *_windows.go / *_unix.go   ← 进程入口、托盘、浮窗（package main）
services/**                                                  ← 面向前端的「门面」层（Wails 绑定）
  ├─ services/*.go          宿主单例 AppService、常驻循环、尚未下沉的领域逻辑
  └─ services/<domain>/service.go   XxxService + NewXxxService(app)，一领域一子包
internal/**                                                  ← 可复用「引擎 / 基础设施」，不直接面向前端
plugins/**                                                   ← 内置插件与脚手架（前端资源 + 模板，非主程序）
```

**新增代码放哪里（决策树）**
1. 与操作系统/外部系统打交道、可被多处复用的引擎 → `internal/<domain>/`
2. 需要暴露给前端调用的业务能力 → `services/<domain>/service.go`（薄门面，只做参数校验 + 调 `internal`）
3. 进程级单例状态、常驻循环（剪贴板监听、定时巡检等）→ 留在 `services/*.go`，并在文件头注释写明"为何留在宿主"
4. **禁止**同一领域一边在 `services/<domain>/` 一边在 `services/*.go` 各自实现一半

## 3. 提交前自检清单

```bash
# 后端（必须全绿）
gofmt -l internal services *.go          # 输出应为空
go vet ./internal/... ./services/...     # 无告警
go build ./internal/... ./services/...   # 通过（go build ./... 需先构建前端 dist）

# 前端（必须全绿）
cd frontend && npx vue-tsc --noEmit -p tsconfig.json
```

人工核对：
- [ ] 无新增硬编码色值 / 无新增未定义 CSS 变量
- [ ] 无新增 `style="..."` 内联样式
- [ ] 新增 UI 文案在 `zh-CN.ts`、`en-US.ts` 双份齐全
- [ ] 新增后端方法返回 `*services.ApiResult`
- [ ] 未手改 `frontend/bindings/**`

## 4. 细则索引

- 前端样式与组件规范：`docs/conventions/frontend.md`
- Go 后端目录与代码风格：`docs/conventions/backend.md`
- 产品与设计意图：`DESIGN.md`；首次使用引导：`docs/first-use-tutorial.md`
