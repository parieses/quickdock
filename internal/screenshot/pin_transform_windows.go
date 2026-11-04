//go:build windows

package screenshot

// 贴图的像素变换与 DIB 资源管理。
//
// 【为什么旋转/翻转直接改像素，而不是存一个变换矩阵】
// 贴图窗口最终只有两个消费方：WM_PAINT 的 StretchBlt、右键菜单的「复制 / 保存」。
// 存矩阵的话这两处都得各自做一次逆变换，还要把裁剪后的坐标换算回去。
// 直接在落菜单的那一刻把 p.img 换成变换后的位图，复制/保存/绘制自动一致，
// 代价只是一次 W×H 的像素搬运（1080p 约 8MB，用户点一下菜单感知不到）。
//
// 代价是「转四次回到原样」会有插值误差——但这里没有插值，四个 90° 旋转
// 是像素的精确置换，转回去逐字节相同。

import "unsafe"

// rotateBitmapCW 顺时针旋转 90°：原图 (sx,sy) → 新图 (H-1-sy, sx)。
// 顺时针旋转后原图左下角落在左上角，用它校验方向最直观。
func rotateBitmapCW(src *Bitmap) *Bitmap {
	if src.Empty() {
		return nil
	}
	dst := &Bitmap{W: src.H, H: src.W, Pix: make([]byte, src.W*src.H*4)}
	rowBytes := dst.W * 4
	for dy := 0; dy < dst.H; dy++ {
		sx := dy
		for dx := 0; dx < dst.W; dx++ {
			sy := src.H - 1 - dx
			s := (sy*src.W + sx) * 4
			d := dy*rowBytes + dx*4
			copy(dst.Pix[d:d+4], src.Pix[s:s+4])
		}
	}
	return dst
}

// rotateBitmapCCW 逆时针旋转 90°：原图 (sx,sy) → 新图 (sy, W-1-sx)。
func rotateBitmapCCW(src *Bitmap) *Bitmap {
	if src.Empty() {
		return nil
	}
	dst := &Bitmap{W: src.H, H: src.W, Pix: make([]byte, src.W*src.H*4)}
	rowBytes := dst.W * 4
	for dy := 0; dy < dst.H; dy++ {
		sx := src.W - 1 - dy
		for dx := 0; dx < dst.W; dx++ {
			sy := dx
			s := (sy*src.W + sx) * 4
			d := dy*rowBytes + dx*4
			copy(dst.Pix[d:d+4], src.Pix[s:s+4])
		}
	}
	return dst
}

// flipBitmapH 左右镜像（水平翻转）。
func flipBitmapH(src *Bitmap) *Bitmap {
	if src.Empty() {
		return nil
	}
	dst := &Bitmap{W: src.W, H: src.H, Pix: make([]byte, src.W*src.H*4)}
	rowBytes := src.W * 4
	for y := 0; y < src.H; y++ {
		for x := 0; x < src.W; x++ {
			s := (y*src.W + src.W - 1 - x) * 4
			d := y*rowBytes + x*4
			copy(dst.Pix[d:d+4], src.Pix[s:s+4])
		}
	}
	return dst
}

// flipBitmapV 上下镜像（垂直翻转）。整行拷贝，比逐像素快一个数量级。
func flipBitmapV(src *Bitmap) *Bitmap {
	if src.Empty() {
		return nil
	}
	dst := &Bitmap{W: src.W, H: src.H, Pix: make([]byte, src.W*src.H*4)}
	rowBytes := src.W * 4
	for y := 0; y < src.H; y++ {
		s := (src.H - 1 - y) * rowBytes
		copy(dst.Pix[y*rowBytes:(y+1)*rowBytes], src.Pix[s:s+rowBytes])
	}
	return dst
}

// makePinDIB 把一张位图拷进一块独立的 32bpp top-down DIB。
//
// 返回一个「资源三元组」而不是直接写进 pin，是为了让变换能先建好新 DIB、
// 确认成功后再释放旧的——中途失败时原图仍完整，不会出现黑窗。
func makePinDIB(img *Bitmap) (dc, bmp, old uintptr, ok bool) {
	if img.Empty() {
		return 0, 0, 0, false
	}
	hdc, _, _ := procCreateCompatibleDC.Call(0)
	if hdc == 0 {
		return 0, 0, 0, false
	}
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(img.W),
			Height:      -int32(img.H), // top-down
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
		},
	}
	var bits unsafe.Pointer
	hb, _, _ := procCreateDIBSection.Call(
		hdc, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0,
	)
	if hb == 0 || bits == nil {
		procDeleteDC.Call(hdc)
		return 0, 0, 0, false
	}
	copy(unsafe.Slice((*byte)(bits), img.W*img.H*4), img.Pix)
	oldBmp, _, _ := procSelectObject.Call(hdc, hb)
	return hdc, hb, oldBmp, true
}

// freePinDIB 释放 makePinDIB 建的三件套。顺序不能变：
// 选回原位图 → 删自己的 DIB → 删 DC，否则 DeleteObject 会失败并留下 GDI 句柄。
func freePinDIB(dc, bmp, old uintptr) {
	if dc == 0 {
		return
	}
	if bmp != 0 && old != 0 {
		procSelectObject.Call(dc, old)
	}
	if bmp != 0 {
		procDeleteObject.Call(bmp)
	}
	procDeleteDC.Call(dc)
}
