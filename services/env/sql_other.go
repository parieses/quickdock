//go:build !windows

package env

// killSQLHolders 非 Windows 平台的无操作实现（SQL 运行时仅支持 Windows）。
func killSQLHolders(versionDir, serverBin string) {}
