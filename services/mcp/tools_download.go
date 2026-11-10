package mcp

// 下载器域工具：让 AI 能回答「现在在下载什么、下了多少、有没有失败的」，
// 并控制**已经存在**的任务。下载页有完整 UI，这一域补的是「问一句就知道」。
//
// 为什么 download_add 是 LevelRisk 而其余写操作是 LevelWrite：
//   - 与 tools_monitor.go 的边界同源：让宿主向「模型给出的地址」发请求是 SSRF
//     的经典形状（内网地址、云元数据端点），下载还额外写本机磁盘、占带宽；
//   - 已存在的 gid 是用户自己建的任务，pause / resume / remove 只是复用，
//     目标不是模型决定的，风险完全不同；
//   - 环境管理页只提供「只读 / 只读+低危写」两档，LevelRisk 当前恒不开放。
//     保留注册是为了将来放开上限时不必改注册代码，但描述必须如实写不可用，
//     否则模型会反复尝试一个不存在的开关。
//
// 刻意不提供 addTorrent / addMetalink（要传本机文件路径）、setOptions / shutdown
// （会改全局引擎行为）：这些都不是外部模型该触发的动作。

import (
	"errors"
	"strings"

	"quickdock/internal/aria2"
	mcpsrv "quickdock/internal/mcp"
)

func registerDownloadTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	guard := func() error {
		if s.Download == nil {
			return errors.New("下载服务未初始化")
		}
		return nil
	}
	requireGID := func(args map[string]any) (string, error) {
		gid := strings.TrimSpace(mcpsrv.Arg(args, "gid"))
		if gid == "" {
			return "", errors.New("缺少参数 gid（先用 download_list 取）")
		}
		return gid, nil
	}

	r.add(func(args map[string]any) (any, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		return unwrap(s.Download.DlStatus())
	}, mcpsrv.LevelRead, "download_status",
		"查看下载引擎状态：是否安装/运行、aria2 版本、连接的是本机还是远程、事件通道是否在线、默认下载目录与全局限速。问「下载器在跑吗」先看这里；running=false 时不要反复调 download_list",
		nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		data, err := unwrap(s.Download.DlList())
		if err != nil {
			return nil, err
		}
		tasks, ok := data.([]aria2.TaskView)
		if !ok {
			return nil, errors.New("下载任务列表格式异常")
		}
		status := strings.TrimSpace(mcpsrv.Arg(args, "status"))
		limit := mcpsrv.ArgInt(args, "limit", 30)
		if limit <= 0 {
			limit = 30
		}
		if limit > 100 {
			limit = 100
		}
		out := make([]map[string]any, 0, limit)
		for _, t := range tasks {
			if status != "" && t.Status != status {
				continue
			}
			out = append(out, map[string]any{
				"gid":       t.GID,
				"name":      mcpsrv.TruncateRunes(t.Name, 160),
				"status":    t.Status,
				"progress":  t.Progress,
				"total":     t.Total,
				"completed": t.Completed,
				"speed":     t.Speed,
				"dir":       t.Dir,
				"isTorrent": t.IsTorrent,
				"error":     mcpsrv.TruncateRunes(t.Error, 200),
			})
			if len(out) >= limit {
				break
			}
		}
		return map[string]any{"count": len(out), "tasks": out}, nil
	}, mcpsrv.LevelRead, "download_list",
		"列出下载任务：gid、文件名、状态、进度百分比、总/已完成字节、当前速度、保存目录、是否 BT、错误信息。按 status 过滤（active / waiting / paused / complete / error / removed），limit 默认 30 上限 100。total 与 completed 单位是字节，speed 是字节/秒",
		nil, map[string]any{
			"status": str("只看这个状态，留空列全部"),
			"limit":  intp("最多返回条数，默认 30，上限 100"),
		})

	r.add(func(args map[string]any) (any, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		gid, err := requireGID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Download.DlDetail(gid))
	}, mcpsrv.LevelRead, "download_detail",
		"查单个任务的完整详情：文件清单（可勾选）、URI 列表、BT Peers / 服务器、任务选项、Tracker 与种子信息。比 download_list 重（要发多次 RPC），只在确实需要文件级信息时用",
		[]string{"gid"}, map[string]any{"gid": str("任务 gid，用 download_list 获取")})

	r.add(func(args map[string]any) (any, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		return unwrap(s.Download.DlGlobalStat())
	}, mcpsrv.LevelRead, "download_global_stat",
		"查下载器全局统计：当前总下载/上传速度（字节/秒）、活跃数、等待数、已结束数。回答「现在总速度多少」「还有几个在跑」用这个，不必拉整个任务列表",
		nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		gid, err := requireGID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Download.DlPause(gid))
	}, mcpsrv.LevelWrite, "download_pause",
		"暂停一个正在下载或等待中的任务（保留已完成进度，可用 download_resume 恢复）。只作用于已存在的 gid，不会删除文件",
		[]string{"gid"}, map[string]any{"gid": str("任务 gid")})

	r.add(func(args map[string]any) (any, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		gid, err := requireGID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Download.DlResume(gid))
	}, mcpsrv.LevelWrite, "download_resume",
		"恢复一个已暂停的任务。对已结束（complete / error）的任务无效，那种情况应重新建任务或看 error 字段",
		[]string{"gid"}, map[string]any{"gid": str("任务 gid")})

	r.add(func(args map[string]any) (any, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		gid, err := requireGID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Download.DlRemove(gid, false))
	}, mcpsrv.LevelWrite, "download_remove",
		"移除一个任务（活跃任务取消下载，已结束任务清掉结果记录）。注意：aria2 的 remove 不会删除磁盘上已下载的文件，只移除任务本身；删除后不能再恢复进度",
		[]string{"gid"}, map[string]any{"gid": str("任务 gid")})

	// 与 tools_monitor.go 同一条边界：目标由模型决定的「向外发请求」不默认开放。
	// 保留实现，将来若提供高危档位或用户显式授权，无需再改注册代码。
	r.add(func(args map[string]any) (any, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		uri := strings.TrimSpace(mcpsrv.Arg(args, "url"))
		if uri == "" {
			return nil, errors.New("缺少参数 url")
		}
		return unwrap(s.Download.DlAdd(uri, strings.TrimSpace(mcpsrv.Arg(args, "dir")), ""))
	}, mcpsrv.LevelRisk, "download_add",
		"新建下载任务（高危）。当前版本不对外暴露，本工具不可用；请让用户在下载页手动新建",
		[]string{"url"}, map[string]any{
			"url": str("下载链接（http/https/ftp/sftp/magnet）"),
			"dir": str("保存目录，留空用默认下载目录"),
		})
}
