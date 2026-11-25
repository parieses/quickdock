package db

// 书签（网页条目）的统计口径。
//
// 全部走 SQL 聚合而不是把条目取到内存里数：书签上千条时前端只想要结论，
// 拉全量再算既慢又白占内存。口径集中在本文件，书签页与洞察报告共用同一份，
// 避免两处各写一套条件导致「页面数字与报告数字对不上」。

// Bucket 一个分组计数（场景 / 集合 / 状态通用）。
type Bucket struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// BookmarkStats 书签库的整体画像。
type BookmarkStats struct {
	Total        int      `json:"total"`
	NeverOpened  int      `json:"neverOpened"` // 收藏后一次都没打开过（usage_count = 0）
	NewLast7     int      `json:"newLast7"`    // 最近 7 天新增
	Stale        int      `json:"stale"`       // 从未打开且入库超过 30 天
	Collections  int      `json:"collections"` // 有条目的集合数
	Duplicates   int      `json:"duplicates"`  // 归一化 URL 重复的「多余条数」，不是组数
	Broken       int      `json:"broken"`      // 死链
	Blocked      int      `json:"blocked"`     // 反爬 / 需登录
	Moved        int      `json:"moved"`       // 地址已变
	Changed      int      `json:"changed"`     // 内容已变化
	Checked      int      `json:"checked"`     // 有检查记录的条数
	ByScene      []Bucket `json:"byScene"`
	TopCollected []Bucket `json:"topCollected"`
}

// BookmarkStatsFor 计算画像。duplicate 数为调用方传入（它在 service 层按归一化
// URL 算，涉及 URL 归一化规则，不适合下沉到 SQL）。
func (d *Database) BookmarkStatsFor(duplicates int) (*BookmarkStats, error) {
	st := &BookmarkStats{Duplicates: duplicates}

	row, err := d.QueryOne(`SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN usage_count = 0 THEN 1 ELSE 0 END), 0) AS never_opened,
			COALESCE(SUM(CASE WHEN julianday(created_at) >= julianday('now', '-7 day') THEN 1 ELSE 0 END), 0) AS new_last7,
			COALESCE(SUM(CASE WHEN usage_count = 0 AND julianday(created_at) < julianday('now', '-30 day') THEN 1 ELSE 0 END), 0) AS stale,
			COUNT(DISTINCT collection_id) AS collections
		FROM items WHERE type = ?`, "网页")
	if err != nil {
		return nil, err
	}
	if row != nil {
		st.Total = integer(row["total"])
		st.NeverOpened = integer(row["never_opened"])
		st.NewLast7 = integer(row["new_last7"])
		st.Stale = integer(row["stale"])
		st.Collections = integer(row["collections"])
	}

	if rows, e := d.Query(`SELECT status, COUNT(*) AS c FROM item_health GROUP BY status`); e == nil {
		for _, r := range rows {
			n := integer(r["c"])
			st.Checked += n
			switch str(r["status"]) {
			case "broken":
				st.Broken = n
			case "blocked":
				st.Blocked = n
			case "moved":
				st.Moved = n
			}
		}
	}

	if row, e := d.QueryOne("SELECT COUNT(*) AS c FROM item_health WHERE changed = 1"); e == nil && row != nil {
		st.Changed = integer(row["c"])
	}

	if rows, e := d.Query(`SELECT COALESCE(s.name, '未分类') AS name, COUNT(*) AS c
		FROM items i
		LEFT JOIN collections c ON c.id = i.collection_id
		LEFT JOIN scenes s ON s.id = c.scene_id
		WHERE i.type = ?
		GROUP BY s.name ORDER BY c DESC`, "网页"); e == nil {
		for _, r := range rows {
			st.ByScene = append(st.ByScene, Bucket{Name: str(r["name"]), Count: integer(r["c"])})
		}
	}

	if rows, e := d.Query(`SELECT c.name AS name, COUNT(*) AS c
		FROM items i JOIN collections c ON c.id = i.collection_id
		WHERE i.type = ?
		GROUP BY c.name ORDER BY c DESC LIMIT 8`, "网页"); e == nil {
		for _, r := range rows {
			st.TopCollected = append(st.TopCollected, Bucket{Name: str(r["name"]), Count: integer(r["c"])})
		}
	}

	return st, nil
}
