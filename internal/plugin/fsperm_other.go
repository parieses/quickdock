//go:build !windows

package plugin

// knownFolderDir 非 Windows 平台无 Known Folder Redirection 概念，
// 一律回退到主目录字面拼接（调用方 expandHome 已处理）。
func knownFolderDir(sub string) (string, bool) {
	return "", false
}
