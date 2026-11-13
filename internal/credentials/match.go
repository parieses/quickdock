package credentials

import (
	"net/url"
	"sort"
	"strings"

	"quickdock/internal/db"
)

// MatchQuery 一次「这个场景该用哪条密码」的询问。
//
// 浏览器扩展传 Host（从 location.hostname 来），内部弹层传 Host+Port，
// 两边的字段含义一致，所以共用一套打分。
type MatchQuery struct {
	Kind     string // 空串表示不限类型
	Host     string // 主机名或完整 URL 均可，内部会归一化
	Port     int
	Username string
}

// MatchResult 一条候选与它的得分（得分只用于排序，不对外暴露）。
type MatchResult struct {
	Credential db.Credential
	Score      int
}

// Match 按场景给候选条目打分，返回分数最高的一批（最多 8 条）。
//
// 为什么在内存里打分而不是 SQL LIKE：条目量级是几十到几百，
// 而「域名后缀匹配 + 用户名加权 + 收藏/频次」这种混合排序用 SQL 写会是一坨
// 难以解释的 CASE WHEN，还容易在域名边界上出错（example.com 不该匹配 notexample.com）。
func Match(q MatchQuery) ([]db.Credential, error) {
	list, err := List()
	if err != nil {
		return nil, err
	}
	host := normalizeHost(q.Host)
	// 没有 host 就没有「场景」：返回全库只会把别人的密码推到当前页面，
	// 用户看到的就是「完全不搭嘎」，点一下还可能把密码填进错误的站点。
	// 三个调用方（扩展 / 内部弹层 / SSH 回落）都必然带 host，收紧不会误伤。
	if host == "" {
		return nil, nil
	}

	out := make([]MatchResult, 0, len(list))
	for _, c := range list {
		score := scoreOne(c, q, host)
		if score <= 0 {
			continue
		}
		out = append(out, MatchResult{Credential: c, Score: score})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})

	const limit = 8
	if len(out) > limit {
		out = out[:limit]
	}
	res := make([]db.Credential, len(out))
	for i := range out {
		res[i] = out[i].Credential
	}
	return res, nil
}

// scoreOne 单条打分。0 表示不该出现在这个场景里。
func scoreOne(c db.Credential, q MatchQuery, host string) int {
	score := 0

	// 类型不符直接出局：把 SSH 主机的密码推荐给网页登录框是纯噪音。
	if q.Kind != "" {
		if c.Kind != q.Kind {
			return 0
		}
		score += 25
	}

	credHost := normalizeHost(c.Host)
	if host != "" {
		// 有 host 的查询（浏览器扩展、内部弹层）必须过域名这道门。
		//
		// 早期版本在「域名完全对不上」时保留 kind 的基础分，本意是让
		// 「按类型搜索」也能看到东西，结果变成：A 网站的登录框里会列出
		// B 网站的密码，用户点一下就填进 A 站；更糟的是扩展据此判定
		// 「这条是 A 站的旧密码」→ 保存时把 B 站那条直接覆盖掉。
		// 场景匹配没有 host 就没有意义，对不上就是出局。
		if credHost == "" {
			// 条目没存域名（手动建的），无法确认属于这个站，不给推荐。
			return 0
		}
		switch {
		case credHost == host:
			score += 100
		case hostRelated(host, credHost):
			// 父子域关系要认，且双向都认：登录框常在 sso.example.com 而用户
			// 存的是 example.com，反过来也有「在 passport.baidu.com 存过、
			// 回主站 baidu.com 登录」的情况。早期只认一个方向，后者直接 0 分，
			// 用户看到的就是「明明存了却不弹」。
			score += 70
		default:
			return 0
		}
	}

	if q.Port != 0 && c.Port != 0 && q.Port == c.Port {
		score += 10
	}
	if q.Username != "" && c.Username != "" && strings.EqualFold(q.Username, c.Username) {
		score += 30
	}

	// 收藏与使用频次只做微调：它们不该压过域名匹配。
	if c.Favorite {
		score += 5
	}
	if c.UseCount > 0 {
		use := c.UseCount
		if use > 15 {
			use = 15 // 封顶，避免一条老密码永远霸榜
		}
		score += use
	}
	return score
}

// hostRelated 判断两个域名是否属于同一站点：互为父子域（按标签边界）。
//
// 必须按 "." 切：裸 HasSuffix 会让 notexample.com 匹配 example.com，
// 那会把密码填到钓鱼站上 —— 这是这个功能唯一不能出的安全事故。
// 双向是对称的：查询 host 是条目子域、或条目 host 是查询子域，都算关联。
//
// 单标签域名（localhost、com、内网主机名）一律不做父子域匹配：
// 一旦允许，「com」就会跟所有 .com 站点关联，等于把公共后缀当成一个站点。
// 这类 host 只能靠精确相等命中（由 scoreOne 的第一个 case 处理）。
func hostRelated(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if !strings.Contains(a, ".") || !strings.Contains(b, ".") {
		return false
	}
	return strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// normalizeHost 把「URL / host:port / 裸域名」统一成小写域名。
//
// 去 www. 前缀是为了让 www.example.com 与 example.com 归为同一条，
// 用户不会在存密码时纠结该不该带 www。
func normalizeHost(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	// 带 scheme 的走标准解析；不带的手工剥端口。
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			s = u.Hostname()
		}
	} else if strings.HasPrefix(s, "[") {
		// IPv6 字面量：[::1]:8080 → [::1]。冒号在地址内部，不能按 LastIndex 切。
		if i := strings.LastIndex(s, "]"); i > 0 {
			s = s[:i+1]
		}
	} else if i := strings.LastIndex(s, ":"); i > 0 && isDigits(s[i+1:]) {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".")
	s = strings.TrimPrefix(s, "www.")
	return s
}

// isDigits 判断是否全为十进制数字（空串返回 false）。
//
// 用它而不是「冒号后随便截」：裸 IPv6（::1）冒号后也是数字，
// 但那是地址本身，截了就变成 ":"，导致永远匹配不上。
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
