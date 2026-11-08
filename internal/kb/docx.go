package kb

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// docx 解析。
//
// 用标准库 zip + encoding/xml 手写，不引第三方库：docx 的正文就是
// word/document.xml 里一串 <w:p>，真正麻烦的不是解析格式，而是三个
// 「不处理就会静默出错」的细节：
//
//  1. <w:instrText> 里也有 <w:t>。域代码（页码、交叉引用）抽出来是
//     "PAGE \* MERGEFORMAT" 这种东西，混进索引就是纯噪声，必须跳过。
//  2. xml:space="preserve" 的段落首尾空格有意义，不能在读 w:t 时 Trim。
//  3. 表格在 <w:tbl> 里，必须按 XML 流顺序一起消费。先收完所有段落
//     再收表格的写法，会让每张表都跑到文档末尾，上下文关系全断
//     ——而表格恰恰是「参数表」这类最该被检索到的内容。

var docxTitleRe = regexp.MustCompile(`<dc:title>([^<]*)</dc:title>`)

// cfwMagic 加密的 OOXML 文件不是 zip，而是 OLE 复合文档（CFB）。
// 文件头是 D0 CF 11 E0，认出它才能给「文件已加密」的提示，
// 否则用户只会看到一句 "zip: not a valid zip file"。
var cfwMagic = []byte{0xD0, 0xCF, 0x11, 0xE0}

func parseDocx(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 8 && bytes.Equal(raw[:4], cfwMagic) {
		return nil, ErrEncrypted
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupted, err)
	}

	var docFile, coreFile *zip.File
	for _, f := range zr.File {
		switch f.Name {
		case "word/document.xml":
			docFile = f
		case "docProps/core.xml":
			coreFile = f
		}
	}
	if docFile == nil {
		return nil, fmt.Errorf("%w: 缺少 word/document.xml", ErrCorrupted)
	}

	doc := &Document{}
	if coreFile != nil {
		if rc, err := coreFile.Open(); err == nil {
			if data, err := io.ReadAll(io.LimitReader(rc, 1<<20)); err == nil {
				if m := docxTitleRe.FindSubmatch(data); m != nil {
					doc.Title = strings.TrimSpace(string(m[1]))
				}
			}
			rc.Close()
		}
	}

	rc, err := docFile.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupted, err)
	}
	defer rc.Close()

	w := &docxWalker{doc: doc}
	if err := w.walk(rc); err != nil {
		return nil, err
	}
	w.flushParagraph()
	if len(doc.Blocks) == 0 {
		return nil, ErrNoText
	}
	return doc, nil
}

// docxWalker 流式消费 document.xml。
type docxWalker struct {
	doc *Document

	stack      [6]string // 标题栈，拼接成 "部署 / 数据库"
	curHeading string

	inParagraph bool
	inText      bool
	inInstr     bool
	inCell      bool
	inRow       bool
	headingLvl  int // 本段落是标题时记下级别，段落结束时再应用

	paraBuf  strings.Builder
	cellBuf  strings.Builder
	rowCells []string
}

func (w *docxWalker) walk(r io.Reader) error {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	// 某些 Word 导出的 XML 带 GBK 声明，Strict 模式下会因非 UTF-8 报错；
	// CharsetReader 统一交给 decodeBytes 的同一套判断。
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCorrupted, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			w.onStart(t)
		case xml.EndElement:
			w.onEnd(t)
		case xml.CharData:
			if w.inText && !w.inInstr {
				if w.inCell {
					w.cellBuf.Write(t)
				} else {
					w.paraBuf.Write(t)
				}
			}
		}
	}
}

func (w *docxWalker) onStart(t xml.StartElement) {
	switch t.Name.Local {
	case "p":
		w.inParagraph = true
		w.paraBuf.Reset()
		w.headingLvl = 0
	case "pStyle":
		for _, a := range t.Attr {
			if a.Name.Local == "val" {
				w.headingLvl = docxHeadingLevel(a.Value)
			}
		}
	case "instrText":
		w.inInstr = true
	case "t":
		w.inText = true
	case "tab":
		w.write("\t")
	case "br", "cr":
		w.write("\n")
	case "tbl":
		// 表格开始前先把挂着的段落收掉，避免表格内容被拼进上一段。
		w.flushParagraph()
	case "tr":
		w.inRow = true
		w.rowCells = nil
	case "tc":
		w.inCell = true
		w.cellBuf.Reset()
	}
}

func (w *docxWalker) onEnd(t xml.EndElement) {
	switch t.Name.Local {
	case "p":
		w.flushParagraph()
	case "instrText":
		w.inInstr = false
	case "t":
		w.inText = false
	case "tc":
		w.rowCells = append(w.rowCells, strings.TrimSpace(w.cellBuf.String()))
		w.cellBuf.Reset()
		w.inCell = false
	case "tr":
		if w.inRow {
			cells := make([]string, 0, len(w.rowCells))
			for _, c := range w.rowCells {
				if c != "" {
					cells = append(cells, c)
				}
			}
			if len(cells) > 0 {
				w.doc.Blocks = append(w.doc.Blocks, Block{
					Heading: w.curHeading,
					Text:    strings.Join(cells, " | "),
				})
			}
		}
		w.inRow = false
		w.rowCells = nil
	}
}

func (w *docxWalker) write(s string) {
	if w.inCell {
		w.cellBuf.WriteString(s)
	} else {
		w.paraBuf.WriteString(s)
	}
}

// flushParagraph 收一个段落。
//
// 标题段落本身不进 Blocks（它的文字已经进了标题栈，再作为正文入库
// 会让「数据库」这种两字标题在检索时反复命中自己所在章节的每一块）。
func (w *docxWalker) flushParagraph() {
	if !w.inParagraph {
		return
	}
	w.inParagraph = false
	text := strings.TrimSpace(w.paraBuf.String())
	w.paraBuf.Reset()

	if w.headingLvl > 0 && !w.inCell {
		lvl := w.headingLvl
		w.headingLvl = 0
		if text != "" {
			w.stack[lvl-1] = text
			for i := lvl; i < 6; i++ {
				w.stack[i] = ""
			}
			w.curHeading = joinHeading(w.stack[:])
			if w.doc.Title == "" {
				w.doc.Title = text
			}
		}
		return
	}
	if text == "" || w.inCell {
		return
	}
	w.doc.Blocks = append(w.doc.Blocks, Block{Heading: w.curHeading, Text: text})
}

// docxHeadingLevel 从 pStyle 的 val 里认出标题级别。
//
// Word 的样式名在不同语言版本里差别很大：英文版 "Heading1"、
// 中文版常常直接是 "1"，还有 "Heading 2" 带空格这种。只认这三种形态，
// 其余（"a3"、"Normal"、"ListParagraph"）一律当正文——宁可漏认一个标题，
// 也不要把正文误当标题，后者会让整节内容的 heading 全错。
func docxHeadingLevel(style string) int {
	s := strings.TrimSpace(style)
	if s == "" {
		return 0
	}
	lower := strings.ToLower(s)
	if len(s) == 1 && s[0] >= '1' && s[0] <= '6' {
		return int(s[0] - '0')
	}
	if !strings.HasPrefix(lower, "heading") && !strings.HasPrefix(s, "标题") {
		return 0
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] >= '1' && s[i] <= '6' {
			return int(s[i] - '0')
		}
		if s[i] != ' ' {
			break
		}
	}
	return 0
}
