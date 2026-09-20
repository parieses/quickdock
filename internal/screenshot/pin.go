package screenshot

// PinActions 是贴图钉屏右键菜单需要的宿主能力（复制到剪贴板 / 保存为文件）。
//
// 刻意放在这个**没有构建标签**的文件里，而不是 pin_windows.go：
//   - services 层不分平台，它无条件调用 SetPinActions 把两个回调注入进来；
//   - 非 Windows 的占位实现（pin_unix.go）也要引用这个类型。
//
// 之前它立在 pin_windows.go 里，结果是 darwin / linux 下整个包连同 services 都编译不过——
// Windows 单平台构建时暴露不出来，但 mac 发布链路一旦恢复就会立刻炸。
type PinActions struct {
	Copy func(*Bitmap) error
	Save func(*Bitmap) (string, error)
}

var pinActions PinActions

// SetPinActions 注入贴图右键菜单的「复制」「保存」实现。
// 非 Windows 平台上注入后也不会被用到（PinImage 恒返回 false）。
func SetPinActions(a PinActions) { pinActions = a }
