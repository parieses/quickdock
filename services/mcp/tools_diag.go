package mcp

// 诊断域工具：日志文件与崩溃记录的列举/读取，供 AI 做对话式排障。

import (
	"errors"

	mcpsrv "quickdock/internal/mcp"
)

func registerDiagTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Diag.ListLogFiles())
	}, mcpsrv.LevelRead, "log_list", "列出应用日志文件（含 crash/ 子目录的崩溃记录），返回文件名、大小、修改时间", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		name := mcpsrv.Arg(args, "name")
		if name == "" {
			return nil, errors.New("缺少参数 name（用 log_list 获取）")
		}
		return unwrap(s.Diag.ReadLogFile(name, mcpsrv.ArgInt(args, "tail", 0)))
	}, mcpsrv.LevelRead, "log_read", "读取日志文件内容；tail>0 只返回末尾 N 行，适合「给我最后 200 行」式排障", []string{"name"},
		map[string]any{
			"name": str("日志文件名，如 quickdock-2026-09-09.log 或 crash/panic-xxx.log"),
			"tail": intp("只取末尾 N 行，0 表示全文"),
		})

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Diag.ListCrashFiles())
	}, mcpsrv.LevelRead, "crash_list", "列出崩溃/异常记录（Go panic 与前端 JS 异常），新的在前", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		name := mcpsrv.Arg(args, "name")
		if name == "" {
			return nil, errors.New("缺少参数 name（用 crash_list 获取）")
		}
		return unwrap(s.Diag.ReadCrashFile(name))
	}, mcpsrv.LevelRead, "crash_read", "读取单个崩溃文件内容（供 AI 分析 panic/JS 异常栈）", []string{"name"},
		map[string]any{"name": str("崩溃文件名")})
}
