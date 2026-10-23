//go:build windows

package platform

import (
	"testing"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

// 注意：本文件里的「打不开剪贴板」一律 Skip 而不是 Fatal。
// 剪贴板是全系统独占的，本应用运行时它自己的捕获协程会在每次写入后立刻抢读，
// 测试进程可能连续落败——那属于环境竞争，不是读路径回归。内容读错仍会让
// 调用方的断言失败，只有「始终打不开」才跳过。

// TestWithClipboardRoundTrip 守住 WithClipboard 读路径的回归：
// 先经 w32 写入一段文本，再经 WithClipboard 在临界区内读回，内容必须一致。
//
// 「Open/Close 线程绑定」这一不变量本身无法在单测里确定性复现（依赖 Go 调度
// 恰好在临界区中间切换线程），这里至少保证重构后的读路径行为不回退——
// 尤其是闭包内 snap 填充与临界区进出没有被写坏。
func TestWithClipboardRoundTrip(t *testing.T) {
	const want = "quickdock-clipboard-round-trip"

	if err := w32.SetClipboardText(want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}

	var got string
	if !WithClipboard(0, func() {
		h := w32.GetClipboardData(13) // CF_UNICODETEXT
		if h == 0 {
			return
		}
		ptr := w32.GlobalLock(h)
		if ptr == nil {
			return
		}
		defer w32.GlobalUnlock(h)

		sz := globalSize(uintptr(h))
		if sz == 0 {
			return
		}
		buf := unsafe.Slice((*uint16)(ptr), int(sz)/2)
		for i := 0; i < len(buf); i++ {
			if buf[i] == 0 {
				buf = buf[:i]
				break
			}
		}
		got = string(utf16.Decode(buf))
	}) {
		t.Skip("剪贴板被其他进程占用（如正在运行的 QuickDock），跳过本次断言")
	}

	if got != want {
		t.Fatalf("读回内容不一致: got %q, want %q", got, want)
	}
}

// TestSetClipboardTextRoundTrip 守住自有写入路径（SetClipboardText）的回归：
// 它替代了 wails 的 app.Clipboard.SetText（后者把写入同步派发到主线程，且错误分支
// 会把剪贴板留在打开状态）。写入后必须能在临界区内原样读回。
//
// 同时覆盖中文：旧实现里的转码/字节序错误会先把内容写坏，再由「点了复制没反应」暴露。
func TestSetClipboardTextRoundTrip(t *testing.T) {
	const want = "quickdock-复制-文本-a\nb"

	if err := SetClipboardText(0, want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	if got := readClipboardTextForTest(t); got != want {
		t.Fatalf("读回内容不一致: got %q, want %q", got, want)
	}

	// 空串也是合法内容：必须真的清掉剪贴板文本，而不是报错或保留旧值。
	if err := SetClipboardText(0, ""); err != nil {
		t.Fatalf("SetClipboardText(空串): %v", err)
	}
	if got := readClipboardTextForTest(t); got != "" {
		t.Fatalf("空串写入后仍读到内容: %q", got)
	}
}

// readClipboardTextForTest 在 WithClipboard 临界区内读回 CF_UNICODETEXT。
func readClipboardTextForTest(t *testing.T) string {
	t.Helper()
	var got string
	if !WithClipboard(0, func() {
		h := w32.GetClipboardData(13) // CF_UNICODETEXT
		if h == 0 {
			return
		}
		ptr := w32.GlobalLock(h)
		if ptr == nil {
			return
		}
		defer w32.GlobalUnlock(h)
		sz := globalSize(uintptr(h))
		if sz == 0 {
			return
		}
		buf := unsafe.Slice((*uint16)(ptr), int(sz)/2)
		for i := 0; i < len(buf); i++ {
			if buf[i] == 0 {
				buf = buf[:i]
				break
			}
		}
		got = string(utf16.Decode(buf))
	}) {
		t.Skip("剪贴板被其他进程占用（如正在运行的 QuickDock），跳过本次断言")
	}
	return got
}

// TestClipboardSequenceNumberAdvances 守住兜底轮询依赖的判据：
// 每次写入剪贴板后 GetClipboardSequenceNumber 必须前进。
//
// 监听（WM_CLIPBOARDUPDATE）一旦失效就再没有任何补救渠道，序号轮询是唯一的
// 兜底——它读错（永远返回 0 或永不变）会让整个兜底机制静默失效，而且不会有
// 任何报错，所以在这里把「会变化」这件事钉住。
func TestClipboardSequenceNumberAdvances(t *testing.T) {
	before := ClipboardSequenceNumber()
	if before == 0 {
		t.Skip("系统未提供剪贴板序号（GetClipboardSequenceNumber 返回 0）")
	}

	if err := SetClipboardText(0, "quickdock-seq-probe-1"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	first := ClipboardSequenceNumber()
	if first == before {
		t.Fatalf("写入剪贴板后序号未变化: %d", before)
	}

	if err := SetClipboardText(0, "quickdock-seq-probe-2"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	second := ClipboardSequenceNumber()
	if second == first {
		t.Fatalf("第二次写入后序号未变化: %d", first)
	}
}

// TestClipboardOwnerResponsive 确认 owner 探测不会误伤正常写入的内容。
// 我们自己写入时 owner 为 NULL，探测必须放行（否则文件/图片读取会被无条件跳过）。
func TestClipboardOwnerResponsive(t *testing.T) {
	if err := SetClipboardText(0, "quickdock-owner-probe"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	var responsive bool
	if !WithClipboard(0, func() {
		responsive = ClipboardOwnerResponsive(200)
	}) {
		t.Skip("剪贴板被其他进程占用（如正在运行的 QuickDock），跳过本次断言")
	}
	if !responsive {
		t.Fatal("对无 owner 的剪贴板应判定为可读，实际判定为不可读")
	}
}
