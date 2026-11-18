package ai

// WP10：SKILL.md 的 allowed-tools 声明。
//
// 三件事都要钉住，因为出错时都没有报错：
//  1. 解析要能容忍三种常见写法，且丢掉非工具名的垃圾项（不能因此把工具面清空）；
//  2. 收窄**只收不放**——技能不能把用户已经关掉的工具重新打开；
//  3. 只在「明确只选了一个技能」时收窄，多选与全局默认都不动。

import (
	"testing"

	mcpsrv "quickdock/internal/mcp"
)

func TestParseAllowedTools(t *testing.T) {
	cases := map[string][]string{
		"file_read, file_grep":          {"file_read", "file_grep"},
		"[file_read, file_grep]":        {"file_read", "file_grep"},
		"file_read file_grep":           {"file_read", "file_grep"},
		"file_read;file_grep":           {"file_read", "file_grep"},
		`"file_read", 'file_grep'`:      {"file_read", "file_grep"},
		"file_read,file_read,file_read": {"file_read"},
		"":                              nil,
	}
	for in, want := range cases {
		got := parseAllowedTools(in)
		if len(got) != len(want) {
			t.Errorf("parseAllowedTools(%q) = %v，期望 %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("parseAllowedTools(%q)[%d] = %q，期望 %q", in, i, got[i], want[i])
			}
		}
	}

	// 非法项要丢掉，但不能把合法的也一起丢掉。
	// 注意 `run_command --force` 里的 `run_command` 本身是合法工具名，
	// 该丢的是 `--force` 与那个路径。
	got := parseAllowedTools("file_read, ../../etc/passwd, run_command --force, file_grep")
	want := []string{"file_read", "run_command", "file_grep"}
	if len(got) != len(want) {
		t.Fatalf("非法项应被丢掉而保留合法项，实际 %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

func TestParseSkillMetaAllowedTools(t *testing.T) {
	content := `---
name: review
description: 只读审阅
allowed-tools: file_read, file_grep, git_diff
---
正文`
	name, desc, allowed := parseSkillMeta(content, "dir-name")
	if name != "review" || desc != "只读审阅" {
		t.Errorf("name/desc 解析不对: %q %q", name, desc)
	}
	if len(allowed) != 3 || allowed[0] != "file_read" {
		t.Errorf("allowed-tools 解析不对: %v", allowed)
	}

	// 没有声明时为空（表示不限制）。
	_, _, none := parseSkillMeta("---\nname: x\n---\n正文", "d")
	if len(none) != 0 {
		t.Errorf("没声明时不该有限制: %v", none)
	}
}

// ToolFilter.AllowNames 只能收窄：被等级/域策略撤掉的工具不可能被加回来。
func TestToolFilterAllowNamesOnlyNarrows(t *testing.T) {
	f := &ToolFilter{MaxLevel: mcpsrv.LevelRead, AllowNames: map[string]bool{
		"run_command": true, // 写类，但等级上限是只读
		"file_read":   true,
	}}
	if f.keep(mcpsrv.Tool{Name: "run_command", Level: mcpsrv.LevelWrite, Domain: mcpsrv.CategoryCode}) {
		t.Error("技能声明不能越过等级上限把写类工具加回来")
	}
	if !f.keep(mcpsrv.Tool{Name: "file_read", Level: mcpsrv.LevelRead, Domain: mcpsrv.CategoryCode}) {
		t.Error("白名单内的只读工具应放行")
	}
	if f.keep(mcpsrv.Tool{Name: "file_grep", Level: mcpsrv.LevelRead, Domain: mcpsrv.CategoryCode}) {
		t.Error("白名单外的工具应收窄掉")
	}
	// nil 白名单 = 不按名字收窄（MaxLevel=-1 表示跳过等级判定）。
	g := &ToolFilter{MaxLevel: -1}
	if !g.keep(mcpsrv.Tool{Name: "anything", Level: mcpsrv.LevelWrite, Domain: mcpsrv.CategoryCode}) {
		t.Error("AllowNames 为 nil 时不该按名字收窄")
	}
}

// 多选技能与全局默认都不收窄：那时用户没表达「只要这些」。
func TestSkillAllowNamesScope(t *testing.T) {
	// 没选过技能（raw 为空）→ 回落到全局默认，不收窄。
	if allow, name := skillAllowNames(""); allow != nil || name != "" {
		t.Errorf("全局默认不该收窄: %v %q", allow, name)
	}
	// 明确「这条不用技能」→ 回落全局，不收窄。
	if allow, _ := skillAllowNames("[]"); allow != nil {
		t.Errorf("明确的空选择不该收窄: %v", allow)
	}
}
