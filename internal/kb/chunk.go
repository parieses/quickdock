package kb

import "strings"

// 切片器。
//
// 目标不是「把文本切成 800 字的块」这么简单，而是「让每一块自己站得住」：
// 一块被单独取出来送进模型时，它得能说清自己讲的是什么、属于哪一节。
// 所以按文档结构（标题 / 页码）下刀，字数只是兜底约束。
//
// 三个决定质量的选择：
//  1. 标题变化必切，哪怕上一块才 50 字。把「安装」的尾巴和「故障排查」的
//     开头混在一块，模型会拿排障步骤当安装步骤答。
//  2. 相邻块重叠 100 字，而且按句子边界回退。硬切会让一个句子被劈成两半，
//     两边的词法匹配都不完整。
//  3. 800 是按「字符」不是「字节」算。中文 800 字约 1200 token，
//     正好是一批 embedding 的合理粒度，也是「模型读得完、检索够聚焦」的甜点。

const (
	// ChunkSize 单块目标长度（rune）。
	ChunkSize = 800
	// ChunkOverlap 相邻块重叠长度（rune）。
	ChunkOverlap = 100
	// chunkMinMerge 小于这个长度的块尝试与下一块合并，避免产生一堆碎片块
	// （碎片块检索时特别容易误命中：三个字就成一块，随便搜什么都排前面）。
	chunkMinMerge = 60
)

// Chunk 一个切片。
type Chunk struct {
	Idx     int
	Heading string
	Text    string
	Page    int
	// Ordinal 同一标题下的第几块（从 1 开始）。预览里标「第 3 页（2/3）」
	// 这种位置信息用，也让用户一眼看出切分粒度是否合理。
	Ordinal int
}

// Split 把解析结果切成检索单元。
func Split(doc *Document) []Chunk {
	if doc == nil || len(doc.Blocks) == 0 {
		return nil
	}

	var out []Chunk
	var cur []rune
	curHeading, curPage := "", 0
	hasNew := false
	ordinal := 0

	emit := func() {
		text := strings.TrimSpace(string(cur))
		if text == "" {
			cur = cur[:0]
			hasNew = false
			return
		}
		ordinal++
		out = append(out, Chunk{
			Idx:     len(out),
			Heading: curHeading,
			Text:    text,
			Page:    curPage,
			Ordinal: ordinal,
		})
	}

	// flushKeepOverlap 超长切块：保留尾部作为下一块的开头。
	flushKeepOverlap := func() {
		if !hasNew {
			return
		}
		emit()
		cur = tailForOverlap(cur, ChunkOverlap)
		hasNew = false
	}

	// flushHard 章节边界：不重叠。重叠的意义是「接上跨块的一句话」，
	// 跨章节重叠只会把上一节的结论混进下一节的开头，反而误导。
	flushHard := func() {
		if !hasNew {
			return
		}
		emit()
		cur = cur[:0]
		hasNew = false
	}

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if len(cur) > 0 {
			cur = append(cur, '\n', '\n')
		}
		cur = append(cur, []rune(s)...)
		hasNew = true
	}

	for _, blk := range doc.Blocks {
		if hasNew && (blk.Heading != curHeading || blk.Page != curPage) {
			flushHard()
			ordinal = 0
		}
		curHeading, curPage = blk.Heading, blk.Page

		for _, para := range splitParagraphs(blk.Text) {
			for _, piece := range splitLongText(para, ChunkSize) {
				// +2 是即将插入的分段空行。
				if len(cur)+len([]rune(piece))+2 > ChunkSize && len(cur) > 0 {
					flushKeepOverlap()
				}
				add(piece)
			}
		}
	}
	if hasNew {
		emit()
	}

	return mergeTiny(out)
}

// mergeTiny 把小碎片并入前一块。
//
// 为什么会出碎片：文档末尾、表格之间的过渡句、只有一行的小节标题。
// 单独成块之后，它既没有上下文，又因为字数少而在 bm25 里被异常放大
// （BM25 对短文档有天然加成），检索时老是排在一个明明更相关的长块前面。
func mergeTiny(in []Chunk) []Chunk {
	if len(in) == 0 {
		return in
	}
	out := make([]Chunk, 0, len(in))
	for _, c := range in {
		if len(out) > 0 && len([]rune(c.Text)) < chunkMinMerge {
			prev := &out[len(out)-1]
			merged := prev.Text + "\n\n" + c.Text
			// 合并后不能超过两块的大小，否则会挤爆 embedding 的批预算。
			if len([]rune(merged)) <= ChunkSize*2 {
				prev.Text = merged
				continue
			}
		}
		out = append(out, c)
	}
	// 索引重排：合并之后 idx 会出现空洞，而 idx 是 UNIQUE(source_id, idx) 的一部分，
	// 空洞本身无害但预览里会显示「第 1、3、4 块」，看着像丢数据。
	for i := range out {
		out[i].Idx = i
	}
	return out
}

// tailForOverlap 取尾部作为下一块的开头，优先从句子边界起。
func tailForOverlap(cur []rune, overlap int) []rune {
	if len(cur) <= overlap*2 {
		// 整块本来就不长，重叠会把内容重复一遍，不如不重叠。
		return cur[:0]
	}
	start := len(cur) - overlap
	minStart := start - 200
	if minStart < 0 {
		minStart = 0
	}
	for i := start - 1; i >= minStart; i-- {
		if isSentenceEnd(cur[i]) {
			start = i + 1
			break
		}
	}
	out := make([]rune, len(cur)-start)
	copy(out, cur[start:])
	return out
}

// splitLongText 把超长段落按句子边界切成 <= max 的若干片。
func splitLongText(s string, max int) []string {
	r := []rune(s)
	if len(r) <= max {
		return []string{s}
	}
	var out []string
	for start := 0; start < len(r); {
		if len(r)-start <= max {
			out = append(out, strings.TrimSpace(string(r[start:])))
			break
		}
		cut := start + max
		limit := cut - 200
		if limit < start {
			limit = start
		}
		for i := cut - 1; i > limit; i-- {
			if isSentenceEnd(r[i]) {
				cut = i + 1
				break
			}
		}
		out = append(out, strings.TrimSpace(string(r[start:cut])))
		start = cut
	}
	return out
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '…', '.', '!', '?', ';', '\n':
		return true
	}
	return false
}
