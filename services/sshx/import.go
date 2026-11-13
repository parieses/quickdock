package sshx

import (
	"fmt"
	"os"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/sshconfig"
	"quickdock/services"
)

// importSshConfigResult 是 ImportSshConfig 的返回数据。
//
// Total 是解析出的 Host 块总数（含被跳过的），Imported 是真正落库的条数，
// Skipped 逐条说明为什么没导——前端要能原样展示给用户，而不是只给一个数字。
type importSshConfigResult struct {
	Imported int      `json:"imported"`
	Skipped  []string `json:"skipped"`
	Total    int      `json:"total"`
}

// identityPlaceholders 是 OpenSSH IdentityFile 里最常见的路径 token。
// QuickDock 连接时不会展开这些 token，照搬进 KeyPath 只会得到一个永远打不开的
// 路径（loadHost 随后报「没有凭据」），所以含占位符时宁可不导 KeyPath。
var identityPlaceholders = []string{"%d", "%h", "%r", "%u"}

func hasIdentityPlaceholder(p string) bool {
	for _, ph := range identityPlaceholders {
		if strings.Contains(p, ph) {
			return true
		}
	}
	return false
}

// ImportSshConfig 从 OpenSSH config 单向导入主机（绝不写回用户文件）。
//
// 为什么只做单向、不做双向同步：~/.ssh/config 是用户手工维护的资产，写回会动到
// 注释、顺序、Match/Include 与通配块；两边都改过同一台主机时「以谁为准」没有安全
// 答案；真要写回还得先处理 0600 权限、首写备份、符号链接与 Windows 路径。更关键
// 的是 ProxyCommand/ProxyJump 在 QuickDock 里是「只记录、默认不执行」的字符串，
// 回写会丢掉这层保守语义，甚至把外部命令又带回去执行。所以导入只读，用户要改
// 配置请直接改原文件，再重新导入。
func (s *SshService) ImportSshConfig(path string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}

	path = strings.TrimSpace(path)
	if path == "" {
		return failCode(CodeInvalidInput, "SSH config 路径不能为空")
	}
	info, err := os.Stat(path)
	if err != nil {
		return failCode(CodeInvalidInput, fmt.Sprintf("无法读取 SSH config 文件: %v", err))
	}
	if info.IsDir() {
		return failCode(CodeInvalidInput, "SSH config 路径指向目录，必须是普通文件")
	}
	if !info.Mode().IsRegular() {
		return failCode(CodeInvalidInput, "SSH config 路径不是普通文件")
	}

	hosts, err := sshconfig.ParseFile(path)
	if err != nil {
		return failCode(CodeInvalidInput, fmt.Sprintf("解析 SSH config 失败: %v", err))
	}

	existing, err := s.App.DB.ListSshHosts()
	if err != nil {
		return failSsh(err)
	}
	// 按 host+port+user 判重：这是「同一台机器同一个账号」的最小充分身份，
	// 别名不同但指向同一目标的两条 config 不该导成两台重复主机。
	seen := make(map[string]bool, len(existing))
	for _, h := range existing {
		if h.IsFolder {
			continue // 文件夹没有连接目标，不该占用判重键
		}
		seen[hostIdentityKey(h.Host, h.Port, h.User)] = true
	}

	out := importSshConfigResult{Skipped: []string{}, Total: len(hosts)}
	for _, h := range hosts {
		key := hostIdentityKey(h.HostName, h.Port, h.User)
		if seen[key] {
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("%s：已存在相同 host+port+user 的主机，跳过（不覆盖已有密码/备注）", h.Alias))
			continue
		}

		// 没有可用 IdentityFile 时 AuthType 留空，由 db.normalizeSshHost 归一成
		// password；config 里本来就不含密码，用户仍需在主机表单里补凭据才能连。
		authType := ""
		keyPath := ""
		if h.IdentityFile != "" {
			if hasIdentityPlaceholder(h.IdentityFile) {
				out.Skipped = append(out.Skipped,
					fmt.Sprintf("%s：IdentityFile 含占位符，跳过 KeyPath（%s）", h.Alias, h.IdentityFile))
			} else {
				keyPath = h.IdentityFile
				authType = db.SshAuthKey
			}
		}

		// 显式写 0 在 OpenSSH 里是「关掉保活」，db 用 -1 表示；正数原样；
		// 未配置（或非法负数）留 0 沿用默认 45s，不擅自解释成关闭。
		keepalive := 0
		if h.HasAliveInterval {
			switch {
			case h.ServerAliveInterval == 0:
				keepalive = -1
			case h.ServerAliveInterval > 0:
				keepalive = h.ServerAliveInterval
			}
		}

		remark := ""
		if h.ProxyJump != "" {
			// 不解析成 jump_host_id：跨主机引用要按名字/别名匹配，误配比不配更危险
			// （会把流量悄悄引到另一台机器上）。只留文字让用户在表单里自己确认。
			remark = fmt.Sprintf("ProxyJump: %s（未自动映射）", h.ProxyJump)
		}

		// 导入的外部命令默认不执行：用户必须在主机表单里显式确认才会打开；
		// MCPAllowed 也一律关，Agent 不该因为一次导入就获得连接权限。
		if _, err := s.App.DB.SaveSshHost(db.SshHost{
			Name:                h.Alias,
			Host:                h.HostName,
			Port:                h.Port,
			User:                h.User,
			AuthType:            authType,
			KeyPath:             keyPath,
			Env:                 db.SshEnvTest,
			ProxyCommand:        h.ProxyCommand,
			ProxyCommandEnabled: false,
			MCPAllowed:          false,
			KeepaliveInterval:   keepalive,
			Remark:              remark,
		}); err != nil {
			return failSsh(err)
		}
		seen[key] = true // config 里两条不同别名指向同一目标时只导第一条
		out.Imported++
	}
	return services.Ok(out)
}

// hostIdentityKey 构造判重键。抽出来是为了让「已有主机」与「待导入主机」用完全
// 相同的拼法——两处各写一遍 fmt.Sprintf，漏个 TrimSpace 就会让去重悄悄失效。
func hostIdentityKey(host string, port int, user string) string {
	return fmt.Sprintf("%s|%d|%s", strings.TrimSpace(host), port, strings.TrimSpace(user))
}
