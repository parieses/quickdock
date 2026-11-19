// Package sites 本地开发站点：域名 → 目录，由 nginx/caddy 以 HTTPS 对外提供服务。
// QuickDock 负责签发 mkcert 证书、写入 hosts 解析、生成站点片段 —— 自己不监听任何端口。
//
// 与 httpserve 包的区别：httpserve 是「把某个目录挂到一个随机端口」（http://127.0.0.1:随机端口），
// sites 是「给某个目录一个域名 + 自动 HTTPS」（https://myapp.test）——本地开发站点的正统形态，
// 代价是需要用户装一个 nginx/caddy 来真正提供服务。
package sites

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/sysutil"
)

// hostsBlockBegin / hostsBlockEnd 是 QuickDock 在 hosts 文件里维护的标记区块。
// 只增删这一区块、其余内容逐字节保留 —— 用户自己写的条目绝不能被动。
const (
	hostsBlockBegin = "# quickdock-sites begin"
	hostsBlockEnd   = "# quickdock-sites end"
)

// hostsEntryRe 提权写入路径上接受的「域名」形态：字母/数字/连字符/点，首尾必须是字母或数字。
// 比 domainRe 宽（允许单段 localhost），但严格排除了空白、引号、斜杠、井号、逗号、换行——
// 提权子进程的命令行参数直接来自父进程，这里是参数注入的检查点。
var hostsEntryRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// syncHostsFile 把 domains 写入 hostsPath 的标记区块（幂等，只做直接写、绝不提权）。
//
// 提权重试刻意不放在这里：提权子进程也会调到本函数，若这里自带提权就会无限弹 UAC。
// 「先直写、失败再提权」的编排在 syncHostsTo，提权子进程入口在 SyncHostsCLI。
func syncHostsFile(hostsPath string, domains []string) error {
	content, err := mergeHostsBlock(hostsPath, domains)
	if err != nil {
		return err
	}
	return writeHostsDirect(hostsPath, content)
}

// mergeHostsBlock 读出原文并把标记区块替换成 domains 对应的内容（纯计算，不落盘）。
// 已有区块则摘掉后重新追加，没有则直接追加到末尾；区块外的内容逐字节保留。
// domains 为空表示清空区块内容但保留标记，方便下次原地更新。
func mergeHostsBlock(hostsPath string, domains []string) (string, error) {
	orig, err := os.ReadFile(hostsPath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("读取 hosts 失败: %w", err)
	}
	return withHostsBlock(string(orig), renderHostsBlock(domains)), nil
}

// withHostsBlock 把 content 里所有标记区块摘干净，再把 block 追加到末尾。
//
// 为什么不是「定位第一段就地替换」：用户手动编辑过、或别的工具截断过之后，
// 文件里可能同时存在两段 begin（只有一段有 end）。按「第一个 begin 到第一个
// end」整段替换，会把夹在两段标记之间的用户条目一起删掉——hosts 不是我们的
// 文件，删用户数据不可接受。宁可每次重排标记段的位置，也不动标记外的内容。
func withHostsBlock(content, block string) string {
	begin := strings.Index(content, hostsBlockBegin)
	end := strings.Index(content, hostsBlockEnd)
	// 正常情况（恰好一段完整区块）：就地替换，位置与前后文完全不动。
	// 这条路径必须保持「写一次和写十次结果一样」——站点每次启动都会同步一次。
	if begin >= 0 && end > begin &&
		strings.Count(content, hostsBlockBegin) == 1 && strings.Count(content, hostsBlockEnd) == 1 {
		end += len(hostsBlockEnd)
		// 连同标记行尾的换行一起吃掉，避免反复重写时累积空行
		if end < len(content) && content[end] == '\n' {
			end++
		}
		return content[:begin] + block + content[end:]
	}

	// 异常情况（重复标记 / 只有 begin 的单边残留）：先把所有标记段摘干净，再追加。
	if begin >= 0 || end >= 0 {
		logger.W("[sites] hosts 中存在成对/成单的 QuickDock 标记残留，已按标记段重排并保留标记外内容")
	}
	content = strings.TrimRight(stripHostsBlocks(content), "\n")
	if content != "" {
		content += "\n\n"
	}
	return content + block
}

// stripHostsBlocks 摘掉所有标记区块（含只有 begin 的单边残留），只留用户内容。
func stripHostsBlocks(content string) string {
	for {
		start := strings.Index(content, hostsBlockBegin)
		if start < 0 {
			return content
		}
		rel := strings.Index(content[start:], hostsBlockEnd)
		if rel < 0 {
			// 只有 begin：只删这一行标记本身，后面的用户条目原样保留。
			lineEnd := strings.Index(content[start:], "\n")
			if lineEnd < 0 {
				return content[:start]
			}
			content = content[:start] + content[start+lineEnd+1:]
			continue
		}
		end := start + rel + len(hostsBlockEnd)
		// 连同标记行尾的换行一起吃掉，避免反复重写时累积空行
		if end < len(content) && content[end] == '\n' {
			end++
		}
		content = content[:start] + content[end:]
	}
}

// writeHostsDirect 直接写 hosts 文件。
// 不用「临时文件 + rename」的方式：Windows 上 hosts 常被安全软件监控/锁定，
// rename 覆盖会被拒；就地写虽然理论上可能被中断，但保住"能写"更重要。
func writeHostsDirect(hostsPath, content string) error {
	if err := os.WriteFile(hostsPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("写入 hosts 失败: %w", err)
	}
	return nil
}

// syncHostsTo 写入 hosts；escalate=true 且被系统拒绝时，自动以管理员身份重试一次（弹一次 UAC）。
//
// 四个刻意的取舍：
//  1. 「已是目标状态」直接返回——站点每次启动都会走到这里，内容本来就对还去提权写一遍，
//     等于每次启动都弹 UAC。这是避免打扰的关键，不是优化。
//  2. escalate=false 只做直写：应用启动自动拉起服务（Resume）走这条路。用户没做任何动作
//     就弹 UAC 是骚扰；写不进去不影响服务运行，用户点「重新同步」时照常提权。
//  3. 只在 permission 类错误上提权：磁盘满、路径不存在之类提权也救不了，白弹一次框。
//  4. 已经以管理员运行还被拒 → 说明是安全软件锁定/文件被独占，再提权毫无意义，直接上报。
func syncHostsTo(hostsPath string, domains []string, escalate bool) error {
	if cur, err := hostsBlockDomains(hostsPath); err == nil && sameDomainSet(cur, domains) {
		return nil
	}
	err := syncHostsFile(hostsPath, domains)
	if err == nil || !escalate || !isPermissionError(err) || sysutil.IsElevated() {
		return err
	}
	if eerr := syncHostsElevated(domains); eerr != nil {
		return fmt.Errorf("%w（自动提权写入也失败：%v）", err, eerr)
	}
	return nil
}

// isPermissionError 判断写 hosts 失败是否属于「权限不足」。
// Windows 上 os.WriteFile 返回的是 *fs.PathError 包着 ERROR_ACCESS_DENIED(5)，
// 标准库的 Errno.Is 已把它映射到 fs.ErrPermission；字符串兜底覆盖其它平台/包装层。
func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "Access is denied") || strings.Contains(msg, "Permission denied")
}

// renderHostsBlock 渲染标记区块。域名做去重 + 小写 + 排序：
// 排序让区块内容稳定，否则"仅顺序变化"也会导致整块重写。
func renderHostsBlock(domains []string) string {
	list := normalizeDomains(domains)
	var b strings.Builder
	b.WriteString(hostsBlockBegin + "\n")
	for _, d := range list {
		b.WriteString("127.0.0.1 " + d + "\n")
	}
	b.WriteString(hostsBlockEnd + "\n")
	return b.String()
}

// normalizeDomains 去空白、转小写、去重、排序。
func normalizeDomains(domains []string) []string {
	seen := map[string]bool{}
	list := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		list = append(list, d)
	}
	sort.Strings(list)
	return list
}

// hostsBlockDomains 读出标记区块里当前登记的域名（不含 IP 列）。
func hostsBlockDomains(hostsPath string) ([]string, error) {
	data, err := os.ReadFile(hostsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseHostsBlock(string(data)), nil
}

// parseHostsBlock 从 hosts 全文里抽出标记区块登记的域名。
func parseHostsBlock(content string) []string {
	start := strings.Index(content, hostsBlockBegin)
	end := strings.Index(content, hostsBlockEnd)
	if start < 0 || end <= start {
		return nil
	}
	body := content[start+len(hostsBlockBegin) : end]
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// 只认我们自己写的 127.0.0.1 行，避免把别人手改进来的 IPv6 条目当成我们的
		if fields[0] != "127.0.0.1" {
			continue
		}
		out = append(out, strings.ToLower(fields[1]))
	}
	return out
}

// sameDomainSet 判断两个域名集合是否等价（排序后逐项比较）。
func sameDomainSet(a, b []string) bool {
	na, nb := normalizeDomains(a), normalizeDomains(b)
	if len(na) != len(nb) {
		return false
	}
	for i := range na {
		if na[i] != nb[i] {
			return false
		}
	}
	return true
}
