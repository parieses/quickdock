<!-- README 结构：定位 → 截图 → 功能一览 → 功能详解 → 架构 → 技术栈 → 开发 → 界面设计 → 插件 → 许可。
     长文详解收进 <details>，默认折叠。改完请同步 website/index.html 的更新日志（见 AGENTS.md 发版纪律）。 -->

<div align="center">

# 快启坞 QuickDock

Windows 桌面工具。资源集合、快速启动、本地开发环境、若干常用工具，都放在一个窗口里。

![Platform](https://img.shields.io/badge/Platform-Windows%2010%2F11-4a9eff)
![Version](https://img.shields.io/badge/Version-v0.0.32-4a9eff)
![License](https://img.shields.io/badge/License-MIT-4a9eff)
![Built with](https://img.shields.io/badge/Built%20with-Go%20%2B%20Vue-4a9eff)
![Framework](https://img.shields.io/badge/Framework-Wails%20v3-4a9eff)

官网 <https://parieses.github.io/quickdock/>　·　Issues <https://github.com/parieses/quickdock/issues>　·　插件市场 <https://github.com/parieses/quickdock-plugins>

</div>

---

![主界面截图](image/main-ui.png)

---

## 定位

开发者的桌面上通常同时开着这些东西：项目目录、常用命令、接口文档、待办、监控面板，还有几个版本不同的运行时。每一样单独看都不重，但一天要来回切几十次。

QuickDock 把它们收进同一个窗口，按资源树和快捷键组织，另外附带了截图、窗口排版、本地站点 HTTPS、AI 助手和插件系统。

- **资源树**：工作空间 → 场景 → 集合 → 项目 四级结构，支持拖拽排序、FTS5 全文搜索、变量展开
- **快捷键**：`Ctrl+Space` 主窗口、`Ctrl+K` 命令面板、`` Ctrl+` `` 剪贴板、`F1` 截图，均可改键
- **运行环境**：28 种语言与服务，安装、切换版本、启停、看日志都在应用内完成
- **AI**：多模型对话、SSH 终端侧栏、剪贴板与笔记的快捷问答，对外提供 MCP 服务
- **安装**：单文件，用户态安装，不需要管理员权限，更新就地替换

---

## 功能一览

<details open>
<summary><b>核心效率</b></summary>

| 能力 | 默认热键 | 说明 |
|------|---------|------|
| 工作空间 / 场景 / 集合 / 项目 | — | 四级资源树，6 种项目类型，拖拽排序，FTS5 全文搜索。主界面分导航、场景、集合、项目四栏。条目可勾选批量删除 / 跨集合移动，可设标记颜色与自定义图标 |
| 命令面板 | `Ctrl+K` | 搜工作空间、项目、笔记、待办、应用、文件、插件命令、系统命令等，结果按来源分为 14 个区。URL 自动识别保存为项目 |
| 剪贴板历史 | `` Ctrl+` `` | 监听文本、图片、文件。支持固定、搜索、过期清理、批量清空，文件条目可在资源管理器定位，图片可另存 |
| 剪贴板自动规则 | — | 按条件清洗复制进来的内容：去空白、删空行、去重行、排序、JSON 格式化、去 ANSI 颜色、URL 解码、大小写转换。规则可排优先级，可模拟来源窗口试跑 |
| 快捷笔记 | `Ctrl+Alt+N` | 树形 Markdown 笔记库，自动保存，失焦隐藏 |
| 快捷键备忘单 | — | 命令面板输入 `cheatsheet`，或点「设置 → 快捷键」页头按钮。列出当前生效的键位 |
| 最近动态 | — | 「设置 → 历史」：左栏使用热度，右栏操作时间线，同页可切到回收站 |
| 日志查看器 | — | 「设置 → 日志」按天查看应用日志与崩溃记录，可过滤级别、清理旧日志 |
| 日志实时尾随 | — | 命令面板输入 `logtail`，对任意本机文件执行 `tail -f`，支持暂停与关键字高亮 |
| 通知中心 | — | 侧栏底部铃铛，汇总监控状态、定时任务结果与 AI 回答完成，未读带角标 |

</details>

<details open>
<summary><b>任务与监控</b></summary>

| 能力 | 说明 |
|------|------|
| 待办 | 列表 / 看板视图，子任务、标签、重复规则、到期提醒；子任务可单独设截止日，页内可搜索；番茄专注计时结束后发系统通知，可选机器人推送 |
| 定时任务 | 5 种动作 × 5 种调度（一次性 / 间隔 / 每天 / 每周 / 每月），列表可搜索筛选、显示下次执行倒计时，执行历史可导出 CSV，可手动立即执行 |
| 网站监控 | HTTP 状态码与响应时间、SSL 到期预警、关键字与正则匹配、响应趋势图；状态变化推送通知并可导出 CSV。支持搜索、批量启停、拖拽排序、回收站与维护窗口 |

</details>

<details open>
<summary><b>AI 与开发</b></summary>

| 能力 | 说明 |
|------|------|
| AI 助手 | OpenAI / DeepSeek / Kimi / 通义 / Ollama / Azure / 自定义兼容接口；SSE 流式、思考折叠、多会话，会话可重命名与搜索；Key 经 DPAPI 加密 |
| 回答渲染 | LaTeX 公式、Mermaid 流程图、代码高亮、表格排序与 CSV 导出、html 代码块沙箱预览；图与公式可复制为图片 |
| AI 用量统计 | 近 7 天 / 30 天 / 全部的估算花费、问答次数、Token，按模型与入口拆分，缓存命中单独列出 |
| 图片理解 | 对话中粘贴、选择或拖入图片，按模型能力配置输入方式 |
| 终端内 AI | SSH 终端侧栏，可附带当前屏显、选中输出即问，命令一键填入或执行 |
| 剪贴板 AI | 条目上「问 AI」就地弹层，预置翻译、摘要、格式化和自由提问 |
| 笔记 AI | 笔记浮窗内嵌面板，支持总结、润色、续写、翻译 |
| DeepSeek Harness | 内嵌完整 Agent 编程入口，自动装填便携 Node，原生窗口承载 3080 端口；打开前校验 WebView2 版本，进程意外退出自动重拉 |
| MCP 服务 | `http://127.0.0.1:9230/mcp`，工具集分只读、低危写、高危三级权限 |
| 环境管理 | 28 种运行时，安装、切换、启停、配置、日志、控制台，全部在用户目录内；可按项目目录的版本声明做检查 |

</details>

<details open>
<summary><b>工具箱</b></summary>

| 能力 | 默认热键 | 说明 |
|------|---------|------|
| 截图工具 | `F1` | 原生分层窗口覆盖层，框选、标注、复制、保存、贴图 |
| 窗口管理 | `Ctrl+Alt+W` | 15 个贴屏热键，九宫格排版浮层，不占用 `Win+方向` |
| 本地开发站点 | — | 为项目绑定自定义域名并自动签发 HTTPS 证书（mkcert），自身不监听端口 |
| SSH 远程终端 | `Ctrl+Alt+S` | 多标签终端与 SFTP 文件管理（含多选批量操作），指纹信任，多主机批量执行，一次性执行命令并核对风险 |
| WebDAV 同步与快照 | — | 全量 JSON 备份恢复、多版本、任意 WebDAV 服务器 |
| 数据目录搬家 | — | 「设置 → 存储与同步」更换存储位置，整个数据目录打包迁移，重启生效 |
| 界面缩放 | — | 主界面与全部独立窗口统一 1.15 倍等比放大 |

</details>

<details open>
<summary><b>插件</b></summary>

- 三种运行时：**none**（纯前端）、**goja**（内嵌 JS 沙箱）、**native**（独立子进程），通信走 JSON-RPC 2.0
- 34 个 Host API、14 组能力，权限声明式校验（文件系统 scope、网络白名单、剪贴板、进程等）
- 向插件开放宿主 AI：`host.ai.ask` 复用用户已配好的档案，凭据不出后端；带 `streamId` 即流式
- 官方插件经在线市场安装与升级，清单和开发文档在 [quickdock-plugins](https://github.com/parieses/quickdock-plugins) 仓库

</details>

---

## 安装

### 系统要求

- Windows 10 1809+ 或 Windows 11
- WebView2 Runtime（Windows 自带，建议 ≥ 119；低于此版本 DeepSeek Harness 窗口会反复 `connection lost`）
- 磁盘约 50 MB，运行时按需下载

### 步骤

1. 从 [Releases](https://github.com/parieses/quickdock/releases) 下载 `quickdock-amd64-installer.exe`
2. 双击安装，默认装到 `%LOCALAPPDATA%\Programs\快启坞`，不需要管理员权限
3. 运行 `QuickDock.exe`，任务栏出现托盘图标即启动完成

安装路径建议保持默认。就地替换更新需要对安装目录有写权限，改到 `C:\Program Files\...` 后每次更新都要过 UAC，自动更新会失败，只能手动覆盖。

也可直接下载 `quickdock-amd64.exe` 作为便携版，放在当前用户可写的目录即可，同样支持就地更新。

### 快捷键

| 功能 | 默认快捷键 |
|------|-----------|
| 切换主窗口 | `Ctrl+Space` |
| 命令面板 | `Ctrl+K` |
| 剪贴板历史 | `` Ctrl+` `` |
| 快捷笔记 | `Ctrl+Alt+N` |
| 区域截图 | `F1`，被占用时回退 `Ctrl+Shift+A` |
| 窗口排版浮层 | `Ctrl+Alt+W` |
| SSH 远程终端 | `Ctrl+Alt+S` |

以上热键都能在「设置 → 快捷键」和「设置 → 窗口管理」中修改，运行时动态重新注册。命令面板输入 `cheatsheet` 可打开快捷键备忘单，内容读自当前设置而非出厂默认值。

---

## 功能详解

<details>
<summary><b>工作空间与资源管理</b></summary>

- **工作空间**：顶级容器，用于隔离不同项目的上下文
- **场景**：工作空间下的视图分组，支持标签、图标、颜色
- **集合**：资源逻辑分组，四种打开策略
- **项目**：六种类型，`directory` 目录、`file` 文件、`url` 网页、`command` 终端命令、`app` 应用、`quicklink` 带参快捷方式

全层级支持拖拽排序与 FTS5 全文搜索。`value`、命令、URL 支持 `{date}` `{time}` `{username}` `{clipboard}` 变量，打开时即时展开。

- **批量操作**：项目列表可勾选，批量删除或移到当前场景的其它集合，失败项单独回报
- **外观**：条目可设标记颜色与自定义图标（256 KB 内的图片），列表里直接看出来是哪个环境
- **附加参数**：命令类型拼在命令末尾，其它类型接在工具参数之后，同样支持变量
- **快捷链接**：`quicklink` 类型支持 `{query}` 占位符，打开时弹窗问一句再拼进 URL
- **只导出不导入**：场景可以导出成 JSON 随仓库走，没有导入入口。按名字匹配的静默合并不比重名更安全，宁可不开这个口子

</details>

<details>
<summary><b>剪贴板历史</b></summary>

监听文本、图片、文件三类内容，支持固定、搜索、复制、批量删除，过期条目按配置的天数自动清理。浮窗通过 `` Ctrl+` `` 唤出，失焦自动隐藏。

监听除了系统通知之外，还会按剪贴板序号做兜底巡检，漏掉的通知能补抓。捕获卡住或剪贴板被其他程序长期占用时会写日志，被占用的情况下会记录进程名、PID、窗口类名与标题；对方以 NULL 句柄打开、没有关联窗口时，日志会注明无法定位。全应用的复制操作统一走后端 Win32 写入，不受 WebView2 焦点状态影响。

筛选标签分两类，语义不同：

- **整类标签**（图片、文件、全部、已收藏）按条目类型筛选。图片和文件是独立类型，不附带"必须是文本"的前提
- **智能分类标签**（网址、邮箱、电话、JSON、代码）只在文本条目内查找。分类在入库时计算并写入数据库，切换标签直接查库，可覆盖全部历史
- 「全部」列表本身按 1000 条载入，需要更早的条目时切换到对应标签，那条路径是全量查询

条目上的「问 AI」可就地提问，预置翻译、摘要、格式化，回答流式呈现，代码块单独成卡。这里不提供执行动作。

文件类条目的右键菜单里「在资源管理器显示」会定位到源文件，图片条目可以「另存为图片」直接落到下载目录。列表顶部有一键清空，清完提示清掉了多少条。

</details>

<details>
<summary><b>剪贴板自动规则</b></summary>

复制进来的内容可以先按规则清洗，再写入历史。

- **9 种动作**：去除首尾与行尾空白、删除空行、重复行去重（保留首次出现）、行排序、JSON 缩进格式化（宽度可配）、JSON 压成一行、去除 ANSI 颜色与 OSC 序列、URL 解码、大小写转换
- **3 个命中条件**：全部文本、内容匹配正则、来自指定来源窗口。条件不满足的内容原样放过
- **按顺序执行**：多条规则串成流水线，上一条的输出是下一条的输入；规则带优先级，数字小的先跑
- **试运行**：规则页内粘贴样例文本即可预览结果；来源窗口条件可以填一个窗口标题模拟，不用真的从那个窗口复制一次
- **总开关默认关闭**：开启后规则才在入库前生效
- **不做脱敏**：不识别也不替换身份证号、手机号、密钥等内容。脱敏会静默改写用户复制的内容，误判的代价高于收益；需要脱敏时请自行交给 AI 处理

</details>

<details>
<summary><b>通知中心</b></summary>

侧栏底部的铃铛汇总三类事件：网站监控状态变化（掉线、恢复）、定时任务执行结果、AI 回答完成。

AI 完成通知有前置判断：主窗口可见且当前停在 AI 对话页时不推送，窗口隐藏或最小化、已切到其他页面时才收进中心。未读角标使用强调色，红色留给监控掉线一类故障。

点击通知可跳回对应对象：监控跳到那条监控，定时任务跳到那个任务，AI 跳回那次对话。支持单条删除、全部标记已读、一键清空。

</details>

<details>
<summary><b>日志与实时尾随</b></summary>

「设置 → 日志」提供两种查看方式。

- **按天回看**：主日志按日期列出，可按级别过滤，可清理旧日志；同页查看 JS 与 Go panic 的崩溃现场
- **实时尾随**：命令面板输入 `logtail`，对任意本机文件执行 `tail -f`。打开时读取末尾 N 行，之后每 500ms 只读取新增部分
- **自动对齐**：文件被轮转（新建同名文件）或原地截断重写时能识别出来，重新对齐读取位置
- **暂停与过滤**：暂停期间新行暂存，恢复时一并补齐；支持关键字过滤与高亮
- **上限**：缓冲区保留 5000 行，超出丢弃最旧的；渲染同样封顶 5000 行，超出部分不再插入页面，顶部提示改用搜索或筛选缩小范围

</details>

<details>
<summary><b>命令面板</b></summary>

全局搜索工作空间、场景、集合、项目，选中即可操作，支持键盘导航与多选批量处理。空输入时展示最近使用，可展开查看全部。

结果按来源分区，每区带标题与计数：最佳匹配、最近、项目、快速链接、笔记、待办、应用程序、文件（Everything）、插件命令、环境服务、系统命令、剪贴板建议，以及计算器与网页两组就地结果。

**待办检索**：输入标题、备注或标签会出现「待办」区，只列未完成的顶层任务。回车会直接定位到那一条：切到待办页、跳到所在日期、必要时清除标签过滤并切回列表视图，目标行高亮两秒。动作菜单中可直接标记完成。

已知边界：剪贴板历史条目不参与搜索，面板里的「剪贴板建议」只作用于刚复制的那一条；各区有显示上限（应用 8 条、文件 20 条、快链 3 条），目前只有「最近」区可展开。

面板内输入 `cheatsheet` 弹出快捷键备忘单，包含全局热键、窗口管理 15 个贴屏键、面板内操作键三张表，全部读自当前热键设置。

</details>

<details>
<summary><b>待办任务</b></summary>

- 列表与看板两种视图（待办、进行中、已完成）
- 子任务、标签分类、重复规则（每日 / 每周 / 每月）、到期系统通知
- 子任务可改名、上下移动、单独设截止日；页内搜索按标题与备注过滤
- 番茄专注：在待办页启动倒计时，结束时发系统通知，可选机器人推送

</details>

<details>
<summary><b>定时任务</b></summary>

- 5 种动作：打开软件、目录、网页、执行命令、发送 HTTP 请求
- 5 种调度：一次性、按间隔、每天、每周、每月
- 列表支持按名称搜索，可按失败 / 启用中 / 已停用过滤，每条显示下次执行还有多久；执行后发系统通知，支持手动立即执行
- 执行历史可只看失败、导出 CSV，保留最近 500 次或 30 天
- 机器人推送地址统一在「设置 → 机器人通知」配一次，任务里只决定发不发、走哪个渠道
- 删除的任务进入回收站，回收站内单独一类。恢复时按当前时间重新计算下次运行时间，避免快照中已过期的运行时间导致恢复后立刻补跑

</details>

<details>
<summary><b>网站监控</b></summary>

- 定时探测 HTTP 状态码与响应时间（GET / HEAD / POST）
- SSL 证书到期预警，提前天数可配置
- 关键字与正则内容匹配
- 在线率统计、检测日志、状态变化通知（桌面通知与机器人推送：钉钉、企业微信、飞书、Server酱、PushPlus、Telegram）
- 响应时间趋势图（24 小时 / 7 天 / 全部）
- 监控分组，支持导出 CSV
- 搜索、勾选批量启用 / 停用、拖拽排序
- 删除进回收站，恢复时日志与配置一起回来；彻底删除才抹掉检测日志
- 维护窗口：临时暂停检测（默认 1 小时），期间不检测、不写日志、不告警

</details>

<details>
<summary><b>快捷笔记</b></summary>

浮动窗口 `Ctrl+Alt+N` 唤出，树形结构（文件夹 + Markdown 多层级），支持搜索、重命名、删除，自动保存（500ms 防抖），失焦隐藏。

浮窗内的「问 AI」打开侧栏面板：有选区时只发送选中段落，否则发送整篇。预置总结、润色、续写、翻译，也可自由追问。整个过程在浮窗内完成，不写入 AI 对话历史。回答可以插入到光标、追加到末尾或替换全文。

编辑区右侧会按标题生成目录，点一下跳到那一节；底部实时显示字数（汉字 + 英文单词）、字符数与行数。左侧树可以拖拽排序，整篇能导出成 `.md`。

</details>

<details>
<summary><b>DeepSeek Harness</b></summary>

内嵌 [DeepSeek Harness](https://github.com/deepseek-ai/dsh)，提供完整的 Agent 编程入口（工具、文件、终端、会话）。

- **环境装填**：检测 node、npx、dsh，缺失时下载便携 Node v22（npmmirror 优先，nodejs.org 兜底）并安装 `@deepseek-ai/dsh`
- **原生窗口**：`node` 拉起 `dsh web --no-open`（`CREATE_NO_WINDOW` 隐藏控制台），固定使用官方端口 3080。端口被健康的 dsh 占用则复用，被残留 node 占用则清理重绑，仅被无关进程占用时才退到随机端口
- **后台常驻**：关闭窗口不停止服务，下次打开即时可用。停止由设置手动触发或随应用退出
- **安装日志**：下载、解压、npm 安装的每一步逐行推送 `quickdock:dsh:log`
- **环境隔离**：过滤第三方注入的 `NODE_OPTIONS`，避免影响 dsh 进程
- **版本校验**：打开前读取系统 WebView2 运行时版本，低于 dsh 要求的 119 时红字提示并拦截打开，避免窗口内反复 `connection lost`
- **崩溃自愈**：dsh 进程意外退出时，已打开窗口自动重拉并重导航（带熔断）；状态判断改用真实进程探测，杜绝「进程已死却显示运行中」的假存活
- **流程全可见**：安装 / 卸载 / 启动 / 停止的进度实时输出到「环境管理 → DeepSeek Harness」日志面板；卸载改为直接清理目录并支持一键重装

</details>

<details>
<summary><b>AI 助手</b></summary>

- **多档案**：OpenAI、DeepSeek、Kimi、通义千问、Ollama、Azure OpenAI、自定义兼容接口
- **模型列表**：填好 BaseURL 与 API Key 后点「拉取模型列表」，直接获取该接口的可用模型；未保存的草稿也能拉取
- **5 种模式**：聊天、解释代码、翻译、总结、使用教程，可叠加自定义 System Prompt
- **流式输出**：本地 HTTP 流式服务，监听 127.0.0.1 随机端口，随机 token 校验，逐 token 显示
- **思考过程**：`reasoning_content` 以折叠块呈现，默认收起
- **Markdown 渲染**：`marked` + `DOMPurify`，代码块由 highlight.js 高亮，明暗两套配色跟随主题
  - LaTeX 公式用 KaTeX，支持 `\(..\)`、`\[..\]`、`$..$`、`$$..$$` 与裸 `\begin{}`；流式生成中公式未写完时显示原文，写完即渲染
  - Mermaid 在流式结束后绘制成 SVG，配色跟随主题；图上带工具栏，可折叠长流程图、可切换查看源码；解析失败时保留源码显示
  - 表格超宽可横向滚动，点击表头排序，可导出 CSV
- **导出**：公式、流程图、代码块可右键「复制为图片」，输出 2 倍图。矢量图序列化 SVG，公式走 foreignObject，由后端写入剪贴板；流程图另可下载 PNG 或 SVG。`html` 代码块提供全屏沙箱预览，`sandbox` 不含 `allow-same-origin`
- **后台运行**：切换到其他页面时回答继续生成，返回后仍在原位置。侧栏 AI 入口带状态点，生成中呼吸，回答完成未查看为实心点
- **中断与重试**：生成中可以停下，停下那条会标注「已中断」并保留内容，随时可以重试
- **会话**：多会话、标题自动生成与手动重命名、会话搜索、清空、删除
- **模型可见**：每条回答下方标注实际用的模型，流式生成和刷新后一致
- **Key 存储**：Windows DPAPI 加密，前端不接触密文，支持测试连接
- **多处复用**：同一份档案同时服务对话页、SSH 侧栏、剪贴板与笔记的问答，以及插件调用的 `host.ai.ask`，各入口互不干扰

</details>

<details>
<summary><b>AI 用量统计</b></summary>

「设置 → AI」中查看近 7 天、30 天、全部三个区间的花费估算、问答次数、Token 与对话数，配每日柱状图。数据按模型和调用入口拆分，入口包括对话、面板与插件、标题生成、摘要压缩；缓存命中节省的输入部分单独标出。

记账覆盖全部调用入口：对话页、SSH 侧栏、剪贴板与笔记的「问 AI」、插件的 `host.ai.ask`、自动起标题、长对话压缩。

统计口径按内置单价线性估算，不构成账单。缓存折扣、批量优惠、阶梯计价均未计入，实际以服务商结算为准。

旧版本只记录会话累计 token，升级后这部分折算为一条「历史折算」记录，总额保留，但当时的分布无法还原。

</details>

<details>
<summary><b>插件系统</b></summary>

- 三种运行时（none / goja / native），通信走 JSON-RPC 2.0
- Host API 共 34 个方法、14 组能力：日志、存活探测、通知、剪贴板、对话框、按插件隔离的 KV 存储（`db.*`）、文件读写（`host.fs.*`，按 `permissions.filesystem` 的目录 scope 校验）、网络代发（`http.get/post`，域名白名单）、Shell 打开、进程 list/exec/kill（需 `process` 与 `processKill` 权限）、窗口显隐、加解密（`host.crypto.*`）、复用宿主 MCP 的 `host.mcp.call`、复用宿主 AI 档案的 `host.ai.ask`
- 前端桥接：`qdConfirm`、`qdAlert`、`qdPickFile`、`qdPickFolder`、`qdReadFile`、`qdHostCall`、`qdHttp`、`qdAiAsk`、`qdPrint`、`qdCrypto`
- **权限模型**：manifest 的 `permissions` 声明所需能力，未声明的一律拒绝；native 运行时在安装期用 `go/ast` 静态审计高危调用（`os.Remove`、`exec.Command`、`net.Dial` 等）与声明的一致性，不符则告警
- **安装前看清权限**：装 zip 之前先解出清单，把要用的权限逐条列出来确认，不点同意不落盘
- **卸载可选保留数据**：卸载时勾一下保留 `db.*` 里的插件数据，重装回来还在
- 支持运行时安装、卸载、启用、禁用、热键绑定，以及「检查更新」一次性比对市场里的版本

</details>

<details>
<summary><b>MCP 服务</b></summary>

应用对外暴露 Model Context Protocol 服务，供 AI 客户端调用本地能力。

- **地址**：`http://127.0.0.1:9230/mcp`，Streamable HTTP，仅接受 POST，协议版本 2025-06-18
- **工具集共 35 个，分三级权限**，默认开放只读与低危写，可在环境管理页按档位或逐个工具开关：
  - 只读（19）：`app_info`、`env_list`、`env_status`、`env_log`、`env_versions`、`file_search`、`workspace_list`、`item_search`、`recent_items`、`todo_list`、`note_search`、`clipboard_recent`、`port_list`、`log_list`、`log_read`、`crash_list`、`crash_read`、`plugin_list`、`plugin_exec_logs`
  - 低危写（14）：`env_start`、`env_stop`、`env_restart`、`robot_notify`、`item_open`、`todo_create`、`todo_done`、`clipboard_copy`、`note_create`、`note_update`、`note_quick`、`note_folder_create`、`plugin_execute`、`plugin_open`
  - 高危（需在环境管理页开启）：`process_kill`、`system_command`
- 插件相关：`plugin_execute` 可传结构化参数（`input` 为字符串或 JSON 对象，键由插件定义）；纯前端插件（`runtime=none`）宿主无法执行，改用 `plugin_open` 带参打开界面；`plugin_exec_logs` 回看执行记录，AI 触发的记录来源标为 `mcp`
- 插件通过 `host.mcp.call` 复用全部工具，沿用同一套等级门
- 服务随主进程启停，应用退出即失效

</details>

<details>
<summary><b>环境管理</b></summary>

28 种运行时的版本管理，安装、切换、启停都在用户目录内完成，不写注册表，不改系统 PATH，不申请管理员权限。

| 分组 | 运行时 |
|------|--------|
| 语言 | Node.js、Go、PHP、Python、Bun、Erlang、JDK |
| 网络服务 | Nginx、Caddy、Apache、Traefik、FTP、WebDAV（内置 9080） |
| 数据库 | MariaDB、MySQL、PostgreSQL、MongoDB |
| 中间件 | Redis、Memcached、MinIO、RabbitMQ |
| 开发工具 | Git、Composer、FFmpeg、Mailpit、frpc、GitHub CLI、mkcert |
| AI 服务 | Ollama、MCP 服务（内置 9230） |

- **存储结构**：`%APPDATA%\QuickDock\runtime\<name>\<version>`，便携优先，多版本共存
- **下载**：源地址可切换，支持 `{version}` `{v}` `{os}` `{arch}` `{ext}` 占位符；版本列表智能筛选并缓存 1 小时；代理容错与多源回退；进度后台推送
- **服务管理**：启停与重启，配置编辑（Nginx、Apache、Caddy 在启动前执行 `*-t` 校验），实时日志（尾部 8KB），Web 控制台，端口总览，本地开发站点（自定义域名 + 自动 HTTPS）
- **项目版本检查**：选一个目录，读 `.nvmrc` / `.tool-versions` / `go.mod` / `package.json` 等版本声明（含上级目录），和本机已装、当前激活的版本逐条对比，缺的当场切过去
- **文件搜索自测**：页内可以直接调一次与命令面板相同的检索接口。返回 0 条但状态正常时，通常是 Everything 索引没建好或以管理员身份运行导致 IPC 不通
- **Ollama**：单版本语义，程序目录在 `runtime/ollama/`；有新版本只提示不自动更新；模型库共享 `~/.ollama/models`；卸载只删程序
- **代码位置**：门面层 `services/env/`，引擎层 `internal/env/`（注册表、下载、解压、服务生命周期、项目级版本探测，平台差异用 `_windows` `_darwin` `_unix` 分文件）

</details>

<details>
<summary><b>截图工具</b></summary>

`F1` 唤起全屏覆盖层，框选、标注、复制、保存、贴图在同一个覆盖层内完成，不打开第二个窗口。

- **覆盖层**：Win32 分层窗口，`UpdateLayeredWindow(ULW_ALPHA)` 整帧提交，不走 `WM_PAINT`；显示顺序为 `SW_HIDE → SetWindowPos → 绘制 → SW_SHOW`
- **工具条**：框选完成后原地浮出。矩形、椭圆、箭头、直线、马赛克、文字六种标注，颜色 9 种、线宽 4 档、字号 6 档，支持撤销重做与复制、保存、贴图、关闭
- **窗口吸附**：实时高亮光标下的窗口，单击选中，按住 `Ctrl` 下沉到子控件；排除自身窗口、`WS_EX_TRANSPARENT` 与 DWM cloaked 窗口
- **可再编辑**：选框 8 个控制点，可平移；标注图形可点选、拖动、删除；文字双击重新编辑（另开 owned 顶层窗口承载 IME）
- **贴图**：原生 GDI 窗口置顶，可拖动、滚轮缩放（以光标为锚点）、双击关闭
- **快捷键**：`Enter` 或 `Ctrl+C` 复制，`Ctrl+S` 保存，`Ctrl+Z` / `Ctrl+Shift+Z` 撤销重做，`Ctrl+A` 全选，`R` `E` `A` `L` `M` `T` 切换工具，方向键微调（按住 `Shift` 为 10 倍），`Delete` 删除，`Esc` 逐层退出

</details>

<details>
<summary><b>窗口管理</b></summary>

纯热键的贴屏与排版，不占用 `Win+方向`。

- **15 个贴屏热键**：左右上下半屏、四分之一屏、居中、最大化、还原、最小化、置顶切换、移到上一屏 / 下一屏，统一 `Ctrl+Alt` 前缀，均可改键
- **排版浮层 `Ctrl+Alt+W`**：点击九宫格任意一格，当前窗口进入该格，其余格由同屏其他可见窗口按 Z 序填充。9 种模板：左右对半、左主右二、右主左二、上主下二、下主上二、四象限、三列、三行、九宫格
- **分屏比例**：左右半屏宽度占比 10%–90%，默认 50%；多显示器按光标所在屏幕换算几何

</details>

<details>
<summary><b>SSH 远程终端</b></summary>

- **窗口**：`Ctrl+Alt+S` 唤出独立可缩放窗口，切换窗口不中断会话
- **主机管理**：主机与分组（文件夹）树形组织，拖拽排序，环境标签（生产、预发、测试），备注。凭据与配置分开存储
- **认证**：密码、私钥（ed25519 等）及私钥口令；兼容旧设备的 `ssh-rsa` 与 SHA-1，需开启 legacy 开关
- **指纹信任**：首次连接弹出指纹确认，可与服务器端命令输出对照。已信任的指纹在 SSH 窗口顶栏的 known_hosts 管理中查看与清除，指纹变更时主动告警
- **多标签**：同主机连接池复用，多标签只握手一次；PTY 终端模拟，支持复制、粘贴、全选、清屏、滚动回放与断线重连；关掉窗口再打开会恢复上次的标签页
- **主机表单**：没保存也能先测连通性，避免存下一个连不上的配置
- **SFTP**：远程目录浏览、上传、下载，可多选批量下载 / 移动 / 删除，路径框能直接输入跳转，终端字号可临时放大缩小
- **命令执行**：非交互式 `Exec`，带超时、退出码与输出截断，用于脚本化运维
- **一次性执行**：单独的「执行命令」窗口，跑完即断开、不留会话。生产主机会先核对风险，高危命令只给填入不给执行，`Ctrl+Enter` 运行，输出可复制
- **批量执行**：在分组上选择多台主机，先预演（只读，列出每台将要执行的命令与风险等级）再运行，逐台回传输出与退出码，可随时取消；列表上会显示上次执行时间，避免重复跑

</details>

<details>
<summary><b>终端内 AI 助手</b></summary>

SSH 终端右侧的 AI 侧栏，把终端里看到的现象转成可执行的命令。

- **屏显上下文**：问题里提到「屏显」时，这一轮附带终端当前画面。不提到就不发送，节省 token，也避免把无关内容带进上下文。屏显内容不作故障假设，正常输出、命令结果、运行中的程序界面都会如实描述
- **环境徽章**：侧栏头部常驻当前环境。生产环境下除了告知模型，还约束输出口径：默认先给只读探查命令，任何写入、删除、重启、改配置的命令必须写明影响范围与回滚方式
- **选中即问**：终端中选中一段输出，右键「问 AI 这段输出」，选区单独作为上下文发送（权重高于屏显），侧栏自动打开并预填问题
- **命令卡片**：回答中的命令单独成卡，可一键填入终端或一键执行；空状态里的示例问题点击即填入输入框
- **安全边界**：只给命令，不自动执行。三层相互独立：
  - 提示词层：生产环境要求先探查后变更，变更须带回滚
  - 标注层：`rm -rf`、`dd`、`mkfs`、`iptables -F`、`reboot`、`kubectl delete`、`docker rm`、`DROP TABLE`、`git reset --hard`、`git push -f` 等命令只提供「填入」，不提供「执行」；中风险命令（`systemctl restart`、`sed -i`、卸载软件包、`crontab -r` 等）保留一键执行，但风险说明排在按钮之前。多条规则同时命中时取最高等级
  - 确认层：生产环境点「执行」需两次确认，第一次只把按钮切成「再点一次确认」。规则表是有限收录的，`sed -i` 改错配置、写了一半的 `DELETE` 都匹配不到规则，这一层用于兜底；测试与预发环境保持一步执行
  - 「填入」只写入远端输入缓冲，不发送回车
- **多轮追问**：侧栏内保留上下文。提示词按本次问题、屏显、历史的顺序控制预算，历史过长时丢弃最旧的，更早的轮次压缩成摘要继续携带
- **中途停止**：「思考中」时发送键变为停止键
- **会话隔离**：切换会话清空上下文，不把 A 机器的屏显带到 B 机器
- **回答方式**：面板头部可切换流式或一次性返回，偏好存本地，SSH、剪贴板、笔记三处共用
- **档案提示**：标题旁显示当前激活的 AI 档案名，悬停查看模型
- **未配置时**：没有 API Key 时点「问 AI」直接跳转到「设置 → AI」，不打开必然失败的面板
- **与对话页独立**：复用同一份档案，但不落库、不占用对话上下文

</details>

<details>
<summary><b>数据目录搬家</b></summary>

「设置 → 存储与同步」中可以更换存储位置。C 盘空间不足时，可把整个数据目录迁到其他磁盘。

- **迁移范围**：数据库（工作空间、待办、笔记、剪贴板索引）、剪贴板图片、插件、日志，以及便携运行时、模块缓存、依赖树。迁移后不需要重新下载运行时
- **路径规则**：选择 `D:\` 落到 `D:\.quickdock`，选择 `D:\data` 落到 `D:\data\.quickdock`。界面会回显实际目标路径
- **进度**：打包、复制、解压、改写环境变量、清理旧目录，每段显示文件数、体积与当前处理的文件
- **环境变量**：指向旧目录的 `PATH`、`GOROOT`、`GOPATH` 会改写为新目录。已打开的终端需要重开才能读到新变量
- **重启完成**：数据库文件被进程独占，无法直接移动，先导出快照，重启时写入新位置，旧目录也在重启后删除。迁移完成到重启之间两个目录同时占空间，目标磁盘需留出压缩包与解压后的峰值余量
- **失败处理**：最后一步之前旧目录始终完整，报错后可重试；打包中断留下的半成品会被清理

安装器提供「选择数据目录」页，仅首次安装出现，覆盖升级不会重置已选位置。卸载只删除程序，不动数据。

</details>

<details>
<summary><b>WebDAV 同步、快照与系统命令</b></summary>

- **WebDAV 同步**：全量 JSON 备份与恢复、多版本、任意 WebDAV 服务器。后端分层为 `services/sync.go` → `internal/sync` → `internal/webdav`
- **快照备份**：一键把全部数据导出为 JSON，可导入恢复
- **系统命令**：锁屏、关机、重启、睡眠、清空回收站

</details>

<details>
<summary><b>自动更新</b></summary>

启动后拉取签名更新清单并验签：

```
https://github.com/parieses/quickdock/releases/latest/download/manifest.json
```

| 阶段 | 行为 |
|------|------|
| 检查 | 拉取 manifest.json，ed25519ph 验签 + sha512 校验，版本更高才提示 |
| 下载 | 下载裸 `quickdock-amd64.exe` 并校验摘要，支持多源镜像回退 |
| 重启 | 点「重启更新」，派生 helper，主进程退出 |
| 替换 | helper 备份旧 exe，覆盖新 exe（失败回滚），重新拉起 |

全程不弹 UAC、不运行安装器。用户数据、数据库、插件、运行时目录全部保留，单文件应用中前端经 `go:embed` 打进可执行文件。

macOS 每个 release 产出 `quickdock-darwin-arm64` 与 `quickdock-darwin-amd64` 两个包，`.app` 需安装到 `~/Applications` 才能就地更新；无 Developer ID 时为 ad-hoc 签名，正式分发还需补签名与公证。

发版时版本号的唯一来源是 git tag，`main.appVersion` 恒为 `"0.0.0"`，打包时由 Taskfile 的 `APP_VERSION`（`git describe --tags --abbrev=0`）通过 ldflags 注入。

</details>

---

## 架构

服务门面分层，引擎下沉，依赖单向无环：

```
┌──────────────────────────────────────────────────────────────────┐
│                         QuickDock (Wails v3)                      │
│                                                                   │
│   ┌────────────────────┐      ┌────────────────────────────────┐  │
│   │  Vue 3 前端         │◄────►│  services/  Wails 绑定门面      │  │
│   │  (frontend/dist     │ IPC  │  AppService + 23 领域子包      │  │
│   │   go:embed)         │      │  456 个绑定方法                 │  │
│   └────────────────────┘      └───────────────┬────────────────┘  │
│                                                │ 单向依赖          │
│                                 ┌──────────────▼────────────────┐  │
│                                 │  internal/  引擎层（无绑定）    │  │
│                                 │  db / env / sites / sshx /    │  │
│                                 │  dsh / plugin / platform /    │  │
│                                 │  sync / webdav / mcp /        │  │
│                                 │  screenshot / winmgr / dl /   │  │
│                                 │  logger / sysutil / validators│  │
│                                 └───────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────┘
```

几点实现上的选择：

- **门面分层**：领域能力下沉到 23 个门面子包（334 个导出方法），宿主核心留在 AppService（129 个导出方法），最终暴露 456 个绑定方法
- **截图层用原生窗口**：自建 Win32 分层窗口整帧提交，工具条、标注、图形都画进同一张 DIB，可以避免 WebView 流程带来的闪烁
- **次级窗口延迟创建**：首次热键触发时才创建 WebView2，减少启动白屏；浮窗位置经 kvstore 持久化
- **后台服务**：SQLite WAL、待办（10s）、定时与监控调度器、AI 流式 HTTP 服务、插件健康检查、DSH 隐藏进程、内置 MCP（9230）
- **安全**：纯 Go SQLite 无 CGO，SQL 列名白名单，API Key 走 DPAPI，单实例锁防止并发写库，本地流式服务使用随机端口与随机 token
- **规模**：Go 源码约 5.2 万行（`services` 2.5 万 + `internal` 1.9 万 + 入口与窗口 0.3 万，含 104 个测试文件），前端约 6.4 万行（70 个 Vue 组件 + 83 个 TS 模块，其中 23 个单测文件）

---

## 技术栈

**后端**

| 技术 | 用途 |
|------|------|
| Go 1.26 | 主语言 |
| Wails3 v3.0.0 | 桌面应用框架 |
| modernc.org/sqlite | 纯 Go SQLite，无 CGO |
| go-sql-driver/mysql + go-redis/v9 | MySQL 与 Redis 客户端 |
| golang.org/x/sys | Windows 系统 API |
| dop251/goja | JavaScript 沙箱，插件执行 |
| DPAPI | Windows 数据保护 API，用于加密 Key |

**前端**

| 技术 | 用途 |
|------|------|
| Vue 3 + TypeScript | UI 框架 |
| Vite 8（Rolldown） | 构建工具 |
| Pinia 3 | 状态管理 |
| vue-i18n 11 | 国际化，简中与英文 |
| @lucide/vue | 图标 |
| pinyin-pro | 拼音搜索 |
| marked 18 + DOMPurify | Markdown 渲染与净化 |
| KaTeX、Mermaid、highlight.js | 公式、流程图、代码高亮，按需分包 |
| @floating-ui/vue 2 | 浮层定位 |
| @wailsio/runtime 3.0.0-alpha.96 | Wails 前端运行时 |

---

## 开发

### 前置条件

- Go 1.26+
- Node.js 22+
- Wails3 CLI，版本须与 `go.mod` 中一致（当前 `v3.0.0`）

```bash
# 安装 Wails3 CLI，版本必须与 go.mod 的 wails 模块完全一致
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0

# 前端依赖
cd frontend && npm install

# 重新生成 bindings，修改 Go 绑定后必须执行
wails3 generate bindings -ts -i -clean   # 等价于 cd frontend && npm run bindings
```

### 常用命令

| 命令 | 说明 |
|------|------|
| `wails3 dev` | 开发模式，前后端热重载 |
| `wails3 build` | 生产构建，含前端构建与绑定生成 |
| `task build` / `task run` | 通过 Taskfile 构建或运行 |
| `task windows:version` | 输出最终注入 `main.appVersion` 的版本号 |

开发版与正式版共用同一 SQLite 数据库与单实例锁，同一时间只能运行一个实例。

### 数据存储

默认位于 `~/.quickdock`，可在「设置 → 存储与同步」整目录迁移到其他磁盘。配置文件放在数据目录之外，否则更换数据目录后就找不到「数据目录在哪」这条配置。

| 路径 | 说明 |
|------|------|
| `~/.quickdock/quickdock.db` | SQLite，WAL 模式、外键开启、busy timeout 5s |
| `~/.quickdock/images/` | 剪贴板图片 |
| `~/.quickdock/plugins/` | 已安装插件 |
| `%APPDATA%/QuickDock/` | 应用配置 |
| `~/.quickdock/runtime/node`、`~/.quickdock/dsh` | 便携 Node 与 DSH |
| `~/.quickdock/logs/quickdock-YYYY-MM-DD.log` | 主日志 |
| `~/.quickdock/logs/crash/` | 崩溃现场，`js-*` 与 `panic-*`，可在设置中查看 |

### 项目结构

```
quickdock/
├── main.go / tray.go / windows.go        # 入口、托盘、浮动窗口、单实例锁
├── services/                             # Wails 门面层，宿主 + 23 领域子包，456 个绑定方法
│   ├── service.go  api_result.go  types.go  lifecycle.go
│   ├── hotkey.go  theme.go  autostart.go  winmgr_service.go
│   ├── app_launcher.go  focus.go  tool.go  clipboard_sys.go  clipboard_files.go
│   ├── screenshot.go  sync.go  palette.go  plugin_hotkey.go
│   ├── schedule.go  schedule_runner.go  reminder.go  webhook_notify.go
│   ├── monitor_checker.go  notify.go  visibility.go  navbadge.go
│   ├── storage.go  storage_service.go  clipboard_image.go
│   └── ai/ clipboard/ collection/ diag/ dsh/ env/ frecency/ item/
│       logtail/ mcp/ monitor/ note/ notify/ plugin/ port/ recycle/
│       scene/ snapshot/ sshx/ system/ todo/ update/ workspace/
├── internal/                             # 引擎层，被门面调用，无 Wails 绑定
│   ├── db/  env/  sites/  sshx/  dsh/  plugin/  platform/  sync/  webdav/  webdavsrv/
│   ├── mcp/  screenshot/  winmgr/  dl/  logger/  sysutil/  validators/
│   └── aiutil/  cliprule/  logtail/      # 提示词预算、剪贴板规则、文件尾随
├── frontend/src/                         # Vue 3 + TS
│   ├── App.vue  main.ts  style.css  pages/  i18n/  stores/  composables/  utils/
│   └── components/                       # 三栏主界面、面板、设置、插件、环境、监控等
├── plugins/                              # 宿主骨架与开发模板，外部插件在独立仓库
├── build/  docs/  website/  image/  Taskfile.yml
├── DESIGN.md  AGENTS.md  LICENSE
└── main_unix.go  main_windows.go  windows_unix.go  windows_windows.go
```

工程约束（提交纪律、样式 token、目录职责、自检清单）见 [AGENTS.md](./AGENTS.md)。

---

## 界面设计

暗色为主，灰阶分层而非纯黑，支持深色、浅色与跟随系统。强调色 `#4a9eff` 只用于功能交互。

- 三栏布局：侧边栏 210px，集合列表 300px，项目列表自适应
- 8px 基准间距，系统字体栈，基础字号 13px，过渡 150ms
- 键盘优先，焦点顺序与快捷键覆盖全部主流程
- 用 `box-shadow` 替代 `border`，避免状态切换引起布局偏移

完整规范见 [DESIGN.md](./DESIGN.md)。

---

## 插件生态

官方插件在独立仓库维护与分发：[quickdock-plugins](https://github.com/parieses/quickdock-plugins)，包含插件列表、`plugin.json` 字段说明、三种运行时的快速开始、通信协议、调试与发布流程。

主仓库只保留宿主实现（`internal/plugin/` 引擎与 `services/plugin/` 门面），用户安装的插件落在 `~/.quickdock/plugins/<plugin-id>/`。现有插件覆盖 HTTP 客户端、数据库连接、OCR、端口检查、JSON / 正则 / 哈希 / 颜色 / JWT / 二维码工具、PDF 与图片处理、代码对比、Hosts 管理、网络诊断、磁盘分析、重复文件查找、批量重命名、本地文件搜索、图床上传等。

---

## 许可

[MIT License](./LICENSE) © 2025–2026 王亮亮

## 第三方组件

[Wails](https://wails.io/) · [Vue.js](https://vuejs.org/) · [Floating UI](https://floating-ui.com/) · [Lucide](https://lucide.dev/) · [modernc.org/sqlite](https://modernc.org/sqlite) · [marked](https://marked.js.org/) · [DOMPurify](https://github.com/cure53/DOMPurify) · [KaTeX](https://katex.org/) · [Mermaid](https://mermaid.js.org/) · [highlight.js](https://highlightjs.org/)
