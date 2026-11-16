package mcp

// 域目录的完备性检查。
//
// 这一组测试存在的理由很具体：改造前「一个域」的信息散在五个地方（Category 常量、
// i18n、前端 MCP_CATEGORY_ORDER、toolHint 常量、服务层白名单），加一个域必漏一处，
// 而漏掉的后果都是**静默**的——工具还在，只是不显示 / 不说明 / 分组跑到末尾。
// 现在目录只有 services/mcp/catalog.go 一处，这些测试负责让它保持「一处就够」。

import (
	"sort"
	"strings"
	"testing"

	mcpsrv "quickdock/internal/mcp"
	aitools "quickdock/services/ai/tools"
)

// registerAllTools 注册两边的工具：对外能力 + 内置 AI 专有（web / code 两个域）。
//
// 只注册 MCPService 那批是不够的：web_fetch / file_write 这些写在同一个域目录里，
// 却不在这里注册。少了它们，提示词一致性检查会对着一个「不存在的工具」报错，
// 而那其实只是本测试进程没装另一半。
func registerAllTools() {
	s := &MCPService{}
	s.registerTools()
	// 空 Deps 即可：这里只关心工具的定义（名字/等级/域），不调用 Handler。
	aitools.Register(aitools.Deps{})
}

// toolNamesIn 从一段说明文字里挑出形如 file_write / env_stop 的工具名。
//
// 只认「全小写 + 下划线」的形态：中文正文里不会出现这种串，而 _cwd / _scope
// 这类宿主注入键（以下划线开头）被显式排掉。
func toolNamesIn(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && r != '_'
	}) {
		if !strings.Contains(w, "_") || strings.HasPrefix(w, "_") || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// TestEveryRegisteredToolHasKnownDomain 每个注册的工具都必须落在一个已登记的域里。
//
// 没登记的域不会报错，它只会表现成面板末尾一个用 id 当标题的孤儿分组，
// 提示词里也没有关于它的一句话——模型拿到一个没有任何使用说明的工具。
func TestEveryRegisteredToolHasKnownDomain(t *testing.T) {
	registerAllTools()

	known := make(map[string]bool, 24)
	for _, d := range mcpsrv.Domains() {
		known[d.ID] = true
	}
	if len(known) == 0 {
		t.Fatal("域目录是空的：registerDomains 没被调用")
	}

	var orphans []string
	for _, ti := range mcpsrv.Catalog() {
		if ti.Domain == "" {
			orphans = append(orphans, ti.Name+"（未声明 Domain）")
			continue
		}
		if !known[ti.Domain] {
			orphans = append(orphans, ti.Name+" → "+ti.Domain)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Fatalf("这些工具挂在了没登记的域上，去 services/mcp/catalog.go 补一条：\n  %v", orphans)
	}
}

// TestDomainMetaComplete 每个域都要有 i18n key、顺序号，且顺序不重复。
//
// 顺序重复不会崩，只会让分组次序变成「看 map 遍历心情」——同一台机器上
// 两次打开面板的区块顺序可能不一样，而用户会以为工具被删了。
func TestDomainMetaComplete(t *testing.T) {
	registerAllTools()

	byOrder := map[int]string{}
	for _, d := range mcpsrv.Domains() {
		if d.ID == "" {
			t.Fatal("域缺 ID")
		}
		if d.LabelKey == "" {
			t.Fatalf("域 %s 缺 LabelKey：面板会显示成裸 id", d.ID)
		}
		if d.Order <= 0 {
			t.Fatalf("域 %s 缺 Order：分组位置会变成随机的", d.ID)
		}
		if prev, dup := byOrder[d.Order]; dup {
			t.Fatalf("域 %s 与 %s 的 Order 都是 %d：同一序号的区块顺序不确定", d.ID, prev, d.Order)
		}
		byOrder[d.Order] = d.ID
	}
}

// TestExternalDomainIsLast 外部 MCP 工具固定排在最后。
//
// 它们的数量由用户接了几个 server 决定，可能一次冒出几十条；排在中间会把
// 宿主自己的工具挤到屏幕外，而用户来找的通常正是后者。
func TestExternalDomainIsLast(t *testing.T) {
	registerAllTools()

	var ext, maxOther int
	for _, d := range mcpsrv.Domains() {
		if d.ID == mcpsrv.CategoryExternal {
			ext = d.Order
			continue
		}
		if d.Order > maxOther {
			maxOther = d.Order
		}
	}
	if ext <= maxOther {
		t.Fatalf("外部工具域的 Order=%d 不比其它域（最大 %d）大，会被夹在中间", ext, maxOther)
	}
}

// TestDomainHintsOnlyMentionMountedTools 提示词只提该域**必然挂载**的工具名。
//
// 约定（见 catalog.go 顶部）：只读工具写 Hint，写类工具写 WriteHint。
// 违反它的后果不是排版难看，而是模型在只读档里照着 Hint 去调 file_write，
// 拿到「本次未开放」之后反复重试——它没有别的线索判断这是能力缺失而不是参数错。
//
// 反方向是允许的：WriteHint 里提读类工具是刻意的（「改笔记前先 note_read 看清原文」），
// 那种引用只会出现在写类工具已经挂上的场合，不会误导只读档。
func TestDomainHintsOnlyMentionMountedTools(t *testing.T) {
	registerAllTools()

	levelOf := map[string]int{}
	for _, ti := range mcpsrv.Catalog() {
		levelOf[ti.Name] = ti.Level
	}

	for _, d := range mcpsrv.Domains() {
		for _, name := range toolNamesIn(d.Hint) {
			lvl, ok := levelOf[name]
			if !ok {
				t.Fatalf("域 %s 的 Hint 提到 %q，但没这个工具（拼错了？）", d.ID, name)
			}
			if lvl > mcpsrv.LevelRead {
				t.Fatalf("域 %s 的 Hint 提到写类工具 %q：只读档不挂它，应该写进 WriteHint", d.ID, name)
			}
		}
		for _, name := range toolNamesIn(d.WriteHint) {
			if _, ok := levelOf[name]; !ok {
				t.Fatalf("域 %s 的 WriteHint 提到 %q，但没这个工具（拼错了？）", d.ID, name)
			}
		}
	}
}
