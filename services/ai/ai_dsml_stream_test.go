package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 端到端验证「模型把工具调用写成正文」这条路径：
// 流式入口要把它还原成真正的 tool_calls，并且一个标记字符都不推给前端。
func TestStreamAIChatRecoversDSMLTextCalls(t *testing.T) {
	chunks := []string{
		"骨架文件已经建好，我开始逐段填入内容。\n\n",
		"<｜｜DSML｜｜ calls>\n",
		"<｜｜DSML｜｜ invoke name=\"file_read\">\n",
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">quickdock-guide.md</｜｜DSML｜｜ parameter>\n",
		"<｜｜DSML｜｜ parameter name=\"limit\" string=\"false\">40</｜｜DSML｜｜ parameter>\n",
		"</｜｜DSML｜｜ invoke>\n",
		"</｜｜DSML｜｜ calls>",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			b, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"content": c},
				}},
			})
			fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	svc := &AIService{aiHTTPClient: srv.Client()}
	cfg := AIProfile{Provider: "custom", BaseURL: srv.URL, Model: "mock", APIKey: "k"}

	var streamed strings.Builder
	turn, err := svc.streamAIChat(context.Background(), cfg,
		[]map[string]interface{}{{"role": "user", "content": "写个文档"}},
		"conv-dsml", nil,
		func(text string) { streamed.WriteString(text) }, nil)
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}

	if strings.Contains(streamed.String(), "DSML") || strings.Contains(turn.Content, "DSML") {
		t.Fatalf("标记泄漏到输出：streamed=%q content=%q", streamed.String(), turn.Content)
	}
	if !strings.HasPrefix(turn.Content, "骨架文件已经建好") {
		t.Fatalf("正文丢了：%q", turn.Content)
	}
	if len(turn.ToolCalls) != 1 {
		t.Fatalf("期望还原出 1 个工具调用，实际 %d 个", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Name != "file_read" {
		t.Fatalf("工具名不对：%q", turn.ToolCalls[0].Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(turn.ToolCalls[0].Args), &args); err != nil {
		t.Fatalf("参数不是合法 JSON：%v", err)
	}
	if args["path"] != "quickdock-guide.md" {
		t.Fatalf("参数丢了：%v", args)
	}
}

// 结构化 tool_calls 与文本标记同时出现时，两者都要收下（网关混用时会出现）。
func TestStreamAIChatKeepsBothCallForms(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		frames := []string{
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"app_info","arguments":"{}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\"port_list\"></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>"}}]}`,
		}
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	svc := &AIService{aiHTTPClient: srv.Client()}
	cfg := AIProfile{Provider: "custom", BaseURL: srv.URL, Model: "mock", APIKey: "k"}
	turn, err := svc.streamAIChat(context.Background(), cfg,
		[]map[string]interface{}{{"role": "user", "content": "看看"}},
		"conv-both", nil, func(string) {}, nil)
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	if len(turn.ToolCalls) != 2 {
		t.Fatalf("期望 2 个调用，实际 %d：%+v", len(turn.ToolCalls), turn.ToolCalls)
	}
	if turn.ToolCalls[0].Name != "app_info" || turn.ToolCalls[1].Name != "port_list" {
		t.Fatalf("调用顺序或名称不对：%+v", turn.ToolCalls)
	}
	if turn.Content != "" {
		t.Fatalf("标记不该进正文：%q", turn.Content)
	}
}
