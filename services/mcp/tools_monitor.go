package mcp

// 监控域工具：看有哪些监控、现在的状态与近 24h 在线率、检测历史、立即检测一次。
//
// 为什么**不给**创建 / 修改 / 删除：
//   - 删监控是数据丢失（历史一起没），属于「必须由人决定」的动作；
//   - 建监控意味着让宿主代替用户向外发请求，目标由模型决定——那是 SSRF 的经典形状
//     （内网地址、云元数据端点）。已存在的监控目标是人填的，「立即检测」只是复用，
//     风险完全不同。
//
// 这一域的价值主要体现在「回答状态」：监控页有趋势图与在线率，但 AI 之前完全看不到，
// 被问「服务是不是挂了」只能回一句不知道。

import (
	"errors"
	"strings"

	"quickdock/internal/db"
	mcpsrv "quickdock/internal/mcp"
)

func registerMonitorTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	r.add(func(args map[string]any) (any, error) {
		if s.Monitor == nil {
			return nil, errors.New("监控服务未初始化")
		}
		res := s.Monitor.ListMonitors()
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		list, _ := res.Data.([]db.Monitor)
		// 在线率一次聚合查完再按 id 挂上去：逐条查是 N 次 SQL，且监控一多就明显变慢。
		stats := map[string]db.MonitorStat{}
		if sres := s.Monitor.ListMonitorStats(); sres.Code == 0 {
			if arr, ok := sres.Data.([]db.MonitorStat); ok {
				for _, st := range arr {
					stats[st.MonitorID] = st
				}
			}
		}
		onlyAbnormal := mcpsrv.ArgBool(args, "onlyAbnormal")

		out := make([]map[string]any, 0, len(list))
		for _, m := range list {
			if onlyAbnormal && m.LastStatus == "up" && m.Enabled {
				continue
			}
			row := map[string]any{
				"id":            m.ID,
				"name":          m.Name,
				"url":           m.URL,
				"method":        m.Method,
				"enabled":       m.Enabled,
				"group":         m.GroupName,
				"intervalSec":   m.IntervalSec,
				"status":        m.LastStatus, // up | down | ''（还没检测过）
				"lastCheckedAt": m.LastCheckedAt,
				"latencyMs":     m.LastLatencyMs,
				"statusCode":    m.LastStatusCode,
				"lastError":     mcpsrv.TruncateRunes(m.LastError, 200),
			}
			if st, ok := stats[m.ID]; ok {
				row["uptime24h"] = round1(st.UptimeRatio)
				row["checks24h"] = st.TotalChecks
				row["avgLatencyMs"] = st.AvgLatencyMs
				row["lastDownAt"] = st.LastDownAt
			}
			// 维护窗口与证书到期是「看起来挂了其实没事」的两个主要来源，
			// 不报出来模型会照着 lastStatus 给出错误结论。
			if m.MaintenanceUntil > 0 {
				row["maintenanceUntil"] = m.MaintenanceUntil
			}
			if m.CertExpiresAt > 0 {
				row["certExpiresAt"] = m.CertExpiresAt
			}
			out = append(out, row)
		}
		return map[string]any{"count": len(out), "monitors": out}, nil
	}, mcpsrv.LevelRead, "monitor_list",
		"列出全部网站/接口监控及其当前状态：up/down、最近检测时间、延迟、状态码、错误、近 24h 在线率与检测次数、维护窗口、证书到期。回答「服务是不是挂了」「在线率多少」用这个；只看有问题的传 onlyAbnormal=true",
		nil,
		map[string]any{
			"onlyAbnormal": mcpsrv.Bool("可选。只返回异常或已停用的（跳过 up 且启用的），默认 false 返回全部"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Monitor == nil {
			return nil, errors.New("监控服务未初始化")
		}
		id := strings.TrimSpace(mcpsrv.Arg(args, "monitorId"))
		if id == "" {
			return nil, errors.New("缺少参数 monitorId（用 monitor_list 获取）")
		}
		limit := mcpsrv.ArgInt(args, "limit", 20)
		if limit <= 0 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}
		res := s.Monitor.GetMonitorLogs(id, limit)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		logs, _ := res.Data.([]db.MonitorLog)
		out := make([]map[string]any, 0, len(logs))
		for _, lg := range logs {
			out = append(out, map[string]any{
				"checkedAt":  lg.CheckedAt,
				"status":     lg.Status,
				"statusCode": lg.StatusCode,
				"latencyMs":  lg.LatencyMs,
				"error":      mcpsrv.TruncateRunes(lg.Error, 200),
			})
		}
		return map[string]any{"monitorId": id, "count": len(out), "logs": out}, nil
	}, mcpsrv.LevelRead, "monitor_logs",
		"查看某个监控最近的逐次检测记录（时间、up/down、状态码、延迟、错误）。用于判断「是刚挂还是一直在抖」：单看最后一次的状态分不出偶发失败与持续宕机",
		[]string{"monitorId"},
		map[string]any{
			"monitorId": str("监控 ID，来自 monitor_list"),
			"limit":     intp("返回条数，默认 20，最多 100"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Monitor == nil {
			return nil, errors.New("监控服务未初始化")
		}
		id := strings.TrimSpace(mcpsrv.Arg(args, "monitorId"))
		if id == "" {
			return nil, errors.New("缺少参数 monitorId（用 monitor_list 获取）")
		}
		res := s.Monitor.CheckMonitorNow(id)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		return unwrap(res)
	}, mcpsrv.LevelWrite, "monitor_check_now",
		"立即对某个已有监控发起一次检测（等价于监控页的「立即检测」），不影响定时检测计划。返回本次结论；检测失败时返回失败原因。只能检测用户已创建的监控目标，不能新建监控",
		[]string{"monitorId"},
		map[string]any{"monitorId": str("监控 ID，来自 monitor_list")})
}

// round1 保留一位小数：在线率 99.9876543% 对判断毫无帮助，只会占上下文。
func round1(v float64) float64 {
	if v < 0 {
		return 0
	}
	return float64(int(v*10+0.5)) / 10
}
