//go:build darwin || linux

package sites

// hostsFilePath 系统 hosts 文件路径（写入通常需要 root）。
func hostsFilePath() string { return "/etc/hosts" }
