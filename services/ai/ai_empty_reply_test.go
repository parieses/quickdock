package ai

// 「只思考不作答」的空响应防御：这类轮次一旦被当成正常收尾，就会跳过落库，
// 用户看到的是「任务停了、什么都没做」。判据必须是纯函数，逐种情形钉死。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quickdock/internal/db"
)

func TestIsEmptyModelReply(t *testing.T) {
	cases := []struct {
		name      string
		turn      chatTurn
		reasoning int
		want      bool
	}{
		{
			name:      "只思考不作答——用户实机的故障形态",
			turn:      chatTurn{Content: "", Usage: db.TokenUsage{}},
			reasoning: 28890,
			want:      true,
		},
		{
			name:      "有 usage 但正文和工具都空",
			turn:      chatTurn{Content: "", Usage: db.TokenUsage{Prompt: 18395, Completion: 0}},
			reasoning: 0,
			want:      true,
		},
		{
			name:      "思考和使用量都没有——上游彻底空返回",
			turn:      chatTurn{Content: "", Usage: db.TokenUsage{}},
			reasoning: 0,
			want:      false,
		},
		{
			name:      "正常收尾：有正文",
			turn:      chatTurn{Content: "登录页已经实现好了", Usage: db.TokenUsage{Completion: 120}},
			reasoning: 500,
			want:      false,
		},
		{
			name:      "正文只有空白字符",
			turn:      chatTurn{Content: "  \n\t ", Usage: db.TokenUsage{Completion: 10}},
			reasoning: 0,
			want:      true,
		},
		{
			name: "有工具调用——即便正文空也是正常推进",
			turn: chatTurn{
				Content:   "",
				ToolCalls: []aiToolCall{{Name: "dir_list", Args: "{}"}},
				Usage:     db.TokenUsage{Completion: 30},
			},
			reasoning: 100,
			want:      false,
		},
		{
			name:      "纯中止轮：正文空、有 usage、无思考——靠 usage 判定",
			turn:      chatTurn{Content: "", Usage: db.TokenUsage{Prompt: 100, Completion: 5}},
			reasoning: 0,
			want:      true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isEmptyModelReply(c.turn, c.reasoning); got != c.want {
				t.Errorf("isEmptyModelReply = %v，期望 %v（turn=%+v reasoning=%d）",
					got, c.want, c.turn, c.reasoning)
			}
		})
	}
}

func TestRaisedMaxTokensOnLength(t *testing.T) {
	cases := []struct {
		name                  string
		current, window, want int
	}{
		{"默认8192抬到32K", 8192, 1000000, 32768},
		{"16K抬到32K", 16384, 1000000, 32768},
		{"32K翻倍到64K", 32768, 1000000, 65536},
		{"小窗口按四分之一封顶", 16384, 128000, 32000},
		{"已有预算高于窗口上限时不降低", 32768, 128000, 32768},
		{"窗口未知不做上限裁剪", 32768, 0, 65536},
		{"已顶到窗口四分之一不再抬", 250000, 1000000, 250000},
		{"零值给一个可用起点", 0, 0, 32768},
	}
	for _, c := range cases {
		if got := raisedMaxTokensOnLength(c.current, c.window); got != c.want {
			t.Errorf("%s: raisedMaxTokensOnLength(%d, %d) = %d，期望 %d",
				c.name, c.current, c.window, got, c.want)
		}
	}
}

// 复现实机故障：thinking 模型把 max_tokens 花在 reasoning 上，上游以
// finish_reason=length 截断，正文和工具调用全空。流层必须把 length 带出来，
// 否则主循环只看到「空回复」，无法区分「不想答」和「被掐断」。
func TestStreamAIChatExposesLengthFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		frames := []string{
			`{"choices":[{"index":0,"delta":{"reasoning_content":"用户要建登录页，我先规划一下技术栈……"}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"先用 JSON 存储避免原生依赖，再写路由……"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
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
	cfg := AIProfile{Provider: "custom", BaseURL: srv.URL, Model: "mock-thinking", APIKey: "k"}
	var reason strings.Builder
	turn, err := svc.streamAIChat(context.Background(), cfg,
		[]map[string]interface{}{{"role": "user", "content": "继续"}},
		"conv-length", nil, nil, func(text string) { reason.WriteString(text) })
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	if turn.FinishReason != "length" {
		t.Fatalf("finish_reason 未透传：got %q, want length", turn.FinishReason)
	}
	if turn.Content != "" || len(turn.ToolCalls) != 0 {
		t.Fatalf("故障形态不对：正文=%q 工具=%d", turn.Content, len(turn.ToolCalls))
	}
	if !strings.Contains(reason.String(), "先规划") {
		t.Fatalf("思考内容丢了：%q", reason.String())
	}
	// 流层带出 length 后，主循环才能把它判成需要抬预算重试的空响应。
	if !isEmptyModelReply(turn, len([]rune(reason.String()))) {
		t.Fatal("被 length 截断、只有思考的一轮应判为空响应")
	}
}
