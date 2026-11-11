package kb

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// pdf 解析。
//
// 这是所有格式里唯一「纯 Go 不可靠」的一个：抽不抽得出文字，取决于
// 生成这份 PDF 的工具有没有嵌 ToUnicode CMap。Word 导出的正常，
// 扫描件（纯图片）完全没有文本层，某些工具生成的 CID 子集字体会抽成乱码。
//
// 所以这里的策略是「尽力抽 + 严格验」：
//  1. 优先用 pdftotext（poppler）：它的 CMap 处理成熟得多，中文成功率高。
//     探测到就用，探测不到静默回退，不写进依赖、不影响分发。
//  2. 回退到纯 Go：按坐标重排文本（不能用 GetPlainText，它按内容流顺序拼，
//     两栏排版会左右交错成乱码）。
//  3. 抽完做可读性检查，不合格直接判失败并给 OCR 提示——
//     把乱码索引进去比拒绝更糟：用户会看到「明明导入了却搜不到」。
const (
	pdfMaxPages     = 500
	pdfPageMaxRunes = 6000 // 单页上限：正常 A4 页面 800~1500 字，超了就是抽取异常
	pdfQualityFloor = 0.5  // 有效字符占比低于此值视为抽取失败
	pdfOCRMinRunes  = 30   // 全文有效字符少于此值 → 判定为扫描件
)

func parsePDF(path string) (*Document, error) {
	if st, err := os.Stat(path); err == nil && st.Size() > 200<<20 {
		return nil, fmt.Errorf("%w: PDF 超过 200MB", ErrUnsupported)
	}

	doc := &Document{}

	// 路线 1：pdftotext（若本机有 poppler）。\f 是它的分页符。
	if pages, ok := pdfTextViaExec(path); ok {
		for i, page := range pages {
			if i >= pdfMaxPages {
				doc.Truncated = true
				break
			}
			text := normalizePDFPage(page)
			if text == "" {
				continue
			}
			if len([]rune(text)) > pdfPageMaxRunes {
				text = string([]rune(text)[:pdfPageMaxRunes])
				doc.Truncated = true
			}
			doc.Blocks = append(doc.Blocks, Block{Heading: fmt.Sprintf("第 %d 页", i+1), Text: text, Page: i + 1})
		}
		doc.Pages = len(pages)
		if len(doc.Blocks) > 0 {
			return doc, nil
		}
		// pdftotext 一个字都没抽出来 → 大概率扫描件，继续用纯 Go 兜一遍。
	}

	// 路线 2：纯 Go。
	f, r, err := pdf.Open(path)
	if err != nil {
		if isPDFEncryptedErr(err) {
			return nil, ErrEncrypted
		}
		return nil, fmt.Errorf("%w: %v", ErrCorrupted, err)
	}
	defer f.Close()

	total := r.NumPage()
	doc.Pages = total
	limit := total
	if limit > pdfMaxPages {
		limit = pdfMaxPages
		doc.Truncated = true
	}

	valid := 0
	for i := 1; i <= limit; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text := extractPDFPageText(p)
		if text == "" {
			continue
		}
		if quality(text) < pdfQualityFloor {
			// 这一页抽出来是乱码，跳过但不算整份失败：
			// 有的 PDF 前几页是封面/目录（图片），正文正常。
			continue
		}
		valid += len([]rune(text))
		if len([]rune(text)) > pdfPageMaxRunes {
			text = string([]rune(text)[:pdfPageMaxRunes])
			doc.Truncated = true
		}
		doc.Blocks = append(doc.Blocks, Block{Heading: fmt.Sprintf("第 %d 页", i+1), Text: text, Page: i + 1})
	}

	if len(doc.Blocks) == 0 || valid < pdfOCRMinRunes {
		doc.OCRHint = true
		if len(doc.Blocks) == 0 {
			return doc, ErrNoText
		}
	}
	return doc, nil
}

// extractPDFPageText 把一页的文本对象按坐标重排成可读文本。
//
// 直接 GetPlainText() 是按内容流的绘制顺序拼字符串，两栏排版会变成
// 「左栏第一行 右栏第一行 左栏第二行」，抽出来完全没法读，而且切片后
// 每块都是两栏交错的碎片。按 (Y 降序, X 升序) 重排能把同一视觉行拼回去。
//
// 多栏仍可能跨栏混行（同一 Y 上左右栏文字会被拼到一行），彻底解决需要
// 版面分析，不在这个量级里做。
func extractPDFPageText(p pdf.Page) string {
	content := p.Content()
	texts := make([]pdf.Text, 0, len(content.Text))
	for _, t := range content.Text {
		if strings.TrimSpace(t.S) != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) == 0 {
		return ""
	}

	// Y 降序（PDF 坐标原点在左下，Y 大在上），同 Y 按 X 升序。
	sort.SliceStable(texts, func(i, j int) bool {
		if math.Abs(texts[i].Y-texts[j].Y) > 2 {
			return texts[i].Y > texts[j].Y
		}
		return texts[i].X < texts[j].X
	})

	var b strings.Builder
	var line []pdf.Text
	prevY, prevFont := math.NaN(), 0.0
	// lastRune 记录上一行行尾字符：判断行内空格该不该插时要用到
	// 「上一个写出的字符是不是 CJK」，而不是每个文本块各自为政。
	lastRune := rune(0)

	flushLine := func() {
		if len(line) == 0 {
			return
		}
		sort.SliceStable(line, func(i, j int) bool { return line[i].X < line[j].X })
		var lb strings.Builder
		prevEnd := math.NaN()
		for _, t := range line {
			// 两个文本块之间的间距明显大于字宽 → 中间应该有个空格。
			// 没有这一步，英文会变成 "Thequickbrownfox"。
			// 但 CJK/数字之间例外：Word/WPS 导出的 PDF 常把每个汉字（甚至
			// 每个数字）作为独立文本对象摆放，字距判断在这里几乎必然误触发，
			// 把整页中文插成「项 目 立 项」、「6 0 分」，trigram 索引直接
			// 废掉（实测事故）。这些字符本来就不需要词间空格，直接不插。
			rs := []rune(t.S)
			if !math.IsNaN(prevEnd) && t.X-prevEnd > t.FontSize*0.3 &&
				!(gluedRune(lastRune) && len(rs) > 0 && gluedRune(rs[0])) {
				lb.WriteString(" ")
			}
			lb.WriteString(t.S)
			if len(rs) > 0 {
				lastRune = rs[len(rs)-1]
			}
			prevEnd = t.X + t.W
		}
		y, fs := line[0].Y, line[0].FontSize
		// 行距明显大于字号 → 认为是分段，插空行（切片器靠空行分段）。
		if !math.IsNaN(prevY) && prevY-y > math.Max(fs, prevFont)*1.8 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimRight(lb.String(), " "))
		b.WriteString("\n")
		prevY, prevFont = y, fs
		line = line[:0]
	}

	for _, t := range texts {
		if len(line) == 0 {
			line = append(line, t)
			continue
		}
		last := line[len(line)-1]
		tol := math.Max(2, math.Max(last.FontSize, t.FontSize)*0.5)
		if math.Abs(t.Y-last.Y) <= tol {
			line = append(line, t)
			continue
		}
		flushLine()
		line = append(line, t)
	}
	flushLine()

	return normalizePDFPage(b.String())
}

// normalizePDFPage 收敛空白：PDF 抽出来的行首常有大量空格（版心缩进），
// 保留它们既浪费块预算又会让词法匹配变差。
func normalizePDFPage(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue // 连续空行压成一个，段间距不需要那么多
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, l)
	}
	// CJK 间空格压缩是兜底防线：不管空格来自纯 Go 的字距判断还是
	// pdftotext -layout 的版面还原，中文词间空格对检索都是纯噪声——
	// trigram 把「项 目 立 项」当带空格的串索引，用户搜「立项」永远搜不到。
	return collapseCJKSpaces(strings.TrimSpace(strings.Join(out, "\n")))
}

// isCJK 判断 rune 是否属于中日韩文字、假名或全角标点。
// 全角标点也要算：它们与汉字之间的空格同样该压掉（「一 、 总 则」→「一、总则」）。
func isCJK(r rune) bool {
	switch {
	case r >= 0x2E80 && r <= 0x9FFF: // CJK 部首、汉字、日文假名、注音
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK 兼容表意文字
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // 全角标点与全角字母数字
		return true
	}
	return false
}

// gluedRune 空格可以安全压掉的字符类别：CJK/全角标点、ASCII 数字、百分号。
// 这三类相邻时的空格在 PDF 抽取结果里几乎都是版面噪声（「6 0 分」「7 0 %」），
// 而拉丁字母参与时（「使用 Docker」「PDF 文件」）空格是有信息量的分隔，要保留。
func gluedRune(r rune) bool {
	return isCJK(r) || (r >= '0' && r <= '9') || r == '%'
}

// collapseCJKSpaces 删除夹在两段 CJK/数字文字之间的空白（连续空格/制表符整段删）。
// 中英文之间的空格（「使用 Docker 部署」）保留不动——那才是有信息量的分隔。
func collapseCJKSpaces(s string) string {
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	i := 0
	for i < len(runes) {
		r := runes[i]
		if (r == ' ' || r == '\t') && i > 0 && gluedRune(runes[i-1]) {
			j := i
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
				j++
			}
			if j < len(runes) && gluedRune(runes[j]) {
				i = j
				continue
			}
		}
		out = append(out, r)
		i++
	}
	return string(out)
}

// quality 有效字符占比。
//
// 判「乱码」而不是「好不好」：替换字符（U+FFFD）、私用区（U+E000-F8FF，
// 子集字体的典型产物）、控制符都算无效。抽成乱码的 PDF 这些字符占比很高，
// 正常文本几乎为 0。
func quality(s string) float64 {
	if s == "" {
		return 0
	}
	total, good := 0, 0
	for _, r := range s {
		if r == '\n' || r == '\t' || r == ' ' {
			continue
		}
		total++
		switch {
		case r == unicode.ReplacementChar:
		case r >= 0xE000 && r <= 0xF8FF:
		case r < 0x20:
		case r >= 0xFFF0 && r <= 0xFFFF:
		default:
			good++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(good) / float64(total)
}

func isPDFEncryptedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pdf.ErrInvalidPassword) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "encrypt") || strings.Contains(msg, "password")
}

// pdfTextViaExec 尝试用 pdftotext 抽取。
//
// 只做「有没有」的探测，不做安装提示：没有 poppler 的机器上，
// 纯 Go 那条路仍然是可用的（只是中文成功率低一些）。
func pdfTextViaExec(path string) ([]string, bool) {
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		return nil, false
	}
	cmd := exec.Command(bin, "-layout", "-enc", "UTF-8", path, "-")
	hideCmdWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	if out.Len() == 0 {
		return nil, false
	}
	return strings.Split(out.String(), "\f"), true
}
