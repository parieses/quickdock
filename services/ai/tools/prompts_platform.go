package aitools

// 平台注记的公共装配（WP9）。
//
// 两个平台的差异只落在 `prompts/platform/*.md` 与对应的 `_windows.go` /
// `_unix.go` 里（build tag 决定哪个文件进构建），本文件不含任何 GOOS 判断。

import "strings"

// platformShellNote 返回当前平台的 shell 注记，末尾不带换行。
//
// 裁行尾换行是因为它要直接拼在 run_command 的描述后面：`.md` 文件按惯例
// 以换行结尾，拼进去会在描述中间留一个换行，模型看到的是一句被截断的话。
func platformShellNote() string {
	return strings.TrimRight(platformShellNoteRaw, "\r\n")
}
