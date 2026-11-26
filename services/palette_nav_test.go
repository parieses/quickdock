package services

// 导航白名单与前端页面注册表的对账。
//
// 两处清单分属不同语言，没法共享定义，只能靠测试兜住。漏一项的症状很难查：
// Ctrl+K 能搜到页面名（前端从 registry 自动生成候选），回车却毫无反应——
// 后端 NavigateMainWindow 返回「未知的导航页面」，面板把错误吃掉了。
// bookmarks / download / netdisk / credentials 就这么静默失效过。

import (
	"os"
	"regexp"
	"testing"
)

func TestNavigablePagesCoverRegistry(t *testing.T) {
	raw, err := os.ReadFile("../frontend/src/pages/registry.ts")
	if err != nil {
		t.Skipf("读不到前端注册表（单包测试时会这样），跳过：%v", err)
	}

	// 条目形如 { id: 'notes', labelKey: ..., icon: ..., component: XxxPage, ... }。
	// 只取「带 component 的内容页」—— ssh / dsh 是 activate 入口，本来就不在
	// 白名单里，用 [^}]*? 把匹配限制在同一条目内部，避免跨条目误吃。
	re := regexp.MustCompile(`(?s)\{ id: '([a-z]+)'[^}]*?component:`)
	matches := re.FindAllStringSubmatch(string(raw), -1)
	if len(matches) == 0 {
		t.Fatalf("没从 registry.ts 解析出任何内容页，正则可能已失效")
	}

	for _, m := range matches {
		id := m[1]
		if !navigablePages[id] {
			t.Errorf("内容页 %q 不在 navigablePages 里：Ctrl+K 能搜到但回车打不开", id)
		}
	}
}
