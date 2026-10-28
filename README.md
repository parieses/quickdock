<!-- README 设计说明：英雄区 → 截图 → 功能卡片网格 → 可折叠详解 → 架构 → 技术栈 → 开发指南 → 设计/插件/许可。
     所有长文详解收进 <details>，保证「一眼看懂」且不影响信息完整性。
     修改后请同步官网 website/index.html 的更新日志（见 AGENTS.md 发版纪律）。 -->

<div align="center">

# 快启坞 QuickDock

**面向 Windows 开发者的桌面效率中枢** —— 资源集合 · 快速启动 · 工作空间管理

*Raycast 的快启体验 × VS Code 的开发者质感，加上开箱即用的本地开发环境*

![Platform](https://img.shields.io/badge/Platform-Windows%2010%2F11-4a9eff)
![Version](https://img.shields.io/badge/Version-v0.0.30-4a9eff)
![License](https://img.shields.io/badge/License-MIT-4a9eff)
![Built with](https://img.shields.io/badge/Built%20with-Go%20%2B%20Vue-4a9eff)
![Framework](https://img.shields.io/badge/Framework-Wails%20v3-4a9eff)

🌐 官网 <https://parieses.github.io/quickdock/>　·　🐛 Issues <https://github.com/parieses/quickdock/issues>　·　🔌 插件市场 <https://github.com/parieses/quickdock-plugins>

</div>

---

![主界面截图](image/main-ui.png)

> 一张图看不全 —— 它把「资源管理、剪贴板、命令面板、待办、监控、AI、环境管理、插件」全收进一个窗口，下面拆开讲。

---

## ✨ 它解决什么

开发者桌面上永远散落着：项目目录、常用命令、文档链接、待办、监控地址、一堆需要切换版本的运行时……QuickDock 把它们统一收进一个**键盘优先**的窗口里，并额外塞进了截图、窗口排版、本地站点 HTTPS、AI 助手和插件市场。

- **一处收纳**：工作空间 → 场景 → 集合 → 项目 四级资源树，支持拖拽、全文搜索与变量展开
- **键盘驱动**：`Ctrl+Space` 主窗口、`Ctrl+K` 命令面板、`Ctrl+`` 剪贴板、`F1` 截图，全部可改键
- **开箱环境**：28 种运行时（语言 / 服务 / 数据库 / 中间件 / AI）一键安装、版本切换、启停、日志、控制台
- **本地智能**：内置 AI 助手（多模型 SSE 流式）+ DeepSeek Harness 编程入口 + 对 Agent 开放的 MCP 服务
- **免管理员**：单文件应用、用户态安装、免 UAC 就地更新

---

## 🧩 功能全景

<details open>
<summary><b>核心效率</b></summary>

| 能力 | 默认热键 | 一句话 |
|------|---------|--------|
| 工作空间 / 场景 / 集合 / 项目 | — | 四级资源树，6 种项目类型，拖拽排序 + FTS5 全文搜索；主界面按「导航 / 场景 / 集合 / 项目」分栏，场景独立成栏，导航再多也挤不到场景 |
| 命令面板 | `Ctrl+K` | 一个框搜遍全部资源，**结果按来源分成 14 个区**：项目 / 快速链接 / 笔记 / 待办 / 应用程序 / 文件 / 插件命令 / 环境服务 / 系统命令 / 剪贴板建议 / 计算器 / 网页，外加「最佳匹配」与「最近」两区；待办搜到就能直接标记完成或跳回它那一天；URL 智能识别存为项目 |
| 剪贴板历史 | `` Ctrl+` `` | 文本 / 图片 / 文件监听，固定、搜索、过期清理，浮动窗口唤出；「图片」「文件」标签与网址 / 邮箱 / 电话 / JSON / 代码这类智能分类都在入库时分好类、查库直取，不受「最近 1000 条」限制 |
| 剪贴板自动规则 | — | 复制进来的内容按条件自动清洗：去首尾空白 / 删空行 / 去重行 / 排序 / JSON 格式化与压缩 / 去 ANSI 颜色 / URL 解码 / 大小写转换，可只对匹配来源或正则的内容生效，改之前能先「试运行」 |
| 快捷笔记 | `Ctrl+Alt+N` | 树形 Markdown 笔记库，自动保存，失焦隐藏 |
| 快捷键备忘单 | — | 命令面板输入 `cheatsheet` 或点「设置 → 快捷键」页头的按钮，一张表列出全部热键；内容实时读自热键设置，你改过键它就跟着变 |
| 最近动态 | — | 「设置 → 历史」：左栏是使用热度榜（最常打开的项目 / 场景），右栏是操作时间线，知道自己这段时间在忙什么；同一页可切到回收站 |
| 日志查看器 + 实时尾随 | — | 「设置 → 日志」：按天查看应用日志与崩溃记录；任意本机文件都能 `tail -f`（命令面板 `logtail`），新增行增量追加、可暂停 / 过滤高亮 / 清空，日志被轮转或原地重写会自动重新对齐 |
| 通知中心 | — | 侧栏底部铃铛：监控状态翻转、定时任务执行、AI 回答完成统一收口，未读有角标，点开可跳回对应对象 |

</details>

<details open>
<summary><b>任务与监控</b></summary>

| 能力 | 说明 |
|------|------|
| 待办 + 🍅 番茄专注 | 列表 / 看板视图、子任务、标签、重复与到期提醒；专注结束系统通知 + 可选 Webhook |
| 定时任务 | 5 种动作 × 4 种调度（一次性 / 间隔 / 每天 / 每周），执行后系统通知 + 结果进通知中心 |
| 网站监控 | HTTP 状态码 / 响应时间、SSL 到期预警、关键字 / 正则匹配、响应趋势图，状态翻转多渠道推送 + 进通知中心 |

</details>

<details open>
<summary><b>AI 与开发</b></summary>

| 能力 | 说明 |
|------|------|
| AI 助手 | OpenAI / DeepSeek / Kimi / 通义 / Ollama / Azure / 自定义兼容；SSE 流式、思考折叠、Markdown 渲染、DPAPI 加密 Key；填好 BaseURL 与 Key 即可一键拉取可选模型列表 |
| 回答渲染 | LaTeX 公式（KaTeX）、Mermaid 流程图、代码高亮、表格点表头排序与导出 CSV、`html` 代码块沙箱预览；流程图和公式右键「复制为图片」，流程图还能下载 PNG / SVG |
| AI 用量与花费 | 「设置 → AI」：近 7 天 / 30 天 / 全部，估算花费、问答次数、Token、每日柱状图，按模型与按入口（对话 / 面板与插件 / 标题 / 摘要）拆分；输入缓存命中省了多少单独标出 |
| AI 图片理解 | 对话里贴图 / 选文件 / 拖拽，AI 直接理解图片内容；缩略图可点击全屏放大；按模型能力配置输入方式 |
| 终端内 AI 助手 | SSH 终端里的 AI 侧栏：问题里提到「屏显」才附带当前屏幕、选中输出右键即问、命令一键填入 / 执行；回答可中途停止；高危命令只给填入，生产环境额外要求二次确认 |
| 剪贴板 AI | 剪贴板条目上「问 AI」：翻译 / 摘要 / 格式化或自由提问，就地弹层给出答案，不跳页 |
| 笔记 AI | 笔记浮窗内嵌面板：总结 / 润色 / 续写 / 翻译，关掉面板就回到原文 |
| DeepSeek Harness | 内嵌 `dsh` 完整 Agent 编程入口，便携 Node 自动装填，原生窗口承载 3080 端口 |
| 内置 MCP 服务 | `http://127.0.0.1:9230/mcp`，三级权限门（只读 / 低危写 / 高危），把本地能力开放给智能体 |
| 环境管理 | 28 种运行时一键装 / 切 / 启停 / 配 / 看日志 / 开控制台，纯用户态不污染 PATH |

</details>

<details open>
<summary><b>工具箱</b></summary>

| 能力 | 默认热键 | 说明 |
|------|---------|--------|
| 截图工具 | `F1` | 原生分层窗口零闪；框选 → 标注 → 复制 / 保存 / 贴图一条链路；窗口 / 元素吸附 |
| 窗口管理 | `Ctrl+Alt+W` | 15 个贴屏热键 + 九宫格排版浮层，不抢 `Win+方向` |
| 本地开发站点 | — | 项目一键绑自定义域名 + 自动 HTTPS（mkcert），自身不监听端口 |
| SSH 远程终端 | `Ctrl+Alt+S` | 独立可缩放窗口管理远程主机：多标签终端 + SFTP 文件管理，指纹信任防中间人；同一分组可多选主机批量执行，逐台回传结果 |
| WebDAV 云同步 / 快照备份 | — | 全量 JSON 备份恢复、多版本、任意 WebDAV；一键导出导入 |
| 数据目录搬家 | — | 「设置 → 存储与同步」改存储位置：整个数据目录（含已装好的运行时）打包搬到新盘，环境变量一并改写，重启后生效；安装时也能直接选目录 |
| 界面缩放 | — | 主界面与全部独立窗口（剪贴板 / 笔记 / 命令面板 / 窗口管理 / SSH / 插件）统一 1.15x 等比放大，文字、图标、间距一起放大而布局比例不变 |

</details>

<details open>
<summary><b>插件生态</b></summary>

- 开放插件架构，三种运行时：**none（纯前端）/ goja（内嵌 JS 沙箱）/ native（独立子进程）**，基于 JSON-RPC 2.0
- 34 个 Host API / 14 组能力，权限声明式校验（文件系统 scope、网络白名单、剪贴板、进程等），native 安装期做 `go/ast` 静态审计
- 向插件开放宿主 AI 能力：`host.ai.ask` 复用用户已配好的档案（凭据不出后端），带 `streamId` 即流式——三种运行时各有增量通道（native 走 `host.ai.delta` 通知、goja 走 `api.aiStream` 回调、前端插件走 `qdAiAsk`）
- 官方插件经「在线市场」一键安装 / 升级，清单与开发文档统一在 [quickdock-plugins](https://github.com/parieses/quickdock-plugins)

</details>

---

## 🚀 快速开始

### 系统要求

- **操作系统**：Windows 10 1809+ 或 Windows 11
- **运行时**：WebView2 Runtime（Windows 自带）
- **磁盘**：约 50 MB（运行时按需下载）

### 下载安装

1. 从 [Releases](https://github.com/parieses/quickdock/releases) 下载 `quickdock-amd64-installer.exe`
2. 双击安装（**免管理员权限**，默认装到用户目录 `%LOCALAPPDATA%\Programs\快启坞`）
3. 运行 `QuickDock.exe`，任务栏托盘出现图标即启动成功

> **保持默认安装路径**：就地替换更新需要对该目录有写权限。若改到 `C:\Program Files\...`，每次更新都要过 UAC，自动更新会失败（只能手动覆盖）。
> 也可直接下载 `quickdock-amd64.exe` 当便携版：只要放在**当前用户可写**的目录（非 `Program Files`），同样支持就地更新。

### 快捷键速览

| 功能 | 默认快捷键 |
|------|-----------|
| 切换主窗口 | `Ctrl+Space` |
| 命令面板 | `Ctrl+K` |
| 剪贴板历史 | `` Ctrl+` `` |
| 快捷笔记 | `Ctrl+Alt+N` |
| 区域截图 | `F1`（被占用回退 `Ctrl+Shift+A`） |
| 窗口排版浮层 | `Ctrl+Alt+W` |
| SSH 远程终端 | `Ctrl+Alt+S` |

> 所有热键均可在「设置 → 快捷键 / 窗口管理」页面自定义、运行时动态重注册。
>
> 记不住？命令面板输入 `cheatsheet` 打开**快捷键备忘单**，或点「设置 → 快捷键」页头的备忘单按钮 —— 里面列的是你当前的键位，不是出厂默认值。

---

## 📚 功能详解

<details>
<summary><b>📦 工作空间与资源管理</b></summary>

- **工作空间（Workspace）** — 顶级容器，隔离不同项目上下文
- **场景（Scene）** — 工作空间下的视图分组，支持标签 / 图标 / 颜色
- **集合（Collection）** — 资源逻辑分组，支持四种打开策略
- **项目（Item）** — 6 种类型：
  - `directory` 目录 · `file` 文件 · `url` 网页 · `command` 终端命令 · `app` 应用 · `quicklink` 带参快捷方式
- 全层级拖拽排序、FTS5 全文搜索；`value` / 命令 / URL 支持 `{date}` `{time}` `{username}` `{clipboard}` 变量即时展开

</details>

<details>
<summary><b>📋 剪贴板历史</b></summary>

自动监听文本 / 图片 / 文件剪贴板；支持固定、搜索、复制粘贴、批量删除；过期自动清理（可配保留天数）；浮动窗口失焦自动隐藏，`` Ctrl+` `` 一键唤出。

监听带自愈：除了系统通知，还会按剪贴板序号兜底巡检，某次通知漏了也能补抓到；捕获卡住或剪贴板被别的程序长期占用会写进日志，不会表现为"复制了却没记上"。被占用的那几行日志会**点名占用者**——进程名、PID、窗口类名与标题都写出来，省得只能看着"探测失败 N 次"猜是谁；实在定位不到（对方用 NULL 句柄打开、没有关联窗口）也会明说，而不是留白。全应用「复制」统一走后端 Win32 写入，不受 WebView2 焦点状态影响——不会再有"点了复制有时没反应"。

筛选分两类标签，语义不同：

- **整类标签**（图片 / 文件 / 全部 / 已收藏）按条目类型筛。图片和文件是独立的一整类，不套"文本"的前提条件——老版本把它们错当成文本的子类，点「图片」永远是空列表
- **智能分类标签**（网址 / 邮箱 / 电话 / JSON / 代码）只在文本条目里找。分类在**入库时**就算好存进去，切标签走数据库直查，所以能翻到全部历史，而不是只在你最近载入的那一千条里筛
- 「全部」列表本身仍按 1000 条载入（越多越拖），要找更早的条目就切到对应标签——那条路是全量的


条目上还有 **「问 AI」**：文本条目直接就地弹层提问，预置翻译 / 摘要 / 格式化和自由追问，回答流式呈现，代码块单独成卡可复制。没有执行动作——剪贴板里的代码是拿去看的，不是拿来跑的。

</details>

<details>
<summary><b>🧹 剪贴板自动规则</b></summary>

复制进来的内容可以先被自动收拾一遍，再进历史：

- **9 种动作**：去首尾空白与每行行尾空白、删空行、重复行去重（保留首次出现）、行排序、JSON 缩进格式化（宽度可配）、JSON 压成一行、去掉 ANSI 颜色与 OSC 序列、URL 解码、大小写转换（全小写 / 全大写 / 首字母大写）
- **三个命中条件**：全部文本 / 内容匹配正则 / 来自某个来源窗口，条件不满足就原样放过
- **按顺序串成流水线**：多条规则依次执行，上一条的输出是下一条的输入
- **改之前先试运行**：规则页里粘一段样例文本，直接预览清洗结果，不用真的去复制一次
- **总开关默认关**：不希望任何「自动改写剪贴板」的行为在没被知情的情况下生效；开启后规则才会在入库前生效
- **不做脱敏**：不识别也不替换身份证、手机号、密钥这类内容。脱敏会静默改写你复制的东西，一旦误判反而更危险——要脱敏请自己粘进 AI 处理

</details>

<details>
<summary><b>🔔 通知中心</b></summary>

侧栏底部一个铃铛，把散落各处的「事情办完了」收在一起：

- **三个来源**：网站监控状态翻转（掉线 / 恢复）、定时任务执行结果、AI 回答完成
- **不打扰**：AI 完成时只要「主窗口可见 **且** 正停在 AI 对话页」就不推通知（窗口有焦点当然更不推）——你眼睛就在这段回答上，复述一遍只会淹掉真正需要看的东西；只有切去了别的页面、或窗口隐藏/最小化时才收进中心
- **未读角标用强调色**：红色留给「监控掉线」这类真故障，未读数不冒用告警色
- **点了能回到现场**：监控通知跳到那条监控，定时通知跳到那个任务，AI 通知回到那次对话；可单条删除、全部标记已读、一键清空

</details>

<details>
<summary><b>📜 日志查看器与实时尾随</b></summary>

「设置 → 日志」既能按天回看，也能盯住一个正在被写入的文件：

- **按天查看**：主日志按日期列出，可按级别筛选、一键清理旧日志；同页看 JS / Go panic 崩溃现场
- **实时尾随**：任意本机文件都能 `tail -f`（命令面板输入 `logtail`）—— 打开时先读末尾 N 行，之后每 500ms 只读新增的部分，不是整本重读
- **自动对齐**：文件被轮转（新建同名文件）或被原地截断重写，都能识别出来并重新对齐读取位置，不会停在旧偏移上假装没有新内容
- **可暂停、可过滤**：暂停后新行攒着不显示，恢复时一并补上；关键字过滤 + 高亮，只看关心的那几行
- **有上限**：最多留 5000 行，超了丢最旧的，长时间挂着不会把内存吃满；**渲染**同样封顶 5000 行，超出的部分不再往页面里塞，顶部会明说"已渲染前 N 行，请用搜索 / 筛选缩小范围"，而不是让窗口越滚越卡

</details>

<details>
<summary><b>🔍 命令面板</b></summary>

全局搜索工作空间 / 场景 / 集合 / 项目；选中即操作，键盘导航 + 多选批量；FTS5 全文搜索、URL 智能识别存为项目；最近 / 最常使用列表；浮动窗口 `Ctrl+K` 即时唤出。

**结果按来源分区**，每个区带标题与计数：最佳匹配、最近、项目、快速链接、笔记、待办、应用程序、文件（Everything）、插件命令、环境服务、系统命令、剪贴板建议，以及计算器与网页两组就地结果。空输入时展示最近使用，可展开看全部。

**待办也能搜**：输入标题、备注或标签会出现「待办」区。只列还没完成的顶层任务——已完成的和子任务不掺进来，搜了也用不上。回车不是"打开待办页"，而是**直接站到那一条上**：切到待办页、跳到它所在的日期、必要时清掉标签过滤再切回列表视图，目标行高亮两秒；动作菜单里还能直接「标记完成」。

剩下的边界：**剪贴板历史条目不参与搜索**（面板里的「剪贴板建议」只作用于你刚复制的那一条）；各区有显示上限（应用 8 / 文件 20 / 快链 3），目前只有「最近」区能展开。

面板里输入 `cheatsheet` 直接弹出**快捷键备忘单**：全局热键、窗口管理 15 个贴屏键、面板内操作键三张表，全部实时读自热键设置 —— 改完键位不用等文档更新。

</details>

<details>
<summary><b>✅ 待办任务（Todos）</b></summary>

- 待办列表 + 看板视图（待办 / 进行中 / 已完成）
- 子任务、标签分类、重复待办（每日 / 每周 / 每月）、到期系统通知
- 🍅 番茄专注：待办页启动倒计时，结束系统通知 + 可选 Webhook 推送

</details>

<details>
<summary><b>⏰ 定时任务（Scheduler）</b></summary>

- 5 种动作：打开软件 / 目录 / 网页 / 命令 / HTTP 请求
- 4 种调度：一次性 / 按间隔 / 每天 / 每周
- 执行后系统通知，支持手动立即执行
- **删错了能找回**：删除先进回收站（回收站里单独一类「定时任务」），恢复后按**当前时间**重算下次运行——不会因为快照里的运行时间早就过去了而立刻补跑一次

</details>

<details>
<summary><b>📡 网站监控（Monitor）</b></summary>

- 定时探测 HTTP 状态码与响应时间（GET / HEAD / POST）
- SSL 证书到期预警（可自定义提前天数）
- 关键字 / 正则内容匹配
- 在线率统计、检测日志、状态翻转通知（桌面 + Webhook：钉钉 / 企微 / 飞书 / Server酱 / PushPlus / Telegram）
- 响应时间趋势图（24h / 7d / 全部）

</details>

<details>
<summary><b>🗒️ 快捷笔记</b></summary>

浮动笔记窗口 `Ctrl+Alt+N` 即时唤出；**树形笔记库**（文件夹 + Markdown 多层级），搜索 / 重命名 / 删除；自动保存（500ms 防抖）；失焦自动隐藏。

浮窗里的 **「问 AI」** 直接开面板（划选则只送选中的那段，否则送整篇），预置总结 / 润色 / 续写 / 翻译，也能自由追问。整个过程不离开浮窗，也**不会把整篇笔记写进 AI 对话历史**。

</details>

<details>
<summary><b>🧠 DeepSeek Harness 集成</b></summary>

内嵌 [DeepSeek Harness](https://github.com/deepseek-ai/dsh) —— 完整 Agent 编程入口（工具 / 文件 / 终端 / 会话）：

- **一键环境装填** — 自动检测 node / npx / dsh；缺失时下载便携 Node v22（npmmirror → nodejs.org 兜底）并安装 `@deepseek-ai/dsh`
- **原生窗口承载** — `node` 拉起 `dsh web --no-open`（CREATE_NO_WINDOW 隐藏控制台），固定官方端口 **3080**；被健康 dsh 占用则复用、残留 node 则清理重绑，仅被无关进程占用才退随机端口
- **后台常驻** — 关窗不停服务，下次点击秒开；真正停止由设置手动或退出触发
- **实时安装日志** — 下载 / 解压 / npm 安装全程逐行推送 `quickdock:dsh:log`
- **环境净化** — 过滤第三方注入的 `NODE_OPTIONS`，避免污染 dsh 进程

</details>

<details>
<summary><b>💬 AI 助手</b></summary>

- **多配置档案** — OpenAI / DeepSeek / Kimi / 通义千问 / Ollama / Azure OpenAI / 自定义兼容接口
- **模型不用手记** — 填好 BaseURL 与 API Key 后点「拉取模型列表」，直接从这个接口取回可用模型供你选（OpenAI / DeepSeek / Kimi / 通义 / Ollama / Azure 通用）；没保存的草稿也能拉
- **5 种对话模式** — 聊天 / 解释代码 / 翻译 / 总结 / 使用教程，可叠加自定义 System Prompt
- **SSE 流式输出** — 本地 HTTP 流式服务（127.0.0.1，随机端口 + 随机 token），逐 token 即时显示
- **思考过程折叠** — `reasoning_content` 以 `<details>` 折叠，默认收起
- **Markdown 渲染** — `marked` + `DOMPurify` 安全渲染，代码块走 highlight.js（明暗各一套配色，跟主题走）；**LaTeX 公式**（KaTeX，支持 `\(..\)` / `\[..\]` / `$..$` / `$$..$$` / 裸 `\begin{}`，流式生成中公式还没写完时先显示原文，写完即刻）与 **Mermaid 图**（` ```mermaid ` 围栏，流式结束后自动画成 SVG，配色跟随主题；图上带工具栏，可**折叠**收起长流程图、可**一键切换源码**，画不出来就留源码，不给你一个报错红框）；表格太宽可横向滚，点表头能排序，也能导出 CSV
- **回答里的东西能直接用** — ` ```html ` 代码块工具栏有「预览」，点开在全屏沙箱 iframe 里跑（`sandbox` 不带 `allow-same-origin`，拿不到宿主）；流程图、公式、代码块**右键可「复制为图片」**（PNG 直进剪贴板，矢量 SVG 走序列化、公式走 foreignObject，2 倍图），流程图另外能给 PNG / SVG 文件
- **切走也不断** — AI 回答搬到后台继续跑，切到监控、SSH、笔记再回来，流还在，历史也还在（不会因为换过页面就重新加载一次会话）；侧栏 AI 那条会有个小状态点：生成中呼吸、回答完了没看是实心点，侧栏收起时也看得见
- **参数可配** — Temperature / MaxTokens / TopP / FrequencyPenalty / PresencePenalty
- **花了多少，自己看得见** — 「设置 → AI」的用量统计：近 7 天 / 30 天 / 全部的花费估算、问答次数、Token、对话数，每日柱状图看趋势，按模型和按入口（对话 / 面板与插件 / 标题生成 / 摘要压缩）拆开看谁在花钱；输入缓存命中省下的那部分单独标出来——命中率高的时候，同一轮重复的上下文几乎是"白送"的。记账覆盖所有入口：对话页、SSH 侧栏、剪贴板与笔记的「问 AI」、插件里的 `host.ai.ask`、自动起标题、长对话压缩。口径写在卡片上：**这是按内置单价线性估算的，不是账单**，缓存折扣、批量优惠、阶梯计价都没算，以服务商结算为准
- **会话管理** — 多会话 / 标题自动生成 / 清空 / 删除；**Token 用量统计**（旧版本只记了会话累计值，升级后会折算成一条"历史折算"记录，总额不丢，但还原不出当时的分布）
- **API Key 安全存储** — Windows DPAPI 加密，前端不接触密文；支持测试连接
- **一处配置，多处复用** — 同一份档案同时服务对话页、SSH 终端侧栏、剪贴板与笔记的「问 AI」，以及插件调用的 `host.ai.ask`；各入口互不干扰

</details>

<details>
<summary><b>🔌 插件系统</b></summary>

- 三种运行时（none / goja / native）+ JSON-RPC 2.0；统一 Host API（**34 方法 / 14 组**）：日志、存活探测、通知、剪贴板、对话框、按插件隔离 KV（`db.*`）、文件读写（`host.fs.*`，按 `permissions.filesystem` 目录 scope 校验）、网络代发（`http.get/post`，域名白名单）、Shell 打开、进程 list/exec/kill（需 `process` / `processKill` 权限）、窗口显隐、加解密（`host.crypto.*`）、复用宿主 MCP 的 `host.mcp.call`、复用宿主 AI 档案的 `host.ai.ask`（带 `streamId` 即流式）
- 前端桥接：`qdConfirm` / `qdAlert` / `qdPickFile` / `qdPickFolder` / `qdReadFile` / `qdHostCall` / `qdHttp` / `qdAiAsk`（流式问答）/ `qdPrint` / `qdCrypto`
- **权限模型**：manifest `permissions` 声明所需能力，未声明一律拒绝；native 运行时安装期 `go/ast` 静态审计高危能力（如 `os.Remove` / `exec.Command` / `net.Dial`）与声明一致性，不符则告警
- 运行时安装 / 卸载 / 启用 / 禁用 / 热键绑定；官方插件经「在线市场」一键安装升级

</details>

<details>
<summary><b>🛠️ MCP 服务</b></summary>

自暴露 **Model Context Protocol** 服务，供 AI 客户端调用本地能力：

- **地址**：`http://127.0.0.1:9230/mcp`（Streamable HTTP，仅 POST；协议 2025-06-18）
- **工具集三级权限门**（默认只开放只读 + 低危写）：
  - 只读：`app_info` / `env_list` / `env_status` / `env_log` / `env_versions` / `workspace_list` / `item_search` / `recent_items` / `todo_list` / `note_search` / `clipboard_recent` / `port_list` / `log_list` / `log_read` / `crash_list` / `crash_read` / `plugin_list`
  - 低危写：`env_start` / `env_stop` / `env_restart` / `item_open` / `todo_create` / `todo_done` / `clipboard_copy` / `note_create` / `note_update` / `note_quick` / `plugin_execute`
  - 高危（环境管理页开启）：`process_kill` / `system_command`
  - 插件经 `host.mcp.call` 复用全部工具（沿用 MCP 等级门）
- **生命周期**：随主进程，退出即失效；门面装配在 `services/mcp`，工具经各领域门面转发到宿主

</details>

<details>
<summary><b>🔧 环境管理（28 种运行时）</b></summary>

多运行时版本管理，一键安装 / 切换 / 启停，多版本共存于用户目录，不污染系统 PATH、不写注册表、不申请管理员权限。

| 运行时 | 分组 | 类型 | 平台 |
|--------|------|------|------|
| Node.js / Go / PHP / Python / Bun / Erlang / JDK | 语言 | 命令行 / 服务型 | Windows |
| Nginx / Caddy / Apache / Traefik / FTP / WebDAV(内置 9080) | 网络服务 | 服务型 | Windows |
| Redis / Memcached / MinIO / RabbitMQ | 中间件 | 服务型 | Windows |
| Git / Composer / FFmpeg / Mailpit / frpc / GitHub CLI / mkcert | 开发工具 | 命令行 / 服务型 | Windows |
| Ollama / MCP 服务(内置 9230) | AI 服务 | 服务型 | Windows |
| MariaDB / MySQL / PostgreSQL / MongoDB | 数据库 | 服务型 | Windows |

- **核心设计**：便携优先（`%APPDATA%\QuickDock\runtime\<name>\<version>`）、多版本共存、下载源可切换（`{version}` `{v}` `{os}` `{arch}` `{ext}` 占位符）、版本列表智能筛选 + 1h 内存缓存、代理容错与多源回退、后台异步进度推送
- **服务管理能力**：一键启停 / 重启、配置编辑（Nginx / Apache / Caddy 启动前 `*-t` 校验）、实时日志（尾部 8KB）、Web 控制台一键打开、端口全景、本地开发站点（自定义域名 + 自动 HTTPS，自身不监听端口）
- **Ollama 本地大模型**：单版本语义（程序目录 `runtime/ollama/`）、原地更新（只提示不自动）、模型库共享 `~/.ollama/models`、卸载只删程序不碰模型
- **文件结构**：门面层 `services/env/`（31 方法） + 引擎层 `internal/env/`（注册表 / 下载 / 解压 / 服务生命周期 / 项目级版本探测，跨平台 `_windows` `_darwin` `_unix` 分文件）

</details>

<details>
<summary><b>📷 截图工具</b></summary>

Snipaste 形态，`F1` 唤起全屏覆盖层，**框选 → 标注 → 复制 / 保存 / 贴图**一条链路，全程不弹第二窗口。

- **原生覆盖层零闪** — 全屏遮罩是自建 Win32 分层窗口，`UpdateLayeredWindow(ULW_ALPHA)` 整帧提交、不走 `WM_PAINT`；显示顺序 `SW_HIDE → SetWindowPos → 画完 → SW_SHOW` 无空窗期
- **框选后原地浮出工具条** — 矩形 / 椭圆 / 箭头 / 直线 / 马赛克 / 文字 + 颜色(9) / 线宽(4) / 字号(6) + 撤销重做 + 复制 / 保存 / 贴图 / 关闭
- **窗口 / 元素吸附** — 实时高亮光标下窗口，单击选中；`Ctrl` 下沉到子控件；已排除自身窗、`WS_EX_TRANSPARENT`、`DWM cloaked`
- **选框可调、标注可再编辑** — 8 控制点 + 平移；图形点选 / 拖动 / `Delete`；文字双击重编辑（另开 owned 顶层窗承载 IME）
- **贴图钉屏** — 原生 GDI 窗口钉桌面最上层，可拖动 / 滚轮缩放（光标锚点）/ 双击关闭，单张内存即位图本身（非 WebView2 进程）
- **快捷键** — `Enter`/`Ctrl+C` 复制，`Ctrl+S` 保存，`Ctrl+Z`/`Ctrl+Shift+Z` 撤销重做，`Ctrl+A` 全选，`R/E/A/L/M/T` 切工具，方向键微调（`Shift`×10），`Delete` 删除，`Esc` 逐层退出

</details>

<details>
<summary><b>🪟 窗口管理</b></summary>

纯热键驱动「单窗口贴屏 + 批量排版浮层」，不占 `Win+方向`：

- **15 个贴屏热键** — 左 / 右 / 上 / 下半屏、四四分之一、居中、最大化、还原、最小化、置顶切换、移上一屏 / 下一屏（`Ctrl+Alt` 前缀，默认全部可改）
- **排版浮层 `Ctrl+Alt+W`** — 点九宫格任意一块即排版：当前窗口进点击格，其余用同屏其他可见窗口按 Z 序填充；9 种模板（左右对半 / 左主右二 / 右主左二 / 上主下二 / 下主上二 / 四象限 / 三列 / 三行 / 九宫格）
- **分屏比例可调** — 左右半屏宽度占比 10%–90%（默认 50%）；多显示器按光标所在屏换算几何

</details>

<details>
<summary><b>🔐 SSH 远程终端</b></summary>

- **默认热键** — `Ctrl+Alt+S` 唤出独立可缩放窗口，切换窗口不中断会话
- **主机管理** — 主机 + 分组（文件夹）树形组织、拖拽排序、环境标签（生产 / 预发 / 测试）、备注；凭据与配置分离存储
- **认证方式** — 密码 / 私钥（ed25519 等）+ 私钥口令；兼容旧设备 `ssh-rsa` / SHA-1（legacy 开关）
- **服务器指纹信任** — 首次连接弹出指纹确认对话框，可对照服务器命令核对；known_hosts 统一管理（查看 / 清除已信任指纹），指纹变更主动告警，防中间人
- **多标签会话** — 同主机连接池复用（多标签只握手一次）；PTY 终端模拟，支持复制 / 粘贴 / 全选 / 清屏；滚动回放与断线重连
- **SFTP 文件管理** — 远程目录浏览、上传 / 下载
- **命令执行** — 非交互一次性 `Exec`（带超时、退出码、输出截断），用于脚本化运维
- **多主机批量执行** — 在分组上点「批量执行…」勾选多台主机：先**预演**（只读，列出每台将要执行的命令与风险等级）再运行，逐台回传输出与退出码，可随时取消；生产环境的一键执行同样要求二次确认，高危命令仍只给「填入」

</details>

<details>
<summary><b>✨ 终端内 AI 助手（SSH × AI）</b></summary>

终端右侧的 AI 侧栏，把「看到的现象」直接变成「可执行的命令」。

- **看得见现场** — 问题里提到**「屏显」**，这一轮就带上终端当前画面（提到就够了，后面不用再补别的内容；不提就不发，省 token 也不把模型带偏）。屏显不一定报错：正常输出、命令结果、正在运行的程序界面都会被如实说明，不会硬当成故障来分析
- **环境感知** — 侧栏头部常驻环境徽章。生产环境除了告知模型，还会额外约束它的输出口径：**默认先给只读探查命令**，任何写入 / 删除 / 重启 / 改配置的命令都必须写明影响范围与回滚方式
- **选中即问** — 在终端里选中一段输出 → 右键「问 AI 这段输出」，选区单独作为上下文送进模型（比整屏屏显权重更高），侧栏自动打开并预填问题，一个回车就能问
- **命令卡片** — 回答里的命令单独成卡，支持**一键填入终端**或**一键执行**，不用再复制粘贴；空状态里的示例问题点一下即填入输入框
- **安全边界** — 只给命令，绝不自动执行，共三层且互为独立：
  - **提示词层**：生产环境要求模型先探查后变更、变更带回滚（约束默认行为，而不是事后匹配词表）
  - **标注层**：`rm -rf` / `dd` / `mkfs` / `iptables -F` / `reboot` / `kubectl delete` / `docker rm` / `DROP TABLE` / `git reset --hard` / `git push -f` 等高危命令**只有「填入」，没有「执行」**，必须由你亲自按回车；中风险命令（`systemctl restart`、`sed -i`、卸载软件包、`crontab -r` 等）保留一键执行，但卡片上先给出风险说明再给按钮。多个规则同时命中时取**最高等级**，不会出现"命中了高危却被判成中等"
  - **确认层**：生产环境上「执行」需点两次（第一次只把按钮切成「再点一次确认」）。规则表是有限收录的——`sed -i` 改错配置、一条没写完的 `DELETE` 都匹配不到任何规则，界面上看起来"很安全"，这一层就是给这类漏网命令兜底的；测试 / 预发保持一步到位
  - 「填入」只写进远端输入缓冲不发回车，你仍能在屏幕上审视这条命令
- **多轮追问** — 侧栏内保留上下文连续追问；提示词按优先级控预算（本次问题 > 屏显 > 历史），历史过长时丢最旧的；更早的轮次会被**自动压成一段摘要**继续带着走，所以问得再多也不会越问越卡
- **问不下去就停** — 「思考中」时发送键变成停止键，模型迟迟不来或你已看清一半，点一下立刻退出等待，不用干等转圈
- **多主机隔离** — 切换会话自动清空上下文，不会把 A 机器的屏显带给 B 机器
- **回答方式可选** — 面板头部一个开关：**流式**（默认，边生成边显示）或**一次性**返回整段。偏好存在本地，SSH / 剪贴板 / 笔记三处共用同一份，不会这个面板关了那个还开着
- **用的是哪个档案** — 标题旁显示当前激活的 AI 档案名（hover 看模型），配了多个 Key 时一眼能看出这一问走的是哪个
- **未配置就带走** — 没配 API Key 时点「问 AI」不会打开一个注定失败的面板，而是直接带你去「设置 → AI」填 Key
- **配置独立** — 复用 AI 档案，但与对话页互不干扰：不落库、不占对话上下文

</details>

<details>
<summary><b>💾 数据目录搬家（换盘 / 安装时选目录）</b></summary>

C 盘塞不下、想把整个家挪到另一块盘？「设置 → 存储与同步」里改**存储位置**，点「应用」它自己搬：

- **搬的是整个目录** — 数据库（工作空间 / 待办 / 笔记 / 剪贴板索引都在里面）、剪贴板图片、插件、日志，以及便携运行时、模块缓存、依赖树，一件不落；搬完**不需要重新下载任何运行时**
- **选盘符就行** — 选 `D:\` 会自动落到 `D:\.quickdock`，选 `D:\data` 则落到 `D:\data\.quickdock`；界面会回显「实际将迁移到」，看清楚了再点
- **进度看得见** — 打包 → 复制 → 解压 → 改环境变量 → 清旧目录，每一段都有文件数与体积，还显示"当前在处理哪个文件"
- **环境变量一起改** — 指向旧目录的 `PATH` / `GOROOT` / `GOPATH` 这些会被改写成新目录，所以运行环境搬过去照样能用；**已经开着的终端要重开**才认新变量
- **要重启才算搬完** — 数据库文件正被本进程独占、搬不动，先导出快照，重启时再落到新位置；旧目录也是重启后才删。也就是说**从迁完到重启这段时间，两个目录一起占空间**，目标盘得留出「压缩包 + 解压后」的峰值余量
- **半路失败不慌** — 最后一步之前旧目录始终完好，报错重试即可；打包中断留下的半成品会被清掉，不会白占地方

安装时也能选：安装器多一页「选择数据目录」，**只在新装时出现**（覆盖升级不会重置你选过的位置）；卸载只删程序，不碰数据。

</details>

<details>
<summary><b>☁️ WebDAV 云同步 / 📸 快照备份 / 🖥️ 系统命令</b></summary>

- **WebDAV 云同步** — 全量 JSON 备份 / 恢复、多版本、任意 WebDAV 服务器；统一「同步后端」分层（`services/sync.go` → `internal/sync` → `internal/webdav`），未来可接入 Git / 对象存储
- **快照备份** — 一键导出全部数据为 JSON，导入恢复，迁移无忧
- **系统命令** — 锁屏 / 关机 / 重启 / 睡眠 / 清空回收站

</details>

<details>
<summary><b>🔄 自动更新（Windows 免安装器就地替换）</b></summary>

启动后拉取**签名更新清单**并验签：

```
https://github.com/parieses/quickdock/releases/latest/download/manifest.json
```

| 阶段 | 行为 |
|------|------|
| 检查 | 拉取 manifest.json，ed25519ph 验签 + sha512 校验；版本更高才提示 |
| 下载 | 下载裸 `quickdock-amd64.exe` 并校验摘要（多源镜像回退） |
| 重启 | 点「重启更新」→ 派生 helper → 主进程退出 |
| 替换 | helper 备份旧 exe → 覆盖新 exe（失败回滚）→ 重新拉起 |

整个过程**不弹 UAC、不跑安装器**，用户数据 / 数据库 / 插件 / 运行时目录全部保留（单文件应用，前端 `go:embed` 进 exe）。

- **macOS**：每 release 产出 `quickdock-darwin-arm64` / `quickdock-darwin-amd64` 两个包；`.app` 须装到 `~/Applications`（普通权限可写）方可就地更新；无 Developer ID 时为 ad-hoc 签名，正式分发需补签名 + 公证
- **发版纪律**：版本号唯一来源是 **git tag**，`main.appVersion` 恒为 `"0.0.0"`，打包时由 Taskfile 的 `APP_VERSION`（`git describe --tags --abbrev=0`）经 ldflags 注入 —— **发版只打 tag，不改任何版本文件**

</details>

---

## 🏗️ 架构

服务门面分层 + 引擎下沉，依赖方向单向无环：

```
┌──────────────────────────────────────────────────────────────────┐
│                         QuickDock (Wails v3)                       │
│                                                                    │
│   ┌────────────────────┐      ┌────────────────────────────────┐  │
│   │  Vue 3 Frontend     │◄────►│  services/  Wails 绑定门面        │  │
│   │  (frontend/dist     │ IPC  │  AppService + 23 领域门面子包    │  │
│   │   go:embed)         │      │  约 430 绑定方法                 │  │
│   └────────────────────┘      └───────────────┬────────────────┘  │
│                                                │ 单向依赖          │
│                                 ┌──────────────▼────────────────┐  │
│                                 │  internal/  引擎层（无绑定）    │  │
│                                 │  db / env / sites / dsh /      │  │
│                                 │  plugin / platform / sync /    │  │
│                                 │  webdav / webdavsrv / mcp /   │  │
│                                 │  screenshot / winmgr / dl /   │  │
│                                 │  logger / sysutil / validators │  │
│                                 └───────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────┘
```

**架构亮点**

- **门面分层**：领域能力下沉为 23 个门面子包（311 个导出方法），宿主核心留在 AppService（116 个导出方法），依赖单向无环
- **截图覆盖层走原生窗口而非 WebView**：自建 Win32 分层窗口 `UpdateLayeredWindow` 整帧提交，工具条 / 标注 / 图形全手绘进同一张 DIB —— 这是「截图不闪」的唯一解
- **次级窗口延迟创建**：首次热键触发才创建 WebView2，避免白屏；浮窗位置经 kvstore 持久化，重启原位恢复
- **后台服务**：SQLite WAL + 待办(10s) / 定时 / 监控调度器 + 流式 HTTP（AI）+ 插件健康检查 + DSH 隐藏进程 + 内置 MCP(9230)
- **安全**：纯 Go SQLite（零 CGO）、SQL 白名单防注入、API Key DPAPI 加密、单实例锁防并发写库、本地 SSE 流式用随机端口 + 随机 token
- **规模（实测）**：Go 源码约 5.1 万行（`services` 2.5w + `internal` 2.0w + 入口/托盘/浮窗 0.3w，含 104 个测试文件），前端约 5.7 万行（70 个 Vue 组件 + 79 个 TS 模块，其中 23 个单测文件）

---

## 💻 技术栈

**后端**

| 技术 | 说明 |
|------|------|
| Go 1.25 | 主语言 |
| Wails3 v3.0.0-alpha2.122 | 桌面应用框架 |
| modernc.org/sqlite | 纯 Go SQLite（无 CGO） |
| go-sql-driver/mysql + go-redis/v9 | MySQL / Redis 客户端 |
| golang.org/x/sys | Windows 系统 API |
| dop251/goja | JavaScript 沙箱（插件执行） |
| DPAPI | Windows 数据保护 API（Key 加密） |

**前端**

| 技术 | 说明 |
|------|------|
| Vue 3 + TypeScript | UI 框架 |
| Vite 8（Rolldown） | 构建工具 |
| Pinia 3 | 状态管理 |
| vue-i18n 11 | 国际化（简中 / English） |
| @lucide/vue | 图标库 |
| pinyin-pro | 拼音搜索 |
| marked 18 + DOMPurify | Markdown 安全渲染 |
| KaTeX · Mermaid · highlight.js | 公式 / 流程图 / 代码高亮（按需分包） |
| @floating-ui/vue 2 | 浮层定位 |
| @wailsio/runtime 3.0.0-alpha.96 | Wails 前端运行时 |

---

## 🔧 开发指南

### 前置条件

- Go 1.25+
- Node.js 22+
- Wails3 CLI（**版本须与 `go.mod` 一致**，当前 `alpha2.122`）

```bash
# 安装 Wails3 CLI（版本必须与 go.mod 的 wails 模块完全一致）
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.122

# 前端依赖
cd frontend && npm install

# 重新生成 bindings（新增/修改 Go 绑定后必须执行）
wails3 generate bindings -ts -i -clean   # 等价于 cd frontend && npm run bindings
```

### 常用命令

| 命令 | 说明 |
|------|------|
| `wails3 dev` | 开发模式（前后端热重载） |
| `wails3 build` | 生产构建（前端构建 + 绑定生成） |
| `task build` / `task run` | 经 Taskfile 构建 / 运行 |
| `task windows:version` | 输出最终注入 `main.appVersion` 的版本号（发版自检） |

> 开发版与正式版共用同一 SQLite 库与单实例锁，同一时刻只能运行一个实例。

### 数据存储

默认全在 `~/.quickdock`，可在「设置 → 存储与同步」整目录搬到别的盘（见上文「数据目录搬家」）。配置文件刻意放在数据目录**之外**——否则改了数据目录就找不到"数据目录在哪"这条配置了。

| 路径 | 说明 |
|------|------|
| `~/.quickdock/quickdock.db` | SQLite（WAL + 外键 + 5s busy timeout） |
| `~/.quickdock/images/` | 剪贴板图片 |
| `~/.quickdock/plugins/` | 已安装插件 |
| `%APPDATA%/QuickDock/` | 应用配置 |
| `~/.quickdock/runtime/node` · `~/.quickdock/dsh` | 便携 Node / DSH |
| `~/.quickdock/logs/quickdock-YYYY-MM-DD.log` | 主日志 |
| `~/.quickdock/logs/crash/` | 崩溃现场（`js-*` / `panic-*`，可在「设置 → 日志」查看） |

### 项目结构

```
quickdock/
├── main.go / tray.go / windows.go        # 入口 / 托盘 / 浮动窗口 + 单实例锁
├── services/                             # Wails 门面层（宿主 + 23 领域子包，~430 方法）
│   ├── service.go  api_result.go  types.go lifecycle.go
│   ├── hotkey.go  theme.go  autostart.go  winmgr_service.go
│   ├── app_launcher.go  focus.go  tool.go  clipboard_sys.go
│   ├── screenshot.go  sync.go  palette.go  plugin_hotkey.go
│   ├── schedule.go  schedule_runner.go  reminder.go  webhook_notify.go
│   ├── monitor_checker.go  notify.go  visibility.go  navbadge.go
│   ├── storage.go  storage_service.go  clipboard_image.go   # 数据目录搬家 / 图片写剪贴板
│   └── ai/ clipboard/ collection/ diag/ dsh/ env/ frecency/ item/
│       logtail/ mcp/ monitor/ note/ notify/ plugin/ port/ recycle/
│       scene/ snapshot/ sshx/ system/ todo/ update/ workspace/  # 领域门面子包
├── internal/                             # 引擎层（被门面调用，无 Wails 绑定）
│   ├── db/  env/  sites/  sshx/  dsh/  plugin/  platform/  sync/  webdav/  webdavsrv/
│   ├── mcp/  screenshot/  winmgr/  dl/  logger/  sysutil/  validators/
│   ├── aiutil/  cliprule/  logtail/     # 提示词预算 / 剪贴板规则 / 文件尾随
├── frontend/src/                         # Vue 3 + TS（组件 + TS 模块）
│   ├── App.vue  main.ts  style.css  pages/  i18n/  stores/  composables/  utils/
│   └── components/  # 三栏主界面 / 面板 / 设置 / 插件 / 环境 / 监控 …
├── plugins/                              # 宿主骨架 + 开发模板（外部插件见独立仓库）
├── build/  docs/  website/  image/  Taskfile.yml
├── DESIGN.md  AGENTS.md  LICENSE
└── main_unix.go  main_windows.go  windows_unix.go  windows_windows.go
```

> 更完整的工程约束（提交纪律、样式 token、目录职责、自检清单）见 [AGENTS.md](./AGENTS.md)。

---

## 🎨 设计哲学

**精准暗色极简主义（Precision Dark Minimalism）**

- 暗色为主（层次化灰阶非纯黑），支持深 / 浅 / 跟随系统；强调色 `#4a9eff` 仅用于功能交互
- 三面板布局：侧边栏(210px) | 集合列表(300px) | 项目列表(flex-1)
- 8px 基准间距、系统字体栈（基字 13px）、150ms 过渡、键盘优先、`box-shadow` 替代 `border` 消除布局偏移
- 完整规范见 [DESIGN.md](./DESIGN.md)

---

## 🔌 插件生态

官方插件统一在独立仓库维护与分发 👉 **[quickdock-plugins](https://github.com/parieses/quickdock-plugins)**（插件列表、功能说明与完整开发文档：目录结构、`plugin.json` 字段、三种运行时快速开始、通信协议、调试与发布）。

主仓库仅保留**宿主实现**（`internal/plugin/` 引擎 + `services/plugin/` 门面）；用户安装的插件落在 `~/.quickdock/plugins/<plugin-id>/`。随仓库挂载的部分外部插件涵盖：HTTP 客户端、数据库连接、OCR、端口检查、JSON / 正则 / 哈希 / 颜色 / JWT / 二维码工具、PDF / 图片工作室、代码对比、Hosts 管理、网络诊断、磁盘分析、重复文件查找、批量重命名、本地文件搜索、图床上传 等。

---

## 📄 许可协议

[MIT License](./LICENSE) © 2025–2026 王亮亮

## 🙏 致谢

- [Wails](https://wails.io/) · [Vue.js](https://vuejs.org/) · [Floating UI](https://floating-ui.com/) · [Lucide](https://lucide.dev/)
- [modernc.org/sqlite](https://modernc.org/sqlite) · [marked](https://marked.js.org/) · [DOMPurify](https://github.com/cure53/DOMPurify)
- [KaTeX](https://katex.org/) · [Mermaid](https://mermaid.js.org/) · [highlight.js](https://highlightjs.org/)
