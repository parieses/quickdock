package mcp

// 高危域工具（LevelRisk）：杀进程、执行系统指令。
//
// 环境管理页只提供「只读 / 只读+低危写」两档，故这两条当前恒不开放：
// tools/list 里看不到，Call 也会被等级门拒绝。保留注册是为了将来放开上限时
// 不必改注册代码，但描述必须如实——写成「去面板开启高危等级」会引导模型
// 反复尝试一个根本不存在的开关。
//
// 单独成文件是为了让「新增高危工具」显式多一步：你得主动打开这个文件，
// 而不是在正常的域文件里顺手加一条。

import (
	"errors"

	mcpsrv "quickdock/internal/mcp"
)

func registerRiskTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	r.add(func(args map[string]any) (any, error) {
		pid := mcpsrv.ArgInt(args, "pid", 0)
		if pid <= 0 {
			return nil, errors.New("缺少或无效的参数 pid")
		}
		return unwrap(s.Port.KillProcess(pid))
	}, mcpsrv.LevelRisk, "process_kill", "结束指定 PID 的进程（高危：误杀会导致数据丢失）。当前版本不对外暴露，本工具不可用", []string{"pid"},
		map[string]any{"pid": intp("要结束的进程 PID")})

	r.add(func(args map[string]any) (any, error) {
		cmd := mcpsrv.Arg(args, "cmd")
		if cmd == "" {
			return nil, errors.New("缺少参数 cmd")
		}
		return unwrap(s.System.ExecuteSystemCommand(cmd))
	}, mcpsrv.LevelRisk, "system_command", "执行系统指令（lock/shutdown/restart/sleep/emptytrash，高危）。当前版本不对外暴露，本工具不可用", []string{"cmd"},
		map[string]any{"cmd": str("系统指令，如 shutdown / restart / lock / sleep / emptytrash")})
}
