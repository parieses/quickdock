// Package kb 知识库的文件解析与切片引擎。
//
// 这一层不认识数据库、不认识 Wails，输入是一个文件路径，输出是「按文档结构
// 分好的文本块」。之所以要单独一层，是因为「怎么把 Word/Excel/PDF 变成文本」
// 这件事的复杂度远超它看起来的样子——每种格式各有一套会静默出错的地方，
// 混在 service 里会很快失控。
//
// 单向依赖：services/kb → internal/kb。
package kb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

// Block 一段语义单元。Page 只在分页格式（PDF）里非零。
type Block struct {
	Heading string
	Text    string
	Page    int
}

// Document 解析结果。Blocks 必须保持文档原始顺序——切片器靠标题变化和
// 段落顺序决定在哪里下刀，顺序一乱，切出来的块就是拼接怪。
type Document struct {
	Title     string
	Blocks    []Block
	Pages     int
	Truncated bool
	// OCRHint 为真表示「这个文件里抽不出可用文本，大概率是扫描件」。
	// 由 PDF 解析设置，service 层据此给出「用 OCR 插件」的提示，
	// 而不是丢一句「解析失败」让用户猜。
	OCRHint bool
}

// 解析层错误。service 层按类型翻译成用户能看懂的提示，
// 不要把 err.Error() 直接怼到界面上（"zip: not a valid zip file" 对用户毫无意义）。
var (
	ErrUnsupported = errors.New("不支持的文件格式")
	ErrEncrypted   = errors.New("文件已加密")
	ErrCorrupted   = errors.New("文件已损坏")
	ErrNoText      = errors.New("文件里没有可提取的文本")
)

// MaxTextRunes 单文件送入索引的文本上限。
//
// 200 万字约等于 3000 个页面，正常文档不会到；这个限制是为了挡住
// 「误导入一个几百 MB 的日志」把库撑爆——那种情况下切片本身就要几万块，
// 而检索质量并不会因为多这几万块变好，只会让噪声变多。
const MaxTextRunes = 2_000_000

// SupportedExts 支持导入的扩展名（含点，小写）。
var SupportedExts = map[string]bool{
	".md": true, ".markdown": true,
	".txt": true, ".text": true, ".log": true, ".csv": true, ".json": true, ".yaml": true, ".yml": true,
	".docx": true,
	".xlsx": true,
	".pdf":  true,
}

// IsSupported 判断扩展名是否支持。
func IsSupported(ext string) bool {
	return SupportedExts[strings.ToLower(ext)]
}

// ParseFile 按扩展名分派解析器。
func ParseFile(path string) (*Document, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%w: 这是一个目录", ErrUnsupported)
	}

	ext := strings.ToLower(filepath.Ext(path))
	var doc *Document
	switch ext {
	case ".md", ".markdown":
		doc, err = parseMarkdown(path)
	case ".txt", ".text", ".log", ".csv", ".json", ".yaml", ".yml":
		doc, err = parsePlain(path)
	case ".docx":
		doc, err = parseDocx(path)
	case ".xlsx":
		doc, err = parseXlsx(path)
	case ".pdf":
		doc, err = parsePDF(path)
	case ".doc", ".xls", ".ppt", ".pptx", ".wps", ".et":
		// 老二进制格式（OLE2）与演示文稿，纯 Go 没有可靠的抽取路径。
		return nil, fmt.Errorf("%w: %s 请另存为 %s", ErrUnsupported, ext, modernExt(ext))
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, ext)
	}
	if err != nil {
		return nil, err
	}
	if doc.Title == "" {
		doc.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	normalizeDocWhitespace(doc)
	if err := truncateDoc(doc, MaxTextRunes); err != nil {
		return nil, err
	}
	return doc, nil
}

// normalizeDocWhitespace 所有格式的收尾归一。
//
// 各解析器都有自己的空白来源：PDF 按坐标重排会插空格、xlsx 单元格内的
// 换行被换成空格、docx 的 <w:br/> 也是一次换行。这些空白一旦落在两个
// 汉字之间，就会把「项目立项」切成「项 目 立 项」，而 FTS5 用的是
// trigram 分词——索引里存什么，用户就必须原样搜什么，中间多一个空格
// 等于这段文字永远搜不到（11-11 事故，PDF 路径已经踩过）。
//
// 所以不再在每个解析器里各修一遍，统一在这里收口：任何格式进来，
// 汉字之间都不允许留空白。中英文之间（「使用 Docker」）保留空格。
func normalizeDocWhitespace(doc *Document) {
	for i := range doc.Blocks {
		doc.Blocks[i].Text = collapseCJKSpaces(foldWidth(doc.Blocks[i].Text))
		doc.Blocks[i].Heading = collapseCJKSpaces(foldWidth(doc.Blocks[i].Heading))
	}
}

// foldWidth 全角 ASCII 折成半角（＋全角空格→半角空格）。
//
// 中文排版里「６０分」和「60分」混用是常态；FTS 是字面匹配，不折叠的话
// 用户搜半角、原文是全角（或反过来）就永远命中不了。查询侧
// (internal/db 的 kbFoldWidth) 做同样的折叠，两边口径必须一致。
func foldWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0x3000:
			b.WriteRune(' ')
		case r >= 0xFF01 && r <= 0xFF5E:
			b.WriteRune(r - 0xFEE0)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func modernExt(ext string) string {
	switch ext {
	case ".doc", ".wps":
		return ".docx"
	case ".xls", ".et":
		return ".xlsx"
	default:
		return ".pdf"
	}
}

// truncateDoc 按 rune 总量截断。按 rune 不按字节：中文一个字 3 字节，
// 按字节截会把上限砍到实际的三分之一。
func truncateDoc(doc *Document, maxRunes int) error {
	total := 0
	for i, b := range doc.Blocks {
		n := len([]rune(b.Text))
		if total+n <= maxRunes {
			total += n
			continue
		}
		keep := maxRunes - total
		if keep > 0 {
			r := []rune(b.Text)
			doc.Blocks = doc.Blocks[:i+1]
			doc.Blocks[i].Text = string(r[:keep])
		} else {
			doc.Blocks = doc.Blocks[:i]
		}
		doc.Truncated = true
		break
	}
	if len(doc.Blocks) == 0 {
		return ErrNoText
	}
	return nil
}

// ---- 纯文本类 ----

// maxReadBytes 本地文本导入的单文件读取上限。
// 入库最终只保留 MaxTextRunes（2M rune），UTF-8 最坏 4 字节/rune 也就 8MB，
// 16MB 之外的字节注定会被 truncateDoc 丢掉——先全量读进来再截属于纯浪费，
// 几百 MB 的源文件会造成一次性内存尖峰。与网页抓取路径的 32MB 预算同思路。
const maxReadBytes = 16 << 20

// readDecoded 读文件并解出字符串。
//
// 编码探测是必须的：Windows 上的老 .txt / .md 大量是 GBK，当 UTF-8 读
// 不会报错，只会得到一屏「锟斤拷」——而且这种文本能正常入库、正常切片，
// 只是永远检索不到，属于最难排查的那类问题。
func readDecoded(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	if size > maxReadBytes {
		size = maxReadBytes
	}
	raw := make([]byte, size)
	if size > 0 {
		if _, err := f.ReadAt(raw, 0); err != nil && err != io.EOF {
			return "", err
		}
	}
	if len(raw) == 0 {
		return "", ErrNoText
	}
	// 只有真截断过才允许「削掉尾部不完整的字节」：整份读入的文件
	// 尾部非法就是真的编码有问题，不该被削成合法 UTF-8。
	return decodeBytes(raw, size < st.Size())
}

func decodeBytes(raw []byte, truncated bool) (string, error) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return string(raw[3:]), nil
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		out, err := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder().Bytes(raw[2:])
		return string(out), err
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		out, err := unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder().Bytes(raw[2:])
		return string(out), err
	}
	if utf8.Valid(raw) {
		return string(raw), nil
	}
	// 16MB 硬截断正好切在一个汉字中间时，utf8.Valid 会为假，整份文件
	// 被误判成 GBK —— 于是几万字的正常中文变成一屏乱码，而且照样入库、
	// 照样能切片，只是永远搜不到（最难排查的那类）。
	// 削掉尾部 1~3 个字节（不足一个 rune）再判一次即可。
	if truncated {
		if fixed := trimIncompleteUTF8(raw); utf8.Valid(fixed) {
			return string(fixed), nil
		}
	}
	// 不是合法 UTF-8 就当 GBK。GBK 解码几乎不会失败（任意字节都有映射），
	// 所以这条回退是「尽力而为」而不是「可靠判定」。
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		return string(raw), nil
	}
	return string(out), nil
}

// trimIncompleteUTF8 从一个可能被切断的尾部往前退，最多 3 个字节
// （UTF-8 单个 rune 最长 4 字节）。只用于「确实按字节截断过」的场景。
func trimIncompleteUTF8(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0; i++ {
		b = b[:len(b)-1]
		if utf8.Valid(b) {
			return b
		}
	}
	return b
}

func parsePlain(path string) (*Document, error) {
	text, err := readDecoded(path)
	if err != nil {
		return nil, err
	}
	doc := &Document{}
	for _, para := range splitParagraphs(text) {
		doc.Blocks = append(doc.Blocks, Block{Text: para})
	}
	if len(doc.Blocks) == 0 {
		return nil, ErrNoText
	}
	return doc, nil
}

// splitParagraphs 按空行分段并压掉多余空白。
func splitParagraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var out []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		joined := strings.TrimSpace(strings.Join(cur, "\n"))
		if joined != "" {
			out = append(out, joined)
		}
		cur = cur[:0]
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

var mdHeadingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// parseMarkdown 按标题分节，正文按段落聚成一个 Block。
//
// 维护一个 6 层标题栈拼出路径（"部署 / 数据库"）：切片时这个路径会作为
// heading 写进每一块，检索时 bm25 对它加权——用户搜「数据库备份」，
// 标题就叫「数据库」的那一节应该排在只是正文顺带提到「数据库」的段落前面。
func parseMarkdown(path string) (*Document, error) {
	text, err := readDecoded(path)
	if err != nil {
		return nil, err
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	doc := &Document{}
	var stack [6]string
	var curHeading string
	var buf []string
	inFence := false

	flush := func() {
		body := strings.TrimSpace(strings.Join(buf, "\n"))
		buf = buf[:0]
		if body != "" {
			doc.Blocks = append(doc.Blocks, Block{Heading: curHeading, Text: body})
		}
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		// 围栏代码块里的 # 是注释不是标题，必须跳过；
		// 否则一段 shell 脚本里的注释会被当成章节标题，把后面的内容挂错地方。
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			buf = append(buf, line)
			continue
		}
		if !inFence {
			if m := mdHeadingRe.FindStringSubmatch(trimmed); m != nil {
				level := len(m[1])
				title := strings.TrimSpace(m[2])
				flush()
				stack[level-1] = title
				for i := level; i < 6; i++ {
					stack[i] = ""
				}
				curHeading = joinHeading(stack[:])
				if doc.Title == "" && title != "" {
					doc.Title = title
				}
				continue
			}
		}
		if trimmed == "" {
			flush()
			continue
		}
		buf = append(buf, line)
	}
	flush()

	if len(doc.Blocks) == 0 {
		return nil, ErrNoText
	}
	return doc, nil
}

func joinHeading(parts []string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, strings.TrimSpace(p))
		}
	}
	return strings.Join(kept, " / ")
}
