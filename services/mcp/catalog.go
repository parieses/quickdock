package mcp

// 能力域的唯一定义处。
//
// 这里放三样东西，都只写一遍：
//   - 面板分组与顺序（LabelKey / Order / Summary）
//   - 注入 system 的工具说明（Hint / WriteHint）
//   - 场景开放程度与对外可见性（Scenes / External）
//
// 以前这三样散在五个地方（Category 常量、i18n、前端 MCP_CATEGORY_ORDER、
// toolHint 常量、服务层白名单），加一个域必漏一处。现在加域 = 在下面加一条。
//
// 写 Hint 的纪律：**只写该段必然挂载的工具名**。写类工具写进 WriteHint——
// 只读档不挂它们，提示词却讲「用 file_write 新建」的话，模型会去调一个不存在的函数，
// 然后对着报错反复重试（见 ai_tools.go 里 toolHintExhausted 的教训）。

import (
	mcpsrv "quickdock/internal/mcp"
)

// domainOrder 面板顺序：agent 域在前（它们带「仅内置 AI」标记，聚在一起好读），
// 其余按「宿主 → 环境 → 内容 → 自动化 → 扩展 → 高危」排。
const (
	orderWeb       = 10
	orderCode      = 20
	orderApp       = 30
	orderEnv       = 40
	orderFile      = 50
	orderItem      = 60
	orderNote      = 70
	orderKnowledge = 80
	orderDownload  = 90
	orderSSH       = 95
	orderTodo      = 100
	orderClipboard = 110
	orderDiag      = 120
	orderSchedule  = 130
	orderMonitor   = 140
	orderPlugin    = 150
	orderRisk      = 160
	orderExternal  = 900
)

// registerDomains 注册全部能力域。必须在注册工具之前调用：装配要用它判场景。
func registerDomains() {
	for _, d := range domainCatalog() {
		mcpsrv.DefineDomain(d)
	}
}

// 场景名与 internal/mcp 保持同源，避免「前端叫 assistant、后端叫 default」这种漂移。
const (
	sceneAssistant = mcpsrv.SceneAssistant
	sceneReadOnly  = mcpsrv.SceneReadOnly
)

func domainCatalog() []mcpsrv.Domain {
	// 默认只有「助手档」：全开。只读档由 ResetReadOnly 在装配时按域声明收窄，
	// 这里显式写出来是为了让「这个域只读档开不开」变成一条能看见的决定，
	// 而不是靠 Scene 里没声明就默认全开。
	readScenes := func() map[string]mcpsrv.Access {
		return map[string]mcpsrv.Access{
			sceneAssistant: mcpsrv.AccessFull,
			sceneReadOnly:  mcpsrv.AccessRead,
		}
	}
	onlyRead := func() map[string]mcpsrv.Access {
		return map[string]mcpsrv.Access{
			sceneAssistant: mcpsrv.AccessRead,
			sceneReadOnly:  mcpsrv.AccessRead,
		}
	}

	return []mcpsrv.Domain{
		{
			ID: "web", LabelKey: "mcpCatWeb", Order: orderWeb, External: false,
			Summary: "抓网页、取链接、看响应头（仅内置 AI）",
			Hint: "**网页**：web_fetch 打开网址读正文，web_links 取页面里的链接，web_head 只看状态码与响应头。" +
				"查文档、看报错页、确认链接里写了什么时用它们，不要凭印象编造网页内容。" +
				"打不开（403 / 超时 / 需要登录 / 只返回脚本）时如实说明并给用户链接，不要假装读到，也不要反复重试同一地址。\n",
			Scenes: readScenes(),
		},
		{
			ID: "code", LabelKey: "mcpCatCode", Order: orderCode, External: false,
			Summary: "目录、文件、git、命令与后台任务（仅内置 AI）",
			Hint: "**代码**：dir_list 看目录结构，file_read 读文件（行号在 content 里），file_find 按文件名找，" +
				"file_grep 按内容搜，" +
				"git_status / git_diff 看改动与差异，check_workdir 一次问清「这是什么项目、该怎么跑」，" +
				"job_status / job_output 看后台任务，project_memory 读写本工作区的记忆。" +
				"改已有文件的流程永远是：先看 → 读清原文 → 精确替换 → 看 diff 确认。\n",
			WriteHint: "file_write 新建文件、file_edit 精确替换、run_command 跑命令。" +
				"**没有工作目录时写操作会被拒绝**：这时不要换个路径重试，让用户把任务建在一个有效的空间里。" +
				"run_command 只做必须走命令行的事（装依赖、跑测试、构建）；高危命令会被拒绝，那就把命令写给用户自己执行。" +
				"要产出文件就用 file_write，不要用 echo / Set-Content 在命令里造文件。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategoryApp, LabelKey: "mcpCatApp", Order: orderApp, External: true,
			Summary: "软件信息、监听端口、机器人通知",
			Hint: "**软件与端口**：app_info 看版本与数据目录，port_list 看本机谁在监听哪个端口。credentials_status 看密码库状态（不含明文），mail_codes 取最近收到的邮箱验证码、mail_read 读缓存邮件正文。" +
				"password_generate 只生成新密码，不会改密码库里的条目（只读档也能用，属于只读工具）。\n",
			WriteHint: "robot_notify 会**真的**把消息发到用户配置的机器人（企业微信 / 钉钉 / 飞书 / Telegram 等），" +
				"不是草稿。发之前确认内容，不要为测试而发。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategoryEnv, LabelKey: "mcpCatEnv", Order: orderEnv, External: true,
			Summary: "运行时与服务的状态、日志、启停",
			Hint: "**环境**：env_list 看装了哪些运行时/服务，env_status 看某个的当前状态，env_log 看日志，env_versions 看可安装版本。" +
				"env_port_conflict 查端口被占，env_project_versions 读项目的版本声明，env_disk_usage 看各运行时占多少空间。\n",
			WriteHint: "env_start / env_stop / env_restart 直接改变正在运行的服务——停之前说清会影响什么，" +
				"用户没让你停就不要顺手停。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategoryFile, LabelKey: "mcpCatFile", Order: orderFile, External: true,
			Summary: "文件检索、读取、按内容搜索",
			Hint: "**文件**：file_read 读文件内容（带行号），file_grep 按内容搜（正则），file_find 在目录树里按文件名找" +
				"（*.go 这类通配符或普通子串都行，不依赖任何外部程序）。" +
				"只能读当前工作目录内的文件；越界读取需要用户当场同意。\n",
			Scenes: onlyRead(),
		},
		{
			ID: mcpsrv.CategoryItem, LabelKey: "mcpCatItem", Order: orderItem, External: true,
			Summary: "快捷项、最近使用、工作空间",
			Hint: "**启动台**：workspace_list 看工作空间/场景/集合/项目，item_search 按名字找快捷项，recent_items 看最近用过什么。" +
				"要按层级浏览用 workspace_list_all → scene_list → collection_list → item_list，shelf_list 看常用路径分块。\n",
			WriteHint: "item_open 真的会在用户机器上打开那个项目（窗口 / 网页 / 命令），先确认是哪一个再打开。\n",
			Scenes:    readScenes(),
		},
		{
			ID: mcpsrv.CategoryNote, LabelKey: "mcpCatNote", Order: orderNote, External: true,
			Summary: "笔记与文件夹的读写、搜索、回收站",
			Hint: "**笔记**：note_search 按内容找，note_read 读全文，note_tags 看标签，note_revisions 看历史版本，" +
				"note_revision_read 读某个版本的正文，note_quick_read 读快捷笔记。" +
				"用户说「找一下我写过的 xx」时先搜再读，不要凭标题猜内容。\n",
			WriteHint: "note_create / note_quick 新建，note_update 改内容，note_move 换文件夹，note_delete 进回收站（可 note_restore 恢复）。" +
				"改笔记前先 note_read 看清原文；删之前告诉用户「进了回收站」。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategoryKnowledge, LabelKey: "mcpCatKnowledge", Order: orderKnowledge, External: true,
			Summary: "本地文档知识库检索（只读）",
			Hint: "**知识库**：kb_search 按内容检索用户导入的文档，kb_list 看有哪些文档，kb_libraries 看分库，" +
				"kb_status 看索引进度，kb_chunks 看某份文档切成了哪些片段。" +
				"回答里引用知识库内容时说明来自哪份文档。\n",
			Scenes: onlyRead(),
		},
		{
			ID: mcpsrv.CategoryDownload, LabelKey: "mcpCatDownload", Order: orderDownload, External: true,
			Summary: "下载器任务的状态与控制",
			Hint: "**下载**：download_list 看任务，download_status / download_global_stat / download_detail 看进度与详情。" +
				"网盘在 netdisk_status / netdisk_list_dir / netdisk_search 看状态、列目录、搜文件。\n",
			WriteHint: "download_pause / download_resume / download_remove 改变任务状态，download_add 会真的开始下载（高危档）。" +
				"删任务前确认是哪一个。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategorySSH, LabelKey: "mcpCatSSH", Order: orderSSH, External: true,
			Summary: "远程主机会话与文件（只对用户已授权的主机可见）",
			Hint: "**远程主机**：ssh_list_hosts 看用户授权过的主机，ssh_list_sessions 看已有会话，" +
				"ssh_terminal_read 读终端输出，ssh_sftp_list / ssh_sftp_read 看远程文件。" +
				"只有用户显式打开过的主机才在列表里；不要假装能连别的主机。\n",
			WriteHint: "ssh_exec 在**远程生产机**上执行命令，结果不可撤销。执行前把命令原样告诉用户，" +
				"并说明在哪个主机上跑；危险操作（重启服务、删文件）先问清楚。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategoryTodo, LabelKey: "mcpCatTodo", Order: orderTodo, External: true,
			Summary: "待办清单与完成状态",
			Hint:    "**待办**：todo_list 看清单（含状态与到期时间）。\n",
			WriteHint: "todo_create 建一条（标题必填，可一并给到期日、提醒时间、标签、重复规则）；" +
				"todo_update 只改要改的字段（其余保持原值），todo_done 标记完成。批量「都标记完成」之前先 todo_list 确认范围。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategoryClipboard, LabelKey: "mcpCatClipboard", Order: orderClipboard, External: true,
			Summary:   "剪贴板历史与写入",
			Hint:      "**剪贴板**：clipboard_recent 看最近复制过的文本/图片/文件，clipboard_search 全库搜旧内容。\n",
			WriteHint: "clipboard_copy 会覆盖用户当前剪贴板内容——先说一句再写。\n",
			Scenes:    readScenes(),
		},
		{
			ID: mcpsrv.CategoryDiag, LabelKey: "mcpCatDiag", Order: orderDiag, External: true,
			Summary: "应用日志与崩溃记录",
			Hint: "**诊断**：log_list 列日志文件，log_read 读内容，crash_list / crash_read 看崩溃记录，log_storage 看日志占多少空间。" +
				"用户报「刚才崩了 / 某功能不对」时先看这里，不要凭猜测解释。\n",
			Scenes: onlyRead(),
		},
		{
			ID: mcpsrv.CategorySchedule, LabelKey: "mcpCatSchedule", Order: orderSchedule, External: true,
			Summary: "定时任务与执行历史",
			Hint: "**定时任务**：schedule_list 看已有任务，schedule_runs 看执行历史。" +
				"用户说「以后每半小时 / 每天 xx 帮我看一次 yy」时，这是明确要一个长期任务，不是要你现在跑一次。\n",
			WriteHint: "schedule_create 建任务：提示词要把「做什么、产出什么」写全，因为跑的时候用户不在场；" +
				"没说频率就先问。只能建 action=ai 的任务（命令/应用类让用户去定时任务页建）。" +
				"schedule_run_now 立即跑一次，不影响排期；命令类任务会等到结束，AI 类会立刻返回「已开始」。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategoryMonitor, LabelKey: "mcpCatMonitor", Order: orderMonitor, External: true,
			Summary:   "网站监控状态与检测记录",
			Hint:      "**站点监控**：monitor_list 看监控项与当前状态，monitor_logs 看最近检测记录，monitor_logs_since 按时间段查。\n",
			WriteHint: "monitor_check_now 会立刻发一次真实请求并计入统计，别拿它当连通性玩具。\n",
			Scenes:    readScenes(),
		},
		{
			ID: mcpsrv.CategoryPlugin, LabelKey: "mcpCatPlugin", Order: orderPlugin, External: true,
			Summary:   "插件清单、命令与执行日志",
			Hint:      "**插件**：plugin_list 看装了哪些插件、是否启用，plugin_exec_logs 看命令执行记录。\n",
			WriteHint: "plugin_execute 在插件自己的环境里跑命令（会真的产生副作用），plugin_open 会打开插件窗口。\n",
			Scenes:    readScenes(),
		},
		{
			ID: mcpsrv.CategoryRisk, LabelKey: "mcpCatRisk", Order: orderRisk, External: true,
			Summary: "杀进程、系统命令（默认被等级挡）",
			// 这个域没有只读工具：两句都写进 WriteHint。放进 Hint 的话，只读档
			// 会因为「提示词提了一个没挂的工具」而让模型反复试 process_kill
			// （见 catalog_test.go 里那条一致性检查）。
			Hint: "",
			WriteHint: "**高危**：process_kill 杀进程，system_command 执行锁屏/关机/重启等系统命令。" +
				"这两条默认被等级挡在外面；能用也只在你确认过「确实要终止/关机」之后才调。\n",
			Scenes: readScenes(),
		},
		{
			ID: mcpsrv.CategoryExternal, LabelKey: "mcpCatExternal", Order: orderExternal, External: true,
			Summary: "外部 MCP server 转发进来的工具",
			// 外部工具没有静态说明：它们的用法就在各自的 description 里，
			// 这里写死一段「有哪些工具」会在 server 增删工具后立刻过时。
			Hint:   "",
			Scenes: readScenes(),
		},
	}
}
