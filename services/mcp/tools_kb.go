package mcp

// 知识库域工具。
//
// 只开放读：知识库的写入路径（导入、重建索引）会做解析与 embedding 调用，
// 耗时以分钟计、还会花 API 费用，让模型去触发它们等于给「一句话把账单拉满」
// 开了口子。用户要导入文档，去界面上点。
//
// kb_search 是这套工具的核心：它让主对话能查本地文档，而不必把整篇文档
// 塞进上下文。描述里强调「返回片段而非全文」，是因为模型很容易以为
// 它拿到了整份文档，然后对着一段 800 字的内容回答「全文的主要观点是…」。

import (
	"errors"

	mcpsrv "quickdock/internal/mcp"
)

func registerKBTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str

	r.add(func(args map[string]any) (any, error) {
		if s.KB == nil {
			return nil, errors.New("知识库未就绪")
		}
		q := mcpsrv.Arg(args, "query")
		if q == "" {
			return nil, errors.New("缺少参数 query")
		}
		topN := mcpsrv.ArgInt(args, "topN", 5)
		if topN <= 0 || topN > 20 {
			topN = 5
		}
		return unwrap(s.KB.SearchKB(q, topN, ""))
	}, mcpsrv.LevelRead, "kb_search",
		"搜索本地知识库文档（Word/Excel/PDF/Markdown），返回最相关的若干文本片段及其来源文件与页码。"+
			"返回的是片段不是全文，回答时请说明依据的是哪一份文档。",
		[]string{"query"},
		map[string]any{
			"query": str("检索内容，自然语言或关键词均可（至少 3 个字符）"),
			"topN":  mcpsrv.Int("返回片段数，默认 5，最大 20"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.KB == nil {
			return nil, errors.New("知识库未就绪")
		}
		return unwrap(s.KB.ListKBSources(""))
	}, mcpsrv.LevelRead, "kb_list",
		"列出知识库中已导入的文档及其索引状态（ready / pending / failed）。"+
			"检索之前先用它确认有没有相关文档，比盲目 kb_search 更省事。",
		nil, nil)

	// 以下三条补齐「有哪些库 / 索引到哪了 / 某文档切成几段」——此前只有
	// kb_search + kb_list，AI 想判断检索是否覆盖某个库时只能猜。
	r.add(func(args map[string]any) (any, error) {
		if s.KB == nil {
			return nil, errors.New("知识库未就绪")
		}
		return unwrap(s.KB.ListKBLibraries())
	}, mcpsrv.LevelRead, "kb_libraries", "列出所有知识库（分库）。检索前先确认有哪些库，避免把 A 库的内容当成 B 库的", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if s.KB == nil {
			return nil, errors.New("知识库未就绪")
		}
		return unwrap(s.KB.GetKBStatus())
	}, mcpsrv.LevelRead, "kb_status", "查看知识库索引状态：已导入多少文档/分块、是否有待索引或失败项。用户问「文档索引好了没」时用这个，不要猜", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if s.KB == nil {
			return nil, errors.New("知识库未就绪")
		}
		return unwrap(s.KB.ListKBChunks(mcpsrv.Arg(args, "sourceID")))
	}, mcpsrv.LevelRead, "kb_chunks", "列出某份文档切分后的片段（含来源位置）。确认检索命中来自原文哪一段时用；sourceID 用 kb_list 返回的 id", []string{"sourceID"},
		map[string]any{"sourceID": str("文档来源 id，来自 kb_list")})
}
