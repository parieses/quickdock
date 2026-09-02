# 环境管理 端到端冒烟验证清单

- 日期：2026-09-02
- 提交：`a29aeaa`（已推送 `origin/main`）
- 说明：本清单区分「已确认可用」与「需你实机点」。GUI 应用的点击交互无法在无头环境验证，相关项标记为需实机。

## 一、自动化静态冒烟（本次已跑，全绿）

| 检查项 | 命令 / 动作 | 结果 |
|---|---|---|
| 后端编译 | `go build ./services/...` | exit 0 |
| 静态检查 | `go vet ./services/...` | exit 0 |
| 回归测试 | `TestHTTPServeRunningPersistence` | PASS |
| 回归测试 | `TestWritePHPConfigPreservesComments` | PASS |
| 前端类型 | `vue-tsc --noEmit` | exit 0 |
| 生产构建 | `wails3 build` | exit 0（产出 `bin/quickdock.exe` 26MB） |
| 绑定接口面 | 15 个 env/httpserve 方法存在于 `frontend/bindings/quickdock/services/appservice.ts` | 全部命中 |
| 二进制符号 | 新方法符号编入 `bin/quickdock.exe` | grep 命中 |

> 注：绑定文件为 `.ts`（非 `.js`），`vue-tsc` 据此类型检查，前端构建正常。

## 二、五大功能验证矩阵

| 功能 | 后端实现 | 绑定方法 | 前端编译 | 需实机点 |
|---|---|---|---|---|
| 1. PHP 配置编辑（php.ini / 禁用函数 / 错误日志 / 扩展） | ✅ `phpconfig.go` + 单测覆盖写回保留注释 | ✅ `EnvPHPConfigGet/Set` | ✅ | 打开弹窗、切 4 个 tab、保存后看 php.ini 变化 |
| 2. 环境管理分组（language/webserver/cache/tool） | ✅ `source.go` 加 `group` | —（纯前端分组） | ✅ | 看左侧是否按 4 类归组，node/harness/http 在 tool 组 |
| 3. 导入已装版本（linked scope） | ✅ `ImportVersion` + `links.json`（非破坏性） | ✅ `EnvImportVersion` | ✅ | 点「导入已安装」→ 选目录 → toast 显示探测到的版本 |
| 4. HTTP Serve 静态服务 | ✅ `httpserve.go` + 重启自动恢复（单测覆盖） | ✅ `HTTPServeList/Create/Start/Stop/Delete` | ✅ | 建服务→启动→浏览器开 localhost:port→关应用重开验证自恢复 |
| 5. 版本获取地址修正 | ✅ PHP archives / Redis redis-windows / Go | — | ✅ | 拉列表确认 PHP archives、Redis redis-windows 能取到版本 |

## 三、四项打磨验证矩阵

| 打磨项 | 状态 |
|---|---|
| HTTP Serve 重启自动恢复 | ✅ 单测 `TestHTTPServeRunningPersistence` PASS |
| HTTP Serve「打开」按钮 | ✅ 编译通过 + i18n `httpOpen`；需实机点按钮 |
| PHP 配置写回保留 `;extension=` 注释行 | ✅ 单测 `TestWritePHPConfigPreservesComments` PASS |
| 导入版本反馈 | ✅ 前端 `importExisting` 已含 toast + 列表刷新（原本已在，无需改动） |

## 四、已知未覆盖（必须你实机验证）

1. **GUI 点击交互**：弹窗/操作菜单/分组视觉渲染（无头环境无法点）。
2. **真实下载安装**：`EnvInstall` 会联网拉 PHP/Redis/Go 安装包，未做自动化（避免耗时与网络依赖）；逻辑代码已就位，建议在实机小版本试装一次。
3. **文件夹选择器** `PickFolderPath`：依赖系统原生对话框（Windows 文件夹选择），需实机触发。
4. **HTTP 服务真实可达**：需在浏览器访问 `http://localhost:<port>` 确认静态文件可下载/浏览。

## 五、建议的实机验证顺序

1. 启动 `bin/quickdock.exe` → 打开环境管理。
2. 看左侧是否分 语言 / Web 服务器 / 缓存 / 工具 四组。
3. 选一个 PHP 已装/导入版本 → 操作菜单「编辑 php.ini」→ 四个 tab 切换、保存后检查原 php.ini 的 `;extension=` 注释是否还在。
4. 工具组点 `⬡ http serve` → 选目录 + 端口 8080 → 创建并启动 → 点「打开」跳浏览器验证 → 关掉整个 QuickDock 重开，确认服务自动恢复且显示「运行中」。
5. 某运行时（如 PHP）拉一次版本列表，确认来源是 archives / redis-windows。
