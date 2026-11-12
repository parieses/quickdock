package kb

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// xlsx 解析。
//
// 这里用 excelize 而不是像 docx 那样自己解 zip+xml，唯一的理由是
// 「日期和数字格式化」：xlsx 的单元格里日期的真实值是一个序列号
// （2025-03-01 = 45717），要靠 styles.xml 里的 numFmt 才知道该怎么显示。
// 自己实现这块极容易把日期读成 45717，而用户搜「2025 年 3 月」时
// 永远命中不了——错得安静且难查。excelize 已经把这一步做对了。
//
// 切片的硬要求：**表头必须重复进每一块**。第 200 行单独成块后是
// "2025-03-01 | 128 | 3000"，没有表头就是一串无意义数字，
// 关键词和向量都检索不出来。

const (
	xlsxRowsPerChunk = 30   // 每块数据行数
	xlsxMaxRows      = 5000 // 单 sheet 上限，超出截断
	xlsxCellMaxRunes = 500  // 单单元格字符上限
)

func parseXlsx(path string) (*Document, error) {
	// 加密的 xlsx 同样是 OLE 复合文档，先按文件头判掉，
	// 免得用户拿到一句 "zip: not a valid zip file"。
	if f, err := os.Open(path); err == nil {
		head := make([]byte, 4)
		n, _ := f.Read(head)
		f.Close()
		if n == 4 && string(head) == string(cfwMagic) {
			return nil, ErrEncrypted
		}
	}

	// GetRows 是整表载入内存，先挡掉离谱的大文件（与 docx 的门禁同思路）。
	if st, err := os.Stat(path); err == nil && st.Size() > 100<<20 {
		return nil, fmt.Errorf("%w: xlsx 超过 100MB", ErrUnsupported)
	}

	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupted, err)
	}
	defer f.Close()

	doc := &Document{}
	truncated := false

	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			// 单个 sheet 读失败不该让整份文件报废：图表 sheet、
			// 隐藏的宏表都可能读不出来，跳过继续。
			continue
		}
		// 行内容全部为空的先剔掉，否则「行 2-31」这种标号会和实际内容对不上，
		// 用户在预览里看到的行号范围会误导人。
		var data [][]string
		for _, r := range rows {
			if !rowIsEmpty(r) {
				data = append(data, r)
			}
		}
		if len(data) == 0 {
			continue
		}
		if len(data) > xlsxMaxRows {
			data = data[:xlsxMaxRows]
			truncated = true
		}

		header := joinCells(data[0])
		if doc.Title == "" && header != "" {
			doc.Title = sheet
		}
		body := data[1:]
		if len(body) == 0 {
			// 只有表头没有数据，也算一块，否则这个 sheet 整个消失。
			doc.Blocks = append(doc.Blocks, Block{Heading: sheet, Text: header})
			continue
		}
		for start := 0; start < len(body); start += xlsxRowsPerChunk {
			end := start + xlsxRowsPerChunk
			if end > len(body) {
				end = len(body)
			}
			var b strings.Builder
			if header != "" {
				b.WriteString(header)
				b.WriteString("\n")
			}
			for _, r := range body[start:end] {
				b.WriteString(joinCells(r))
				b.WriteString("\n")
			}
			// 行号用 Excel 里的真实行号（+1 是因为 data 已去掉空行、
			// 且第 1 行是表头），用户对着 Excel 能直接定位。
			heading := fmt.Sprintf("%s · 行 %d-%d", sheet, start+2, end+1)
			doc.Blocks = append(doc.Blocks, Block{Heading: heading, Text: strings.TrimRight(b.String(), "\n")})
		}
	}

	if len(doc.Blocks) == 0 {
		return nil, ErrNoText
	}
	doc.Truncated = truncated
	return doc, nil
}

func rowIsEmpty(r []string) bool {
	for _, c := range r {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func joinCells(r []string) string {
	cells := make([]string, 0, len(r))
	for _, c := range r {
		// 单元格内的换行（Alt+Enter）改成一个空格：既保住「两个词别粘一起」，
		// 又不会在汉字之间留下空白把 trigram 索引切断——汉字间的空格
		// 由 ParseFile 的 normalizeDocWhitespace 统一压掉。
		c = strings.ReplaceAll(c, "\r\n", " ")
		c = strings.ReplaceAll(c, "\n", " ")
		c = strings.ReplaceAll(c, "\r", " ")
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if utf8.RuneCountInString(c) > xlsxCellMaxRunes {
			c = string([]rune(c)[:xlsxCellMaxRunes]) + "…"
		}
		cells = append(cells, c)
	}
	return strings.Join(cells, " | ")
}
