//go:build windows

package platform

import (
	"testing"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

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
		t.Fatal("WithClipboard 未能打开剪贴板")
	}

	if got != want {
		t.Fatalf("读回内容不一致: got %q, want %q", got, want)
	}
}
