# 快启坞 QuickDock

> 面向 Windows 开发者的效率工具 —— 资源集合、快速启动与工作空间管理

快启坞（QuickDock）是一款专为 Windows 开发者打造的桌面效率工具，融合了 **Raycast 的快速启动** 与 **VS Code 的开发者体验**。它帮助你统一管理工作空间、项目、目录、网页链接、常用命令与应用，并内置剪贴板历史、文本片段（树形笔记）、命令面板、待办（含番茄专注）、定时任务、网站监控、Webhook 通知，并随附 40+ 个开箱即用插件（含 HTTP 客户端、数据库连接等，经插件市场一键安装，详见 [quickdock-plugins](https://github.com/parieses/quickdock-plugins)），以及多运行时环境管理（26 个运行时：Node.js / PHP / Python / Go / Bun 等语言，Nginx / Caddy / Apache / Traefik 等 Web 服务器，Redis / Memcached / MinIO / RabbitMQ 等缓存存储，MySQL / MariaDB / PostgreSQL / MongoDB 等数据库，一键安装、版本切换、启停、配置编辑、日志查看与 Web 控制台）（含可选的 AI 助手），并原生集成 DeepSeek Harness，让开发工作流更高效。

![主界面截图](image/主界面截图.png)

---

## 目录

- [快启坞 QuickDock](#快启坞-quickdock)
  - [目录](#目录)
  - [功能特性](#功能特性)
    - [📦 工作空间与资源管理](#-工作空间与资源管理)
    - [📋 剪贴板历史](#-剪贴板历史)
    - [🔍 命令面板](#-命令面板)
    - [📝 文本片段（Snippets）](#-文本片段snippets)
    - [✅ 待办任务（Todos）](#-待办任务todos)
    - [⏰ 定时任务（Scheduler）](#-定时任务scheduler)
    - [📡 网站监控（Monitor）](#-网站监控monitor)
    - [🗒️ 快捷笔记](#️-快捷笔记)
    - [🧠 DeepSeek Harness 集成](#deepseek-harness-集成)
    - [💬 AI 助手](#-ai-助手)
    - [🔌 插件系统](#-插件系统)
    - [☁️ WebDAV 云同步](#️-webdav-云同步)
    - [📸 快照备份](#-快照备份)
    - [🔧 全局热键（可自定义）](#-全局热键可自定义)
    - [🖥️ 系统命令](#️-系统命令)
  - [🔧 环境管理](#-环境管理)
  - [快速开始](#快速开始)
    - [系统要求](#系统要求)
    - [下载安装](#下载安装)
    - [首次使用](#首次使用)
  - [开发指南](#开发指南)
    - [前置条件](#前置条件)
    - [常用命令](#常用命令)
    - [数据库](#数据库)
  - [技术栈](#技术栈)
    - [后端](#后端)
    - [前端](#前端)
  - [项目结构](#项目结构)
  - [数据模型](#数据模型)
  - [全局热键](#全局热键)
  - [设计哲学](#设计哲学)
  - [插件生态](#插件生态)
  - [构建与打包](#构建与打包)
    - [本地构建](#本地构建)
    - [平台支持](#平台支持)
  - [架构亮点](#架构亮点)
  - [许可协议](#许可协议)
  - [致谢](#致谢)

---

## 功能特性

### 📦 工作空间与资源管理

- **工作空间（Workspace）** — 顶级容器，隔离不同项目上下文
- **场景（Scene）** — 工作空间下的视图分组，快速切换关注点，支持标签/图标/颜色
- **集合（Collection）** — 资源的逻辑分组，可按项目或类型归类，支持四种打开策略
- **项目（Item）** — 支持 6 种类型：
  - `directory` — 目录，用系统/终端打开
  - `file` — 文件，用系统默认程序打开
  - `url` — 网页链接，用浏览器打开
  - `command` — 终端命令，在终端中执行
  - `app` — 应用程序路径
  - `quicklink` — 快速链接，带参数的快捷方式

全层级支持拖拽排序、FTS5 全文搜索。

### 📋 剪贴板历史

- 自动监听并记录文本、图片、文件剪贴板内容
- 支持固定、搜索、复制粘贴、批量删除
- 过期自动清理（可配保留天数）
- 浮动窗口，失焦自动隐藏，`` Ctrl+` `` 一键唤出

### 🔍 命令面板

- 全局搜索：工作空间 / 场景 / 集合 / 项目统一搜索
- 快速执行：选中即操作，支持键盘导航、多选批量
- FTS5 全文搜索，URL 智能识别存为项目
- 最近使用 / 最常使用列表
- 浮动窗口，`Ctrl+K` 即时唤出

### 📝 文本片段（Snippets）

- 预定义的常用文本模板，关键词 + 内容 + 分类
- 一键复制或粘贴到当前活动窗口
- 支持 `{date}` / `{time}` / `{username}` / `{clipboard}` 变量替换
- 搜索、快捷笔记自动保存
- 已升级为**树形笔记**：文件夹 + Markdown 文档任意层级组织（见「🗒️ 快捷笔记」）

### ✅ 待办任务（Todos）

- 待办列表 + 看板视图（待办 / 进行中 / 已完成三列）
- 子任务（单层级 checklist）、标签分类
- 重复待办（每日 / 每周 / 每月）、到期定时提醒（系统通知）
- 🍅 番茄专注计时 — 待办页启动专注倒计时，结束时发系统通知 + 可选 Webhook 推送

### ⏰ 定时任务（Scheduler）

- 五种动作类型：打开软件 / 目录 / 网页 / 命令 / HTTP 请求
- 五种调度方式：一次性 / 间隔 / 每天 / 每周 / 每月
- 执行后系统通知，支持手动立即执行

### 📡 网站监控（Monitor）

- 定时探测 HTTP 状态码与响应时间（GET / HEAD / POST）
- SSL 证书到期预警（可自定义提前天数）
- 关键字 / 正则内容匹配检测
- 在线率统计、检测日志、状态翻转通知（桌面 + Webhook：钉钉 / 企微 / 飞书 / Server酱 / PushPlus / Telegram）
- 响应时间趋势图（24h / 7d / 全部）

### 🗒️ 快捷笔记

- 浮动笔记窗口，`Ctrl+Shift+N` 即时唤出
- **树形笔记库** — 文件夹 + Markdown 文档多层级组织，支持搜索 / 重命名 / 删除
- 自动保存（500ms 防抖）；旧快捷笔记自动兼容为根级文档
- 失焦自动隐藏

### 🧠 DeepSeek Harness 集成

内嵌 [DeepSeek Harness](https://github.com/deepseek-ai/dsh) —— 完整的 Agent 编程入口（工具 / 文件 / 终端 / 会话），与轻量 AI 助手互补：

- **一键环境装填** — 自动检测 node / npx / dsh；缺失时下载便携 Node v22（npmmirror 镜像 → nodejs.org 兜底）并安装 `@deepseek-ai/dsh`
- **原生窗口承载** — `node` 直接拉起 `dsh web --no-open`（CREATE_NO_WINDOW 隐藏控制台），固定官方端口 **3080** 与浏览器访问同址：被健康 dsh 占用则直接复用、残留 node 则清理后重绑，仅被无关进程占用才退回随机端口；QuickDock 原生 WebviewWindow 承载
- **后台常驻** — 关窗不停服务：dsh 独立于窗口后台运行（下次点击秒开），真正停止由设置手动或 QuickDock 退出触发
- **实时安装日志** — 下载 / 解压 / npm 安装全程逐行推送 `quickdock:dsh:log`，进度可见
- **插件安装入口** — 设置页可直接安装 DSH 插件（默认 `dshmarket`，支持粘贴完整命令）；复用官方 `~/.dsh` 数据目录（皮肤 / 插件 / 会话）
- **环境净化** — 过滤第三方注入的 `NODE_OPTIONS`（如 WorkBuddy shim），避免污染 dsh 进程

### 💬 AI 助手

- **多配置档案** — 支持 OpenAI / DeepSeek / Kimi / 通义千问 / Ollama / Azure OpenAI / 自定义兼容接口
- **四种对话模式** — 聊天 / 解释代码 / 翻译 / 总结，模式 prompt 可叠加自定义 System Prompt
- **SSE 流式输出** — 本地 HTTP 流式服务（127.0.0.1:随机端口），token 到达即显示，非传统轮询
- **思考过程折叠** — 模型思考内容（reasoning_content）以 `<details>` 折叠展示，默认收起
- **思考模式开关** — 可在设置页开启/关闭思考过程显示
- **Markdown 渲染** — 使用 `marked` + `DOMPurify` 安全渲染对话内容
- **参数可配** — Temperature / MaxTokens / TopP / FrequencyPenalty / PresencePenalty
- **自定义 System Prompt** — 设置页 textarea，非空时覆盖默认模式提示
- **会话管理** — 多会话 / 标题自动生成 / 重新生成标题 / 清空上下文 / 删除会话
- **Token 用量统计** — 每次对话自动记录 prompt 和 completion token 数，会话列表可见
- **摘要压缩** — 长对话自动压缩历史摘要（3000 token 阈值），保留最近 12 条完整消息
- **API Key 安全存储** — Windows 下 DPAPI 加密，前端不接触密文
- **测试连接** — 一键验证 API Key 和模型是否可用

### 🔌 插件系统

- 开放插件架构，支持三种运行时：纯前端（none）、内嵌 JS 引擎（goja）、独立子进程（native），基于 JSON-RPC 2.0 通信
- 40+ 官方插件经「在线市场」一键安装 / 升级，插件列表、功能说明与开发文档统一维护在仓库 [quickdock-plugins](https://github.com/parieses/quickdock-plugins)
- 支持运行时安装 / 卸载 / 启用 / 禁用 / 热键绑定

### ☁️ WebDAV 云同步

- 全量 JSON 备份 / 恢复
- 多版本管理
- 任意 WebDAV 服务器（自建 / 第三方）
- 统一「同步后端」分层（`services` → `internal/sync` → `internal/webdav`），未来可无痛接入 Git / 对象存储

### 📸 快照备份

- 一键导出全部数据为 JSON 文件
- 导入恢复，迁移无忧

### 🔧 全局热键（可自定义）

- 所有热键均可在设置页面自定义
- 运行时动态重注册
- 捕获新热键时自动暂停全局监听以避免冲突

### 🖥️ 系统命令

- 锁屏、关机、重启、睡眠、清空回收站

---


## 🔧 环境管理

内置多运行时版本管理，支持一键安装 / 切换 / 启停，多版本共存于用户目录，不污染系统 PATH，纯用户态、清理时随 QuickDock 一并删除。

### 支持的运行时

当前内置 **26 个**运行时，按分组在侧边栏归类（语言 / Web 服务器 / 缓存与存储 / 工具 / 数据库）。「类型」列标注是否支持服务化（一键启停 / 重启 / 日志 / 控制台）。

| 运行时 | 分组 | 类型 | 下载源 | 版本来源 | 平台 |
|--------|------|------|--------|----------|------|
| **Node.js** | 语言 | 命令行 | npmmirror 镜像 / 官方 | 全量（LTS + 近 3 年） | Windows |
| **Go** | 语言 | 命令行 | 官方 / golang.google.cn | 全量 | Windows |
| **PHP** | 语言 | 服务型（php-fpm） | windows.php.net（VS16/VS17） | 全量 | Windows |
| **Python** | 语言 | 命令行 | python.org | 全量 | Windows |
| **Bun** | 语言 | 命令行 | oven-sh/bun | 全量 | Windows |
| **Erlang** | 语言 | 命令行（被 RabbitMQ 依赖） | erlang/otp | 全量 | Windows |
| **Nginx** | Web 服务器 | 服务型 | nginx.org | 全量 | Windows |
| **Caddy** | Web 服务器 | 服务型 | caddyserver/caddy | 全量 | Windows |
| **Apache** | Web 服务器 | 服务型 | Apache Lounge | 全量（目录页动态解析） | Windows |
| **Traefik** | Web 服务器 | 服务型 | traefik/traefik | 全量 | Windows |
| **FTP** | Web 服务器 | 服务型 | FTPDMIN（Sentex） | 单版本 0.96 | Windows |
| **Redis** | 缓存/存储 | 服务型 | redis-windows | 全量 | Windows |
| **Memcached** | 缓存/存储 | 服务型 | adamyg/memcached-win32 | 全量 | Windows |
| **MinIO** | 存储 | 服务型 | dl.min.io | 滚动发布（latest） | Windows |
| **RabbitMQ** | 消息队列 | 服务型 | rabbitmq-server | 全量 | Windows |
| **Git** | 工具 | 命令行 | git-for-windows | 全量 | Windows |
| **Composer** | 工具 | 命令行 | getcomposer.org | 全量 | Windows |
| **FFmpeg** | 工具 | 命令行 | gyan.dev | 固定 3 个 | Windows |
| **Mailpit** | 工具 | 服务型 | axllent/mailpit | 全量 | Windows |
| **frpc** | 工具 | 命令行（可编辑配置） | fatedier/frp | 全量 | Windows |
| **GitHub CLI** | 工具 | 命令行 | cli/cli | 全量 | Windows |
| **mkcert** | 工具 | 命令行 | FiloSottile/mkcert | 全量 | Windows |
| **MariaDB** | 数据库 | 服务型 | archive.mariadb.org | 固定 3 个 | Windows |
| **MySQL** | 数据库 | 服务型 | cdn.mysql.com | 固定 3 个 | Windows |
| **PostgreSQL** | 数据库 | 服务型 | EnterpriseDB 二进制包 | 固定 3 个 | Windows |
| **MongoDB** | 数据库 | 服务型 | fastdl.mongodb.org | 全量 | Windows |

### 核心设计

- **便携优先**：所有版本安装至 `%APPDATA%\QuickDock\runtime\<name>\<version>`，不写注册表、不申请管理员权限
- **多版本共存**：同一运行时可安装多个版本，通过「环境变量」切换当前激活版本
- **下载源可切换**：每个运行时支持官方源 / 国内镜像 / 自定义 URL 模板（支持 `{version}` / `{v}` / `{os}` / `{arch}` / `{ext}` 占位符）
- **版本列表智能筛选**：
  - Node.js：LTS 版本 + 近 3 年发布版本，去重后按语义降序
  - PHP：自动识别 VS16 / VS17 编译器二进制包
  - Go / Redis / Git / Caddy / Traefik / RabbitMQ 等：GitHub Releases API 优先，HTML 兜底解析
  - Apache：动态抓取 Apache Lounge 目录页（VS17/VS18 两页，正则大小写不敏感）解析版本与下载地址
- **缓存策略**：版本列表内存缓存 1h（仅成功时缓存），失败立即重试，避免网络抖动后长时间看到旧数据
- **代理容错**：fetchURL 代理请求失败时自动降级直连，超时 30s
- **下载可靠性**：先写临时文件，完整成功后才落盘目标目录，避免中途失败留下半成品
- **后台异步**：下载 / 解压 / 安装全程异步，进度通过 IPC 事件实时推送前端

### 服务管理能力

- **启停与状态**：服务型运行时（Redis / Nginx / Apache / Caddy / Traefik / FTP / MySQL / MariaDB / PostgreSQL / MongoDB / Memcached / MinIO / Mailpit / RabbitMQ / PHP-fpm）一键启动、停止，实时显示 PID、端口与健康状态
- **重启**：运行中版本提供「重启」按钮，先停后启，并复用启动时的端口冲突检测与配置校验，避免重启后出现端口占用
- **配置编辑**：支持配置文件的运行时提供「编辑配置」入口（如 `Caddyfile` / `redis.conf` / `nginx.conf` / `traefik.yml` / `php.ini` / `frpc.toml`）；Nginx、Traefik 等支持启动前配置校验（`nginx -t` / `traefik validate`），校验失败阻止启动
- **日志查看**：服务型运行时提供「查看日志」弹窗，实时滚动读取进程日志尾部 8KB（覆盖 PostgreSQL / MySQL·MariaDB / MongoDB / Memcached / MinIO / Mailpit / Apache / Nginx / Redis / RabbitMQ / Caddy / Traefik）
- **Web 控制台一键打开**：运行时监听内置 Web UI 时，运行中显示「打开控制台」按钮，直接打开 `http://127.0.0.1:<port>`，覆盖 MinIO(9001) / Mailpit(8025) / Traefik(8080) / RabbitMQ(15672，仅管理插件启用时) / Nginx·Caddy(80)
- **端口全景**：顶栏「端口全景」弹窗汇总所有运行服务的运行时 / 版本 / 端口 / 控制台入口，快速掌握本机开发服务占用情况

### UI 交互

- 点击运行时标签 → 自动加载版本列表
- 点击 `▾` 按钮或聚焦输入框 → 展开版本下拉列表（滚动、高亮当前选中版本）
- 点击外部 → 自动收起列表
- 加载失败 → 显示错误信息 + 「重试」按钮
- 手动刷新 → 点「刷新列表」按钮（换源后重新拉取）
- 有更新 → 显示「可更新：X.X.X → Y.Y.Y」提示，一键升级
- 运行中版本：显示「重启」/「停止」按钮，「打开控制台」「查看日志」按需出现

### 文件结构

```
services/env/
├── source.go          # 运行时注册表 + 版本列表解析（全量拉取/HTML 兜底）+ 缓存 + 多下载源
├── manager.go         # Manager 门面：List / AvailableVersions / SetSource / Restart / LogGet / WebConsolePort
├── meta.go            # 版本元数据（别名 / 备注 / 激活状态）
├── detect_cache.go    # 系统已装版本检测结果持久化缓存（detected.json）
├── download.go        # 下载器（多源回退 + 进度回调 + 代理感知）
├── extract.go         # 解压（zip / tar.gz / tar.xz）
├── config.go          # 通用配置文件读取/写入（ConfigProvider）
├── validate.go        # 配置校验工具（nginx -t / traefik validate 等封装）
├── service.go         # serviceManager：服务生命周期（start/stop/startPTY/startWithEnv）+ 端口探测
├── node.go  go.go  php.go  python.go  bun.go  erlang*.go   # 语言运行时
├── nginx.go  caddy.go  apache.go  traefik.go  ftp.go        # Web 服务器运行时
├── redis.go  memcached.go  minio.go  rabbitmq.go  mongodb.go  # 缓存/存储/消息队列
├── sql.go  postgresql*.go                            # 数据库（MySQL/MariaDB 共用 SQLRuntime）
└── git.go  composer.go  ffmpeg.go  mailpit.go  frpc.go  gh.go  mkcert.go  # 工具类运行时
```
## 快速开始

### 系统要求

- **操作系统**：Windows 10 1809+ 或 Windows 11
- **运行时**：WebView2 Runtime（Windows 自动带）
- **磁盘**：~100MB

### 下载安装

1. 从 [Releases](https://github.com/parieses/quickdock/releases) 下载最新版本
2. 解压到任意目录（推荐 `%LOCALAPPDATA%\QuickDock`）
3. 运行 `QuickDock.exe`
4. 任务栏托盘出现 QuickDock 图标即启动成功

### 首次使用

启动后按 `Ctrl+Space` 唤出主窗口，跟随引导页完成初始设置即可开始使用。

---

## 开发指南

### 前置条件

- Go 1.25+
- Node.js 22+
- Wails3 CLI（**版本须与 `go.mod` 中 wails 模块一致**，见下）

```bash
# 安装 Wails3 CLI —— 版本要与 go.mod 里的 github.com/wailsapp/wails/v3 完全一致
# （当前为 alpha2.115；CLI 与模块版本不一致会导致 bindings 生成格式/内容差异）
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.115

# 安装前端依赖
cd frontend && npm install

# 重新生成前端 bindings（新增/修改 Go 绑定后必须执行；-ts 输出 TS，-clean 先清旧文件）
wails3 generate bindings -ts -i -clean
#   等价于：cd frontend && npm run bindings
```

### 常用命令

| 命令 | 说明 |
|------|------|
| `wails3 dev` | 开发模式（前后端热重载） |
| `go build -o quickdock.exe .` | 直接 Go 构建（需先 `npm run build`） |
| `wails3 build` | 生产构建（含前端构建 + 绑定生成） |
| `task build` | 通过 Taskfile 构建 |
| `task run` | 直接运行已构建的应用 |

**注意**：开发版与正式版共用同一 SQLite 数据库与单实例锁，同一时刻一台机器只能运行一个 QuickDock 实例；正式发布仍走 `wails3 build`（生产 tag 注入版本号与图标 / 清单）。

### 数据库

- SQLite 数据库文件：`~/.quickdock/quickdock.db`（WAL + 外键 + 5s busy timeout）
- 剪贴板图片：`~/.quickdock/images/`
- 应用配置：`%APPDATA%/QuickDock/`
- Node / DSH 运行时：便携 Node 与 dsh 安装于 `~/.quickdock/runtime/node`、`~/.quickdock/dsh`，数据与用户 profile 复用 `~/.dsh`

---

## 技术栈

### 后端

| 技术 | 说明 |
|------|------|
| **Go 1.25** | 主语言 |
| **Wails3 v3.0.0-alpha2.115** | 桌面应用框架 |
| **modernc.org/sqlite** | 纯 Go SQLite（无 CGO） |
| **go-sql-driver/mysql + go-redis/v9** | MySQL / Redis 客户端 |
| **golang.org/x/sys** | Windows 系统 API 调用 |
| **dop251/goja** | JavaScript 沙箱（插件执行） |
| **google/uuid** | UUID 生成 |
| **DPAPI** | Windows 数据保护 API（API Key 加密） |

### 前端

| 技术 | 说明 |
|------|------|
| **Vue 3 + TypeScript** | UI 框架 |
| **Vite 8** | 构建工具（Rolldown） |
| **Pinia 3** | 状态管理 |
| **vue-i18n 11** | 国际化（简体中文 / English） |
| **Lucide Vue** | 图标库 |
| **pinyin-pro** | 拼音搜索支持 |
| **marked 18 + DOMPurify** | Markdown 安全渲染 |
| **@floating-ui/vue 2** | 浮层定位（右键菜单 / 划词浮层 / 图表 tooltip，自动防溢出与翻转） |
| **@wailsio/runtime** | Wails 前端运行时绑定 |

---

## 项目结构

```
quickdock/
├── main.go              # 入口：主窗口创建 + 应用配置 + 更新检查
├── windows.go           # 浮动窗口（剪贴板 / 笔记 / 命令面板）+ 单实例锁
├── tray.go              # 托盘菜单与全局热键注册
├── services/            # Wails 服务层（200+ 前端绑定方法）
│   ├── service.go       # AppService 核心
│   ├── lifecycle.go     # 生命周期管理
│   ├── workspace.go     # 工作空间 CRUD
│   ├── scene.go         # 场景 CRUD
│   ├── collection.go    # 集合 CRUD
│   ├── item.go          # 项目 CRUD
│   ├── clipboard.go     # 剪贴板历史
│   ├── clipboard_sys.go # 系统剪贴板操作
│   ├── palette.go       # 命令面板搜索
│   ├── snippet.go       # 文本片段 / 笔记树
│   ├── note.go          # 笔记树服务（文件夹 + Markdown 文档）
│   ├── todo.go          # 待办任务
│   ├── reminder.go      # 待办提醒调度器（10s 轮询）
│   ├── focus.go         # 番茄专注完成通知
│   ├── schedule.go      # 定时任务
│   ├── schedule_runner.go # 定时任务调度引擎
│   ├── monitor.go       # 网站监控
│   ├── monitor_checker.go # 监控探测引擎
│   ├── system.go        # 系统命令（锁屏 / 关机 / 重启 / 睡眠 / 清空回收站）
│   ├── theme.go         # 主题与窗口底色
│   ├── hotkey.go        # 热键配置管理
│   ├── plugin_*.go      # 插件管理（manage / exec / frontend / host / hotkey / install / window）
│   ├── snapshot.go      # 快照备份
│   ├── sync.go          # WebDAV 同步（统一同步后端）
│   ├── app_launcher.go  # 应用启动
│   ├── frecency.go      # 频率排序算法
│   ├── tool.go          # 打开工具管理
│   ├── autostart.go     # 开机自启
│   ├── api_result.go    # 统一 API 返回
│   ├── types.go         # 配置类型定义
│   ├── ai_*.go          # AI 对话核心（ai_config / ai_conversation / ai_stream / ai_chat）
│   ├── env_service.go    # 环境管理 Wails 绑定（EnvList/EnvStart/EnvStop/EnvRestart/EnvLogGet/EnvConfig*…）
│   └── env/             # 多运行时版本管理（26 个运行时 + 服务生命周期）
│   ├── webhook_notify.go# 多渠道 Webhook 通知
│   ├── updatemirror.go  # 更新下载镜像回退（ghfast.top 等）
│   ├── update.go        # 软件更新检查与下载（Ed25519 签名）
│   └── dsh/             # 独立包：node_env.go(Node 便携) + dsh_runtime.go(dsh web 进程) + sysattr_*.go
├── internal/
│   ├── db/              # SQLite 数据层
│   │   ├── db.go        # Database 封装 + 安全白名单
│   │   ├── schema.go    # 表结构 + 自动迁移
│   │   ├── ai.go        # ai_conversations / ai_messages CRUD
│   │   ├── workspace.go # 工作空间数据层
│   │   ├── collection.go# 集合数据层
│   │   ├── item.go      # 项目数据层
│   │   ├── clipboard.go # 剪贴板数据层
│   │   ├── snippet.go   # 文本片段数据层
│   │   ├── note_tree.go # 笔记树（snippets 表升级：文件夹 + 文档）
│   │   ├── todo.go      # 待办数据层
│   │   ├── schedule.go  # 定时任务数据层
│   │   ├── monitor.go   # 监控数据层
│   │   ├── tool.go      # 打开工具数据层
│   │   ├── plugin.go    # 插件数据层
│   │   ├── plugin_data.go # 插件专属存储
│   │   ├── plugin_log.go  # 插件执行日志
│   │   ├── usage.go     # 使用频率统计
│   │   ├── settings.go  # 设置数据层
│   │   ├── snapshot.go  # 快照数据层
│   │   ├── repository.go# 仓库层
│   │   ├── helpers.go   # 辅助函数
│   │   └── toolexec_*.go # 工具裸名解析（PATH/App Paths/安装目录；_windows/_other）
│   ├── platform/        # 平台 API 封装
│   │   ├── crypto_windows.go # DPAPI 加密（API Key）
│   │   ├── crypto_darwin.go  # macOS 加密兜底
│   │   ├── clipboard.go # 剪贴板读写
│   │   ├── clipboard_listener_*.go # 剪贴板变更监听（隐藏消息窗口 + AddClipboardFormatListener）
│   │   ├── monitor_other.go # 非 Windows 显示器定位占位
│   │   ├── commands.go  # 系统命令
│   │   ├── monitor.go   # 多显示器定位
│   │   ├── hotkey.go    # 全局热键
│   │   ├── apps.go      # 已安装应用扫描
│   │   ├── favicon.go   # URL 图标抓取
│   │   ├── opener.go    # ShellExecute 打开（危险协议拦截）
│   │   ├── itemicon_*.go # 项目图标
│   │   ├── datadir_*.go # 数据目录（生产 / 开发共用）
│   │   └── icon.go      # 图标处理
│   ├── plugin/          # 插件管理器（manifest / manager / rpc / host / installer / window_manager…）
│   ├── sync/            # 统一同步后端（当前实现：WebDAV）
│   └── webdav/          # WebDAV HTTP 客户端
├── frontend/
│   ├── src/
│   │   ├── components/  # 32 个通用 Vue 组件
│   │   │   ├── AIPage.vue         # AI 对话页面
│   │   │   ├── TodoPage.vue       # 待办任务页面（含番茄专注）
│   │   │   ├── SchedulePage.vue   # 定时任务页面
│   │   │   ├── MonitorPage.vue    # 网站监控页面
│   │   │   ├── NotePanel.vue      # 快捷笔记面板
│   │   │   ├── NoteTreeNode.vue   # 笔记树节点
│   │   │   ├── PluginManagerPage.vue # 插件管理页面
│   │   │   ├── SettingsDSH.vue    # DeepSeek Harness 设置页
│   │   │   ├── WebhookSettingsModal.vue # Webhook 通知配置
│   │   │   └── ...（更多组件）
│   │   ├── stores/       # Pinia 状态管理
│   │   ├── types/        # TypeScript 类型（含 ai.ts）
│   │   ├── utils/        # 工具函数（api.ts / pluginBridge.ts / calc.ts …）
│   │   ├── i18n/         # 国际化（zh-CN / en-US）
│   │   └── composables/  # 组合式函数（useFloatMenu / useFrecency …）
│   └── vite.config.ts
├── plugins/builtin/     # 仅存 common.css/js 骨架（宿主注入兼容用，勿删）
├── plugins/templates/   # 插件开发模板（none / goja / native）
├── build/               # 构建配置
├── docs/                # 设计文档
├── DESIGN.md            # 设计系统规范
├── Taskfile.yml         # 构建任务定义
└── go.mod
```

---

## 数据模型

```
Workspace（工作空间）
  └── Scene（场景）— 标签/类型/图标/颜色
       └── Collection（集合）— 类型/打开策略/关联工具
            └── Item（项目）
                  ├── directory   — 目录
                  ├── file        — 文件
                  ├── url         — 网页链接
                  ├── command     — 终端命令
                  ├── app         — 应用程序
                  └── quicklink   — 快速链接
```

**独立模型：**

| 模型 | 说明 |
|------|------|
| **ClipboardEntry** | 剪贴板条目（文本/图片/文件） |
| **Snippet** | 文本片段 / 笔记树节点（`is_folder` 区分文件夹与 Markdown 文档） |
| **Todo** | 待办任务（子任务、标签、状态、重复） |
| **Schedule** | 定时任务（五种调度、五种动作） |
| **Monitor** | 网站监控（状态码/SSL/内容匹配） |
| **AIConversation** | AI 对话会话（标题、摘要、token 统计） |
| **AIMessage** | AI 消息（角色、内容、思考过程） |
| **WebhookConfig** | 多渠道通知配置（钉钉 / 企微 / 飞书 / Server酱 / PushPlus / Telegram） |

所有数据库表通过白名单机制防止 SQL 注入。

---

## 全局热键

| 功能 | 默认快捷键 | 说明 |
|------|-----------|------|
| 切换主窗口 | `Ctrl+Space` | 显示 / 隐藏主界面 |
| 剪贴板历史 | `` Ctrl+` ``（反引号） | 显示 / 隐藏剪贴板浮动窗口 |
| 命令面板 | `Ctrl+K` | 显示 / 隐藏命令面板浮动窗口 |
| 快捷笔记 | `Ctrl+Shift+N` | 显示 / 隐藏笔记浮动窗口 |

> 所有热键均可在「设置 > 热键」页面自定义。

---

## 设计哲学

快启坞遵循 **精准暗色极简主义（Precision Dark Minimalism）**：

- **暗色主题为主** — 层次化灰色调，非纯黑，通过明度对比创造深度；支持深色 / 浅色 / 跟随系统切换，插件窗口同步适配
- **强调色 `#4a9eff`** — 仅用于功能性交互元素，不作装饰
- **三面板布局** — 侧边栏(210px) | 集合列表(300px) | 项目列表(flex-1)
- **8px 基准间距** — 4px 递增，从 2px 到 48px
- **系统字体栈** — 无自定义字体，基字大小 13px
- **150ms 过渡** — 少动效，仅状态变化时使用动画
- **键盘优先** — 所有交互均支持键鼠操作
- **Shadow-border 技术** — `box-shadow` 替代 CSS `border`，消除布局偏移
- **4 种色调层次** — `bg-primary(#1a1a1a)` → `bg-secondary(#1e1e1e)` → `bg-tertiary(#242424)` → `bg-active(#2a2a2a)`

详细设计规范请参阅 [DESIGN.md](./DESIGN.md)。

---

## 插件生态

QuickDock 采用开放的插件架构，官方插件（40+）统一在独立仓库维护与分发：

👉 **[quickdock-plugins](https://github.com/parieses/quickdock-plugins)** —— 插件列表、功能说明与完整开发文档（目录结构、`plugin.json` 字段、三种运行时快速开始、通信协议、调试与发布）均在该仓库 README 中。

主仓库仅保留插件**宿主实现**（安装 / 启停 / 通信 / 热键 / 窗口管理），源码位于 `internal/plugin/`（类型 / 清单 / 管理器 / JSON-RPC / 宿主 / 安装器 / 窗口）与 `services/plugin_*.go`（Wails 前端绑定）；前端经「插件管理」页的「在线市场」标签一键安装 / 升级官方插件。

## 构建与打包

### 本地构建

```bash
# 开发模式（热重载）
wails3 dev

# 生产构建
wails3 build

# 直接 Go 构建（需先 npm run build）
cd frontend && npm run build
cd .. && CGO_ENABLED=0 go build -o quickdock.exe .

# 运行
./quickdock.exe
```

### 平台支持

> **当前仅支持 Windows 10 1809+/Windows 11**。macOS 适配已做平台层抽象（`mac` 分支），但 `.app` 必须在 Mac 上构建（Wails 依赖 CGO 和 macOS 框架）。

---

## 架构亮点

- **四窗口架构**：主窗口 (1100×700) + 剪贴板 / 笔记浮动窗口 (480×420) + 命令面板浮动窗口 (680×460)，另有 DSH 原生长驻窗口
- **所有次级窗口延迟创建**：在首次热键触发时才创建 WebView2，确保运行时完全初始化，避免白屏
- **WebView2 内存优化**：`--in-process-gpu` + `--renderer-process-limit=4` 等参数限制渲染进程数，任务管理器更清爽
- **窗口即隐藏**：关闭主窗口时隐藏到系统托盘而非退出，通过 `atomic.Bool` 标志区分真实退出
- **多显示器支持**：浮动窗口自动定位到鼠标所在屏幕
- **浮窗位置记忆**：剪贴板 / 命令面板 / 笔记浮窗位置经 kvstore 服务持久化，重启后原位恢复；拔出外接屏后自动收拢回窗口所在屏幕工作区
- **框架原生底座**：托盘 / 全局热键 / 单实例 / 开机自启 / 自动更新 / 系统通知均基于 Wails v3 框架 API；系统日志汇入统一面板（`slog.SetDefault` 桥接）；仅剪贴板富内容、图标提取、系统命令等原理性 Win32 调用保留手写
- **纯 Go SQLite**：使用 modernc.org/sqlite，零 CGO 依赖，简化交叉编译
- **回调注入解耦**：热键函数通过注入方式避免 main 和 services 包之间的循环依赖
- **SQL 白名单**：表名和列名校验防止 SQL 注入
- **本地流式架构**：内置 `127.0.0.1` HTTP SSE 流式服务（随机端口 + 随机 token），避免 Wails 事件框架的缓冲限制，实现逐 token 即时显示（用于 AI 对话等场景）
- **API Key 安全加密**：Windows 下 DPAPI 加密存储（`CryptProtectData`），macOS 下 base64 编码，前端全程不接触密文
- **单实例锁**：框架 `Options.SingleInstance`（UniqueID `QuickDock-Instance`，开发/正式共用）——二次启动自动通知首实例把主窗口带到前台后退出，避免多进程并发写同一 SQLite 库
- **后台服务**：SQLite WAL 模式 + 待办提醒调度器 (10s) + 定时任务调度器 + 监控检查器（含 SSL 检测）+ 流式 HTTP 服务（AI）+ 插件健康检查 + DSH 隐藏进程管理

---

## 许可协议

本项目采用 **MIT License** 开源许可证。

Copyright (c) 2025-2026 王亮亮

```
MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

---

## 致谢

- [Wails](https://wails.io/) — 强大的 Go 桌面应用框架
- [Vue.js](https://vuejs.org/) — 渐进式前端框架
- [Floating UI](https://floating-ui.com/) — 浮层定位引擎（防溢出 / 自动翻转）
- [Lucide](https://lucide.dev/) — 优雅的开源图标库
- [modernc.org/sqlite](https://modernc.org/sqlite) — 纯 Go SQLite 实现
- [marked](https://marked.js.org/) — 快速 Markdown 解析
- [DOMPurify](https://github.com/cure53/DOMPurify) — XSS 安全过滤
