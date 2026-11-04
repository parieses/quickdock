// Package fsview 是文件预览的读取引擎：把一个路径探成「一种预览形态 + 已就绪的载荷」。
//
// 设计边界：
//   - 只读，不改动任何文件；不做缓存（预览是用户临时瞄一眼，缓存反而要处理失效）。
//   - 不认识的文件类型退化成 binary，由前端显示元数据，绝不抛「不支持」这种拦路错误。
//   - 文本与压缩包载荷走 Wails IPC 返回字符串；图片 / PDF / 音视频只给一个本地
//     HTTP 地址（见 server.go），避免几十 MB 的字节流被 JSON 序列化后卡住界面。
package fsview

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"image"
	_ "image/gif" // 注册 GIF 解码器，供 DecodeConfig 读尺寸
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

const (
	// maxTextBytes 文本预览的读取上限。512KB 足够看清一个源码文件或配置文件，
	// 再大用户也不会在宽度不足千像素的面板里逐行读，反而只是一次无谓的 IPC。
	maxTextBytes = 512 << 10
	// maxArchiveEntries 压缩包列表上限，防止十万条的归档把界面撑爆。
	maxArchiveEntries = 200
	// maxDirEntries 目录列表上限，理由同上。
	maxDirEntries = 100

	// previewCacheMax 缓存条数上限。预览集中在同一个文件夹里来回划，
	// 二三十条足够覆盖全部重复访问；再多只是把内存留给不会再看的文件。
	previewCacheMax = 32
	// previewCacheMaxText 超过这个体积的文本不进缓存：512KB × 32 条就是 16MB，
	// 而这么大的文件用户基本只看一眼就不再回来。
	previewCacheMaxText = 128 << 10
)

// Kind 是预览形态。前端按它选渲染器，不靠扩展名自己猜。
type Kind string

const (
	KindMissing  Kind = "missing" // 路径不存在
	KindDir      Kind = "dir"
	KindImage    Kind = "image"
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindPDF      Kind = "pdf"
	KindMarkdown Kind = "markdown"
	KindCode     Kind = "code"
	KindText     Kind = "text"
	KindArchive  Kind = "archive"
	KindBinary   Kind = "binary"
)

// Entry 目录项或压缩包条目。
type Entry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
}

// Result 一次预览的完整载荷。字段是并集，前端按 kind 只读其中一部分。
type Result struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Ext      string `json:"ext"`
	Kind     Kind   `json:"kind"`
	Size     int64  `json:"size"`
	SizeText string `json:"sizeText"`
	Modified string `json:"modified"`

	// 文本类（kind = text/code/markdown）
	Text      string `json:"text"`
	LineCount int    `json:"lineCount"`
	Truncated bool   `json:"truncated"`
	Encoding  string `json:"encoding"`
	Language  string `json:"language"`

	// 图片
	ImageWidth  int `json:"imageWidth"`
	ImageHeight int `json:"imageHeight"`

	// 图片 / PDF / 音视频的可访问地址（空表示退化：只显示元数据）
	MediaURL string `json:"mediaUrl"`

	// 目录 / 压缩包
	Entries    []Entry `json:"entries"`
	EntryTotal int     `json:"entryTotal"`

	// Note 给人看的补充说明（如「7z 暂不支持列表」）。前端原样展示。
	Note string `json:"note"`
}

// Viewer 把路径探测成预览载荷。
//
// mediaURL 由调用方注入：它把一个绝对路径变成一个可被 WebView 拉取的地址。
// 传 nil 时二进制类预览退化成只有元数据，不会报错。
//
// 内部带一层短缓存：面板里方向键来回划同一批文件时，第二次不该再读一遍文件
// （读 512KB + 编码转换 + 语法探测完全不便宜）。缓存以 mtime + size 为有效性判据，
// 文件被改写后自动失效，不做定时过期。
type Viewer struct {
	mediaURL func(absPath string) string
	// thumbURL 可选：给了就让图片走服务端降采样，避免 WebView 解码原图。
	// 为 nil 时图片退回 mediaURL（原图直出），行为跟之前一致。
	thumbURL func(absPath string, width int) string

	mu    sync.Mutex
	cache map[string]cacheEntry
	order []string // 先进先出，达到上限时淘汰队头
}

type cacheEntry struct {
	res   Result
	mtime time.Time
	size  int64
}

// New 构造 Viewer。mediaURL 可为 nil。
func New(mediaURL func(absPath string) string) *Viewer {
	return &Viewer{mediaURL: mediaURL, cache: make(map[string]cacheEntry)}
}

// SetThumbURL 注入图片缩略地址生成器。不注入则图片按原图返回。
func (v *Viewer) SetThumbURL(fn func(absPath string, width int) string) {
	v.thumbURL = fn
}

// Preview 探测并读取 path。路径不存在时返回 Kind=missing，不返回 error：
// 用户在命令面板里快速上下移动，路径失效（U 盘拔了、文件被删）是常态，
// 冒一个错误弹窗比显示「路径不存在」更打扰。
func (v *Viewer) Preview(path string) Result {
	raw := strings.TrimSpace(path)
	if raw == "" {
		return Result{Kind: KindMissing}
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return Result{Path: raw, Kind: KindMissing}
	}

	info, err := os.Stat(abs)
	if err != nil {
		// 缺失不缓存：文件可能下一秒就被创建出来。
		return Result{Path: abs, Name: filepath.Base(abs), Kind: KindMissing}
	}

	if cached, ok := v.lookup(abs, info); ok {
		return cached
	}
	res := v.build(abs, info)
	v.store(abs, info, res)
	return res
}

// build 真正做探测与读取。缓存命中时不会走到这里。
func (v *Viewer) build(abs string, info os.FileInfo) Result {
	res := Result{
		Path:     abs,
		Name:     info.Name(),
		Kind:     KindBinary,
		Size:     info.Size(),
		SizeText: formatSize(info.Size()),
		Modified: info.ModTime().Format("2006-01-02 15:04:05"),
		Entries:  []Entry{},
	}
	if info.IsDir() {
		res.Kind = KindDir
		res.Ext = ""
		v.fillDir(abs, &res)
		return res
	}

	ext := strings.ToLower(filepath.Ext(abs))
	res.Ext = strings.TrimPrefix(ext, ".")

	switch kindByExt(ext) {
	case KindImage:
		res.Kind = KindImage
		v.fillImage(abs, &res)
		v.fillImageSize(abs, &res)
	case KindVideo:
		res.Kind = KindVideo
		v.fillMedia(abs, &res)
	case KindAudio:
		res.Kind = KindAudio
		v.fillMedia(abs, &res)
	case KindPDF:
		res.Kind = KindPDF
		v.fillMedia(abs, &res)
	case KindArchive:
		res.Kind = KindArchive
		v.fillArchive(abs, ext, &res)
	case KindMarkdown:
		res.Kind = KindMarkdown
		res.Language = "markdown"
		v.fillText(abs, &res)
	case KindCode:
		res.Kind = KindCode
		res.Language = langByExt(ext)
		v.fillText(abs, &res)
	case KindText:
		// 白名单里的纯文本（.txt/.log/.csv 或干脆没扩展名），不必再嗅探一遍。
		res.Kind = KindText
		v.fillText(abs, &res)
	default:
		// 扩展名没给出结论：读头部内容判定文本还是二进制。
		// 这一步让「没有扩展名的日志 / LICENSE / 脚本」也能正常预览。
		if guessed := sniffKind(abs); guessed == KindText {
			res.Kind = KindText
			v.fillText(abs, &res)
		} else {
			res.Kind = KindBinary
		}
	}
	return res
}

// lookup 命中条件：同一路径且 mtime、size 都没变。
// 用 Equal 而不是相减比较，避免不同来源的时区/单调时钟差异造成误判。
func (v *Viewer) lookup(abs string, info os.FileInfo) (Result, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.cache[abs]
	if !ok {
		return Result{}, false
	}
	if !e.mtime.Equal(info.ModTime()) || e.size != info.Size() {
		return Result{}, false
	}
	return e.res, true
}

// store 写入缓存。大文本与二进制不缓存（前者占内存，后者本来就没内容）。
func (v *Viewer) store(abs string, info os.FileInfo, res Result) {
	if len(res.Text) > previewCacheMaxText {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cache == nil {
		v.cache = make(map[string]cacheEntry)
	}
	if _, exists := v.cache[abs]; !exists {
		if len(v.order) >= previewCacheMax {
			oldest := v.order[0]
			v.order = v.order[1:]
			delete(v.cache, oldest)
		}
		v.order = append(v.order, abs)
	}
	v.cache[abs] = cacheEntry{res: res, mtime: info.ModTime(), size: info.Size()}
}

// fillMedia 给二进制类预览挂上本地服务地址。
func (v *Viewer) fillMedia(abs string, res *Result) {
	if v.mediaURL == nil {
		res.Note = "no_media_server"
		return
	}
	res.MediaURL = v.mediaURL(abs)
}

// fillImage 给图片地址，优先走降采样。
//
// 走缩略而不是原图是有实际代价的：原图解码在 WebView 里是同步的，
// 一张 4000×3000 的照片解码后约 48MB，面板里只显示两三百像素高。
// 快速划过几张就会内存骤增、首帧明显延迟。
func (v *Viewer) fillImage(abs string, res *Result) {
	if v.thumbURL != nil {
		if u := v.thumbURL(abs, thumbMaxEdge); u != "" {
			res.MediaURL = u
			return
		}
	}
	v.fillMedia(abs, res)
}

// fillImageSize 读图片尺寸。只解头部（DecodeConfig），不整图解码。
func (v *Viewer) fillImageSize(abs string, res *Result) {
	f, err := os.Open(abs)
	if err != nil {
		return
	}
	defer f.Close()
	// bmp / webp / svg 没有注册解码器，这里会返回错误，尺寸留空即可 —— 不影响显示。
	if cfg, _, err := image.DecodeConfig(f); err == nil {
		w, h := cfg.Width, cfg.Height
		// 方向 5~8 会把画面转置。预览里显示的是转过之后的图，
		// 元数据行要是还报原始宽高，就会和眼睛看到的对不上。
		if isJPEG(filepath.Ext(abs)) {
			if o := jpegOrientation(f); o >= 5 && o <= 8 {
				w, h = h, w
			}
		}
		res.ImageWidth = w
		res.ImageHeight = h
	}
}

// fillDir 列出目录前 maxDirEntries 项，目录排在文件前面。
func (v *Viewer) fillDir(abs string, res *Result) {
	items, err := os.ReadDir(abs)
	if err != nil {
		res.Note = "read_dir_failed"
		return
	}
	res.EntryTotal = len(items)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].IsDir() != items[j].IsDir() {
			return items[i].IsDir()
		}
		return strings.ToLower(items[i].Name()) < strings.ToLower(items[j].Name())
	})
	for i, it := range items {
		if i >= maxDirEntries {
			break
		}
		e := Entry{Name: it.Name(), IsDir: it.IsDir()}
		if info, err := it.Info(); err == nil {
			e.Size = info.Size()
			if it.IsDir() {
				e.Size = 0
			}
		}
		res.Entries = append(res.Entries, e)
	}
}

// fillArchive 列出压缩包条目。zip / tar / tar.gz 走标准库，其余只给说明。
//
// 注意 ext 是 filepath.Ext 的结果，对 "a.tar.gz" 拿到的是 ".gz" 而不是 ".tar.gz" ——
// 所以 tar.gz 只能靠「按 tar 读一遍、读不出条目」来和普通单文件 .gz 区分。
func (v *Viewer) fillArchive(abs, ext string, res *Result) {
	switch ext {
	case ".zip", ".cbz":
		v.fillZip(abs, res)
	case ".tar":
		f, err := os.Open(abs)
		if err != nil {
			res.Note = "read_archive_failed"
			return
		}
		defer f.Close()
		if v.fillTar(f, res) == 0 {
			res.Note = "read_archive_failed"
		}
	case ".gz", ".tgz":
		f, err := os.Open(abs)
		if err != nil {
			res.Note = "read_archive_failed"
			return
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			res.Note = "read_archive_failed"
			return
		}
		defer gz.Close()
		name := gz.Name // gzip 头里记录的原始文件名，单文件压缩时它就是全部信息
		if v.fillTar(gz, res) == 0 {
			// 不是 tar：普通 gzip 压缩的单个文件。报出原始文件名，比空列表有用。
			res.Note = "gzip_single_file"
			if name != "" {
				res.Entries = []Entry{{Name: name}}
				res.EntryTotal = 1
			}
		}
	default:
		// 7z / rar / bz2 / xz 等：不引入第三方解压库（体积与许可成本都不值），
		// 前端看到这条说明后引导用户用默认程序打开。
		res.Note = "archive_unsupported"
	}
}

func (v *Viewer) fillZip(abs string, res *Result) {
	r, err := zip.OpenReader(abs)
	if err != nil {
		res.Note = "read_archive_failed"
		return
	}
	defer r.Close()
	res.EntryTotal = len(r.File)
	for i, f := range r.File {
		if i >= maxArchiveEntries {
			break
		}
		res.Entries = append(res.Entries, Entry{
			Name:  f.Name,
			Size:  int64(f.UncompressedSize64),
			IsDir: f.FileInfo().IsDir(),
		})
	}
}

// fillTar 按 tar 遍历条目，返回读到的条目数（0 说明这段数据根本不是 tar）。
func (v *Viewer) fillTar(r io.Reader, res *Result) int {
	tr := tar.NewReader(r)
	count := 0
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		count++
		res.EntryTotal++
		if len(res.Entries) >= maxArchiveEntries {
			continue // 继续遍历只为把总数数准，不再追加
		}
		res.Entries = append(res.Entries, Entry{
			Name:  hdr.Name,
			Size:  hdr.Size,
			IsDir: hdr.Typeflag == tar.TypeDir,
		})
	}
	return count
}

// fillText 读文本正文，自动识别 UTF-8 / UTF-16 BOM / GBK。
func (v *Viewer) fillText(abs string, res *Result) {
	f, err := os.Open(abs)
	if err != nil {
		res.Kind = KindBinary
		res.Note = "read_failed"
		return
	}
	defer f.Close()

	// 多读 1 字节用来判断「是不是刚好读完」。
	buf := make([]byte, maxTextBytes+1)
	n, _ := io.ReadFull(f, buf)
	buf = buf[:n]
	if n > maxTextBytes {
		buf = buf[:maxTextBytes]
		res.Truncated = true
	}

	// 截断后末尾可能是半个 UTF-8 字符，或者是半行 —— 两者都让面板显示残缺的最后一行。
	// 退回到最后一个换行处，宁可少一行也不要半个乱码字。
	if res.Truncated {
		if i := strings.LastIndexByte(string(buf), '\n'); i > 0 {
			buf = buf[:i]
		}
	}

	if looksBinary(buf) {
		res.Kind = KindBinary
		return
	}

	text, enc := decodeText(buf)
	res.Text = text
	res.Encoding = enc
	res.LineCount = countLines(text)
	res.Language = normalizeLang(res.Language)
}

// fillText 之外的另一条入口：只需要判定「是不是文本」时用它（无扩展名的情况）。
func sniffKind(abs string) Kind {
	f, err := os.Open(abs)
	if err != nil {
		return KindBinary
	}
	defer f.Close()
	buf := make([]byte, 1024)
	n, _ := io.ReadFull(f, buf)
	buf = buf[:n]
	if len(buf) == 0 {
		return KindText // 空文件按空文本处理，比归到「二进制」贴近事实
	}
	if looksBinary(buf) {
		return KindBinary
	}
	return KindText
}

// looksBinary 用 NUL 字节判定二进制。文本文件（含 UTF-16）在 UTF-16 分支之前
// 会命中 BOM 检查，所以这里对 UTF-16 先放行。
func looksBinary(b []byte) bool {
	if hasUTF16BOM(b) {
		return false
	}
	if len(b) > 512 {
		b = b[:512]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

func hasUTF16BOM(b []byte) bool {
	return len(b) >= 2 && ((b[0] == 0xFF && b[1] == 0xFE) || (b[0] == 0xFE && b[1] == 0xFF))
}

// decodeText 依次尝试 UTF-16 BOM / UTF-8 / GBK，返回正文与编码名。
// 全都解不出可读内容时按 UTF-8 强制转换（前端会看到替换符，但不会空白）。
func decodeText(b []byte) (string, string) {
	if hasUTF16BOM(b) {
		endian := unicode.LittleEndian
		if b[0] == 0xFE {
			endian = unicode.BigEndian
		}
		if out, err := unicode.UTF16(endian, unicode.UseBOM).NewDecoder().Bytes(b); err == nil {
			return string(out), "utf-16"
		}
	}
	if utf8.Valid(b) {
		return string(b), "utf-8"
	}
	// 中文 Windows 上 GBK 编码的日志/配置非常常见，UTF-8 校验失败后优先按 GBK 解。
	if out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil && utf8.Valid(out) {
		return string(out), "gbk"
	}
	return strings.ToValidUTF8(string(b), "\uFFFD"), "unknown"
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
