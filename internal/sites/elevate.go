package sites

import (
	"fmt"
	"os"
	"strings"
	"time"

	"quickdock/internal/sysutil"
)

// ElevateHostsFlag 提权子进程的入口标记：`quickdock.exe --qd-sync-hosts <domain>...`。
//
// 这是宿主交给「管理员权限的那半边自己」的唯一能力，参数只有域名列表——
// 不接受文件路径、不接受文件内容；目标文件恒为系统 hosts，且每个域名都要过 hostsEntryRe。
// 保持这个最小集合，是为了让提权路径不存在「写任意文件」这种可被借用的原语。
const ElevateHostsFlag = "--qd-sync-hosts"

// elevateTimeout 提权子进程的运行上限。它只做一次文件写入，正常毫秒级完成；
// 留 30s 是为兜住安全软件拦一下、磁盘慢之类的偶发情况。
// 用户在 UAC 框上思考的时间不计入——ShellExecuteExW 在用户作出选择之后才返回。
const elevateTimeout = 30 * time.Second

// syncHostsElevated 以管理员身份重新拉起本程序，让「另一个自己」写一次 hosts。
// 之所以不用 cmd/powershell 中转：内容要经命令行传，转义一旦出错就是写坏系统文件；
// 自举则复用同一份 mergeHostsBlock 逻辑，父进程与子进程的区块算法天然一致。
func syncHostsElevated(domains []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("取可执行文件路径失败: %w", err)
	}
	args := append([]string{ElevateHostsFlag}, domains...)
	return sysutil.RunElevated(exe, args, elevateTimeout)
}

// SyncHostsCLI 提权子进程入口：校验域名后直接写系统 hosts（绝不再提权，否则会无限弹 UAC）。
func SyncHostsCLI(domains []string) error {
	if err := validateHostsEntries(domains); err != nil {
		return err
	}
	return syncHostsFile(hostsFilePath(), domains)
}

// validateHostsEntries 校验提权路径收到的域名。
//
// 用 hostsEntryRe 而非站点的 domainRe：hosts 里本来就要写 localhost（单段域名），
// 但任何可能被当作命令行参数、路径或换行的内容都必须拒绝——这一层的调用者握有管理员权限，
// 例如 "../../Windows/System32/drivers/etc/hosts"、"a; rm -rf /"、"a\n127.0.0.1 x"。
// 大小写不敏感：域名本就不区分大小写，写盘前 renderHostsBlock 会统一转小写。
func validateHostsEntries(domains []string) error {
	for _, d := range domains {
		if !hostsEntryRe.MatchString(strings.ToLower(d)) {
			return fmt.Errorf("拒绝写入非法域名: %q", d)
		}
	}
	return nil
}
