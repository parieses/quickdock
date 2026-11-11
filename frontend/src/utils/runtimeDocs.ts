/**
 * 环境管理里每个运行时的「这是什么 / 在这儿能做什么 / 去哪儿看文档」资料。
 *
 * 为什么单独一份数据而不是塞进 i18n 字典：这里每条都是「一个技术名词 + 它在本页
 * 承担什么角色 + 官网/文档/源码地址 + 要点 + 坑」的绑定关系，32 个运行时就是
 * 32 组双语说明。放进字典会变成上百个互不相关的 key，改一个运行时的介绍要在
 * 几千行里找；放在这里则是一个 id 一行块，加运行时顺手补一段即可。
 *
 * 约束：
 *   - home / docs / repo 只放 https 绝对地址（openExternal 只认 http(s)，相对路径会被静默忽略）；
 *   - zh/en 是「一句话定性」，points 是「在 QuickDock 里能拿它做什么」，tip 是「第一次用最容易踩的那个坑」；
 *     points 不写百科常识，只写本页确实提供的动作（装版本 / 切激活 / 改配置 / 启停 / 看日志 / 导入已有安装）；
 *   - 默认端口之类数字只在 QuickDock 里确实是这个值时才写，别把官方默认值混进来；
 *   - 找不到资料就别硬编：UI 侧查不到条目时不显示问号，不做「暂无介绍」的空壳。
 */
export interface RuntimeDoc {
  /** 官网首页 */
  home: string
  /** 教程 / 文档入口；缺省视为与 home 相同 */
  docs?: string
  /** 源码仓库（可选，只有开源项目才给） */
  repo?: string
  /** 一句话定性 */
  zh: string
  en: string
  /** 本页能做什么：2~3 条，双语逐条对应 */
  points: { zh: string[]; en: string[] }
  /** 上手提示：一条最值得先说的坑 / 前提 */
  tip?: { zh: string; en: string }
}

export const RUNTIME_DOCS: Record<string, RuntimeDoc> = {
  node: {
    home: 'https://nodejs.org/',
    docs: 'https://nodejs.org/en/learn',
    repo: 'https://github.com/nodejs/node',
    zh: 'JavaScript 运行时。前端构建、npm 脚本、本地 Node 服务都靠它，版本之间可以随时切换。',
    en: 'The JavaScript runtime behind frontend builds, npm scripts and local Node services; versions can be switched at any time.',
    points: {
      zh: ['多版本并存，一键切换激活版本，PATH 由本页接管', '换下载源 / 开 GitHub 加速，装版本不用手动翻墙', '已装在别处的 Node 可以用「导入已有安装」认回来'],
      en: ['Multiple versions side by side, switch the active one with one click (PATH is managed here)', 'Switch download mirrors or enable GitHub acceleration', '"Import existing install" picks up a Node already on your machine'],
    },
    tip: { zh: '侧栏的 DeepSeek Harness 会先探测 Node，缺了它会自动带你来这一页装。', en: 'DeepSeek Harness in the sidebar probes Node first and brings you here to install it when missing.' },
  },
  php: {
    home: 'https://www.php.net/',
    docs: 'https://www.php.net/manual/zh/',
    zh: 'PHP 解释器与 php.ini。配合 Nginx / Apache 跑本地站点，扩展在本页的配置文件里开启。',
    en: 'PHP interpreter plus php.ini. Pair it with Nginx or Apache for local sites; extensions are enabled in the config file on this page.',
    points: {
      zh: ['多版本并存，按项目切换激活版本', '直接编辑 php.ini 开关扩展、改上传大小与时区', '起 Web 服务前先选好版本，站点才用得上对应的 php-cgi'],
      en: ['Multiple versions side by side, switch per project', 'Edit php.ini to toggle extensions, upload limits and timezone', 'Pick the version before starting a web server so sites use the matching php-cgi'],
    },
    tip: { zh: '改完 php.ini 要重启对应的 Web 服务才会生效。', en: 'Restart the matching web server after editing php.ini for changes to take effect.' },
  },
  go: {
    home: 'https://go.dev/',
    docs: 'https://go.dev/doc/tutorial/',
    repo: 'https://github.com/golang/go',
    zh: 'Go 工具链，提供 go build / test / run 与模块缓存。多版本可并存，激活哪个由 PATH 决定。',
    en: 'The Go toolchain: go build / test / run plus the module cache. Multiple versions can coexist; PATH decides the active one.',
    points: {
      zh: ['多版本并存，随时切换激活版本', '模块缓存与 GOPATH 跟版本走，不用手动改环境变量', '导入系统里已装的 Go，避免重复下载'],
      en: ['Multiple versions side by side, switch the active one anytime', 'Module cache and GOPATH follow the active version, no manual env vars', 'Import an existing system Go instead of downloading it again'],
    },
    tip: { zh: '切换版本后，已经开着的终端要重开才会拿到新的 PATH。', en: 'Reopen existing terminals after switching versions to pick up the new PATH.' },
  },
  bun: {
    home: 'https://bun.sh/',
    docs: 'https://bun.sh/docs',
    repo: 'https://github.com/oven-sh/bun',
    zh: 'Bun 运行时与包管理器，可直接替代 npm / pnpm 装依赖，也能跑 Node 项目。',
    en: 'Bun runtime and package manager; drop-in replacement for npm / pnpm and can run Node projects.',
    points: {
      zh: ['单文件安装，一键装最新 / 一键更新', '和 Node 分开管理，互不覆盖', 'bun install / bun run 直接在系统终端里用'],
      en: ['Single-binary install: one click for latest or update', 'Managed separately from Node, neither overwrites the other', 'Use bun install / bun run directly from your terminal'],
    },
    tip: { zh: 'Bun 不替代 Node：有些依赖仍需要 Node 的原生模块链，两者可以同时装。', en: 'Bun does not replace Node: keep both installed for packages that still rely on Node native modules.' },
  },
  erlang: {
    home: 'https://www.erlang.org/',
    docs: 'https://www.erlang.org/doc',
    repo: 'https://github.com/erlang/otp',
    zh: 'Erlang/OTP 运行时。RabbitMQ 的安装前提，装 RabbitMQ 前先在这里装好它。',
    en: 'Erlang/OTP runtime, required before installing RabbitMQ.',
    points: {
      zh: ['给 RabbitMQ 提供运行时，本身不单独启服务', '多版本并存，RabbitMQ 会挑兼容的那一个'],
      en: ['Provides the runtime for RabbitMQ; no standalone service of its own', 'Versions coexist, RabbitMQ picks a compatible one'],
    },
    tip: { zh: '先装 Erlang 再装 RabbitMQ，反过来的顺序会启动失败。', en: 'Install Erlang before RabbitMQ; the reverse order fails at startup.' },
  },
  nginx: {
    home: 'https://nginx.org/',
    docs: 'https://nginx.org/en/docs/',
    zh: '反向代理与静态文件服务器。启动前会先跑 nginx -t 校验配置，改坏了不会静默失败。',
    en: 'Reverse proxy and static file server. Config is validated with nginx -t before every start.',
    points: {
      zh: ['在本页编辑 nginx.conf，改完先校验再启动', '启停、常驻自启、查看运行日志都在这里', '配置里的侦听端口会被解析出来显示在状态行'],
      en: ['Edit nginx.conf here; config is validated before starting', 'Start/stop, always-on and runtime logs are all here', 'Listen ports parsed from config show up on the status row'],
    },
    tip: { zh: '端口被占用时先改配置里的 listen，别直接改本页的端口数字。', en: 'If a port is taken, change listen in the config rather than the port shown on this page.' },
  },
  caddy: {
    home: 'https://caddyserver.com/',
    docs: 'https://caddyserver.com/docs/',
    repo: 'https://github.com/caddyserver/caddy',
    zh: '带自动 HTTPS 的 Web 服务器，配合本页的本地 CA 可签发受信任的域名证书。',
    en: 'Web server with automatic HTTPS; pairs with the local CA on this page for trusted domain certificates.',
    points: {
      zh: ['编辑 Caddyfile，启动时先做配置校验', '配合「本地站点」面板自动写 hosts 与域名路由', '管理接口（2019）与站点端口会分别显示在状态里'],
      en: ['Edit the Caddyfile; config is validated on start', 'Works with the local sites panel for hosts entries and domain routing', 'Admin API (2019) and site ports are shown separately in status'],
    },
    tip: { zh: '用 https:// 的本地域名时，先在「本地站点」装好本地 CA，浏览器才不报证书警告。', en: 'For https:// local domains, install the local CA in "Local sites" first to avoid browser warnings.' },
  },
  traefik: {
    home: 'https://traefik.io/',
    docs: 'https://doc.traefik.io/traefik/',
    repo: 'https://github.com/traefik/traefik',
    zh: '动态反向代理，按配置文件里的路由规则转发请求，改规则不用重启。',
    en: 'Dynamic reverse proxy that routes by config rules without a restart.',
    points: {
      zh: ['在本页编辑静态配置与路由规则', '启停、常驻、日志与端口状态集中管理'],
      en: ['Edit static config and routing rules on this page', 'Start/stop, always-on, logs and port status in one place'],
    },
    tip: { zh: '规则文件改动会被热加载，但入口点（entryPoints）变更仍需重启。', en: 'Rule changes hot-reload, but entryPoint changes still need a restart.' },
  },
  redis: {
    home: 'https://redis.io/',
    docs: 'https://redis.io/docs/latest/',
    repo: 'https://github.com/redis/redis',
    zh: '内存键值数据库，常用作缓存、会话与队列。连接信息在启动后显示在版本行上。',
    en: 'In-memory key-value store for caching, sessions and queues; connection info appears on the version row once started.',
    points: {
      zh: ['编辑 redis.conf 改端口、密码与持久化策略', '启停服务、开常驻自愈、看实时日志', '内置 Redis 面板可直接看键与内存占用'],
      en: ['Edit redis.conf for port, password and persistence', 'Start/stop, always-on self-healing and live logs', 'Built-in Redis panel shows keys and memory usage'],
    },
    tip: { zh: '默认只监听本机；要给局域网用，得在配置里改 bind 并设密码。', en: 'It listens on localhost by default; to expose it on the LAN, change bind and set a password.' },
  },
  memcached: {
    home: 'https://memcached.org/',
    docs: 'https://github.com/memcached/memcached/wiki',
    repo: 'https://github.com/memcached/memcached',
    zh: '纯内存缓存服务，比 Redis 更简单，只做 key-value 缓存。',
    en: 'Pure in-memory cache service: simpler than Redis, key-value only.',
    points: {
      zh: ['一键启停，端口与内存上限在配置文件里改', '支持常驻自启，进程挂了会自动拉起'],
      en: ['One-click start/stop; port and memory cap live in the config file', 'Always-on support restarts the process if it dies'],
    },
    tip: { zh: '没有持久化：服务一重启，缓存全没了。', en: 'No persistence: a restart wipes the entire cache.' },
  },
  mariadb: {
    home: 'https://mariadb.org/',
    docs: 'https://mariadb.com/kb/en/documentation/',
    zh: 'MySQL 兼容的关系型数据库。多个大版本可以并存，数据目录各自独立。',
    en: 'MySQL-compatible relational database. Major versions coexist with separate data directories.',
    points: {
      zh: ['多个大版本并存，数据目录互不干扰', '初始化、启停、root 密码与配置都在本页', '可查看错误日志定位启动失败'],
      en: ['Multiple major versions coexist with isolated data directories', 'Init, start/stop, root password and config handled here', 'Check the error log when a start fails'],
    },
    tip: { zh: '换版本不会迁移旧数据目录，先备份再装新版本。', en: 'Switching versions does not migrate the old data directory — back it up first.' },
  },
  mysql: {
    home: 'https://www.mysql.com/',
    docs: 'https://dev.mysql.com/doc/',
    zh: '官方 MySQL 服务端。多版本并存，初始化时会生成 root 随机密码。',
    en: 'Official MySQL server. Versions coexist; a random root password is generated on init.',
    points: {
      zh: ['多版本并存，各自独立的数据目录与端口', '初始化后本页会显示生成的 root 随机密码', '启停、常驻、错误日志一屏可见'],
      en: ['Multiple versions with separate data dirs and ports', 'The generated random root password is shown here after init', 'Start/stop, always-on and error logs in one place'],
    },
    tip: { zh: '初始化生成的随机密码先复制走，改掉之后本页不会替你保存。', en: 'Copy the generated root password somewhere safe; this page will not keep it for you.' },
  },
  postgresql: {
    home: 'https://www.postgresql.org/',
    docs: 'https://www.postgresql.org/docs/',
    repo: 'https://github.com/postgres/postgres',
    zh: 'PostgreSQL 服务端。Windows 上不允许以管理员身份运行，本页会自动降权起进程。',
    en: 'PostgreSQL server. Windows refuses to run it as admin, so this page starts it with a reduced-privilege token.',
    points: {
      zh: ['初始化数据目录、启停服务、看日志', '自动降权起进程，绕开「不能以管理员运行」的限制', '多版本并存，端口按版本行分别配置'],
      en: ['Init the data directory, start/stop and read logs', 'Auto privilege-drop works around the "cannot run as admin" restriction', 'Multiple versions, ports configured per version row'],
    },
    tip: { zh: '数据目录初始化后别手动改权限，否则降权进程会读不到文件。', en: 'Do not change data-directory permissions by hand after init, or the dropped-privilege process cannot read it.' },
  },
  mongodb: {
    home: 'https://www.mongodb.com/',
    docs: 'https://www.mongodb.com/docs/',
    zh: '文档型数据库，单机开发实例，默认不开鉴权、只监听本机。',
    en: 'Document database as a single-node dev instance: no auth by default, bound to localhost.',
    points: {
      zh: ['启停服务、改端口、看运行日志', '常驻开启后随应用自启，崩溃自动拉起'],
      en: ['Start/stop, change port and read runtime logs', 'Always-on auto-starts with the app and restarts after crashes'],
    },
    tip: { zh: '默认无密码，别把监听地址改成 0.0.0.0 就直接放到公网。', en: 'No auth by default — do not bind 0.0.0.0 and expose it publicly as-is.' },
  },
  minio: {
    home: 'https://min.io/',
    docs: 'https://min.io/docs/minio/linux/index.html',
    repo: 'https://github.com/minio/minio',
    zh: 'S3 兼容对象存储，本地联调上传/下载时不用连真实云存储。',
    en: 'S3-compatible object storage, so uploads and downloads can be tested without a real cloud bucket.',
    points: {
      zh: ['启停服务，控制台端口显示在状态行', '访问密钥与数据目录在本页配置', '应用里把 S3 endpoint 指向本机即可联调'],
      en: ['Start/stop; console port shown in the status row', 'Access keys and data directory configured here', 'Point your app S3 endpoint at localhost for local testing'],
    },
    tip: { zh: '控制台端口和 API 端口是两个，配错时 SDK 能连通但网页打不开。', en: 'Console and API are separate ports: mixing them up lets the SDK connect while the web UI fails.' },
  },
  rabbitmq: {
    home: 'https://www.rabbitmq.com/',
    docs: 'https://www.rabbitmq.com/docs',
    zh: 'AMQP 消息队列。需要先装 Erlang；可选开启 15672 管理台，账号密码在版本行上。',
    en: 'AMQP message broker. Requires Erlang; the 15672 management UI is optional and credentials show on the version row.',
    points: {
      zh: ['装好 Erlang 后一键启停，常驻自愈可开', '按需启用 / 关闭 15672 管理后台', '查看启动日志定位 Erlang 版本不兼容'],
      en: ['One-click start/stop after Erlang is installed; always-on available', 'Enable/disable the 15672 management UI on demand', 'Read startup logs when the Erlang version is incompatible'],
    },
    tip: { zh: '装完 Erlang 再装 RabbitMQ；Erlang 与 RabbitMQ 的版本要匹配。', en: 'Install Erlang first and keep its version compatible with RabbitMQ.' },
  },
  git: {
    home: 'https://git-scm.com/',
    docs: 'https://git-scm.com/book/zh/v2',
    repo: 'https://github.com/git/git',
    zh: 'Git for Windows，含 Git Bash、SSH 与 LFS。QuickDock 的仓库页直接用它。',
    en: 'Git for Windows with Git Bash, SSH and LFS. The repositories page uses this install.',
    points: {
      zh: ['一键装最新 / 更新到最新，不需要管版本号', '仓库页的分支、提交、改动预览都走这份 git', '已有系统 Git 可以导入，不必重复安装'],
      en: ['One click to install/update to latest, no version juggling', 'The repositories page uses this git for branches, commits and diffs', 'Import an existing system Git instead of reinstalling'],
    },
    tip: { zh: 'Git Bash 和 SSH 都在这份安装里，SSH 终端窗口也能直接复用。', en: 'Git Bash and SSH ship with this install and are reusable from the SSH terminal window.' },
  },
  everything: {
    home: 'https://www.voidtools.com/',
    docs: 'https://www.voidtools.com/support/everything/',
    zh: '本地文件名索引。QuickDock 的文件搜索（Ctrl+K 搜文件）靠它，装好后常驻后台。',
    en: 'Local filename index behind QuickDock file search (Ctrl+K); runs in the background once installed.',
    points: {
      zh: ['装完由本页托管为常驻服务，挂了自动拉起', '「文件搜索」页直接按名字找全盘文件', '只索引文件名，不读内容；按内容找请用知识库'],
      en: ['Managed as an always-on service here and restarted after crashes', 'The File Search page finds files across all drives by name', 'Indexes names only — use the Knowledge page to search file contents'],
    },
    tip: { zh: '第一次装完要等索引建好，刚启动那几分钟搜索结果会不全。', en: 'Wait for the initial index build: results are incomplete for the first few minutes.' },
  },
  aria2: {
    home: 'https://aria2.github.io/',
    docs: 'https://aria2.github.io/manual/en/html/',
    repo: 'https://github.com/aria2/aria2',
    zh: '轻量多协议下载器，是下载页的引擎：QuickDock 通过 JSON-RPC 驱动它下载 HTTP / 磁力 / 种子。',
    en: 'Lightweight multi-protocol downloader and the engine behind the download page, driven over JSON-RPC for HTTP, magnet and torrent tasks.',
    points: {
      zh: ['启停 daemon、改 RPC 端口与密钥、设限速', '下载目录不可用时会自动回退并写回配置', '任务列表、进度与批量操作在「下载」页'],
      en: ['Start/stop the daemon, change RPC port/secret and speed limits', 'Falls back and persists a valid download dir if the configured one is gone', 'Task list, progress and bulk actions live on the Download page'],
    },
    tip: { zh: '这里是引擎设置；下载目录和任务请到「下载」页改。', en: 'This is engine config — set the download directory and manage tasks on the Download page.' },
  },
  composer: {
    home: 'https://getcomposer.org/',
    docs: 'https://getcomposer.org/doc/',
    repo: 'https://github.com/composer/composer',
    zh: 'PHP 依赖管理器，composer.json / vendor 由它维护。需要先装 PHP。',
    en: 'PHP dependency manager for composer.json and vendor; requires PHP first.',
    points: {
      zh: ['一键装最新 / 更新，跟着激活的 PHP 版本走', '配国内镜像源，install / update 不用等国外源'],
      en: ['One-click install/update, follows the active PHP version', 'Configure a local mirror so install/update does not wait on a remote origin'],
    },
    tip: { zh: '先装并激活 PHP，否则 composer 命令找不到解释器。', en: 'Install and activate PHP first, or composer cannot find an interpreter.' },
  },
  ffmpeg: {
    home: 'https://ffmpeg.org/',
    docs: 'https://ffmpeg.org/documentation.html',
    repo: 'https://github.com/FFmpeg/FFmpeg',
    zh: '音视频转码工具链，ffmpeg / ffprobe 都在里面，供命令行或其它工具调用。',
    en: 'Audio/video transcoding toolkit shipping ffmpeg and ffprobe for command line or other tools.',
    points: {
      zh: ['装完即可在系统终端用 ffmpeg / ffprobe', '支持多版本并存与导入已有安装'],
      en: ['ffmpeg / ffprobe are available from any terminal once installed', 'Multiple versions and importing an existing install are supported'],
    },
    tip: { zh: '它不提供图形界面，本页只管版本与 PATH。', en: 'No GUI: this page only manages the version and PATH.' },
  },
  mailpit: {
    home: 'https://mailpit.axllent.org/',
    docs: 'https://mailpit.axllent.org/docs/',
    repo: 'https://github.com/axllent/mailpit',
    zh: '本地 SMTP 收件箱 + Web 界面，联调注册/验证码邮件时不会真的发出去。',
    en: 'Local SMTP sink with a web UI, so signup and verification mails never leave your machine.',
    points: {
      zh: ['启停服务，SMTP 端口与 Web 界面地址在状态里', '应用把 SMTP 指向本机即可收到所有测试邮件', '配合总览的「邮件验证码」卡片，可直接复制验证码'],
      en: ['Start/stop; SMTP port and web UI address appear in status', 'Point your app SMTP at localhost to catch every test mail', 'Pairs with the mail-code card on the dashboard for one-click code copying'],
    },
    tip: { zh: '它只收不发：别拿它当真实邮件服务器用。', en: 'Receive-only: never use it as a real outbound mail server.' },
  },
  frpc: {
    home: 'https://github.com/fatedier/frp',
    docs: 'https://gofrp.org/zh-cn/docs/',
    repo: 'https://github.com/fatedier/frp',
    zh: 'frp 客户端，把本机服务通过公网 frps 暴露出去，配置在本页编辑。',
    en: 'frp client that exposes local services through a public frps; configured on this page.',
    points: {
      zh: ['在本页编辑 frpc 配置（服务端地址、隧道、端口）', '一键更新到最新版，不用手动下压缩包'],
      en: ['Edit frpc config here (server address, tunnels, ports)', 'One-click update to the latest release, no manual archive download'],
    },
    tip: { zh: '本页只管客户端，公网还得有一台跑 frps 的服务端。', en: 'Client only: you still need a public server running frps.' },
  },
  gh: {
    home: 'https://cli.github.com/',
    docs: 'https://cli.github.com/manual/',
    repo: 'https://github.com/cli/cli',
    zh: 'GitHub 官方命令行，gh pr / issue / repo 都在这里，首次使用要 gh auth login。',
    en: 'GitHub CLI for pr, issue and repo work; run gh auth login once.',
    points: {
      zh: ['一键装最新 / 更新到最新', '装完在系统终端用 gh 操作 PR、Issue、仓库'],
      en: ['One-click install/update to latest', 'Use gh from any terminal for PRs, issues and repos'],
    },
    tip: { zh: '第一次用要先 gh auth login 登录 GitHub 账号。', en: 'Run gh auth login once to sign in before using it.' },
  },
  mkcert: {
    home: 'https://github.com/FiloSottile/mkcert',
    docs: 'https://github.com/FiloSottile/mkcert#readme',
    repo: 'https://github.com/FiloSottile/mkcert',
    zh: '本地 CA 工具，一条命令签发被系统信任的证书，给本地 HTTPS 站点用。',
    en: 'Local CA tool that issues system-trusted certificates for local HTTPS sites.',
    points: {
      zh: ['本页「本地站点」用它生成并安装本地根证书', '证书签给配置的域名，浏览器不再报不受信任'],
      en: ['The "Local sites" panel uses it to generate and install a local root CA', 'Certificates are issued for your configured domains, removing browser warnings'],
    },
    tip: { zh: '装完根证书后重启浏览器，已打开的页面才会重新认证书。', en: 'Restart the browser after installing the root CA so open pages re-check certificates.' },
  },
  ollama: {
    home: 'https://ollama.com/',
    docs: 'https://github.com/ollama/ollama/tree/main/docs',
    repo: 'https://github.com/ollama/ollama',
    zh: '本地大模型推理服务，模型拉取与运行都在这里管，接口默认 11434。',
    en: 'Local LLM runtime for pulling and serving models; the API listens on 11434.',
    points: {
      zh: ['启停服务、开常驻，接口默认监听 11434', '模型库单独一块：拉取、删除、看占用空间', 'AI 页把 BaseURL 指向本机即可用本地模型'],
      en: ['Start/stop and always-on; API listens on 11434 by default', 'A separate model library panel for pull/delete/disk usage', 'Point the AI page BaseURL at localhost to use local models'],
    },
    tip: { zh: '模型是全局共享的几十 GB 文件，和程序版本互不相关，别装在 C 盘小分区。', en: 'Models are shared, often tens of GB, and independent of the program version — avoid a small system partition.' },
  },
  python: {
    home: 'https://www.python.org/',
    docs: 'https://docs.python.org/zh-cn/3/',
    repo: 'https://github.com/python/cpython',
    zh: 'Python 解释器与 pip。虚拟环境、依赖安装都用这个版本，可多版本并存。',
    en: 'Python interpreter with pip. Virtualenvs and installs use this version; multiple versions coexist.',
    points: {
      zh: ['多版本并存，一键切换激活版本与 PATH', '已装的系统 Python 可以导入进来', 'pip 镜像源可在本页切换'],
      en: ['Multiple versions side by side; switch the active one and PATH with one click', 'Import an existing system Python', 'pip mirrors can be switched here'],
    },
    tip: { zh: '给项目建虚拟环境再装依赖，切换全局版本不会污染别的项目。', en: 'Create a venv per project so switching the global version does not break other projects.' },
  },
  jdk: {
    home: 'https://adoptium.net/',
    docs: 'https://docs.oracle.com/en/java/',
    zh: 'Adoptium Temurin JDK。激活哪个版本，JAVA_HOME 就跟着指过去。',
    en: 'Adoptium Temurin JDK; JAVA_HOME follows whichever version is active.',
    points: {
      zh: ['多版本并存（JDK 8 / 11 / 17 / 21 常见组合）', '切换激活版本时 JAVA_HOME 与 PATH 一起更新', '可导入系统里已装的 JDK'],
      en: ['Multiple versions side by side (JDK 8 / 11 / 17 / 21 and others)', 'Switching updates JAVA_HOME and PATH together', 'Import a JDK already installed on the system'],
    },
    tip: { zh: 'JAVA_HOME 变了以后，IDE / Maven 终端要重启才会读到新值。', en: 'Restart IDEs or Maven terminals after a switch so they read the new JAVA_HOME.' },
  },
  apache: {
    home: 'https://httpd.apache.org/',
    docs: 'https://httpd.apache.org/docs/',
    zh: 'Apache HTTP Server，支持 mod_php 与 .htaccess。启动前会跑 httpd -t 校验配置。',
    en: 'Apache HTTP Server with mod_php and .htaccess; config is validated with httpd -t before start.',
    points: {
      zh: ['编辑 httpd.conf / 虚拟主机配置，启动前自动校验', '启停、常驻与日志集中管理', '配合 PHP 版本跑传统 PHP 站点'],
      en: ['Edit httpd.conf / vhosts with automatic validation before start', 'Start/stop, always-on and logs in one place', 'Pair with a PHP version to run traditional PHP sites'],
    },
    tip: { zh: '改 Listen 或加载 mod_php 之后要重启服务，不是热加载。', en: 'Changing Listen or loading mod_php requires a full restart, not a reload.' },
  },
  ftp: {
    home: 'https://www.sentex.ca/~mwandel/ftpdmin/',
    zh: '轻量 FTP 服务器 FTPDMIN，给局域网或老设备传文件用。单文件、匿名登录，根目录与端口在本页配置。',
    en: 'Lightweight FTP server (FTPDMIN) for LAN or legacy devices: one binary, anonymous login, root folder and port configured here.',
    points: {
      zh: ['启停服务，根目录与端口在本页改', '常驻开启后随应用自启', '给局域网设备传文件时不用另装大套件'],
      en: ['Start/stop; root folder and port configured here', 'Always-on starts it with the app', 'No full FTP suite needed for quick LAN transfers'],
    },
    tip: { zh: '默认匿名登录，别把共享根目录指到含敏感文件的盘。', en: 'Anonymous by default — do not point the shared root at a folder with sensitive files.' },
  },
  webdav: {
    home: 'https://www.rfc-editor.org/rfc/rfc4918',
    zh: '内置 WebDAV 服务，把本机目录通过 HTTP 共享给局域网里的其它设备。',
    en: 'Built-in WebDAV service that shares a local folder with other devices on the LAN.',
    points: {
      zh: ['不装外部程序，直接启停即可用', '共享目录、端口与账号在本页配置', '适合把文件夹挂到手机 / 另一台电脑'],
      en: ['No external program to install: just start it', 'Shared folder, port and credentials configured here', 'Handy for mounting a folder on a phone or another PC'],
    },
    tip: { zh: '这是 QuickDock 自带能力，没有单独的官网可查，配置说明看本页即可。', en: 'This is built into QuickDock, so there is no separate upstream site — the config is here.' },
  },
  mcp: {
    home: 'https://modelcontextprotocol.io/',
    zh: 'QuickDock 自带的 MCP 服务端，把本地能力（文件、命令、环境、下载）以工具形式开放给 AI 客户端。',
    en: "QuickDock's built-in MCP server, exposing local capabilities (files, commands, environments, downloads) as tools to AI clients.",
    points: {
      zh: ['随应用启动，默认监听 127.0.0.1:9230', '把 AI 客户端的 MCP 地址填成 http://127.0.0.1:9230/mcp', '工具涵盖文件、命令、环境、下载等本地能力'],
      en: ['Starts with the app, listening on 127.0.0.1:9230 by default', 'Point AI clients at http://127.0.0.1:9230/mcp', 'Tools cover files, commands, environments, downloads and more'],
    },
    tip: { zh: '只监听本机；要让别的机器连，需要先处理认证与防火墙。', en: 'Localhost only: exposing it to other machines needs auth and firewall rules first.' },
  },
}
