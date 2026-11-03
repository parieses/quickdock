// SSH 密钥：生成、写进 ~/.ssh/config、测连通、把仓库远端从 HTTPS 换成 SSH。
//
// 为什么面板要管这件事：HTTPS 推送要凭据管理器弹窗 + PAT，SSH 配好一次之后
// push/pull 都不用人管。而「配 SSH」在终端里是四步：ssh-keygen → 复制 .pub →
// 粘到 GitHub/GitLab → 写 config/试连，每一步都能让人卡住。
//
// 三条边界（有意为之）：
//  1. 私钥只在本机生成，**绝不读取也不外传**：面板只列 .pub 与指纹。
//  2. 不替用户往 GitHub 上贴公钥（那要 OAuth/密码），只提供一键复制。
//  3. 默认不设口令：设了口令就需要 ssh-agent 常驻，否则每次 fetch 都会失败，
//     而失败原因（Permission denied）对用户毫无指向性。需要口令的留空输入框自己填。
package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"quickdock/services"
)

// SSHKeyInfo 一把已存在的密钥。只取 .pub：私钥不读、不传、不进日志。
type SSHKeyInfo struct {
	Name        string `json:"name"` // 文件名（不含目录与 .pub）
	Type        string `json:"type"` // ssh-ed25519 / ssh-rsa / ecdsa-sha2-...
	Comment     string `json:"comment"`
	Fingerprint string `json:"fingerprint"`
	Path        string `json:"path"` // 私钥路径（展示用，方便用户自己 ssh-add）
	PublicKey   string `json:"publicKey"`
}

// SSHStatus ~/.ssh 的现状，够面板画一屏。
type SSHStatus struct {
	Dir        string            `json:"dir"`
	Keys       []SSHKeyInfo      `json:"keys"`
	ConfigPath string            `json:"configPath"`
	Hosts      []string          `json:"hosts"`   // config 里出现过的具体 host（去过通配）
	HostKey    map[string]string `json:"hostKey"` // host → IdentityFile，用于标「已配置」
}

// sshDir ~/.ssh。Windows 上 os.UserHomeDir 读 USERPROFILE，与 Git 自带的 ssh 一致。
func sshDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("USERPROFILE")
	}
	return filepath.Join(home, ".ssh")
}

// findSSHTool 定位 ssh / ssh-keygen。
//
// 不能只靠 exec.LookPath：Git for Windows 把这两个放在 <Git>\usr\bin，
// 那个目录通常**不在** GUI 进程的 PATH 里（只在 Git Bash 的会话里）。
func findSSHTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	cands := []string{
		`C:\Program Files\Git\usr\bin\` + name + `.exe`,
		`C:\Program Files (x86)\Git\usr\bin\` + name + `.exe`,
		`C:\Windows\System32\OpenSSH\` + name + `.exe`,
		"/usr/bin/" + name,
		"/usr/local/bin/" + name,
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("找不到 " + name + "：装 Git 时若没勾选 OpenSSH 客户端，就没有这个工具")
}

// GitSSHStatus 返回 ~/.ssh 下已有的公钥、config 里已配的 host。
func (s *GitService) GitSSHStatus() *services.ApiResult {
	dir := sshDir()
	cfg := filepath.Join(dir, "config")
	st := SSHStatus{Dir: dir, Keys: listSSHKeys(), ConfigPath: cfg, Hosts: []string{}, HostKey: map[string]string{}}
	if st.Keys == nil {
		st.Keys = []SSHKeyInfo{}
	}
	hosts, hostKey := readSSHConfigHosts(cfg)
	st.Hosts = hosts
	st.HostKey = hostKey
	return services.Ok(st)
}

// listSSHKeys 扫 .pub 文件。按文件名排序，让 id_ed25519 这类默认名靠前。
func listSSHKeys() []SSHKeyInfo {
	dir := sshDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []SSHKeyInfo{}
	}
	keys := []SSHKeyInfo{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".pub") {
			continue
		}
		pubPath := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(pubPath)
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(data))
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue // 空文件或不是 OpenSSH 公钥格式
		}
		k := SSHKeyInfo{
			Name:      strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())),
			Type:      fields[0],
			Comment:   strings.Join(fields[2:], " "),
			Path:      strings.TrimSuffix(pubPath, ".pub"),
			PublicKey: line,
		}
		k.Fingerprint = sshFingerprint(pubPath)
		keys = append(keys, k)
	}
	return keys
}

// sshFingerprint `ssh-keygen -l -f` 输出形如 `256 SHA256:xxx comment (ED25519)`，取第二段。
func sshFingerprint(pubPath string) string {
	bin, err := findSSHTool("ssh-keygen")
	if err != nil {
		return ""
	}
	out, _, e := runTool(bin, cmdTimeout, "-l", "-f", pubPath)
	if e != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

// readSSHConfigHosts 抽出 config 里的具体 host 与它的 IdentityFile。
//
// 只认「一行一个 Host」的常见写法（含 Host a b 多值）；Match / Include 一律忽略——
// 那些是给高级用户手写复杂配置用的，面板不该假装懂。
func readSSHConfigHosts(cfgPath string) ([]string, map[string]string) {
	hosts := []string{}
	hostKey := map[string]string{}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return hosts, hostKey
	}
	var cur []string
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "host ") {
			cur = strings.Fields(line[5:])
			for _, h := range cur {
				if strings.ContainsAny(h, "*?") {
					continue
				}
				if _, ok := hostKey[h]; !ok {
					hostKey[h] = ""
					hosts = append(hosts, h)
				}
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "identityfile ") && len(cur) > 0 {
			p := strings.TrimSpace(line[len("identityfile "):])
			for _, h := range cur {
				if _, ok := hostKey[h]; ok && hostKey[h] == "" {
					hostKey[h] = p
				}
			}
		}
	}
	return hosts, hostKey
}

// upsertSSHConfig 把 host 的密钥写进 ~/.ssh/config（幂等）。
//
// 三种情况都要处理：没有 config（新建）、有 config 但没有这个 host（追加）、
// 已经有这个 host（就地改 IdentityFile，保留用户其它设置）。直接覆盖整份文件
// 会把用户的 ProxyCommand / Port 之类全抹掉，那是不可接受的。
func upsertSSHConfig(host, keyPath string) (string, error) {
	dir := sshDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cfgPath := filepath.Join(dir, "config")
	keyFwd := filepath.ToSlash(keyPath)

	existing, _ := os.ReadFile(cfgPath)
	lines := strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil // 空文件：当没有内容，免得开头多一个空行
	}

	start, end := -1, len(lines)
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(strings.ToLower(line), "host ") {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		for _, h := range strings.Fields(line[5:]) {
			if strings.EqualFold(h, host) && !strings.ContainsAny(h, "*?") {
				start = i
				break
			}
		}
	}

	if start < 0 {
		block := []string{
			"Host " + host,
			"    HostName " + host,
			"    User git",
			"    IdentityFile " + keyFwd,
			"    IdentitiesOnly yes",
		}
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, block...)
	} else {
		body := append([]string{}, lines[start+1:end]...)
		hasID, hasUser, hasHostName := false, false, false
		out := make([]string, 0, len(body)+2)
		for _, raw := range body {
			line := strings.TrimSpace(raw)
			low := strings.ToLower(line)
			switch {
			case strings.HasPrefix(low, "identityfile"):
				out = append(out, "    IdentityFile "+keyFwd)
				hasID = true
			case strings.HasPrefix(low, "identitiesonly"):
				if !strings.Contains(low, "yes") {
					out = append(out, "    IdentitiesOnly yes")
				} else {
					out = append(out, raw)
				}
			case strings.HasPrefix(low, "user "):
				hasUser = true
				out = append(out, raw)
			case strings.HasPrefix(low, "hostname "):
				hasHostName = true
				out = append(out, raw)
			default:
				out = append(out, raw)
			}
		}
		add := []string{}
		if !hasHostName {
			add = append(add, "    HostName "+host)
		}
		if !hasUser {
			add = append(add, "    User git")
		}
		if !hasID {
			add = append(add, "    IdentityFile "+keyFwd)
		}
		if !hasID {
			add = append(add, "    IdentitiesOnly yes")
		}
		merged := append(add, out...)
		lines = append(append(append([]string{}, lines[:start+1]...), merged...), lines[end:]...)
	}

	body := strings.Join(lines, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("写入 %s 失败：%w", cfgPath, err)
	}
	return cfgPath, nil
}

// GitSSHGenerate 给 host 准备一把密钥并写进 config。
//
// 已经有 quickdock 生成的密钥就复用：一把 key 配多个 host 是正常做法，
// 每次都新生成会让用户在 GitHub 设置页里堆一排公钥。
func (s *GitService) GitSSHGenerate(host, comment, passphrase string) *services.ApiResult {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " \t/\\") {
		return services.FailMsg("请填写正确的主机名（如 github.com）")
	}
	keygen, err := findSSHTool("ssh-keygen")
	if err != nil {
		return services.Fail(err)
	}
	dir := sshDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return services.FailMsg("创建 " + dir + " 失败：" + clip(err.Error(), 200))
	}

	keyPath := filepath.Join(dir, "id_ed25519_quickdock")
	reused := false
	if _, err := os.Stat(keyPath); err == nil {
		reused = true
	} else {
		cmt := strings.TrimSpace(comment)
		if cmt == "" {
			cmt = "quickdock@" + hostnameOr("local")
		}
		// -q 抑制 "Generating public/private..." 噪声；-N 传空串=不设口令
		if _, errOut, e := runTool(keygen, cmdTimeout, "-t", "ed25519", "-C", cmt,
			"-f", keyPath, "-N", passphrase, "-q"); e != nil {
			return services.FailMsg("生成密钥失败：" + clip(strings.TrimSpace(errOut), 300))
		}
	}

	cfgPath, err := upsertSSHConfig(host, keyPath)
	if err != nil {
		return services.FailMsg(err.Error())
	}

	pubBytes, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return services.FailMsg("读不到刚生成的公钥：" + clip(err.Error(), 200))
	}
	pub := strings.TrimSpace(string(pubBytes))
	fields := strings.Fields(pub)
	info := SSHKeyInfo{
		Name:        "id_ed25519_quickdock",
		Type:        "ssh-ed25519",
		Comment:     strings.Join(fields[2:], " "),
		Path:        keyPath,
		PublicKey:   pub,
		Fingerprint: sshFingerprint(keyPath + ".pub"),
	}
	if len(fields) > 0 {
		info.Type = fields[0]
	}

	msg := "已生成密钥并写入 " + filepath.Base(cfgPath) + "：" + host + " ——接着把公钥粘到 " + host + " 的 SSH Keys 页面，再点「测试连接」"
	if reused {
		msg = "已复用现有密钥并写入 " + filepath.Base(cfgPath) + "：" + host + " ——把公钥粘到 " + host + " 的 SSH Keys 页面即可"
	}
	return services.OkMsg(map[string]any{
		"key": info, "configPath": cfgPath, "reused": reused,
	}, msg)
}

func hostnameOr(fallback string) string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return strings.TrimSpace(h)
	}
	return fallback
}

// GitSSHTest 用 ssh -T 试连一次，判断这把 key 在远端是否被接受。
//
// 退出码不能当唯一判据：GitHub 认证成功时故意返回 1（它不提供 shell），
// 只有文案里才带「successfully authenticated」。GitLab 则返回 0 + Welcome。
func (s *GitService) GitSSHTest(host string) *services.ApiResult {
	host = strings.TrimSpace(host)
	if host == "" {
		return services.FailMsg("缺少主机名")
	}
	sshBin, err := findSSHTool("ssh")
	if err != nil {
		return services.Fail(err)
	}
	args := []string{
		"-o", "BatchMode=yes", // 不弹密码交互（GUI 里弹不出来，会直接挂住）
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=12",
		"-T", "git@" + host,
	}
	out, errOut, e := runTool(sshBin, 30*time.Second, args...)
	text := strings.TrimSpace(out + "\n" + errOut)
	ok := e == nil ||
		strings.Contains(text, "successfully authenticated") ||
		strings.Contains(text, "Welcome to GitLab") ||
		strings.Contains(text, "You've successfully authenticated")
	// GitHub 的第一句 "Hi <user>!" 值得单独提出来：它直接回答了「哪把 key 生效了」
	if ok {
		return services.OkMsg(map[string]any{"ok": true, "output": clip(text, 600)}, clip(text, 300))
	}
	return services.Ok(map[string]any{"ok": false, "output": clip(text, 900)})
}

// RemoteInfo 当前仓库的 origin。
type RemoteInfo struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	IsSSH bool   `json:"isSsh"`
}

// GitRemoteInfo 读 origin 地址（没有远端时 URL 为空，不报错）。
func (s *GitService) GitRemoteInfo(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	url, _, e := run(bin, path, cmdTimeout, "remote", "get-url", "origin")
	if e != nil || strings.TrimSpace(url) == "" {
		return services.Ok(RemoteInfo{Name: "origin"})
	}
	u := strings.TrimSpace(url)
	return services.Ok(RemoteInfo{Name: "origin", URL: u, IsSSH: isSSHURL(u)})
}

func isSSHURL(u string) bool {
	return strings.HasPrefix(u, "git@") || strings.HasPrefix(u, "ssh://") || strings.HasPrefix(u, "git+ssh://")
}

// GitRemoteToSSH 把 origin 从 HTTPS 换成 SSH（幂等，已是 SSH 就只回报）。
//
// 只做 origin 一条 remote：多 remote 的仓库（fork + upstream）属于用户自己的编排，
// 面板替他猜该改哪条只会改错。
func (s *GitService) GitRemoteToSSH(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, _, e := run(bin, path, cmdTimeout, "remote", "get-url", "origin")
	u := strings.TrimSpace(out)
	if e != nil || u == "" {
		return services.FailMsg("这个仓库没有 origin 远端：先添加远端再切换")
	}
	if isSSHURL(u) {
		return services.OkMsg(map[string]any{"url": u}, "origin 已经是 SSH 地址")
	}
	sshURL, ok := httpsToSSH(u)
	if !ok {
		return services.FailMsg("无法识别的 origin 地址：" + clip(u, 160) + "（只支持 https://host/owner/repo.git）")
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "remote", "set-url", "origin", sshURL); e != nil {
		return services.FailMsg("改 origin 失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(map[string]any{"url": sshURL},
		"origin 已切到 "+hostOfSSHURL(sshURL)+" 的 SSH 地址；公钥没加到远端的话，推送会提示 Permission denied")
}

// httpsToSSH https://host/owner/repo.git → git@host:owner/repo.git。
// 去掉 URL 里可能带的 user@（HTTPS 的 userinfo 对 SSH 没有意义，用户名固定是 git）。
func httpsToSSH(u string) (string, bool) {
	rest, ok := strings.CutPrefix(u, "https://")
	if !ok {
		return "", false
	}
	if i := strings.Index(rest, "/"); i > 0 {
		host := rest[:i]
		p := strings.TrimPrefix(rest[i:], "/")
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		if host == "" || p == "" {
			return "", false
		}
		return "git@" + host + ":" + p, true
	}
	return "", false
}

func hostOfSSHURL(u string) string {
	rest := strings.TrimPrefix(u, "git@")
	if i := strings.IndexAny(rest, ": /"); i > 0 {
		return rest[:i]
	}
	return rest
}
