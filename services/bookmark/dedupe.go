package bookmark

import (
	"sort"
	"strings"

	ibk "quickdock/internal/bookmark"
	"quickdock/internal/logger"
	"quickdock/services"
)

// 重复条目检测与合并。
//
// 两轮判定，都只用「可解释的规则」，不上向量相似度：
//  1. 归一化 URL 相同 —— 同一条链接被收进多个文件夹，或导入时 URL 带了不同跟踪参数
//  2. 标题相同但 URL 不同 —— 同一篇文章的 http/https、带不带 www、站点改版后的新地址
//
// 判重只给候选、不自动删：删错一条收藏的代价远高于让用户点两下。合并动作也保留
// 「哪条留下」的选择权（保留下来的那条，备注为空时会吸收被删条目的备注）。

// DuplicateItem 重复组里的一条。
type DuplicateItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	URL            string `json:"url"`
	CollectionName string `json:"collectionName"`
	SceneName      string `json:"sceneName"`
	UsageCount     int    `json:"usageCount"`
	Remark         string `json:"remark"`
}

// DuplicateGroup 一组疑似重复。
type DuplicateGroup struct {
	Kind  string          `json:"kind"` // url | title
	Key   string          `json:"key"`  // 归一化 URL（kind=url）或标题（kind=title）
	Items []DuplicateItem `json:"items"`
}

// maxDupGroups 回给前端的组数上限。真实场景里重复不会上千组，
// 这个上限只是防止异常数据把一次 IPC 撑爆。
const maxDupGroups = 200

// FindDuplicates 找出疑似重复，按组大小降序。
func (s *BookmarkService) FindDuplicates() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	rows, err := s.App.DB.ListBookmarks("", 0)
	if err != nil {
		return services.Fail(err)
	}
	groups := groupDuplicates(rows)
	return services.Ok(map[string]interface{}{
		"groups":     groups,
		"extraCount": duplicateExtra(groups),
	})
}

// groupDuplicates 纯函数，便于单测（不碰数据库）。
func groupDuplicates(rows []map[string]interface{}) []DuplicateGroup {
	byURL := map[string][]DuplicateItem{}
	byTitle := map[string][]DuplicateItem{}
	order := []string{} // 保持「先 URL 组、后标题组」的稳定顺序

	for _, r := range rows {
		it := DuplicateItem{
			ID:             str(r["id"]),
			Name:           str(r["name"]),
			URL:            str(r["value"]),
			CollectionName: str(r["collection_name"]),
			SceneName:      str(r["scene_name"]),
			UsageCount:     num(r["usage_count"]),
			Remark:         str(r["remark"]),
		}
		if it.ID == "" || it.URL == "" {
			continue
		}
		key := ibk.NormalizeURL(it.URL)
		if key == "" {
			continue
		}
		if _, ok := byURL[key]; !ok {
			order = append(order, key)
		}
		byURL[key] = append(byURL[key], it)

		if t := strings.ToLower(strings.TrimSpace(it.Name)); t != "" {
			byTitle[t] = append(byTitle[t], it)
		}
	}

	out := make([]DuplicateGroup, 0, 8)
	for _, key := range order {
		if items := byURL[key]; len(items) > 1 {
			out = append(out, DuplicateGroup{Kind: "url", Key: key, Items: items})
		}
	}

	// 标题相同但 URL 不同：只报「各自 URL 唯一」的条目，否则会和上面的 URL 组重复计数
	titles := make([]string, 0, len(byTitle))
	for t := range byTitle {
		titles = append(titles, t)
	}
	sort.Strings(titles)
	for _, t := range titles {
		items := byTitle[t]
		if len(items) < 2 {
			continue
		}
		distinct := map[string]bool{}
		allUnique := true
		for _, it := range items {
			k := ibk.NormalizeURL(it.URL)
			if len(byURL[k]) > 1 {
				allUnique = false
				break
			}
			distinct[k] = true
		}
		if !allUnique || len(distinct) < 2 {
			continue
		}
		out = append(out, DuplicateGroup{Kind: "title", Key: t, Items: items})
	}

	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Items) > len(out[j].Items) })
	if len(out) > maxDupGroups {
		out = out[:maxDupGroups]
	}
	return out
}

// duplicateExtra 多余条数 = 每组「总数 - 1」之和，即「合并后能少几行」。
func duplicateExtra(groups []DuplicateGroup) int {
	n := 0
	for _, g := range groups {
		if len(g.Items) > 1 {
			n += len(g.Items) - 1
		}
	}
	return n
}

// MergeDuplicates 保留 keepID，删除 dropIDs。
//
// 保留条目的备注为空时吸收第一条有备注的重复项——书签导入时备注来自 <DD>，
// 手工条目则常把「为什么收藏」写在备注里，合并时丢掉这段信息太可惜。
func (s *BookmarkService) MergeDuplicates(keepID string, dropIDs []string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if keepID == "" || len(dropIDs) == 0 {
		return services.FailMsg("缺少保留项或待合并项")
	}

	keep, err := s.App.DB.GetWebItem(keepID)
	if err != nil || keep == nil {
		return services.FailMsg("保留的条目不存在")
	}

	merged := 0
	for _, id := range dropIDs {
		if id == "" || id == keepID {
			continue
		}
		drop, e := s.App.DB.GetWebItem(id)
		if e != nil || drop == nil {
			continue
		}
		if strings.TrimSpace(keep.Remark) == "" && strings.TrimSpace(drop.Remark) != "" {
			if uerr := s.App.DB.UpdateItem(keepID, map[string]interface{}{"remark": drop.Remark}); uerr == nil {
				keep.Remark = drop.Remark
			}
		}
		if derr := s.App.DB.DeleteItem(id); derr != nil {
			logger.W("QuickDock: 合并重复条目时删除失败 id=%s: %v", id, derr)
			continue
		}
		merged++
	}
	if merged == 0 {
		return services.FailMsg("没有可合并的条目")
	}
	return services.OkMsg(map[string]interface{}{"merged": merged}, "已合并")
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}

func num(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
