//go:build windows

package platform

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// consoleEncodingByCP 按控制台代码页取解码器；返回 nil 表示无需转换
// （UTF-8 页面或未收录的页面，调用方按原样使用）。
func consoleEncodingByCP(cp uint32) encoding.Encoding {
	switch cp {
	case 936: // 简体中文（GBK）
		return simplifiedchinese.GBK
	case 950: // 繁体中文（Big5）
		return traditionalchinese.Big5
	case 932: // 日文（Shift_JIS）
		return japanese.ShiftJIS
	case 949: // 韩文（EUC-KR）
		return korean.EUCKR
	case 437: // OEM 美国英语（含制表符）
		return charmap.CodePage437
	case 850: // OEM 多语言（拉丁 I）
		return charmap.CodePage850
	case 1252: // ANSI 西欧
		return charmap.Windows1252
	case 65001: // UTF-8：字节已是目标编码
		return nil
	}
	return nil
}

// consoleCP 返回当前控制台输出代码页；宿主是 GUI 进程（-H windowsgui，没有
// 控制台）时 GetConsoleOutputCP 返回 0，退到系统 OEM 页——子进程没有 chcp
// 覆盖时用的正是它，中文系统上两者都是 936。
func consoleCP() uint32 {
	cp, _, _ := modKernel32.NewProc("GetConsoleOutputCP").Call()
	if cp == 0 {
		cp, _, _ = modKernel32.NewProc("GetOEMCP").Call()
	}
	return uint32(cp)
}

// DecodeConsoleText 把子进程输出里的非 UTF-8 片段按控制台代码页解码成 UTF-8。
//
// 为什么需要：Windows 控制台程序（cmd / dir / ping / git …）按 OEM 代码页往
// 管道里写字节，中文系统是 936(GBK)。Go 的 string 是 UTF-8，原样收下会把每个
// 中文拆成 U+FFFD——用户跑个 dir，执行结果就是一坨 �，功能等于没有。
//
// 为什么按行判、而不是整块解码：同一条管道里常常混着两种编码。cmd.exe 自己的
// 错误提示（「'head' 不是内部或外部命令」）是 OEM 代码页，而 go / npm / node
// 这类程序写的是 UTF-8；整块按 GBK 解会把后一半的中文变成乱码，与要修的问题
// 正好对称。整段已经是合法 UTF-8 时直接原样返回，一个字节都不动。
//
// 捕获缓冲按字节封顶，可能把多字节序列劈在尾部。这几个代码页都不会把合法序列
// 映射成 U+FFFD，故截断产生的残渣可以按「行尾连续的 U+FFFD」去掉。
func DecodeConsoleText(s string) string {
	if s == "" || utf8.ValidString(s) {
		return s
	}
	enc := consoleEncodingByCP(consoleCP())
	if enc == nil {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	changed := false
	for i, ln := range lines {
		if utf8.ValidString(ln) {
			continue
		}
		out, _, err := transform.String(enc.NewDecoder(), ln)
		if err != nil || out == "" {
			continue
		}
		// 只削末尾的残渣：行内出现的 U+FFFD 是源数据本身坏了，削掉等于篡改内容。
		if trimmed := strings.TrimRight(out, "\uFFFD"); trimmed != "" {
			out = trimmed
		}
		lines[i] = out
		changed = true
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "")
}
