//go:build windows

package platform

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SHFileOperationW 的操作码与标志位（shellapi.h）
const (
	foDelete          = 0x0003 // FO_DELETE
	fofSilent         = 0x0004 // 不显示进度对话框
	fofNoConfirmation = 0x0010 // 不弹「确认删除」
	fofAllowUndo      = 0x0040 // 移入回收站；没有它等价于永久删除
	fofNoErrorUI      = 0x0400 // 出错也不弹系统错误框（由调用方拿到错误码决定怎么报）
)

// GetDriveTypeW 返回值（部分）
const (
	driveRemovable = 2 // DRIVE_REMOVABLE：U 盘等
	driveRemote    = 4 // DRIVE_REMOTE：网络驱动器 / UNC
	driveCDROM     = 5 // DRIVE_CDROM
)

// shFileOpStructW 对应 SHFILEOPSTRUCTW。
//
// 字段顺序与 C 定义一致，Go 的对齐规则在 32/64 位下与 MSVC 一致
// （UINT 后补 4 字节、BOOL 后补 4 字节以满足指针的对齐要求），
// 因此可直接取地址传给 SHFileOperationW。
type shFileOpStructW struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

// moveToTrash 用 SHFileOperationW + FOF_ALLOWUNDO 把路径移入回收站。
//
// 两个必须在调用前挡掉的坑：
//  1. **FOF_ALLOWUNDO 不是在所有卷上都成立。** 可移动介质、网络驱动器、光驱上的
//     删除会被直接执行且不可恢复——只靠这个标志，插件以为进了回收站，实际已永久丢失。
//     所以先查卷类型，这些卷直接拒绝。
//  2. **SHFileOperationW 不接受 \\?\ 长路径前缀**，超长路径会失败而不是走长路径分支，
//     因此超过 MAX_PATH(260) 时明确报错，不留一个「静默失败」的窗口。
func moveToTrash(abs string) error {
	switch driveTypeOf(abs) {
	case driveRemovable, driveRemote, driveCDROM:
		return fmt.Errorf("拒绝删除 %s：该卷（可移动介质/网络驱动器/光驱）上的删除不可恢复，已拒绝", abs)
	}
	// 按 UTF-16 码元计数：MAX_PATH 限制的是它，不是字节数（中文路径会差很多）
	if n := len(utf16.Encode([]rune(abs))); n > 259 {
		return fmt.Errorf("拒绝删除 %s：路径长度 %d 超过 MAX_PATH，无法安全移入回收站", abs, n)
	}

	// pFrom 是 PCZZWSTR：必须以**两个** NUL 结尾。不能用 UTF16PtrFromString——
	// 它拒绝含 NUL 的字符串，且只补一个结尾。
	buf := append(utf16.Encode([]rune(abs)), 0, 0)
	from := &buf[0]
	op := shFileOpStructW{
		wFunc:  foDelete,
		pFrom:  from,
		fFlags: fofAllowUndo | fofNoConfirmation | fofNoErrorUI | fofSilent,
	}
	ret, _, _ := modShell32.NewProc("SHFileOperationW").Call(uintptr(unsafe.Pointer(&op)))
	if ret != 0 {
		// 返回值是 Win32 错误码（非 HRESULT），失败时 fAnyOperationsAborted 无意义
		return fmt.Errorf("移入回收站失败: %w (code=0x%x)", syscall.Errno(ret), uint32(ret))
	}
	if op.fAnyOperationsAborted != 0 {
		return fmt.Errorf("移入回收站被中止: %s", abs)
	}
	return nil
}

// driveTypeOf 返回路径所在卷的类型；无法判定时返回 0（未知），
// 调用方只对已知的危险类型做拦截，未知不拦（避免误伤正常磁盘）。
func driveTypeOf(abs string) uint32 {
	vol := filepath.VolumeName(abs)
	root := vol + `\`
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0
	}
	ret, _, _ := modKernel32.NewProc("GetDriveTypeW").Call(uintptr(unsafe.Pointer(p)))
	return uint32(ret)
}
