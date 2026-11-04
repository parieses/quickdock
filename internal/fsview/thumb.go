package fsview

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

const (
	// thumbMaxEdge 是缩略图单边上限。
	// 取值被前端的能力反向决定：预览区最宽约 900（面板 900 宽），而 Ctrl+滚轮
	// 最多放大到 5 倍（FilePreviewPane 的 ZOOM_MAX），2800 能覆盖到 3 倍左右仍清晰，
	// 再往上放大本来也只是看像素块，给原图不值那个内存。
	// 代价：解码后约 23MB、缓存 12 份约 8MB，都不是问题。
	thumbMaxEdge = 2800

	// thumbMaxPixels 是解码前的像素闸门。
	// image.Decode 会把整图解成 RGBA，像素数直接决定峰值内存（6000 万像素约 240MB）。
	// 这里的取舍是「后端短时峰值」换「WebView 的长期占用」：不缩的话，
	// 同一张图在 Chromium 里照样要占这么多，而且会一直留着。
	// 超过闸门就返回原图让 Chromium 分块处理 —— 慢一点，但不会把任何一个进程打爆。
	thumbMaxPixels = 60_000_000

	// thumbCacheLimit 是缩略图缓存份数。一份 1600px 的 JPEG 约 200~400KB，
	// 12 份不到 5MB，换来方向键来回划时不重复解码。
	thumbCacheLimit = 12

	// thumbJPEGQuality 兼顾清晰度与体积；再高对缩略图没有肉眼收益。
	thumbJPEGQuality = 82
)

type thumbKey struct {
	path  string
	mtime int64
	size  int64
	width int
}

type thumbEntry struct {
	key  thumbKey
	data []byte
	mime string
}

var thumbCache struct {
	mu    sync.Mutex
	items []thumbEntry // 先进先出，队头最旧
}

func thumbCached(k thumbKey) ([]byte, string, bool) {
	thumbCache.mu.Lock()
	defer thumbCache.mu.Unlock()
	for _, e := range thumbCache.items {
		if e.key == k {
			return e.data, e.mime, true
		}
	}
	return nil, "", false
}

func thumbPut(k thumbKey, data []byte, mime string) {
	thumbCache.mu.Lock()
	defer thumbCache.mu.Unlock()
	for i, e := range thumbCache.items {
		if e.key == k {
			thumbCache.items[i] = thumbEntry{key: k, data: data, mime: mime}
			return
		}
	}
	if len(thumbCache.items) >= thumbCacheLimit {
		thumbCache.items = thumbCache.items[1:]
	}
	thumbCache.items = append(thumbCache.items, thumbEntry{key: k, data: data, mime: mime})
}

// thumbnail 把图片缩到 width 边以内，返回可直接写回的字节与该用的 MIME。
//
// ok=false 表示「这次不缩，调用方原样返回文件」，具体有四种情况：
// 格式不处理（GIF 只解第一帧会丢动画、SVG 是矢量无需缩放）、图本来就不大、
// 像素数过闸门、解码失败。任何一种都退回 serve 原图，绝不因此让预览失败。
func thumbnail(abs string, info os.FileInfo, width int) (data []byte, mimeType string, ok bool) {
	if width <= 0 || width > thumbMaxEdge {
		width = thumbMaxEdge
	}
	if !canThumb(filepath.Ext(abs)) {
		return nil, "", false
	}

	key := thumbKey{path: abs, mtime: info.ModTime().UnixNano(), size: info.Size(), width: width}
	if d, m, hit := thumbCached(key); hit {
		return d, m, true
	}

	f, err := os.Open(abs)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()

	// 先用 DecodeConfig 只读头部拿尺寸，避免为一个必然放弃的图付出整图解码的代价。
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, "", false
	}
	if int64(cfg.Width)*int64(cfg.Height) > thumbMaxPixels {
		return nil, "", false
	}

	// EXIF 方向只在 JPEG 里有（PNG 的 eXIf 块极罕见，忽略）。
	// 这一步不必等解码：重编码会丢掉 EXIF，所以「我们出的图」和「Chromium 看到的图」
	// 之间必须由自己摆正，否则手机竖拍的照片会躺倒。
	orient := 1
	if isJPEG(filepath.Ext(abs)) {
		orient = jpegOrientation(f)
	}
	needScale := cfg.Width > width || cfg.Height > width
	// 小图也得分方向：一张 600×400 的竖拍照片不需要缩，但一样要转。
	if !needScale && orient == 1 {
		return nil, "", false
	}

	// DecodeConfig / jpegOrientation 都消费了文件头，整图解码前必须复位。
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", false
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, "", false
	}

	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	if sw <= 0 || sh <= 0 {
		return nil, "", false
	}

	// 方向校正与缩放合成一个仿射矩阵，一次采样同时完成 ——
	// 分两步会多出一张校正后的全尺寸中间图（4800 万像素就是 192MB）。
	m, ow, oh := orientAffine(orient, sw, sh)
	scale := 1.0
	if ow > width || oh > width {
		longest := ow
		if oh > longest {
			longest = oh
		}
		scale = float64(width) / float64(longest)
	}
	tw := int(math.Round(float64(ow) * scale))
	th := int(math.Round(float64(oh) * scale))
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	if scale != 1 {
		m[0] *= scale
		m[1] *= scale
		m[2] *= scale
		m[3] *= scale
		m[4] *= scale
		m[5] *= scale
	}

	isPNG := strings.EqualFold(filepath.Ext(abs), ".png")

	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	// 旋转/镜像会让边缘落在半像素上，采样不到源像素的地方保持零值（全透明）。
	// PNG 能表达透明，留着；JPEG 没有 alpha 通道，零值编码出来是黑边，
	// 所以先铺一层白底 —— 照片边缘出现一圈黑线比铺白难看得多。
	if !isPNG && orient != 1 {
		draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	}
	// ApproxBiLinear 是质量/速度的折中点：CatmullRom 慢一倍多，对缩略图看不出差别；
	// NearestNeighbor 会出锯齿，大比例缩小时尤其明显。
	draw.ApproxBiLinear.Transform(dst, m, src, src.Bounds(), draw.Src, nil)

	var buf bytes.Buffer
	mimeType = "image/jpeg"
	if isPNG {
		// PNG 直接用 BestSpeed：默认压缩比下编码 2800px 的图要几百毫秒，
		// 而缩略图的体积优势远不如「快」重要。
		mimeType = "image/png"
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, dst); err != nil {
			return nil, "", false
		}
	} else if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return nil, "", false
	}

	// 缩完比原图还大就没意义了（小尺寸高质量 JPEG、已高度压缩的截图都会这样），
	// 直接退回原图，省流量也省内存。
	if int64(buf.Len()) >= info.Size() {
		return nil, "", false
	}
	out := buf.Bytes()
	thumbPut(key, out, mimeType)
	return out, mimeType, true
}

// canThumb 只处理 JPEG / PNG。
// GIF 的 image.Decode 只返回第一帧，缩了会变成静态图；
// SVG 是矢量，交给 WebView 按显示尺寸渲染比栅格化更清晰。
func canThumb(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

func isJPEG(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return true
	}
	return false
}

// orientAffine 返回把原图（sw×sh）校正到正常方向的矩阵，以及校正后的尺寸。
//
// 矩阵是 src→dst 的仿射变换（x' = a·x + b·y + c，y' = d·x + e·y + f），
// 平移量用完整宽高而不是「宽高 − 1」：x/image/draw 把整数坐标当像素边界，
// 不是像素索引，用 w−1 会让整幅图偏半个像素、边缘出现一条透明缝。
//
// EXIF 2.3 的取值含义：
//
//	1 正常   2 水平镜像   3 旋转 180°   4 垂直镜像
//	5 转置   6 顺时针 90°  7 反转置      8 逆时针 90°
//
// 5~8 会把宽高互换。
func orientAffine(o, sw, sh int) (f64.Aff3, int, int) {
	w, h := float64(sw), float64(sh)
	switch o {
	case 2:
		return f64.Aff3{-1, 0, w, 0, 1, 0}, sw, sh
	case 3:
		return f64.Aff3{-1, 0, w, 0, -1, h}, sw, sh
	case 4:
		return f64.Aff3{1, 0, 0, 0, -1, h}, sw, sh
	case 5:
		return f64.Aff3{0, 1, 0, 1, 0, 0}, sh, sw
	case 6:
		return f64.Aff3{0, -1, h, 1, 0, 0}, sh, sw
	case 7:
		return f64.Aff3{0, -1, h, -1, 0, w}, sh, sw
	case 8:
		return f64.Aff3{0, 1, 0, -1, 0, w}, sh, sw
	}
	return f64.Aff3{1, 0, 0, 0, 1, 0}, sw, sh
}
