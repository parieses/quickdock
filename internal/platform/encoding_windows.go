//go:build windows

package platform

import (
	"strings"

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

// decodeConsoleOutput 把子进程按控制台代码页写出的字节流转成 UTF-8。
//
// 为什么需要：Windows 控制台程序（cmd / dir / ping / git …）按 OEM 代码页往
// 管道里写字节，中文系统是 936(GBK)。Go 的 string 是 UTF-8，原样收下会把每个
// 中文拆成 U+FFFD——用户跑个 dir，执行结果就是一坨 �，功能等于没有。
//
// 捕获缓冲按字节封顶，可能把多字节序列劈在尾部。GBK 的解码器不报错、直接吐出
// U+FFFD（这些代码页都没有映射到 U+FFFD 的合法序列，正常输出不会以它结尾），
// 故按「结果以 U+FFFD 结尾」判定劈坏，逐字节回退找完整边界。仍不干净则原样
// 返回：真正损坏的输出罕见，而丢结果比留乱码更糟。
func decodeConsoleOutput(s string) string {
	b := []byte(s)
	enc := consoleEncodingByCP(consoleCP())
	if enc == nil {
		return s
	}
	for cut := 0; cut <= 3 && cut <= len(b); cut++ {
		out, _, err := transform.Bytes(enc.NewDecoder(), b[:len(b)-cut])
		if err != nil {
			continue
		}
		if !strings.HasSuffix(string(out), "\uFFFD") {
			return string(out)
		}
	}
	return s
}
