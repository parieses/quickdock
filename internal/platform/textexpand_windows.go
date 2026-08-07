//go:build windows

package platform

import (
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// Text Expansion（片段自动展开）：通过全局低级键盘钩子捕获用户输入，
// 当输入某个已启用片段的关键词并以空格/制表符结束时，自动用片段内容替换。
//
// 设计要点：
//   - 仅在安装钩子的 goroutine 上运行消息循环（WH_KEYBOARD_LL 要求安装线程泵消息）。
//   - 维护“当前单词”缓冲区（切换前台窗口 / 按下非字符键时重置）。
//   - 命中关键词时：吞掉终止符（回调返回 1，不下发给目标窗口），改由异步 goroutine
//     发送 len(keyword) 个退格 + 展开内容 + 补回终止符。
//     必须吞掉终止符，否则钩子回调先于按键送达执行，退格数会算错并多删一个字符。
//   - 展开一律异步：低级钩子回调有 LowLevelHooksTimeout（默认 300ms）限制，
//     在回调里同步 SendInput 长文本会被系统静默摘钩。
//   - 注入的事件通过 dwExtraInfo 魔法值标记，钩子回调对其跳过，避免自己触发自己。
//   - 在 QuickDock 自身窗口内不展开，避免干扰片段编辑。
//
// 注：所依赖的 Win32 API 在 x/sys/windows@v0.43.0 中无高级封装，故一律走 LazyProc 调用。

const (
	teWHKeyboardLL     = 13
	teVKBack           = 0x08
	teVKTab            = 0x09
	teVKShift          = 0x10
	teVKControl        = 0x11
	teVKMenu           = 0x12
	teVKCapital        = 0x14
	teVKSpace          = 0x20
	teVKLWin           = 0x5B
	teVKRWin           = 0x5C
	teLLKHFInjected    = 0x10
	teLLKHFUp          = 0x80
	teKeyEventfKeyup   = 0x0002
	teKeyEventfUnicode = 0x0004
	teMagicExtraInfo   = 0x51544458 // "QTDX"
	teMaxWord          = 64
	teWMQuit           = 0x0012
	// ToUnicode 的 wFlags bit2：不修改键盘状态（Win10 1607+），
	// 避免钩子里调用 ToUnicode 破坏目标程序的死键组合。
	teToUnicodeNoState = 0x04
)

var (
	teProcSetWindowsHookExW        = modUser32.NewProc("SetWindowsHookExW")
	teProcCallNextHookEx           = modUser32.NewProc("CallNextHookEx")
	teProcUnhookWindowsHookEx      = modUser32.NewProc("UnhookWindowsHookEx")
	teProcGetMessageW              = modUser32.NewProc("GetMessageW")
	teProcTranslateMessage         = modUser32.NewProc("TranslateMessage")
	teProcDispatchMessageW         = modUser32.NewProc("DispatchMessageW")
	teProcPostThreadMessageW       = modUser32.NewProc("PostThreadMessageW")
	teProcToUnicode                = modUser32.NewProc("ToUnicode")
	teProcGetAsyncKeyState         = modUser32.NewProc("GetAsyncKeyState")
	teProcGetKeyState              = modUser32.NewProc("GetKeyState")
	teProcGetForegroundWindow      = modUser32.NewProc("GetForegroundWindow")
	teProcGetWindowThreadProcessId = modUser32.NewProc("GetWindowThreadProcessId")
	teProcGetCurrentProcessId      = modKernel32.NewProc("GetCurrentProcessId")
	teProcGetCurrentThreadId       = modKernel32.NewProc("GetCurrentThreadId")
	teSendInputProc                = modUser32.NewProc("SendInput")
)

type teKbdLLHookStruct struct {
	vkCode      uint32
	scanCode    uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

type teKeybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type teInput struct {
	typ uint32
	ki  teKeybdInput
}

// teMsg 仅作为 GetMessageW 的缓冲区，不读取字段。amd64 下 MSG 为 48 字节。
type teMsg struct {
	_ [48]byte
}

type teState struct {
	mu             sync.Mutex
	installed      bool
	hook           uintptr
	threadID       uint32
	snippets       map[string]string // keyword -> raw content
	currentWord    []rune
	lastForeground uintptr
	ourPID         uint32
}

var (
	teGlobal teState
	// 回调只创建一次：syscall.NewCallback 分配的 trampoline 永不释放，
	// 反复 Start/Stop 会耗尽运行时上限。
	teCallbackOnce sync.Once
	teCallbackPtr  uintptr
	// 展开进行中标记，防止并发展开互相打架
	teExpanding atomic.Bool
)

// teVarResolver 在展开时解析片段内的占位符（{date}{time}{clipboard} 等）。
// 由 services 层注入（避免 platform 反向依赖 services）。
var teVarResolver func(string) string

// TextExpansionSetResolver 设置变量解析器（services 启动时调用一次）。
func TextExpansionSetResolver(fn func(string) string) {
	teVarResolver = fn
}

// TextExpansionSetSnippets 更新当前生效的片段映射（热更新，无需重装钩子）。
func TextExpansionSetSnippets(snippets map[string]string) {
	teGlobal.mu.Lock()
	defer teGlobal.mu.Unlock()
	teGlobal.snippets = snippets
}

// TextExpansionStart 安装键盘钩子并启动消息循环 goroutine。幂等。
func TextExpansionStart(snippets map[string]string) {
	teGlobal.mu.Lock()
	if teGlobal.installed {
		teGlobal.snippets = snippets
		teGlobal.mu.Unlock()
		return
	}
	teGlobal.snippets = snippets
	teGlobal.currentWord = teGlobal.currentWord[:0]
	pid, _, _ := teProcGetCurrentProcessId.Call()
	teGlobal.ourPID = uint32(pid)
	teGlobal.mu.Unlock()

	teCallbackOnce.Do(func() {
		teCallbackPtr = syscall.NewCallback(teKeyboardProc)
	})

	go func() {
		// 钩子回调在安装线程上被调用，必须锁定 OS 线程
		// 否则 Go 调度器换线程后消息循环与钩子不在同一线程。
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hook, _, _ := teProcSetWindowsHookExW.Call(uintptr(teWHKeyboardLL), teCallbackPtr, 0, 0)
		if hook == 0 {
			return
		}
		tid, _, _ := teProcGetCurrentThreadId.Call()

		teGlobal.mu.Lock()
		teGlobal.hook = hook
		teGlobal.threadID = uint32(tid)
		teGlobal.installed = true
		teGlobal.mu.Unlock()

		var msg teMsg
		for {
			ret, _, _ := teProcGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			// GetMessage: 0 = WM_QUIT, -1 = 错误。uintptr 无法直接比 -1，转 int32。
			if ret == 0 || int32(uint32(ret)) == -1 {
				break
			}
			teProcTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			teProcDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}

		teProcUnhookWindowsHookEx.Call(hook)
		teGlobal.mu.Lock()
		teGlobal.installed = false
		teGlobal.hook = 0
		teGlobal.threadID = 0
		teGlobal.mu.Unlock()
	}()
}

// TextExpansionStop 卸载钩子并停止消息循环。
// 必须向钩子线程 PostThreadMessage(WM_QUIT)，否则它会一直阻塞在 GetMessageW 上。
func TextExpansionStop() {
	teGlobal.mu.Lock()
	tid := teGlobal.threadID
	installed := teGlobal.installed
	teGlobal.mu.Unlock()
	if !installed || tid == 0 {
		return
	}
	teProcPostThreadMessageW.Call(uintptr(tid), teWMQuit, 0, 0)
}

// TextExpansionEnabled 返回钩子当前是否已安装。
func TextExpansionEnabled() bool {
	teGlobal.mu.Lock()
	defer teGlobal.mu.Unlock()
	return teGlobal.installed
}

// teKeyboardProc 低级键盘钩子回调（stdcall）。返回非 0 表示吞掉该按键。
func teKeyboardProc(code int, wparam uintptr, lparam uintptr) uintptr {
	if code < 0 {
		return teNextHook(code, wparam, lparam)
	}

	kbd := (*teKbdLLHookStruct)(unsafe.Pointer(lparam))

	// 跳过自己注入的事件 / 其他程序注入的事件 / 按键抬起
	if kbd.dwExtraInfo == teMagicExtraInfo || (kbd.flags&teLLKHFInjected) != 0 || (kbd.flags&teLLKHFUp) != 0 {
		return teNextHook(code, wparam, lparam)
	}
	// 仅处理 WM_KEYDOWN / WM_SYSKEYDOWN
	if wparam != 0x0100 && wparam != 0x0104 {
		return teNextHook(code, wparam, lparam)
	}
	// 展开进行中：不再累积，避免自触发
	if teExpanding.Load() {
		return teNextHook(code, wparam, lparam)
	}

	fg, _, _ := teProcGetForegroundWindow.Call()

	// 在自身窗口内不展开（片段编辑框里打关键词不应被替换）
	if teIsOwnWindow(fg) {
		teResetWord()
		return teNextHook(code, wparam, lparam)
	}

	vk := kbd.vkCode

	// Ctrl / Alt 组合键属于快捷键，不是文本输入：清空缓冲
	if teKeyDown(teVKControl) || teKeyDown(teVKMenu) {
		teResetWord()
		return teNextHook(code, wparam, lparam)
	}

	teGlobal.mu.Lock()
	snippets := teGlobal.snippets
	if fg != teGlobal.lastForeground {
		teGlobal.lastForeground = fg
		teGlobal.currentWord = teGlobal.currentWord[:0]
	}

	switch vk {
	case teVKBack:
		if n := len(teGlobal.currentWord); n > 0 {
			teGlobal.currentWord = teGlobal.currentWord[:n-1]
		}
		teGlobal.mu.Unlock()
		return teNextHook(code, wparam, lparam)

	case teVKSpace, teVKTab:
		word := string(teGlobal.currentWord)
		teGlobal.currentWord = teGlobal.currentWord[:0]
		teGlobal.mu.Unlock()
		if word != "" && snippets != nil {
			if content, ok := snippets[word]; ok && teExpanding.CompareAndSwap(false, true) {
				isTab := vk == teVKTab
				go func() {
					defer teExpanding.Store(false)
					teExpand(word, content, isTab)
				}()
				// 吞掉终止符，由 teExpand 在展开后补发
				return 1
			}
		}
		return teNextHook(code, wparam, lparam)

	case teVKShift, teVKControl, teVKMenu, teVKCapital, teVKLWin, teVKRWin:
		// 修饰键：保留缓冲
		teGlobal.mu.Unlock()
		return teNextHook(code, wparam, lparam)
	}

	if ch := teToChar(vk, kbd.scanCode); ch != 0 {
		if len(teGlobal.currentWord) < teMaxWord {
			teGlobal.currentWord = append(teGlobal.currentWord, ch)
		}
	} else {
		// 回车 / 方向键 / Esc / Delete 等导航键：单词边界已断，清空
		teGlobal.currentWord = teGlobal.currentWord[:0]
	}
	teGlobal.mu.Unlock()
	return teNextHook(code, wparam, lparam)
}

func teNextHook(code int, wparam, lparam uintptr) uintptr {
	r, _, _ := teProcCallNextHookEx.Call(0, uintptr(code), wparam, lparam)
	return r
}

func teResetWord() {
	teGlobal.mu.Lock()
	teGlobal.currentWord = teGlobal.currentWord[:0]
	teGlobal.mu.Unlock()
}

// teKeyDown 用 GetAsyncKeyState 判断按键是否按下。
// 低级钩子回调里 GetKeyboardState 反映的是本线程消息队列状态，不可靠。
func teKeyDown(vk uint32) bool {
	r, _, _ := teProcGetAsyncKeyState.Call(uintptr(vk))
	return uint16(r)&0x8000 != 0
}

// teToChar 用 ToUnicode 把 vkCode 翻译为字符（考虑 Shift / CapsLock）。
// 返回 0 表示非字符键或死键（不累积）。
func teToChar(vk, scan uint32) rune {
	// 手工构造键盘状态：钩子里 GetKeyboardState 拿到的是过期的线程队列状态
	var state [256]byte
	if teKeyDown(teVKShift) {
		state[teVKShift] = 0x80
	}
	if r, _, _ := teProcGetKeyState.Call(uintptr(teVKCapital)); r&0x0001 != 0 {
		state[teVKCapital] = 0x01
	}

	var buf [8]uint16
	n, _, _ := teProcToUnicode.Call(
		uintptr(vk), uintptr(scan),
		uintptr(unsafe.Pointer(&state[0])),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		teToUnicodeNoState,
	)
	cnt := int32(uint32(n))
	if cnt <= 0 || cnt > int32(len(buf)) {
		return 0
	}
	runes := utf16.Decode(buf[:cnt])
	if len(runes) != 1 {
		return 0
	}
	if runes[0] < 0x20 {
		return 0
	}
	return runes[0]
}

// teExpand 执行替换：删掉已输入的关键词 → 注入展开内容 → 补回被吞掉的终止符。
// 必须在钩子回调之外（goroutine）调用。
func teExpand(keyword, content string, tabTerminator bool) {
	if teVarResolver != nil {
		content = teVarResolver(content)
	}
	// 让目标窗口先把关键词的最后一个按键处理完，避免退格跑到前面
	time.Sleep(20 * time.Millisecond)

	teSendBackspaces(len([]rune(keyword)))
	if content != "" {
		teSendUnicodeText(content)
	}
	if tabTerminator {
		teSendKey(teVKTab)
	} else {
		teSendUnicodeText(" ")
	}
}

func teSendBackspaces(n int) {
	if n <= 0 {
		return
	}
	inputs := make([]teInput, 0, n*2)
	for i := 0; i < n; i++ {
		inputs = append(inputs, teInput{typ: 1, ki: teKeybdInput{wVk: teVKBack, dwExtraInfo: teMagicExtraInfo}})
		inputs = append(inputs, teInput{typ: 1, ki: teKeybdInput{wVk: teVKBack, dwFlags: teKeyEventfKeyup, dwExtraInfo: teMagicExtraInfo}})
	}
	teSendInputProc.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(inputs[0])))
}

func teSendKey(vk uint16) {
	inputs := []teInput{
		{typ: 1, ki: teKeybdInput{wVk: vk, dwExtraInfo: teMagicExtraInfo}},
		{typ: 1, ki: teKeybdInput{wVk: vk, dwFlags: teKeyEventfKeyup, dwExtraInfo: teMagicExtraInfo}},
	}
	teSendInputProc.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(inputs[0])))
}

func teSendUnicodeText(text string) {
	units := utf16.Encode([]rune(text))
	if len(units) == 0 {
		return
	}
	inputs := make([]teInput, 0, len(units)*2)
	for _, u := range units {
		inputs = append(inputs, teInput{typ: 1, ki: teKeybdInput{wScan: u, dwFlags: teKeyEventfUnicode, dwExtraInfo: teMagicExtraInfo}})
		inputs = append(inputs, teInput{typ: 1, ki: teKeybdInput{wScan: u, dwFlags: teKeyEventfUnicode | teKeyEventfKeyup, dwExtraInfo: teMagicExtraInfo}})
	}
	teSendInputProc.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(inputs[0])))
}

// teIsOwnWindow 判断给定窗口是否属于 QuickDock 自身进程。
func teIsOwnWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	var pid uint32
	teProcGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return false
	}
	teGlobal.mu.Lock()
	ours := teGlobal.ourPID
	teGlobal.mu.Unlock()
	return pid == ours
}
