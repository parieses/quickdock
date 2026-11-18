package ai

import (
	"errors"
	"strings"
	"testing"
)

// 回传给上游的 arguments 必须是合法 JSON：模型把参数写坏时本地能容忍，
// 但原样回传会让下一发请求直接 400。这组用例锁住"归一"的边界。
func TestWireToolArguments(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"合法对象原样", `{"path":"a.txt"}`, `{"path":"a.txt"}`},
		{"合法对象带空白也原样", `  {"a":1}  `, `  {"a":1}  `},
		{"空串归一", "", "{}"},
		{"纯空白归一", "   ", "{}"},
		{"半截 JSON 归一", `{"path":`, "{}"},
		{"解释文字归一", "参数应该是这样写的", "{}"},
		{"null 归一", "null", "{}"},
		{"数组归一", `[1,2]`, "{}"},
		{"标量归一", `123`, "{}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wireToolArguments(c.raw); got != c.want {
				t.Errorf("wireToolArguments(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// 上游把出错的工具名打码成 *** 时，报错里要补上本地才知道的具体名字。
func TestToolArgErrorHint(t *testing.T) {
	err400 := errors.New(`HTTP 400: {"error":{"message":"Assistant tool call ***.arguments must be valid JSON.","type":"BadRequest","code":400}}`)
	recs := []aiToolRecord{
		{Name: "file_read", Error: "参数解析失败：invalid character..."},
		{Name: "app_info", Error: ""},
	}
	hint := toolArgErrorHint(err400, recs)
	if hint == "" {
		t.Fatal("应补上本地线索")
	}
	if !strings.Contains(hint, "file_read") {
		t.Errorf("提示应含出错的工具名: %s", hint)
	}
	if strings.Contains(hint, "app_info") {
		t.Errorf("不该把没出错的工具列进去: %s", hint)
	}
	if toolArgErrorHint(nil, recs) != "" {
		t.Error("nil 错误不该给提示")
	}
	if toolArgErrorHint(errors.New("HTTP 401: invalid api key"), recs) != "" {
		t.Error("非参数类错误不该给提示")
	}
	if toolArgErrorHint(err400, nil) != "" {
		t.Error("没有失败记录时不该给提示")
	}
}
