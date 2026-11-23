package mcp

// 脚本任务域工具：看脚本、看文件、读日志、建脚本、改文件、装依赖、跑一次。
//
// 边界（和 tools_schedule.go 不给 command 建写入口是同一条理由，但这里放宽了一档）：
//   - 只读四件套（script_list / script_files / script_read_file / script_runs /
//     script_log）随域等级放开，「我那个签到脚本昨天跑成功了吗」这类问题模型能自己查；
//   - 写（script_create / script_write_file / script_install_deps / script_run）
//     一律 LevelWrite：每次弹审批卡，用户点头才落盘。写文件按**脚本 id** 记
//     「本次会话都允许」，因为改一个脚本往往要连着写几个文件（代码 + 依赖文件），
//     按工具名记会变成写一次弹一次；
//   - script_run 仍要求用户在脚本设置里勾选「允许 MCP 调用」——那是用户对某个
//     脚本的长期授权，不能被一次「允许」绕过。script_create 可以一并把这个勾
//     打开（allowRun），但审批卡上会写明，等于用户建的时候就知情；
//   - 没有删除/改名：删脚本是不可逆的，让用户自己去页面上点。

import (
	"errors"
	"fmt"
	"strings"

	"quickdock/internal/db"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/services/script"
)

func registerScriptTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		res := s.Script.ListScripts()
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		list, _ := res.Data.([]db.Script)
		out := make([]map[string]any, 0, len(list))
		for _, sc := range list {
			out = append(out, map[string]any{
				"id":             sc.ID,
				"name":           sc.Name,
				"lang":           sc.Lang,
				"entry":          sc.Entry,
				"description":    mcpsrv.TruncateRunes(sc.Description, 200),
				"tags":           sc.Tags,
				"runCount":       sc.RunCount,
				"failCount":      sc.FailCount,
				"lastRun":        sc.LastRun,
				"lastStatus":     sc.LastStatus,
				"lastDurationMs": sc.LastDurationMs,
				// 模型据此判断 script_run 会不会被拒，省得它先撞一次审批卡再说。
				"mcpAllowed": sc.MCPAllowed,
			})
		}
		return map[string]any{"count": len(out), "scripts": out}, nil
	}, mcpsrv.LevelRead, "script_list",
		"列出本机脚本任务（id/名称/语言/入口/标签/累计运行与失败次数/上次运行状态与耗时/是否允许 MCP 调用）。用户问「我有哪些脚本」「某个脚本上次跑成功没」时先调它拿准确 id，不要靠猜",
		nil, nil)

	// 建脚本：AI 直接落代码，并顺带处理依赖文件。
	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		name := strings.TrimSpace(mcpsrv.Arg(args, "name"))
		if name == "" {
			return nil, errors.New("缺少参数 name（脚本名，一句人话，如「京东签到」）")
		}
		if len([]rune(name)) > 80 {
			return nil, errors.New("name 太长（上限 80 字）")
		}
		lang, err := parseScriptLang(mcpsrv.Arg(args, "lang"))
		if err != nil {
			return nil, err
		}
		files, err := argFileMap(args)
		if err != nil {
			return nil, err
		}
		code := mcpsrv.Arg(args, "code")
		entry := strings.TrimSpace(mcpsrv.Arg(args, "entry"))
		if entry == "" && code != "" {
			entry = db.DefaultScriptEntry(lang)
		}
		if entry != "" && code != "" {
			// code 是「入口正文」的简写：同名的 files 项被它覆盖，避免模型两处
			// 写了两份不一样的代码，最后跑的是它没预料的那份。
			files[entry] = code
		}
		if len(files) == 0 {
			return nil, errors.New("code 与 files 至少要给一个：code 是入口文件正文，files 是 {相对路径: 内容}")
		}
		in := script.ScriptInput{
			Name:        name,
			Description: strings.TrimSpace(mcpsrv.Arg(args, "description")),
			Lang:        lang,
			Entry:       entry,
			Tags:        strings.TrimSpace(mcpsrv.Arg(args, "tags")),
			TimeoutSec:  mcpsrv.ArgInt(args, "timeoutSec", 0),
			Concurrency: 1,
			// 默认开着：AI 写出来的脚本十有八九要 import 第三方包，
			// 关掉的话第一次运行会直接 ModuleNotFoundError，用户还得回来点一次安装。
			// custom 除外（与页面新建时的口径一致）：它连解释器都得自己指定，
			// 自动装依赖极易装到错误的环境里。
			AutoInstall: lang != "custom",
			MCPAllowed:  mcpsrv.ArgBool(args, "allowRun"),
		}
		if v, ok := args["autoInstall"].(bool); ok {
			in.AutoInstall = v
		}
		res := s.Script.CreateScriptWithFiles(in, files)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		data, err := unwrap(res)
		if err != nil {
			return nil, err
		}
		if m, ok := data.(map[string]any); ok {
			m["next"] = scriptCreateNext(m, in.MCPAllowed)
		}
		return data, nil
	}, mcpsrv.LevelWrite, "script_create",
		"新建一个脚本任务并把代码写进去（用户不在脚本页也能建）。lang 只能是 python/javascript/shell/go/php/custom（也认 py/js/node/golang 这类别名）。"+
			"简单脚本用 code 传入口文件正文即可；需要依赖或辅助模块时用 files，键是相对路径、值是内容，例如 {\"requirements.txt\":\"requests==2.32.3\"}、{\"package.json\":\"{\\\"dependencies\\\":{\\\"axios\\\":\\\"^1.7.0\\\"}}\"}、{\"helper.py\":\"...\"}。"+
			"依赖文件按语言选：Python=requirements.txt，Node=package.json，Go=go.mod（不传的话宿主会自动补一个），PHP=composer.json。写完后第一次运行会自动先装依赖。"+
			"allowRun=true 表示同时允许 AI 之后直接运行这个脚本（等价于用户在脚本设置里勾选「允许 MCP 调用」），不传就只能由用户手动运行。会弹审批卡",
		[]string{"name", "lang"},
		map[string]any{
			"name":        str("脚本名，简短可辨认，如「京东签到」"),
			"lang":        str("语言：python / javascript / shell / go / php / custom"),
			"code":        str("入口文件正文。简单脚本只传它就够；与 files 里同名路径冲突时以它为准"),
			"files":       fileMapSchema(),
			"entry":       str("可省。入口文件相对路径，默认按语言给（python→main.py、go→main.go…）；files 里只有 index.js 这类名字时宿主会自动选中它"),
			"description": str("可省。一句话说明这个脚本做什么"),
			"tags":        str("可省。逗号分隔的标签，如「签到,抓取」"),
			"timeoutSec":  intp("可省。单次运行超时秒数，0=不限（定时/触发类建议设，比如 600）"),
			"autoInstall": mcpsrv.Bool("可省。运行前是否自动装依赖，默认 true"),
			"allowRun":    mcpsrv.Bool("可省。是否允许 AI 之后直接运行它，默认 false。用户要求「建完跑一下」时传 true"),
		},
		withRiskNote(func(args map[string]any) string {
			note := "会在本机脚本目录写入可执行代码"
			if mcpsrv.ArgBool(args, "allowRun") {
				note += "，并把该脚本标记为「允许 AI 直接运行」——之后 AI 不必再问就能跑它"
			}
			if len(argFileMapSoft(args)) > 0 {
				note += "，同时写入依赖文件（安装依赖会执行包管理器，第三方包可能带安装脚本）"
			}
			return note + "。"
		}),
		withGrantKey(func(args map[string]any) (string, bool) {
			name := strings.TrimSpace(mcpsrv.Arg(args, "name"))
			if name == "" {
				return "", false
			}
			return "script_create:" + name, true
		}))

	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
		if id == "" {
			return nil, errors.New("缺少参数 scriptId")
		}
		res := s.Script.GetScript(id)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		data, err := unwrap(res)
		if err != nil {
			return nil, err
		}
		if m, ok := data.(map[string]any); ok {
			return map[string]any{"files": m["files"], "deps": m["deps"], "trusted": m["trusted"]}, nil
		}
		return data, nil
	}, mcpsrv.LevelRead, "script_files",
		"列出一个脚本目录里的文件（相对路径/大小/是否入口）以及依赖探测结果。改代码前先看这里，别猜文件名；要读内容用 script_read_file",
		[]string{"scriptId"},
		map[string]any{"scriptId": str("脚本 id（来自 script_list）")})

	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
		rel := strings.TrimSpace(mcpsrv.Arg(args, "path"))
		if id == "" || rel == "" {
			return nil, errors.New("缺少参数 scriptId 或 path")
		}
		res := s.Script.ReadScriptFile(id, rel)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		data, err := unwrap(res)
		if err != nil {
			return nil, err
		}
		if m, ok := data.(map[string]any); ok {
			if c, ok := m["content"].(string); ok && len(c) > 200_000 {
				// 20 万字符已经远超一个脚本的正常体量；再长就是模型不该整个读进来的东西。
				m["content"] = c[:200_000] + "\n…（内容过长，已截断；用 script_files 看大小）"
			}
		}
		return data, nil
	}, mcpsrv.LevelRead, "script_read_file",
		"读一个脚本文件的内容（相对路径来自 script_files）。要改代码先读现状，不要凭记忆重写整个文件",
		[]string{"scriptId", "path"},
		map[string]any{
			"scriptId": str("脚本 id"),
			"path":     str("文件相对路径，如 main.py、requirements.txt"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
		rel := strings.TrimSpace(mcpsrv.Arg(args, "path"))
		content := mcpsrv.Arg(args, "content")
		if id == "" || rel == "" {
			return nil, errors.New("缺少参数 scriptId 或 path")
		}
		res := s.Script.WriteScriptFile(id, rel, content)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		return map[string]any{"scriptId": id, "path": rel, "bytes": len(content)}, nil
	}, mcpsrv.LevelWrite, "script_write_file",
		"新建或覆盖脚本目录里的一个文件（内容整体替换，不是追加）。改代码、补 requirements.txt / package.json / go.mod / composer.json 都用它。"+
			"写完如果改的是依赖文件，运行前会自动重装依赖；要立刻装可以调 script_install_deps。会弹审批卡",
		[]string{"scriptId", "path", "content"},
		map[string]any{
			"scriptId": str("脚本 id"),
			"path":     str("文件相对路径，如 main.py、requirements.txt"),
			"content":  str("完整文件内容（会被整体写入，不是追加）"),
		},
		withRiskNote(func(args map[string]any) string {
			p := strings.TrimSpace(mcpsrv.Arg(args, "path"))
			return "会覆盖脚本「" + p + "」的现有内容（若已存在），旧内容不保留。"
		}),
		// 按脚本 id 记：改一个脚本通常要连写几个文件，按工具名记会写一次弹一次。
		withGrantKey(func(args map[string]any) (string, bool) {
			id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
			if id == "" {
				return "", false
			}
			return "script_write:" + id, true
		}))

	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
		if id == "" {
			return nil, errors.New("缺少参数 scriptId")
		}
		res := s.Script.InstallDeps(id)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		data, err := unwrap(res)
		if err != nil {
			return nil, err
		}
		if m, ok := data.(map[string]any); ok {
			m["next"] = "依赖安装在后台跑：过一会儿用 script_runs(scriptId) 看这一轮状态，失败时用 script_log(runId) 读完整输出"
		}
		return data, nil
	}, mcpsrv.LevelWrite, "script_install_deps",
		"给脚本安装依赖（异步，立刻返回 runId）。装什么由目录里的依赖文件决定：requirements.txt→pip、package.json→npm、go.mod→go mod tidy、composer.json→composer install。"+
			"第一次运行脚本时会自动装一遍，所以只有在「想先装好再跑」或「改了依赖文件想立刻重装」时才需要单独调它。会弹审批卡",
		[]string{"scriptId"},
		map[string]any{"scriptId": str("脚本 id")},
		withRiskNote(func(args map[string]any) string {
			id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
			file := ""
			if id != "" {
				file = s.Script.DepsFileName(id)
			}
			cmd := map[string]string{
				"requirements.txt": "pip install -r requirements.txt",
				"package.json":     "npm install",
				"go.mod":           "go mod tidy",
				"composer.json":    "composer install",
			}[file]
			if cmd == "" {
				return "会在本机执行包管理器安装依赖（第三方包的安装脚本会以当前用户身份运行）。"
			}
			return "会在本机执行 `" + cmd + "`：第三方包自带的安装脚本（postinstall / setup.py）会以当前用户身份运行。"
		}),
		withGrantKey(func(args map[string]any) (string, bool) {
			id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
			if id == "" {
				return "", false
			}
			return "script_deps:" + id, true
		}))

	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
		if id == "" {
			return nil, errors.New("缺少参数 scriptId")
		}
		limit := mcpsrv.ArgInt(args, "limit", 10)
		if limit <= 0 || limit > 50 {
			limit = 10
		}
		res := s.Script.ListScriptRuns(id, limit)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		runs, _ := res.Data.([]db.ScriptRun)
		out := make([]map[string]any, 0, len(runs))
		for _, rn := range runs {
			out = append(out, map[string]any{
				"runId":      rn.ID,
				"trigger":    rn.Trigger,
				"status":     rn.Status,
				"exitCode":   rn.ExitCode,
				"durationMs": rn.DurationMs,
				"startedAt":  rn.StartedAt,
				"finishedAt": rn.FinishedAt,
				// 摘要只留末尾几百字：失败原因通常在最后几行。
				"summary": mcpsrv.TruncateRunes(rn.Summary, 400),
			})
		}
		return map[string]any{"count": len(out), "runs": out}, nil
	}, mcpsrv.LevelRead, "script_runs",
		"列出某个脚本最近的运行记录（时间/触发来源/状态/退出码/耗时/结果摘要末尾）。先用 script_list 拿 scriptId",
		[]string{"scriptId"},
		map[string]any{
			"scriptId": str("脚本 id（来自 script_list）"),
			"limit":    intp("可省。返回条数，1-50，默认 10"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		runID := strings.TrimSpace(mcpsrv.Arg(args, "runId"))
		if runID == "" {
			return nil, errors.New("缺少参数 runId")
		}
		maxBytes := mcpsrv.ArgInt(args, "maxBytes", 0)
		res := s.Script.GetScriptRunLog(runID, int64(maxBytes))
		return unwrap(res)
	}, mcpsrv.LevelRead, "script_log",
		"读某次运行的完整输出（stdout/stderr 合并，从末尾截断）。排查「脚本为什么失败」时先看 script_runs 里那一条的 summary，不够再调它拿全文。runId 来自 script_runs",
		[]string{"runId"},
		map[string]any{
			"runId":    str("运行 id（来自 script_runs）"),
			"maxBytes": intp("可省。最多读回多少字节，默认全部（上限 512KB）"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Script == nil {
			return nil, errors.New("脚本服务未就绪")
		}
		id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
		if id == "" {
			return nil, errors.New("缺少参数 scriptId")
		}
		sc, err := s.App.DB.GetScript(id)
		if err != nil {
			return nil, errors.New("脚本不存在：" + id)
		}
		// 入口开关由用户在脚本设置里打开。这里再挡一次：审批卡解决的是
		// 「这一次要不要跑」，而 mcp_allowed 解决的是「这个脚本能不能被 AI 调」，
		// 后者是用户对某个脚本的授权决定，不能被一次点「允许」绕过。
		if !sc.MCPAllowed {
			return nil, errors.New("脚本「" + sc.Name + "」未开启 MCP 调用，请在脚本页的脚本设置里勾选后才能由 AI 触发")
		}
		res := s.Script.RunScriptWithTrigger(id, mcpsrv.Arg(args, "args"), "mcp")
		return unwrap(res)
	}, mcpsrv.LevelWrite, "script_run",
		"运行一个脚本（异步，立刻返回 runId；之后用 script_runs / script_log 看结果）。只能跑用户在脚本设置里明确允许 MCP 调用的脚本，且每次都会弹审批卡。用户说「跑一下那个脚本」时用；不确定是哪个脚本时先 script_list",
		[]string{"scriptId"},
		map[string]any{
			"scriptId": str("脚本 id（来自 script_list，且必须 mcpAllowed=true）"),
			"args":     str("可省。命令行参数，JSON 字符串数组，如 [\"--days\",\"7\"]；不传用脚本默认参数"),
		},
		// 「本次会话都允许」按脚本名记忆：用户批准的是「跑这个脚本」，
		// 不是「以后随便跑任何脚本」。
		withRiskNote(func(args map[string]any) string {
			id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
			name := id
			if sc, err := s.App.DB.GetScript(id); err == nil {
				name = "「" + sc.Name + "」"
			}
			return "会以当前用户身份在本机执行脚本" + name + "的代码——脚本能做什么它就做什么（读写文件、访问网络、起子进程），没有沙箱。"
		}),
		withGrantKey(func(args map[string]any) (string, bool) {
			id := strings.TrimSpace(mcpsrv.Arg(args, "scriptId"))
			if id == "" {
				return "", false
			}
			return "script_run:" + id, true
		}))
}

// ---------- 参数解析 ----------

// parseScriptLang 把模型常见的语言别名收敛到脚本表的枚举值。
//
// 不认识的直接报错而不是静默兜底成 python：模型传了 "ts"（脚本引擎没有 TS）
// 而我们默默建出一个 python 脚本，是最难从现象反推原因的一类失败。
func parseScriptLang(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "python", "py", "python3", "py3":
		return "python", nil
	case "javascript", "js", "node", "nodejs", "node.js", "mjs":
		return "javascript", nil
	case "shell", "sh", "bash", "zsh":
		return "shell", nil
	case "go", "golang":
		return "go", nil
	case "php", "php7", "php8":
		return "php", nil
	case "custom":
		return "custom", nil
	}
	return "", fmt.Errorf("不支持的语言 %q：只能是 python / javascript / shell / go / php / custom（TypeScript 请写成 javascript 或先自己编译）", raw)
}

// fileMapSchema files 参数的 schema：{相对路径: 文件内容}。
func fileMapSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          "要一起写入的文件：键是相对路径，值是完整内容（依赖文件、辅助模块都放这里）",
		"additionalProperties": map[string]any{"type": "string"},
	}
}

// argFileMap 解析 files 参数。2MB 总量上限：工具参数要进上下文与审批卡，
// 再大就不是「写脚本」而是「传文件」了。
func argFileMap(args map[string]any) (map[string]string, error) {
	raw, ok := args["files"]
	if !ok || raw == nil {
		return map[string]string{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("files 必须是 {相对路径: 文件内容} 的对象")
	}
	out := make(map[string]string, len(obj))
	total := 0
	for p, v := range obj {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("files[%q] 必须是字符串内容", p)
		}
		key := strings.TrimSpace(p)
		if key == "" {
			return nil, errors.New("files 里有空路径")
		}
		total += len(s)
		out[key] = s
	}
	if total > 2<<20 {
		return nil, errors.New("files 总大小超过 2MB，请拆成几次写入或让用户用外部编辑器")
	}
	return out, nil
}

// argFileMapSoft 只给审批卡的 riskNote 用：解析失败就当没有 files。
func argFileMapSoft(args map[string]any) map[string]string {
	m, _ := argFileMap(args)
	return m
}

// scriptCreateNext 给模型一句「接下来该干什么」。
//
// 为什么由宿主写而不是让模型自己推：依赖装没装、能不能直接跑，取决于
// allowRun 与目录里有没有依赖文件，这两件事模型看不到全貌；写清楚能省掉
// 一整轮「建完 → 试着跑 → 被拒 → 回头问用户」。
func scriptCreateNext(m map[string]any, allowRun bool) string {
	var b strings.Builder
	b.WriteString("脚本已创建。")
	if df, _ := m["depsFileName"].(string); df != "" {
		b.WriteString("检测到依赖文件 " + df + "，首次运行会自动先安装依赖（安装日志在同一条运行记录里，装失败会中止运行）。")
	}
	if gm, _ := m["goMod"].(bool); gm {
		b.WriteString("已自动补 go.mod，源码引入第三方包后 go mod tidy 会把它们补进去。")
	}
	if allowRun {
		b.WriteString("已允许 AI 运行它：调 script_run 验证，跑完用 script_runs / script_log 看结果。")
	} else {
		b.WriteString("未允许 AI 运行它：要在本机跑需要用户在脚本页勾选「允许 MCP 调用」，或建脚本时传 allowRun=true。")
	}
	return b.String()
}
