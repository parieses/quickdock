package aiutil

import "regexp"

// LocalCmdDangerRules 是本机场景的命令风险表（日志 / 监控 / 端口 / 环境页共用）。
//
// 为什么不复用 SSH 那张表：那张表的判定前提是「在远程生产机上跑」——
// `systemctl restart`、`docker rm -f`、`iptables -F` 在那里的后果是线上中断，
// 在本机则往往只是重启一下自己的服务。反过来，本机的痛点是
// 删本机文件（还不进回收站）、动系统盘/注册表/启动项、强杀本机进程丢未保存数据，
// 这些在远程机上反而不是重点。两套语义混用，结果是两边都不准。
//
// 还有一层：本机场景没有执行落地通道（不像 SSH 面板能 send 到终端），
// 所以这里的等级只用来决定「卡片是否标红 + 是否显示说明」，不驱动任何按钮。
// 等级错了的代价是一句多余的警告，不是一条被跑错的命令——但说明仍要写准，
// 因为用户会照着它判断要不要复制。
//
// 规则是有限收录的：匹配不到任何规则的命令不代表安全（`sed -i` 改错配置、
// 删错名字的目录都匹配不到）。前端不得因为"没标红"就把这类命令当成无害。
var LocalCmdDangerRules = RiskRules{
	// ---- high：整机级、不可逆 ----
	{regexp.MustCompile(`(?i)\bmkfs(\.[a-z0-9]+)?\b`), "high",
		"格式化文件系统会清空目标分区，本机数据无法从应用内恢复"},
	{regexp.MustCompile(`(?i)\bdd\b[^\n]*\bof=/dev/`), "high",
		"dd 直写块设备会覆盖磁盘内容，写错设备等于毁盘"},
	{regexp.MustCompile(`(?i)\bformat\s+[a-z]:`), "high",
		"格式化盘符会清空整块磁盘"},
	{regexp.MustCompile(`(?i)\bdiskpart\b`), "high",
		"diskpart 可重新分区/清盘，属整机级操作"},
	{regexp.MustCompile(`(?i)\bbcdedit\b`), "high",
		"修改启动配置可能导致系统无法启动"},
	{regexp.MustCompile(`(?i)\breg\s+delete\s+"?(hklm|hkey_local_machine)\b`), "high",
		"删除 HKLM 下的注册表项可能让系统组件或驱动失效"},
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*\s+/(\s|$|\*)`), "high",
		"递归删除根目录会清空系统"},
	{regexp.MustCompile(`(?i)\b(del|rd|rmdir)\b[^\n]*[a-z]:\\(\s|$|\*)`), "high",
		"删除盘符根目录会清空整块磁盘"},
	{regexp.MustCompile(`(?i)(iwr|invoke-webrequest|curl|wget)\b[^\n]*\|\s*(iex|sh|bash|powershell)\b`), "high",
		"下载后直接执行的脚本来源不可控，等同于把本机交给对方"},

	// ---- medium：影响本机文件 / 服务 / 权限 ----
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*r`), "medium",
		"递归删除，删错目录无法撤销（本机删除不进回收站）"},
	{regexp.MustCompile(`(?i)\b(rd|rmdir)\s+/s\b`), "medium",
		"递归删除目录，删错路径无法撤销"},
	{regexp.MustCompile(`(?i)\bdel\s+/[fsq]\b`), "medium",
		"强制递归删除文件，不进回收站"},
	{regexp.MustCompile(`(?i)\b(taskkill|kill\s+-9|kill\s+-kill)\b`), "medium",
		"强制结束进程会丢失未保存的数据，误杀系统进程还可能让桌面异常"},
	{regexp.MustCompile(`(?i)\bsc\s+delete\b|\bnet\s+stop\b|\bstop-service\b`), "medium",
		"停止或删除服务会让依赖它的程序一起失效"},
	{regexp.MustCompile(`(?i)\b(icacls|takeown|chown|chmod\s+[0-7]{3,4})\b`), "medium",
		"修改权限或所有者后，其它程序可能再也无法访问该路径"},
	{regexp.MustCompile(`(?i)\bnetsh\s+advfirewall\b|\biptables\s+-(F|D)\b`), "medium",
		"改防火墙规则可能直接断开网络连接"},
	{regexp.MustCompile(`(?i)\breg\s+(add|delete)\b`), "medium",
		"修改注册表可能影响依赖该键的应用（改前请先备份）"},
}
