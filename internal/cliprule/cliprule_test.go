package cliprule

import (
	"strings"
	"testing"

	"quickdock/internal/db"
)

func rule(action, arg string) db.ClipboardRule {
	return db.ClipboardRule{Name: "t", Enabled: true, MatchKind: MatchAll, Action: action, ActionArg: arg}
}

func TestTransformActions(t *testing.T) {
	cases := []struct {
		name, action, arg, in, want string
	}{
		{"trim 去行尾空白", ActionTrim, "", "  a  \n b\t\n\n", "a\n b"},
		{"去空行", ActionRemoveBlank, "", "a\n\n  \nb\n", "a\nb"},
		// 末尾换行会切出一个空元素，去重后仍保留它 —— 不该顺手把用户文本的结尾换行吃掉
		{"行去重保序", ActionDedupeLines, "", "b\na\nb\nc\n", "b\na\nc\n"},
		{"行排序", ActionSortLines, "", "c\na\nb", "a\nb\nc"},
		{"JSON 格式化 2 空格", ActionJSONFormat, "2", `{"b":1,"a":[2,3]}`, "{\n  \"b\": 1,\n  \"a\": [\n    2,\n    3\n  ]\n}"},
		{"JSON 压缩", ActionJSONMinify, "", "{\n  \"a\": 1\n}", `{"a":1}`},
		{"去 ANSI", ActionStripANSI, "", "\x1b[31mERR\x1b[0m ok", "ERR ok"},
		{"大写", ActionCase, "upper", "abc", "ABC"},
		{"首字母大写", ActionCase, "title", "hello world-foo", "Hello World-Foo"},
		{"URL 解码", ActionURLDecode, "", "a%20b", "a b"},
	}
	for _, c := range cases {
		got, ok := transform(c.action, c.arg, c.in)
		if !ok {
			t.Fatalf("%s: 期望命中，实际 ok=false", c.name)
		}
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// URL 解码不能把普通加号变成空格（QueryUnescape 的坑）
func TestURLDecodeKeepsPlus(t *testing.T) {
	got, ok := transform(ActionURLDecode, "", "a+b%20c")
	if !ok || got != "a+b c" {
		t.Fatalf("got %q ok=%v, want %q", got, ok, "a+b c")
	}
}

// 非法 JSON 必须原样返回且不标记为"已改变"，否则会白白覆盖用户内容
func TestInvalidJSONUnchanged(t *testing.T) {
	in := "not json at all"
	if got, ok := transform(ActionJSONFormat, "2", in); ok || got != in {
		t.Fatalf("非法 JSON 应原样返回: got %q ok=%v", got, ok)
	}
}

// 大整数不能被 float64 改写
func TestJSONKeepsBigInt(t *testing.T) {
	got, ok := transform(ActionJSONMinify, "", `{"n":12345678901234567890}`)
	if !ok {
		t.Fatal("应当命中")
	}
	if !strings.Contains(got, "12345678901234567890") {
		t.Fatalf("大整数被精度破坏: %s", got)
	}
}

func TestMatches(t *testing.T) {
	r := rule(ActionTrim, "")
	r.MatchKind = MatchRegex
	r.MatchExpr = `^https?://`
	if !Matches(r, "https://a.com", "") {
		t.Fatal("正则应命中")
	}
	if Matches(r, "ftp://a.com", "") {
		t.Fatal("正则不应命中")
	}
	// 坏正则当作不命中，不能 panic / 不能把链路拖垮
	r.MatchExpr = "("
	if Matches(r, "https://a.com", "") {
		t.Fatal("坏正则不应命中")
	}

	app := rule(ActionTrim, "")
	app.MatchKind = MatchApp
	app.MatchExpr = "Visual Studio"
	if !Matches(app, "x", "main.go - Visual Studio Code") {
		t.Fatal("应用名应不区分大小写命中")
	}
	if Matches(app, "x", "") {
		t.Fatal("来源为空时 app 规则不应命中")
	}
}

func TestApplyChainAndWriteBack(t *testing.T) {
	rules := []db.ClipboardRule{
		{Name: "trim", Enabled: true, Priority: 1, MatchKind: MatchAll, Action: ActionTrim},
		{Name: "dedupe", Enabled: true, Priority: 2, MatchKind: MatchAll, Action: ActionDedupeLines, WriteBack: true},
		{Name: "off", Enabled: false, Priority: 3, MatchKind: MatchAll, Action: ActionCase, ActionArg: "upper"},
	}
	out, changed, wb := Apply(rules, "a\na\n  \n", "")
	if !changed {
		t.Fatal("应标记为已改变")
	}
	if !wb {
		t.Fatal("命中的回写规则应置位 writeBack")
	}
	if out != "a" {
		t.Fatalf("链式结果不符: %q", out)
	}

	// 没有命中任何规则时：内容不变、不置 writeBack
	out2, changed2, wb2 := Apply([]db.ClipboardRule{
		{Name: "r", Enabled: true, MatchKind: MatchRegex, MatchExpr: `^\d+$`, Action: ActionCase, ActionArg: "upper"},
	}, "abc", "")
	if changed2 || wb2 || out2 != "abc" {
		t.Fatalf("未命中时应原样返回: %q changed=%v wb=%v", out2, changed2, wb2)
	}
}
