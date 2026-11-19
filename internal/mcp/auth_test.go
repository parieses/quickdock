package mcp

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// TestDestructiveToolsRequireToken 破坏性工具必须带令牌，即使 requireToken=false。
//
// requireToken 默认关着是为了不打断已配好的老客户端，但那些客户端的配置里同样
// 没有请求头——如果删除/执行类工具也跟着免验，任何能连上 127.0.0.1:9230 的
// 本地进程都能删数据。这条用例把那个边界钉住。
func TestDestructiveToolsRequireToken(t *testing.T) {
	Default.SetAuth("sk_unit_test", false) // 显式关掉「强制校验」
	defer func() {
		Default.SetAuth("", false)
		_ = Default.Stop()
	}()

	addr, err := Default.Start(0, "test")
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	url := "http://" + addr + "/mcp"

	send := func(body, bearer string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		_, _ = io.ReadAll(res.Body)
		return res.StatusCode
	}

	call := func(name string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
	}

	// 开关关着 + 不带令牌：破坏性工具必须 401
	for _, name := range []string{"note_delete", "ssh_exec", "plugin_execute", "env_stop", "download_remove", "schedule_create"} {
		if code := send(call(name), ""); code != http.StatusUnauthorized {
			t.Errorf("%s 无令牌应返回 401，实际 %d", name, code)
		}
	}

	// 只读工具不受影响（工具本身不存在也只会是 200 + isError，不能是 401）
	if code := send(call("note_search"), ""); code == http.StatusUnauthorized {
		t.Error("只读工具不应被令牌闸拦截")
	}

	// 带上正确令牌后放行（不再 401）
	if code := send(call("note_delete"), "sk_unit_test"); code == http.StatusUnauthorized {
		t.Error("带正确令牌的破坏性调用不应被拒")
	}

	// 批量请求里夹一条破坏性工具：整包拒绝，不能只放行只读那条
	batch := `[` + call("note_search") + `,` + call("note_delete") + `]`
	if code := send(batch, ""); code != http.StatusUnauthorized {
		t.Errorf("批量请求含破坏性工具应整包 401，实际 %d", code)
	}
}

// TestRequiresTokenSet 名单本身的快速校验：防止有人把只读工具误加进去。
func TestRequiresTokenSet(t *testing.T) {
	for _, name := range []string{"note_search", "item_search", "env_list", "download_list", "schedule_list"} {
		if RequiresToken(name) {
			t.Errorf("只读工具 %s 不应要求令牌", name)
		}
	}
	if !RequiresToken("note_delete") || !RequiresToken("ssh_exec") {
		t.Error("破坏性工具必须在名单里")
	}
}
