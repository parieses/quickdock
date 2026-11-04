package fsview

import (
	"encoding/binary"
	"io"
	"os"
)

// exifScanBytes 是找 EXIF 时最多读多少字节。
// JPEG 的 APP1 段本身不超过 64KB，128KB 足够覆盖任何正常的「SOI + 若干头段 + APP1」，
// 又不会为了读一个方向标记把大图的开头整个读进内存。
const exifScanBytes = 128 << 10

// jpegOrientation 读出 JPEG 的 EXIF Orientation（1~8），读不到一律返回 1（不旋转）。
//
// 为什么需要它：Go 的 image.Decode 只解像素、不看 EXIF，而缩略图是重编码出来的、
// 不再携带 EXIF —— 于是手机竖拍的照片会在预览里躺倒 90°。
// 原图直出时 Chromium 自己会按 EXIF 摆正，一旦经我们重编码就必须自己摆。
//
// 只解析 APP1 里的 IFD0 第一个 Orientation 标签，不做通用 EXIF 读取：
// 目标是「知道该转多少度」，不是做元数据浏览器。
func jpegOrientation(f *os.File) int {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 1
	}
	buf := make([]byte, exifScanBytes)
	n, err := io.ReadFull(f, buf)
	// 文件比缓冲区小是正常的（小图），ReadFull 会返回 ErrUnexpectedEOF，此时 buf 前 n 字节有效。
	if n < 4 {
		return 1
	}
	_ = err
	buf = buf[:n]
	if buf[0] != 0xFF || buf[1] != 0xD8 { // 不是 SOI，不是 JPEG
		return 1
	}

	// 逐段扫 marker，跳过段内容找 APP1。
	i := 2
	for i+4 <= len(buf) {
		if buf[i] != 0xFF {
			return 1 // 段结构已经不合法，别硬猜
		}
		marker := buf[i+1]
		if marker == 0xFF { // 填充字节
			i++
			continue
		}
		// 无长度字段的独立 marker：SOI / TEM / RST0-7
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		segLen := int(binary.BigEndian.Uint16(buf[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(buf) {
			return 1
		}
		if marker == 0xE1 {
			if o := parseExifOrientation(buf[i+4 : i+2+segLen]); o != 0 {
				return o
			}
		}
		if marker == 0xDA { // SOS：之后是熵编码数据，不再有元数据段
			return 1
		}
		i += 2 + segLen
	}
	return 1
}

// parseExifOrientation 在 APP1 段内容里找 IFD0 的 Orientation（tag 0x0112）。
// 返回 0 表示没有。任何越界或格式不符都返回 0 —— 这个函数宁可不旋转，
// 也不能因为一张畸形的图让预览崩掉。
func parseExifOrientation(seg []byte) int {
	if len(seg) < 6+8 || string(seg[:6]) != "Exif\x00\x00" {
		return 0
	}
	t := seg[6:] // TIFF header 起点，后续偏移都相对它
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 0
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 0
	}
	count := int(bo.Uint16(t[off : off+2]))
	p := off + 2
	for k := 0; k < count; k++ {
		if p+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[p:p+2]) == 0x0112 {
			// Orientation 的类型固定是 SHORT(3)，值就地存在条目的后 4 字节里的前 2 字节。
			v := int(bo.Uint16(t[p+8 : p+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
		p += 12
	}
	return 0
}
