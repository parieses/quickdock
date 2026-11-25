package bookmark

import (
	"quickdock/services"
)

// 策展洞察：把书签库的「健康度」算成一组可执行结论。
//
// 刻意不调模型：报告里的每个数字都能点进去核对，写成一句话反而没法验证；
// 需要解读时用户本来就能把这份数据丢给 AI 助手（书签页提供入口）。
// 结论只给「该做什么」，不评价内容好坏——那是用户自己的事。

// CurationAdvice 一条待处理建议。Kind 由前端映射成 i18n 文案，
// 后端不回中文，避免与前端语言设置打架。
type CurationAdvice struct {
	Kind  string `json:"kind"` // stale | broken | duplicates | blocked | neverOpened
	Count int    `json:"count"`
}

// CurationInsight 报告载荷。
type CurationInsight struct {
	Stats    interface{}      `json:"stats"`
	Advice   []CurationAdvice `json:"advice"`
	HealthOK int              `json:"healthOk"`
}

// BuildCurationInsight 生成报告。
func (s *BookmarkService) BuildCurationInsight() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	rows, err := s.App.DB.ListBookmarks("", 0)
	if err != nil {
		return services.Fail(err)
	}
	extra := duplicateExtra(groupDuplicates(rows))

	stats, err := s.App.DB.BookmarkStatsFor(extra)
	if err != nil {
		return services.Fail(err)
	}

	out := CurationInsight{Stats: stats, HealthOK: stats.Checked - stats.Broken - stats.Blocked - stats.Moved}

	// 顺序即优先级：死链最该处理（点开就是 404），其次是重复（越攒越难理），
	// 然后是「收了但没打开过」的沉淀，最后是反爬类（多半没法处理，仅供参考）。
	add := func(kind string, n int) {
		if n > 0 {
			out.Advice = append(out.Advice, CurationAdvice{Kind: kind, Count: n})
		}
	}
	add("broken", stats.Broken)
	add("duplicates", stats.Duplicates)
	add("stale", stats.Stale)
	add("blocked", stats.Blocked)

	return services.Ok(out)
}
