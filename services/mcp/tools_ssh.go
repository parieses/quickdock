package mcp

// SSH 域工具：把 QuickDock 已保存的 SSH 主机与会话，以「只读为主、受限执行为辅」
// 的方式开放给外部 AI Agent，参考 Motty MCP server 的最小集。
//
// ===== 几个项目特有的坑，改这个文件前务必先看 =====
//
//  1. 绝不把任何工具标成 LevelRisk。环境管理页只提供「只读 / 只读+低危写」两档，
//     LevelRisk 在默认 maxLvl=LevelWrite 下恒不注册，工具等同不存在
//     （services/mcp/service_test.go 的 TestPluginExecuteReachableByDefault 已断言同类问题）。
//     所以即便是 ssh_exec 也用 LevelWrite，靠「命令风险判定 + 主机级授权」两道闸兜住，
//     而不是靠等级门把它藏起来。
//
//  2. 主机级授权是硬边界：所有带 host_id 的工具（ssh_sftp_list / ssh_sftp_read /
//     ssh_exec）都必须先过 authorizedHost，只有 MCPAllowed==true 的主机可被访问。
//     MCP 客户端是外部进程——没这道闸，等于把用户保存的每一台机器（含生产机）
//     都交给了任意连上来的 Agent。
//
//  3. 审计只记命令、不记输出（Motty 规格）。命令执行前用 logger.I 落一行元信息，
//     stdout/stderr 可能夹着密钥、令牌、业务数据，绝不写进日志。项目没有独立审计
//     入口（services/mcp/tools_file.go 也是用 logger.I 记元信息），这里沿用同一做法。
//
//  4. unknown / changed host key 由 internal/sshx/knownhosts.go 的 fail-closed 保证，
//     所有实际建连都走 SshService 的 dial，本文件不碰连接、不碰指纹，也不绕过它；
//     ListHosts 只查库、不建连，因此不涉及指纹处理。
//
//  5. 本轮只做「看」与「受限命令」，刻意不注册任何写文件 / 删除 / 上传类工具，
//     描述文案也反复强调只读语义，免得模型以为能随意改远端文件。

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
)

// 参数边界：读取长度与执行超时的默认值 / 上限。
// 读文本默认 64KB、上限 256KB；执行默认 30s、上限 120s（仍在 SshService 的 600s
// 硬上限之内，这里再收紧一层，是因为外部 Agent 不该占着一条 SSH 连接跑长活）。
const (
	sshReadDefaultLimit   int64 = 64 << 10
	sshReadMaxLimit       int64 = 256 << 10
	sshExecDefaultTimeout       = 30
	sshExecMaxTimeout           = 120
)

// registerSSHTools 注册 6 个 SSH 工具。分类由 toolProviders 装配时作为
// r.category 传入（mcpsrv.CategorySSH），本文件不直接引用该常量。
func registerSSHTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str

	// ---- 只读 ----

	r.add(func(args map[string]any) (any, error) {
		hosts, err := sshHostsRaw(s)
		if err != nil {
			return nil, err
		}
		keyword := strings.ToLower(strings.TrimSpace(mcpsrv.Arg(args, "keyword")))
		out := make([]map[string]any, 0, len(hosts))
		for _, h := range hosts {
			// 只暴露显式授权给 MCP 的主机：这是「Agent 能看见哪些机器」的唯一来源。
			if !sshMCPAllowed(h) {
				continue
			}
			// 文件夹不是可连接主机，列出来只会误导模型拿它当 host_id 去连。
			if folder, _ := h["isFolder"].(bool); folder {
				continue
			}
			if keyword != "" && !sshHostMatch(h, keyword) {
				continue
			}
			// 手工投影白名单元信息：绝不把密码 / 口令 / hasPassword 之类带出去。
			out = append(out, sshHostMeta(h))
		}
		return out, nil
	}, mcpsrv.LevelRead, "ssh_list_hosts",
		"列出已保存、且开启了「允许 MCP Agent 使用」的 SSH 主机（只读元信息：id/name/host/port/user/env，绝不返回密码、私钥口令等凭据）。返回的 id 可作为其它 ssh_* 工具的 host_id；只有出现在这里的主机才能被访问。可用 keyword 按名称/地址/用户名过滤。这只是主机清单，不建立连接，也不能用来写远端文件。",
		nil,
		map[string]any{
			"keyword": str("可选。按主机名称/地址/用户名做不区分大小写的包含匹配，留空返回全部已授权主机"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.SSH == nil {
			return nil, errors.New("SSH 服务未初始化")
		}
		return unwrap(s.SSH.ListSessions())
	}, mcpsrv.LevelRead, "ssh_list_sessions",
		"列出当前在 QuickDock 中打开的 SSH 终端会话（id、所属主机 hostId、状态、终端尺寸、标签、建连时间等）。只读：不新建、不关闭会话，也不读取屏显内容；要看某条会话的画面请用 ssh_terminal_read。",
		nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if s.SSH == nil {
			return nil, errors.New("SSH 服务未初始化")
		}
		sid := strings.TrimSpace(mcpsrv.Arg(args, "session_id"))
		if sid == "" {
			return nil, errors.New("缺少参数 session_id（用 ssh_list_sessions 获取）")
		}
		return unwrap(s.SSH.Replay(sid))
	}, mcpsrv.LevelRead, "ssh_terminal_read",
		"读取某个已打开 SSH 会话的屏显回放（base64 编码的终端历史输出、是否被截断、当前状态）。只读：只回看已经产生的画面，不向会话写入按键、不执行命令。session_id 取自 ssh_list_sessions。",
		[]string{"session_id"},
		map[string]any{
			"session_id": str("会话 id，来自 ssh_list_sessions"),
		})

	r.add(func(args map[string]any) (any, error) {
		hostID := strings.TrimSpace(mcpsrv.Arg(args, "host_id"))
		if err := authorizedHost(s, hostID); err != nil {
			return nil, err
		}
		remotePath := mcpsrv.Arg(args, "path")
		if strings.TrimSpace(remotePath) == "" {
			return nil, errors.New("缺少参数 path（远端目录绝对路径）")
		}
		return unwrap(s.SSH.SftpList(hostID, remotePath))
	}, mcpsrv.LevelRead, "ssh_sftp_list",
		"列出远端主机某个目录下的文件与子目录（只读，等价于 ls）。只能访问开启了「允许 MCP Agent 使用」的主机；本轮不提供创建、删除、改名、上传或写入远端文件的能力。path 传远端绝对路径。",
		[]string{"host_id", "path"},
		map[string]any{
			"host_id": str("主机 id，来自 ssh_list_hosts（必须已开启「允许 MCP Agent 使用」）"),
			"path":    str("远端目录绝对路径，如 /var/log"),
		})

	r.add(func(args map[string]any) (any, error) {
		hostID := strings.TrimSpace(mcpsrv.Arg(args, "host_id"))
		if err := authorizedHost(s, hostID); err != nil {
			return nil, err
		}
		remotePath := mcpsrv.Arg(args, "path")
		if strings.TrimSpace(remotePath) == "" {
			return nil, errors.New("缺少参数 path（远端文件绝对路径）")
		}
		limit := int64(mcpsrv.ArgInt(args, "limit", int(sshReadDefaultLimit)))
		if limit <= 0 {
			limit = sshReadDefaultLimit
		}
		if limit > sshReadMaxLimit {
			limit = sshReadMaxLimit
		}
		return unwrap(s.SSH.SftpReadText(hostID, remotePath, limit))
	}, mcpsrv.LevelRead, "ssh_sftp_read",
		"读取远端主机上一个文本文件的前若干字节（只读，用于快速查看配置/日志）。只能访问开启了「允许 MCP Agent 使用」的主机；不提供写入或修改远端文件的能力。limit 默认 64KB、上限 256KB，按字节计。",
		[]string{"host_id", "path"},
		map[string]any{
			"host_id": str("主机 id，来自 ssh_list_hosts（必须已开启「允许 MCP Agent 使用」）"),
			"path":    str("远端文件绝对路径，如 /etc/nginx/nginx.conf"),
			"limit":   mcpsrv.Int("可选。读取字节数，默认 65536（64KB），最大 262144（256KB）"),
		})

	// ---- 低危写（非 LevelRisk，靠授权 + 风险判定兜住）----

	r.add(func(args map[string]any) (any, error) {
		hostID := strings.TrimSpace(mcpsrv.Arg(args, "host_id"))
		cmd := strings.TrimSpace(mcpsrv.Arg(args, "cmd"))
		if cmd == "" {
			return nil, errors.New("缺少参数 cmd")
		}
		// 授权先于风险判定与审计：未授权主机的命令连拿去评估、写进日志都不行。
		if err := authorizedHost(s, hostID); err != nil {
			return nil, err
		}
		if note := sshCmdRisk(cmd); note != "" {
			return nil, fmt.Errorf("命令命中中/高危规则并已拦截（%s）：请在 QuickDock 界面手动执行", note)
		}
		timeout := mcpsrv.ArgInt(args, "timeout", sshExecDefaultTimeout)
		if timeout <= 0 {
			timeout = sshExecDefaultTimeout
		}
		if timeout > sshExecMaxTimeout {
			timeout = sshExecMaxTimeout
		}
		// 审计只记「谁、在哪台机、跑了什么命令」，绝不记 Exec 返回的 stdout/stderr：
		// 输出里可能夹着密钥、令牌与业务数据，写进日志等于把它们复制一份到本地文件。
		logger.I("[mcp][ssh] host=%s tool=%s cmd=%s", hostID, "ssh_exec", cmd)
		return unwrap(s.SSH.Exec(hostID, cmd, timeout))
	}, mcpsrv.LevelWrite, "ssh_exec",
		"在开启了「允许 MCP Agent 使用」的主机上执行一条只读/低危命令（一次性非交互执行，返回 stdout/stderr/退出码）。仅限查看与诊断类命令：删除、格式化、重启/关机、下载后直接执行、覆盖系统/磁盘文件等中/高危写法会被直接拒绝，请在 QuickDock 界面手动执行；超时默认 30s、上限 120s。本工具不会写远端文件，也不提供上传/删除类操作。",
		[]string{"host_id", "cmd"},
		map[string]any{
			"host_id": str("主机 id，来自 ssh_list_hosts（必须已开启「允许 MCP Agent 使用」）"),
			"cmd":     str("要执行的一条命令，如 'df -h'、'systemctl status nginx'。只读/低危命令，中/高危会被拒绝"),
			"timeout": mcpsrv.Int("可选。超时秒数，默认 30，最大 120"),
		})
}

// sshHostsRaw 取回全部主机并统一转成 []map[string]any。
//
// 刻意走一遍 JSON，而不是把 ListHosts().Data 断言成 []db.SshHost：
//   - db.SshHost 的 Go 形态（值/指针、以及主流程新加的 MCPAllowed 字段叫什么）
//     不在本文件掌控范围内；而 json key 是对外契约（mcpAllowed / id / name ...），
//     按 key 读最稳，主流程改结构体时这里不必跟着改。
//   - 与「只投影白名单元信息、绝不回传密码」是同一套动作：后面 sshHostMeta
//     只挑 6 个键，凭据类字段根本进不了返回值。
func sshHostsRaw(s *MCPService) ([]map[string]any, error) {
	if s.SSH == nil {
		return nil, errors.New("SSH 服务未初始化")
	}
	data, err := unwrap(s.SSH.ListHosts())
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("SSH 主机列表无法序列化: %w", err)
	}
	var hosts []map[string]any
	if err := json.Unmarshal(raw, &hosts); err != nil {
		return nil, fmt.Errorf("SSH 主机列表格式异常: %w", err)
	}
	return hosts, nil
}

// sshMCPAllowed 读主机上的 MCP 授权开关（json: mcpAllowed）。
// 字段缺失 / 类型不对一律按「未授权」处理——fail-closed，不能因为读不到就放行。
func sshMCPAllowed(h map[string]any) bool {
	v, _ := h["mcpAllowed"].(bool)
	return v
}

// sshHostMeta 对外只暴露这 6 个元信息键，任何凭据相关字段都不投影。
func sshHostMeta(h map[string]any) map[string]any {
	return map[string]any{
		"id":   h["id"],
		"name": h["name"],
		"host": h["host"],
		"port": h["port"],
		"user": h["user"],
		"env":  h["env"],
	}
}

// sshHostMatch keyword 对名称/地址/用户名/id 做包含匹配（调用方已 lowercase）。
func sshHostMatch(h map[string]any, keyword string) bool {
	for _, k := range []string{"name", "host", "user", "id"} {
		if v, ok := h[k].(string); ok && strings.Contains(strings.ToLower(v), keyword) {
			return true
		}
	}
	return false
}

// authorizedHost 主机级授权闸门：只有 MCPAllowed==true 的主机允许被 MCP 工具访问。
//
// 抽成私有 helper 而不是每个工具各写一遍：ssh_sftp_list / ssh_sftp_read / ssh_exec
// 都带 host_id，三处漏一处就等于给未授权主机开了口子。统一按 id 在全量主机里查找，
// 区分「找到了但没授权」与「压根不存在」，都给出可操作的提示（去主机设置开启
// 「允许 MCP Agent 使用」），而不是含糊的一句「无权限」。
//
// 注意授权是「按主机」而非「按工具」：哪怕只是列一次目录、读一段文本，
// 未授权主机也不该被外部 Agent 触达，所以只读 SFTP 同样要过这道闸。
func authorizedHost(s *MCPService, hostID string) error {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return errors.New("缺少参数 host_id（用 ssh_list_hosts 获取）")
	}
	hosts, err := sshHostsRaw(s)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if id, _ := h["id"].(string); id != hostID {
			continue
		}
		if !sshMCPAllowed(h) {
			return fmt.Errorf("主机 %s 未允许 MCP Agent 使用；请到 QuickDock 的主机设置里开启「允许 MCP Agent 使用」后再试", hostID)
		}
		return nil
	}
	return fmt.Errorf("找不到主机 %s（用 ssh_list_hosts 确认 id；只有开启了「允许 MCP Agent 使用」的主机才会出现在列表里）", hostID)
}

// sshExecDenyPatterns MCP 执行门禁的黑名单：命中任意一条即拒绝执行。
//
// === 为什么没有强行复用 services/sshx/ai.go 的 sshDangerRule ===
//  1. sshDangerRule 是 sshx 包的私有变量（小写），services/mcp 跨包引用不到；
//     要复用就得改 sshx 把它导出，而本任务只允许写本文件。
//  2. 那张表的用途是给 AI 生成的命令做「卡片标红 / 附风险说明」（配合
//     aiutil.ExtractCommands / ExtractSegments），其 aiutil.RiskRules.Eval 返回的是
//     level+note 供界面展示，并非「执行前 fail-closed」的门禁；表本身也自述是
//     「有限收录」，匹配不到不等于安全。把它当成执行许可会放开展示层没标红的写法。
//  3. MCP 这条路径要的是宁可错杀的硬拦截，且规则应与本文件的工具边界就地维护。
//
// 因此这里自维护一份小而明确的高危写法黑名单，覆盖 rm -rf、mkfs、dd if=、
// shutdown/reboot、chmod -R 777 /、curl | sh、> /dev/sd、fork 炸弹等，并把
// 「停服务 / 原地改写 / 数据库删除」等会影响远端的写法一并按中危拦下。
var sshExecDenyPatterns = []struct {
	re   *regexp.Regexp
	note string
}{
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f|\brm\s+-[a-z]*f[a-z]*r|\brm\b[^\n]*--no-preserve-root`), "递归强制删除（rm -rf），不可恢复"},
	{regexp.MustCompile(`(?i)\bmkfs\b|\bfdisk\b|\bparted\b`), "格式化或重分区，会清空磁盘数据"},
	{regexp.MustCompile(`(?i)\bdd\b[^\n]*\bif=|\bdd\b[^\n]*\bof=/dev/`), "dd 直读写裸设备，可能覆盖磁盘"},
	{regexp.MustCompile(`(?i)\b(shutdown|halt|poweroff|reboot|init\s+0)\b`), "关机/重启会立即中断远端系统"},
	{regexp.MustCompile(`(?i)\bchmod\s+(-R\s+)?777\b[^\n]*/|\bchmod\b[^\n]*\ba\+rwx\b`), "把权限放开到 777/a+rwx，可能引入越权"},
	{regexp.MustCompile(`(?i)\b(curl|wget)\b[^\n]*\|\s*(ba)?sh\b`), "下载后直接管道执行远程脚本，内容不可预知"},
	{regexp.MustCompile(`(?i)>\s*/dev/(sd|nvme|hd|vd)`), "重定向覆盖磁盘设备，会破坏分区"},
	{regexp.MustCompile(`(?i):\s*\(\s*\)\s*\{`), "fork 炸弹，会让远端失去响应"},
	{regexp.MustCompile(`(?i)\b(iptables|ip6tables|nft)\b[^\n]*(-F|--flush)\b|\bufw\s+disable\b`), "清空/关闭防火墙，可能导致远程失联"},
	{regexp.MustCompile(`(?i)\bkill(all)?\s+-9\s+-1\b|\bpkill\s+-9\b`), "强杀全部进程，可能连 SSH 服务一起杀掉"},
	{regexp.MustCompile(`(?i)\b(systemctl|service)\s+(stop|restart|disable)\b`), "停止/重启服务会影响服务可用性"},
	{regexp.MustCompile(`(?i)\bDROP\s+(DATABASE|TABLE)\b|\bDELETE\s+FROM\b|\bTRUNCATE\b`), "数据库删除/清空，数据不可恢复"},
	{regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b|\bgit\s+push\s+(-f|--force)\b`), "丢弃改动或强推覆盖远端历史"},
	{regexp.MustCompile(`(?i)>\s*/(etc|usr|bin|sbin)/`), "覆盖系统关键目录文件"},
	{regexp.MustCompile(`(?i)\bsed\s+-i\b|\bperl\s+-[a-z]*i\b`), "原地改写文件，无法预览差异"},
}

// sshCmdRisk 返回命中说明（非空即拦截），空串表示允许执行。
// 不再细分等级：对 ssh_exec 而言 medium 与 high 都该拒，命中即返回一条说明。
func sshCmdRisk(cmd string) string {
	for _, p := range sshExecDenyPatterns {
		if p.re.MatchString(cmd) {
			return p.note
		}
	}
	return ""
}
