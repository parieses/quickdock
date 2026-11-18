//go:build windows

package aitools

// 平台注记（WP9）：Windows 版。
//
// 为什么用 build tag + 分片文件，而不是运行期 `if runtime.GOOS == "windows"`：
// 两种写法的行为一样，但**测试不一样**。运行期拼接只能在 Windows 上走到
// Windows 那一段，Linux 那段永远没人验证；分片之后，两个文件分别被两个平台的
// 构建看见，各自的 golden 断言才有意义（AGENTS R12 也是这个要求）。
//
// 内容里刻意没有结尾换行说明：`platformShellNote()` 会裁掉行尾换行，
// 因为它要直接拼在上一条描述后面。

import _ "embed"

//go:embed prompts/platform/windows.md
var platformShellNoteRaw string
