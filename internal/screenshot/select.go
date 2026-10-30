package screenshot

import (
	"fmt"
	"time"

	"quickdock/internal/logger"
)

// outcome 是覆盖层回传给上层的一次会话结果（包内类型，不对外暴露）。
type outcome struct {
	action Action
	rect   Rect
	image  *Bitmap // 覆盖层已裁剪并合成标注
}

// Select 抓取整个虚拟桌面、在其上进入框选/标注界面，并返回用户确认的结果。
//
// 返回的 Result.Rect 使用虚拟桌面坐标系。用户取消（Esc / 右键 / 关闭）时返回
// 零值 Result 且 err == nil —— 调用方据此区分「取消」与「出错」。
//
// 注意：本函数会阻塞到用户完成或取消。调用方（Wails 绑定方法）应保证它不占用
// 需要继续处理消息的线程：覆盖层的消息泵跑在它自己的 OS 线程上，因此这里阻塞
// 是安全的，但前端会一直 await。
func Select(o *Overlay) (Result, error) {
	if o == nil {
		return Result{}, ErrUnsupported
	}

	bounds := VirtualDesktopBounds()
	if bounds.Empty() {
		return Result{}, fmt.Errorf("screenshot: 虚拟桌面尺寸无效")
	}

	// 启动耗时的起点取在这里：从热键回调进来算起，覆盖层的投递延迟、建资源、
	// 暗化、首帧绘制全都算进「看见遮罩」里，与用户的主观感受口径一致。
	started := time.Now()

	// 先抓屏、后显示覆盖层 —— 顺序不能反，否则覆盖层自身会被抓进图里。
	tCapture := time.Now()
	src, err := captureRect(bounds)
	capture := time.Since(tCapture)
	if err != nil {
		// 失败也记耗时：「按了热键没反应」十有八九是抓屏卡住或报错，
		// 没有这行就只能猜。
		logger.W("[screenshot] 抓屏失败（%v，%d×%d）：%v",
			capture.Round(10*time.Microsecond), bounds.W, bounds.H, err)
		return Result{}, err
	}

	tOpaque := time.Now()
	src.ensureOpaque()
	launch := launchStats{started: started, capture: capture, opaque: time.Since(tOpaque)}

	result := make(chan outcome, 1)
	o.Show(src, bounds, result, launch)

	res, ok := <-result
	if !ok || res.rect.Empty() || res.image.Empty() {
		return Result{Action: ActionCancel}, nil
	}
	return Result{Action: res.action, Rect: res.rect, Bitmap: res.image}, nil
}

// SelectFull 抓取整个虚拟桌面，并把整屏作为已定选区送进覆盖层。
//
// 与 Select 的差别只有「要不要用户先拖一个框」：这里抓完屏工具条就在，
// 用户直接 Enter 复制 / Ctrl+S 保存 / 点贴图。全屏截图走这条路，
// 标注、保存、贴图等能力完全复用框选会话，不另开一条链路。
//
// 取消语义与 Select 一致（返回零值 Result，err == nil）。
func SelectFull(o *Overlay) (Result, error) {
	if o == nil {
		return Result{}, ErrUnsupported
	}

	bounds := VirtualDesktopBounds()
	if bounds.Empty() {
		return Result{}, fmt.Errorf("screenshot: 虚拟桌面尺寸无效")
	}

	started := time.Now()

	tCapture := time.Now()
	src, err := captureRect(bounds)
	capture := time.Since(tCapture)
	if err != nil {
		logger.W("[screenshot] 全屏抓屏失败（%v，%d×%d）：%v",
			capture.Round(10*time.Microsecond), bounds.W, bounds.H, err)
		return Result{}, err
	}

	tOpaque := time.Now()
	src.ensureOpaque()
	launch := launchStats{started: started, capture: capture, opaque: time.Since(tOpaque)}

	result := make(chan outcome, 1)
	o.ShowPreset(src, bounds, bounds, result, launch)

	res, ok := <-result
	if !ok || res.rect.Empty() || res.image.Empty() {
		return Result{Action: ActionCancel}, nil
	}
	return Result{Action: res.action, Rect: res.rect, Bitmap: res.image}, nil
}
