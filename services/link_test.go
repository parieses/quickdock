package services

import (
	"encoding/json"
	"testing"

	"quickdock/internal/db"
)

// 反链行的 srcLabel 必须能被前端读到：它靠内嵌 db.EntityLink 展开全部边字段，
// 再额外带一个 srcLabel。若内嵌不展开（或字段名拼错），前端拿不到任何显示名，
// 整列反链会退化成「已删除」—— 这正是修复前的问题。
func TestBacklinkLinkJSONIncludesSrcLabel(t *testing.T) {
	raw, err := json.Marshal(backlinkLink{
		EntityLink: db.EntityLink{
			ID: "e1", SrcType: "note", SrcID: "n1", DstType: "todo",
			DstID: "t1", DstLabel: "目标", Kind: "manual", CreatedAt: "2026-01-01T00:00:00Z",
		},
		SrcLabel: "来源笔记",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["srcLabel"] != "来源笔记" {
		t.Fatalf("srcLabel 缺失: %s", raw)
	}
	// 内嵌结构的字段要平铺在同一层，前端按 EntityLink 的 camelCase 读取
	if m["id"] != "e1" || m["srcType"] != "note" || m["dstLabel"] != "目标" {
		t.Fatalf("内嵌字段没有展开: %s", raw)
	}
}
