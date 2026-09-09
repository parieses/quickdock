package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// post 向 /mcp 发送一段 JSON-RPC 文本，返回状态码与响应体。
func post(t *testing.T, url, body string, origin string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b, res.Header
}

func TestServerLifecycleAndTools(t *testing.T) {
	Register(Tool{
		Name:        "echo",
		Description: "回显文本",
		InputSchema: Schema("", []string{"text"}, map[string]any{"text": Str("要回显的文本")}),
		Level:       LevelRead,
		Handler: func(args map[string]any) (any, error) {
			s := Arg(args, "text")
			if s == "" {
				return nil, fmt.Errorf("text 不能为空")
			}
			return "echo: " + s, nil
		},
	})
	Register(Tool{
		Name:        "boom",
		Description: "高危示例",
		Level:       LevelRisk,
		Handler:     func(map[string]any) (any, error) { return "boom", nil },
	})
	SetMaxLevel(LevelWrite) // 高危工具不应出现在 tools/list

	addr, err := Default.Start(0, "test")
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer func() { _ = Default.Stop() }()
	url := "http://" + addr + "/mcp"

	// initialize
	code, body, hdr := post(t, url, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "")
	if code != http.StatusOK {
		t.Fatalf("initialize 状态码 %d: %s", code, body)
	}
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &init); err != nil {
		t.Fatalf("解析 initialize 失败: %v / %s", err, body)
	}
	if init.Result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("协议版本不符: %s", init.Result.ProtocolVersion)
	}
	if init.Result.ServerInfo.Version != "test" {
		t.Fatalf("版本号未透传: %s", init.Result.ServerInfo.Version)
	}
	if hdr.Get("Mcp-Session-Id") == "" {
		t.Fatal("缺少 Mcp-Session-Id 响应头")
	}

	// 通知：应回 202 且无响应体
	code, body, _ = post(t, url, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "")
	if code != http.StatusAccepted || len(strings.TrimSpace(string(body))) != 0 {
		t.Fatalf("通知响应异常: %d / %q", code, body)
	}

	// tools/list：含 echo，不含高危 boom
	code, body, _ = post(t, url, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "")
	if code != http.StatusOK {
		t.Fatalf("tools/list 状态码 %d", code)
	}
	has, missing := func() (bool, bool) {
		var r struct {
			Result struct {
				Tools []struct{ Name string } `json:"tools"`
			} `json:"result"`
		}
		_ = json.Unmarshal(body, &r)
		var hasEcho, hasBoom bool
		for _, tl := range r.Result.Tools {
			if tl.Name == "echo" {
				hasEcho = true
			}
			if tl.Name == "boom" {
				hasBoom = true
			}
		}
		return hasEcho, hasBoom
	}()
	if !has {
		t.Fatalf("tools/list 缺少 echo: %s", body)
	}
	if missing {
		t.Fatalf("tools/list 泄漏了高危工具: %s", body)
	}

	// tools/call 成功
	code, body, _ = post(t, url, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`, "")
	if code != http.StatusOK {
		t.Fatalf("tools/call 状态码 %d", code)
	}
	var call struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &call); err != nil {
		t.Fatalf("解析 tools/call 失败: %v", err)
	}
	if call.Result.IsError || len(call.Result.Content) == 0 || call.Result.Content[0].Text != "echo: hi" {
		t.Fatalf("tools/call 结果不符: %s", body)
	}

	// tools/call 业务错误：走 isError 内容块，而不是协议错误
	_, body, _ = post(t, url, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{}}}`, "")
	call.Result.IsError = false
	_ = json.Unmarshal(body, &call)
	if !call.Result.IsError {
		t.Fatalf("工具执行失败未标记 isError: %s", body)
	}

	// 未知方法：协议级错误
	_, body, _ = post(t, url, `{"jsonrpc":"2.0","id":5,"method":"nope"}`, "")
	var errResp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &errResp)
	if errResp.Error.Code != codeMethodNotFound {
		t.Fatalf("未知方法错误码不符: %s", body)
	}

	// 跨站 Origin 必须拒绝
	code, _, _ = post(t, url, `{"jsonrpc":"2.0","id":6,"method":"ping"}`, "http://evil.example.com")
	if code != http.StatusForbidden {
		t.Fatalf("跨站请求未被拒绝: %d", code)
	}
	// 本机 Origin 允许
	code, _, _ = post(t, url, `{"jsonrpc":"2.0","id":7,"method":"ping"}`, "http://127.0.0.1:5173")
	if code != http.StatusOK {
		t.Fatalf("本机请求被误拒: %d", code)
	}

	// 停止后端口应释放
	if err := Default.Stop(); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if Default.Running() || Default.Endpoint() != "" {
		t.Fatal("停止后状态未清理")
	}
}

func TestServerRejectsBatchNotificationOnly(t *testing.T) {
	addr, err := Default.Start(0, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = Default.Stop() }()
	code, _, _ := post(t, "http://"+addr+"/mcp", `[{"jsonrpc":"2.0","method":"notifications/cancelled"}]`, "")
	if code != http.StatusAccepted {
		t.Fatalf("纯通知批量应回 202，实际 %d", code)
	}
}
