//go:build windows

package screenshot

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"quickdock/internal/logger"
)

// 本文件只做一件事：探一次「DXGI Desktop Duplication 在这台机器上能不能用」，
// 结果写进日志。**不参与抓屏**，现有 BitBlt 路径一行都不受影响。
//
// 为什么要单独探：双屏 4K 上 BitBlt 实测稳定在 225ms（30.3 ms/百万像素），去掉
// CAPTUREBLT、两线程/四线程分块、面积缩到 40% 都动不了它——地板在驱动把合成结果
// 回读到内存。DDA 是这条路上唯一的替代品，但它依赖「真实硬件适配器 + 每个输出
// 单独复制」，从注册表或显卡型号都猜不出来，只能真调一次 DuplicateOutput。
//
// 为什么按适配器枚举而不是只看默认设备：Optimus / 双显卡机器上，笔记本内屏和外接
// 屏可能分别挂在两块显卡的输出上。只看默认适配器会得出「只有 1 个输出」的结论，
// 而那恰好是最需要搞清楚的场景（截图覆盖整个虚拟桌面）。
var (
	d3d11 = syscall.NewLazyDLL("d3d11.dll")
	dxgi  = syscall.NewLazyDLL("dxgi.dll")

	procD3D11CreateDevice  = d3d11.NewProc("D3D11CreateDevice")
	procCreateDXGIFactory1 = dxgi.NewProc("CreateDXGIFactory1")

	iidIDXGIFactory1 = guid{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidIDXGIOutput1  = guid{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
)

const (
	// D3D11CreateDevice 的 DriverType：指定了显式适配器时必须用 UNKNOWN。
	d3dDriverTypeUnknown = 0
	d3d11SDKVersion      = 7
	// D3D11_CREATE_DEVICE_BGRA_SUPPORT：DDA 交出来的纹理是 BGRA，设备不带这个
	// 标志时 DuplicateOutput 直接 E_INVALIDARG，会被误判成「不支持」。
	d3d11CreateBGRA = 0x20

	// DXGI_ERROR_NOT_FOUND 作为 int32 是负数，按位模式存成 uint32 再比，
	// 免得每处比较都来回转换。
	dxgiErrorNotFound = uint32(0x887A0002)

	// vtable 下标，均相对 IUnknown 的第 0 项。IDXGIObject 占 3..6，
	// 各接口自己的方法从 7 开始。
	idxQueryInterface  = 0
	idxRelease         = 2
	idxFactoryAdapters = 7  // IDXGIFactory::EnumAdapters
	idxAdapterOutputs  = 7  // IDXGIAdapter::EnumOutputs
	idxAdapterGetDesc  = 8  // IDXGIAdapter::GetDesc
	idxDuplicateOutput = 22 // IDXGIOutput1::DuplicateOutput
	// IDXGIOutputDuplication : IDXGIObject，GetDesc(7) 之后就是取帧相关的四个。
	idxAcquireNextFrame = 8
	idxReleaseFrame     = 11

	dxgiErrorWaitTimeout = uint32(0x887A0027)
	// ACCESS_LOST：复制对象失效（桌面切换、别的截图工具抢了、锁屏）。
	// 常见于「第一次取帧」，重开一次复制往往就好了。
	dxgiErrorAccessLost = uint32(0x887A0026)

	// 取帧探测的超时。100ms 足够让 DWM 产出一帧（60Hz 下 6 帧），又不至于让
	// 日志延迟明显——这个探测整个进程只做一次。
	acquireTimeoutMs = 100

	// 适配器/输出枚举上限，防止驱动异常时无限循环。真机不会超过 4 个适配器、
	// 8 个输出。
	maxEnumerated = 8

	vendorMicrosoft = 0x1414
	deviceWARP      = 0x008C // Microsoft Basic Render Driver
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// dxgiAdapterDesc 对应 DXGI_ADAPTER_DESC。
type dxgiAdapterDesc struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLuid           int64
}

// probeOnce 保证整个进程只探一次：DDA 可用性跟硬件绑定，中途接个显示器也不会
// 换一块显卡，重复探测没有意义。
var probeOnce sync.Once

// logRenderCapability 探测并打印一行渲染能力日志。由 finish() 在首次会话结束时
// 异步拉起——那会儿用户已经截完图，探测产生的几十毫秒 GPU 调用不会被感知到。
func logRenderCapability() {
	probeOnce.Do(func() { go logDesktopDuplication() })
}

func logDesktopDuplication() {
	defer func() {
		// 探测纯属附带信息，任何异常都不该把截图功能带下去。
		_ = recover()
	}()

	var factory unsafe.Pointer
	hr, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&iidIDXGIFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if int32(hr) < 0 || factory == nil {
		logger.W("[screenshot] 渲染能力：CreateDXGIFactory1 失败（hr=%#x），Desktop Duplication 不可用，抓屏继续走 BitBlt", uint32(hr))
		return
	}
	defer comRelease(factory)

	var (
		parts    []string
		outputs  int
		dupes    int
		adapters int
		level    = "未知"
		acqFail  string
		tested   bool // 取帧探测只做一次，别让每个输出白等 100ms
	)

	for ai := 0; ai < maxEnumerated; ai++ {
		var adapter unsafe.Pointer
		if h := comCall(factory, idxFactoryAdapters, uintptr(ai), uintptr(unsafe.Pointer(&adapter))); int32(h) < 0 || adapter == nil {
			break
		}
		adapters++

		var desc dxgiAdapterDesc
		comCall(adapter, idxAdapterGetDesc, uintptr(unsafe.Pointer(&desc)))
		name := utf16Z(desc.Description[:])
		if name == "" {
			name = "未知适配器"
		}
		if desc.VendorID == vendorMicrosoft && desc.DeviceID == deviceWARP {
			name += "(软件适配器)"
		}

		aOut, aOK, aFail := probeAdapterOutputs(adapter, &level, &tested, &acqFail)
		outputs += aOut
		dupes += aOK

		entry := fmt.Sprintf("%s %d/%d", name, aOK, aOut)
		if aFail != "" {
			entry += "[" + aFail + "]"
		}
		parts = append(parts, entry)

		comRelease(adapter)
	}

	summary := fmt.Sprintf("%d 个适配器：%s；feature level %s", adapters, strings.Join(parts, "，"), level)
	if acqFail != "" {
		// 能复制但取不到帧，等于不能用：日志里必须说清是哪一步挂的，
		// 否则下次看到「1/1 可复制」会误以为有戏。
		logger.W("[screenshot] 渲染能力：Desktop Duplication 不可用（%s）—— %s，抓屏继续走 BitBlt", acqFail, summary)
		return
	}
	switch {
	case adapters == 0:
		logger.W("[screenshot] 渲染能力：Desktop Duplication 不可用（没有枚举到适配器），抓屏继续走 BitBlt")
	case dupes > 0:
		logger.I("[screenshot] 渲染能力：Desktop Duplication 可用（%d/%d 个输出可复制）—— %s", dupes, outputs, summary)
	default:
		logger.W("[screenshot] 渲染能力：Desktop Duplication 不可用（%d 个输出全部不可复制）—— %s，抓屏继续走 BitBlt", outputs, summary)
	}
}

// probeAdapterOutputs 在一块适配器上枚举输出并逐个试复制。
// 返回「输出数 / 可复制数 / 首个失败原因」。tested 与 acqFail 是跨适配器的共享
// 状态：取帧探测整个进程只做一次，失败结论对全局有效。
func probeAdapterOutputs(adapter unsafe.Pointer, level *string, tested *bool, acqFail *string) (outputs, duplicable int, firstFail string) {
	var dev unsafe.Pointer
	var fl uint32
	hr, _, _ := procD3D11CreateDevice.Call(
		uintptr(adapter),
		d3dDriverTypeUnknown, // 指定了显式适配器就必须用 UNKNOWN
		0,                    // 无软件光栅
		d3d11CreateBGRA,      // 见常量注释
		0, 0,                 // 默认 feature level 列表
		d3d11SDKVersion,
		uintptr(unsafe.Pointer(&dev)),
		uintptr(unsafe.Pointer(&fl)),
		0, // 不需要立即上下文
	)
	if int32(hr) < 0 || dev == nil {
		return 0, 0, fmt.Sprintf("建 D3D11 设备失败 hr=%#x", uint32(hr))
	}
	defer comRelease(dev)
	if n := featureLevelName(fl); n != "未知" {
		*level = n
	}

	for oi := 0; oi < maxEnumerated; oi++ {
		var out unsafe.Pointer
		if h := comCall(adapter, idxAdapterOutputs, uintptr(oi), uintptr(unsafe.Pointer(&out))); int32(h) < 0 || out == nil {
			break
		}
		outputs++

		var out1 unsafe.Pointer
		if h := comCall(out, idxQueryInterface, uintptr(unsafe.Pointer(&iidIDXGIOutput1)), uintptr(unsafe.Pointer(&out1))); int32(h) < 0 || out1 == nil {
			if firstFail == "" {
				firstFail = fmt.Sprintf("输出%d 不支持 IDXGIOutput1", oi)
			}
			comRelease(out)
			continue
		}

		var dup unsafe.Pointer
		dh := comCall(out1, idxDuplicateOutput, uintptr(dev), uintptr(unsafe.Pointer(&dup)))
		if int32(dh) < 0 || dup == nil {
			if firstFail == "" {
				firstFail = fmt.Sprintf("输出%d hr=%#x", oi, uint32(dh))
			}
			comRelease(out1)
			comRelease(out)
			continue
		}

		if !*tested {
			*tested = true
			hr, ok, why := testDuplicateCycle(dup)
			// ACCESS_LOST 常常是一次性的（桌面切换、别的截图工具刚释放）。
			// 直接下「不可用」会误判，重开一次复制再看。
			if !ok && hr == dxgiErrorAccessLost {
				comRelease(dup)
				time.Sleep(80 * time.Millisecond)
				dup = nil
				if h := comCall(out1, idxDuplicateOutput, uintptr(dev), uintptr(unsafe.Pointer(&dup))); int32(h) >= 0 && dup != nil {
					hr, ok, why = testDuplicateCycle(dup)
				} else {
					why = fmt.Sprintf("重建复制失败 hr=%#x", uint32(h))
				}
			}
			if ok {
				duplicable++
			} else if *acqFail == "" {
				*acqFail = why
			}
		} else {
			// 已经测过一轮，其余输出只要复制成功就按同一结论处理。
			duplicable++
		}
		if dup != nil {
			comRelease(dup)
		}

		comRelease(out1)
		comRelease(out)
	}
	return outputs, duplicable, firstFail
}

// testDuplicateCycle 在复制对象上跑一遍「取帧 → 释放 → 再取帧」。
//
// 为什么不是只取一帧就下结论：本机实测 DuplicateOutput 成功、第一次
// AcquireNextFrame 也返回 S_OK，但 ReleaseFrame 被驱动拒绝（E_INVALIDARG），
// 于是第二次取帧立刻变成 DXGI_ERROR_INVALID_CALL——真正的抓屏循环第一步就跑不下去。
// 只看第一次取帧会得出「可用」的错误结论，而按它去写抓屏路径就是全量失败。
//
// 判据：第二次取帧能成功（拿到帧或 WAIT_TIMEOUT 都算）才算可用。
func testDuplicateCycle(dup unsafe.Pointer) (hr uint32, ok bool, why string) {
	h1, ok1 := acquireFrame(dup)
	if !ok1 {
		return h1, false, fmt.Sprintf("首次取帧失败 hr=%#x", h1)
	}
	relH := uint32(comCall(dup, idxReleaseFrame))
	h2, ok2 := acquireFrame(dup)
	if !ok2 {
		if relH != 0 {
			return h2, false, fmt.Sprintf("ReleaseFrame 被拒 hr=%#x，释放后取帧 hr=%#x", relH, h2)
		}
		return h2, false, fmt.Sprintf("释放后再次取帧失败 hr=%#x", h2)
	}
	comCall(dup, idxReleaseFrame)
	return 0, true, ""
}

// acquireFrame 取一帧并立刻放掉资源。返回 (hr, 是否正常)。
// DXGI_ERROR_WAIT_TIMEOUT 算正常——那表示管线通着，只是这段时间屏幕没变化。
func acquireFrame(dup unsafe.Pointer) (uint32, bool) {
	// DXGI_OUTDUPL_FRAME_INFO 的真实布局曾经坑过一次（漏了 PointerPosition.Visible
	// 导致结构体比实际小、被写穿栈）。这里不解释内容，给足空间就行。
	var info [128]byte
	var res unsafe.Pointer
	h := comCall(dup, idxAcquireNextFrame,
		acquireTimeoutMs,
		uintptr(unsafe.Pointer(&info[0])),
		uintptr(unsafe.Pointer(&res)),
	)
	if res != nil {
		comRelease(res)
	}
	switch {
	case int32(h) >= 0:
		return uint32(h), true
	case uint32(h) == dxgiErrorWaitTimeout:
		return uint32(h), true
	}
	return uint32(h), false
}

// comCall 按下标调用 COM 方法：p 是接口指针，第 0 项永远是 QueryInterface。
//
// 接口指针全程用 unsafe.Pointer 而不是 uintptr：uintptr→unsafe.Pointer 的转换会
// 被 go vet 判为 possible misuse（GC 期间地址可能失效），unsafe.Pointer 还能让
// 运行时知道这里引用着对象。
func comCall(p unsafe.Pointer, idx int, args ...uintptr) uintptr {
	// 接口指针指向的对象，第一个字段才是虚表指针，所以要先解一层。
	vtbl := (*[1 << 16]uintptr)(*(*unsafe.Pointer)(p))
	fn := vtbl[idx]
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, uintptr(p))
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return r
}

func comRelease(p unsafe.Pointer) {
	if p != nil {
		comCall(p, idxRelease)
	}
}

// utf16Z 读以 NUL 结尾的宽字符串。syscall.UTF16ToString 要求切片里没有 NUL，
// 而这里拿到的是定长数组，必须先截断。
func utf16Z(s []uint16) string {
	for i, v := range s {
		if v == 0 {
			return syscall.UTF16ToString(s[:i])
		}
	}
	return syscall.UTF16ToString(s)
}

// featureLevelName 只用于日志可读性：0xA000/0xB000 这类数值在排障时没人想翻译。
func featureLevelName(l uint32) string {
	switch l {
	case 0xB100:
		return "11_1"
	case 0xB000:
		return "11_0"
	case 0xA100:
		return "10_1"
	case 0xA000:
		return "10_0"
	case 0x9300:
		return "9_3"
	case 0x9200:
		return "9_2"
	case 0x9100:
		return "9_1"
	case 0:
		return "未知"
	}
	return fmt.Sprintf("%#x", l)
}
