package services

import (
	"hash/fnv"
	"strconv"

	"quickdock/internal/linkutil"
)

// 关联接口的入参上限。id 是 UUID、label 是标题快照，500 已远大于正常值；
// 挡住异常长串，避免把无意义的数据写进边表 / 拖慢全表扫描。
const maxLinkParamLen = 500

func validLinkType(t string) bool {
	return t == "note" || t == "todo" || t == "item"
}

// ListEntityLinks 列出某主体的全部出边。data: []db.EntityLink
func (a *AppService) ListEntityLinks(srcType, srcId string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if !validLinkType(srcType) {
		return FailMsg("非法的实体类型")
	}
	if len(srcId) > maxLinkParamLen {
		return FailMsg("源 id 过长")
	}
	return wrap(a.DB.ListEntityLinks(srcType, srcId))
}

// ListEntityBacklinks 反链（精确 id + 标题兜底）。data: []db.EntityLink
func (a *AppService) ListEntityBacklinks(dstType, dstId, dstLabel string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if !validLinkType(dstType) {
		return FailMsg("非法的实体类型")
	}
	if len(dstId) > maxLinkParamLen || len(dstLabel) > maxLinkParamLen {
		return FailMsg("参数过长")
	}
	return wrap(a.DB.ListEntityBacklinks(dstType, dstId, dstLabel))
}

// AddEntityLink 手动建一条关联，kind 固定为 manual。data: db.EntityLink（落库行）
func (a *AppService) AddEntityLink(srcType, srcId, dstType, dstId, dstLabel string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if !validLinkType(srcType) || !validLinkType(dstType) {
		return FailMsg("非法的实体类型")
	}
	if srcId == "" {
		return FailMsg("源 id 不能为空")
	}
	if len(srcId) > maxLinkParamLen || len(dstId) > maxLinkParamLen || len(dstLabel) > maxLinkParamLen {
		return FailMsg("参数过长")
	}
	return wrap(a.DB.AddEntityLink(srcType, srcId, dstType, dstId, dstLabel, "manual"))
}

// RemoveEntityLink 删一条关联。data: nil
func (a *AppService) RemoveEntityLink(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if id == "" {
		return FailMsg("关联 id 不能为空")
	}
	if len(id) > maxLinkParamLen {
		return FailMsg("关联 id 过长")
	}
	if err := a.DB.RemoveEntityLink(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ListLinkTargets 关联选择器候选项。
// data: { "notes":[{id,label}], "todos":[{id,label}], "items":[{id,label}] }
func (a *AppService) ListLinkTargets() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	notes, todos, items, err := a.DB.ListLinkTargets()
	if err != nil {
		return Fail(err)
	}
	return Ok(map[string]interface{}{
		"notes": notes,
		"todos": todos,
		"items": items,
	})
}

// ResolveWikiTarget 解析单个 wiki 标签到目标。
// 找不到返回 Ok(nil)，前端据此提示「未找到」而不是当成接口报错。
// data: {"type":string,"id":string,"label":string} 或 nil
func (a *AppService) ResolveWikiTarget(label string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if len(label) > maxLinkParamLen {
		return FailMsg("标签过长")
	}
	ref := linkutil.ParseWikiLabel(label)
	dstType, dstID := a.DB.FindLinkTargets(ref.Type, ref.Label)
	if dstID == "" {
		return Ok(nil)
	}
	return Ok(map[string]string{
		"type":  dstType,
		"id":    dstID,
		"label": ref.Label,
	})
}

const (
	graphDefaultLimit = 800
	graphMaxLimit     = 5000
	// 悬空目标（候选里找不到、只剩 dst_label 的边）的稳定伪 id 前缀。
	// 仍以 "type:原始id" 形式出现，但原始 id 用 "~"+hash 标记：前端据此识别为
	// 不可跳转的占位节点，emit openTarget 时给空 id。hash 只吃 type/label，
	// 同一条悬空边每次构图都得到同一个 id，不会随刷新乱跳。
	graphDanglingPrefix = "~"
)

// graphNode / graphLink 是图谱接口固定的输出结构，字段名按前端约定用小写。
type graphNode struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

type graphLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

func graphNodeID(typ, id string) string { return typ + ":" + id }

// hashDanglingID 用 FNV-1a 把 (type, label) 压成稳定十六进制串。
// 不用随机数 / 时间戳：悬空节点的 id 必须在每次刷新间保持不变。
func hashDanglingID(typ, label string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(typ + "\x00" + label))
	return graphDanglingPrefix + strconv.FormatUint(h.Sum64(), 16)
}

// GraphData 给关系图谱一次性返回构图数据。
// data: {"nodes":[{id,type,label}], "links":[{source,target,kind}]}
//
// 节点 id 统一为 "type:原始id"，三类主体即使内部 id 撞车也不会混成一个点。
// nodes = ListLinkTargets() 的三类候选 + 边里出现但候选里没有的源/目标
// （悬空目标用 dst_label 兜底 + 稳定伪 id，见 hashDanglingID）。
// links 的 source/target 必定是 nodes 里存在的 id，同一对 + kind 去重。
func (a *AppService) GraphData(limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if limit == 0 {
		limit = graphDefaultLimit
	}
	if limit < 0 || limit > graphMaxLimit {
		return FailMsg("图谱上限参数非法")
	}
	notes, todos, items, err := a.DB.ListLinkTargets()
	if err != nil {
		return Fail(err)
	}
	edges, err := a.DB.ListAllEntityLinks(limit)
	if err != nil {
		return Fail(err)
	}

	// nodeMap 去重，order 保留插入顺序，保证每次输出的 nodes 顺序稳定。
	nodeMap := make(map[string]*graphNode)
	var order []string
	addNode := func(typ, id, label string) string {
		nid := graphNodeID(typ, id)
		if _, ok := nodeMap[nid]; !ok {
			nodeMap[nid] = &graphNode{ID: nid, Type: typ, Label: label}
			order = append(order, nid)
		}
		return nid
	}

	// 候选按 note → todo → item 顺序登记，与 resolveLinkTarget /
	// ListEntityBacklinks 的「无前缀边按 notes→todos→items 找第一个」一致。
	type candidate struct {
		nodeID string
		typ    string
		label  string
	}
	var candidates []candidate
	for _, c := range notes {
		nid := addNode("note", c.ID, c.Label)
		candidates = append(candidates, candidate{nid, "note", c.Label})
	}
	for _, c := range todos {
		nid := addNode("todo", c.ID, c.Label)
		candidates = append(candidates, candidate{nid, "todo", c.Label})
	}
	for _, c := range items {
		nid := addNode("item", c.ID, c.Label)
		candidates = append(candidates, candidate{nid, "item", c.Label})
	}

	// findByLabel 复刻 ListEntityBacklinks 的兜底命中条件：
	// 边侧 dst_type 为空/非法 → 对任意类型候选命中；合法 → 只认同类候选。
	findByLabel := func(label, hintType string) (string, bool) {
		if label == "" {
			return "", false
		}
		hintOK := validLinkType(hintType)
		for _, c := range candidates {
			if c.label != label {
				continue
			}
			if !hintOK || c.typ == hintType {
				return c.nodeID, true
			}
		}
		return "", false
	}

	seenLink := make(map[string]bool, len(edges))
	links := make([]graphLink, 0, len(edges))
	for _, e := range edges {
		// 源必须是三类之一且带 id：否则这条边没有可作 source 的节点。
		if !validLinkType(e.SrcType) || e.SrcID == "" {
			continue
		}
		// 源主体可能已被删除（悬空边）：边表没有 src_label 快照，
		// 补一个占位节点并退而用 src_id 当显示名，保证边不被丢掉。
		srcNode := addNode(e.SrcType, e.SrcID, "")
		if nodeMap[srcNode].Label == "" {
			nodeMap[srcNode].Label = e.SrcID
		}

		var dstNode string
		if e.DstID != "" && validLinkType(e.DstType) {
			// 已解析出目标 id：按 id 登记，dst_label 只是标题快照。
			dstNode = addNode(e.DstType, e.DstID, e.DstLabel)
			if nodeMap[dstNode].Label == "" {
				nodeMap[dstNode].Label = e.DstID
			}
		} else if e.DstLabel != "" {
			// dst_id 为空（写 [[标题]] 时目标还没建）：按标题在候选里兜底
			// 命中；候选里也没有就是悬空目标，用稳定伪 id 生成占位节点。
			if nid, ok := findByLabel(e.DstLabel, e.DstType); ok {
				dstNode = nid
			} else {
				typ := e.DstType
				if !validLinkType(typ) {
					typ = "note"
				}
				dstNode = addNode(typ, hashDanglingID(typ, e.DstLabel), e.DstLabel)
			}
		} else {
			// 既没有目标 id 也没有标题，无法构成可显示的目标节点。
			continue
		}

		kind := e.Kind
		if kind != "wiki" && kind != "manual" {
			kind = "manual"
		}
		key := srcNode + "\x00" + dstNode + "\x00" + kind
		if seenLink[key] {
			continue
		}
		seenLink[key] = true
		links = append(links, graphLink{Source: srcNode, Target: dstNode, Kind: kind})
	}

	nodes := make([]graphNode, 0, len(order))
	for _, id := range order {
		nodes = append(nodes, *nodeMap[id])
	}
	return Ok(map[string]interface{}{
		"nodes": nodes,
		"links": links,
	})
}
