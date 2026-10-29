package mcp

// 环境管理域工具：运行时列表/状态/日志/版本、服务启停，以及依赖 Everything 的文件检索。
//
// 本层只做参数校验与错误整形，实际能力全部在 internal/env 的 Manager 上。

import (
	"context"
	"errors"
	"strings"

	envengine "quickdock/internal/env"
	mcpsrv "quickdock/internal/mcp"
)

func registerEnvTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	// ---- 只读 ----

	r.add(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		list := s.App.Env.List()
		out := make([]map[string]any, 0, len(list))
		for _, r := range list {
			versions := make([]string, 0, len(r.Installed))
			for _, ins := range r.Installed {
				versions = append(versions, ins.Version)
			}
			row := map[string]any{
				"id":         r.ID,
				"name":       r.Name,
				"group":      r.Group,
				"hasService": r.HasService,
				"enabled":    r.Enabled,
				"versions":   versions,
			}
			if r.HasService && len(versions) > 0 {
				if st, err := s.App.Env.Status(envengine.Runtime(r.ID), versions[0]); err == nil {
					row["running"] = st.Running
					row["ports"] = st.Ports
				}
			}
			out = append(out, row)
		}
		return out, nil
	}, mcpsrv.LevelRead, "env_list", "列出所有受管运行时：已装版本、是否支持服务启停、当前运行状态与端口", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "runtime")
		if id == "" {
			return nil, errors.New("缺少参数 runtime")
		}
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		st, err := s.App.Env.Status(envengine.Runtime(id), pickVersion(s.App.Env, id, mcpsrv.Arg(args, "version")))
		if err != nil {
			return nil, err
		}
		return st, nil
	}, mcpsrv.LevelRead, "env_status", "查询某运行时的运行状态（是否运行、PID、端口、版本）", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id，如 node / php / redis / nginx / mcp"), "version": str("版本号，省略则用已装的第一个版本")})

	r.add(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		id := mcpsrv.Arg(args, "runtime")
		log, err := s.App.Env.LogGet(envengine.Runtime(id), pickVersion(s.App.Env, id, mcpsrv.Arg(args, "version")))
		if err != nil {
			return nil, err
		}
		if log == "" {
			return "（无日志）", nil
		}
		return log, nil
	}, mcpsrv.LevelRead, "env_log", "读取某运行时的服务日志", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id"), "version": str("版本号，省略则用已装的第一个版本")})

	r.add(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		return s.App.Env.AvailableVersions(envengine.Runtime(mcpsrv.Arg(args, "runtime"))), nil
	}, mcpsrv.LevelRead, "env_versions", "列出某运行时可下载的版本（需联网，慢）", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id")})

	// ---- Everything 文件检索（只读）----
	// 不做「启动期绝对可用性门禁」：handler 内动态校验 EverythingRuntime().IsAvailable()，
	// 这样即使用户在 QuickDock 启动后才装好/启动 Everything，也无需重启 MCP 即可用，
	// 且未就绪时给出明确可执行的错误，而不是默默返回一个永远为空的工具。
	r.add(func(args map[string]any) (any, error) {
		q := strings.TrimSpace(mcpsrv.Arg(args, "query"))
		if q == "" {
			return nil, errors.New("缺少参数 query（检索关键词）")
		}
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		ev := s.App.Env.EverythingRuntime()
		if ev == nil || !ev.IsAvailable() {
			return nil, errors.New("文件搜索不可用：请确认已安装 Everything、es.exe 在位且 Everything 正在运行")
		}
		limit := mcpsrv.ArgInt(args, "limit", 30)
		if limit > 200 {
			limit = 200
		}
		sortBy := mcpsrv.Arg(args, "sort")
		results, err := ev.Search(context.Background(), q, limit, sortBy)
		if err != nil {
			return nil, err
		}
		if results == nil {
			results = []envengine.FileSearchResult{}
		}
		files := make([]map[string]any, 0, len(results))
		for _, r := range results {
			files = append(files, map[string]any{
				"path":     r.Path,
				"name":     r.Name,
				"dir":      r.Dir,
				"modified": r.Modified,
			})
		}
		return map[string]any{"query": q, "count": len(files), "files": files}, nil
	}, mcpsrv.LevelRead, "file_search", "通过 Everything (es.exe) 在本地全盘检索文件，返回结构化文件列表（path/name/dir/modified）。仅在 Everything 已安装、es.exe 在位且正在运行时可用；支持 Everything 检索语法（空格为与、| 为或、*.ext 按扩展名）", []string{"query"},
		map[string]any{
			"query": str("检索关键词，支持 Everything 语法（空格为与、| 为或、*.ext 按扩展名等）"),
			"limit": intp("返回条数，默认 30，最大 200"),
			"sort":  str("排序字段：name/path/size/date_modified/date_created，默认 name"),
		})

	// ---- 低危写 ----

	r.add(func(args map[string]any) (any, error) {
		return s.envStart(mcpsrv.Arg(args, "runtime"), mcpsrv.Arg(args, "version"))
	}, mcpsrv.LevelWrite, "env_start", "启动某运行时的服务（如 nginx / redis / mysql / mcp）。已是运行状态会直接返回成功", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id"), "version": str("要启动的版本，省略则用已装的第一个版本")})

	r.add(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		id := mcpsrv.Arg(args, "runtime")
		if err := s.App.Env.Stop(envengine.Runtime(id), pickVersion(s.App.Env, id, mcpsrv.Arg(args, "version"))); err != nil {
			return nil, err
		}
		return "已停止 " + id, nil
	}, mcpsrv.LevelWrite, "env_stop", "停止某运行时的服务", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id"), "version": str("版本号，省略则用已装的第一个版本")})

	r.add(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		id := mcpsrv.Arg(args, "runtime")
		if err := s.App.Env.Restart(envengine.Runtime(id), pickVersion(s.App.Env, id, mcpsrv.Arg(args, "version")), nil); err != nil {
			return nil, err
		}
		return "已重启 " + id, nil
	}, mcpsrv.LevelWrite, "env_restart", "重启某运行时的服务（改配置后常用）", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id"), "version": str("版本号，省略则用已装的第一个版本")})
}
