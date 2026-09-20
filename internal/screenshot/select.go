package screenshot

import "fmt"

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

	// 先抓屏、后显示覆盖层 —— 顺序不能反，否则覆盖层自身会被抓进图里。
	src, err := captureRect(bounds)
	if err != nil {
		return Result{}, err
	}
	src.ensureOpaque()

	result := make(chan outcome, 1)
	o.Show(src, bounds, result)

	res, ok := <-result
	if !ok || res.rect.Empty() || res.image.Empty() {
		return Result{Action: ActionCancel}, nil
	}
	return Result{Action: res.action, Rect: res.rect, Bitmap: res.image}, nil
}
