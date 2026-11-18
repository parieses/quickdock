//go:build !windows

package aitools

// 平台注记（WP9）：类 Unix 版。
//
// 与 Windows 版同构，只是正文不同。两边的 golden 断言各按本平台的
// `runtime.GOOS` 选基准（见 prompts_platform_test.go）。
//
// 这是本次唯一**新增**内容的地方：迁移前 unix 上什么都不追加。之所以补一句，
// 是因为模型在 Linux 上仍然会写 `bash -c` 之外的东西（数组、`[[ ]]`），
// 而默认 shell 是 /bin/sh——这类错要花一整轮才发现。

import _ "embed"

//go:embed prompts/platform/unix.md
var platformShellNoteRaw string
