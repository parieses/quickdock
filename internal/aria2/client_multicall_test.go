package aria2

import (
	"errors"
	"net/http"
	"testing"
)

// 从桩里取出 multicall 的 methodCalls 数组，顺带验证外层参数形状。
func multiCallFrom(t *testing.T, call rpcRequest) []map[string]any {
	t.Helper()
	if call.Method != "system.multicall" {
		t.Fatalf("方法应为 system.multicall，实际 %q", call.Method)
	}
	if len(call.Params) != 1 {
		t.Fatalf("multicall 外层应只有一个数组参数，实际 %d 个", len(call.Params))
	}
	arr, ok := call.Params[0].([]any)
	if !ok {
		t.Fatalf("multicall 参数不是数组: %T", call.Params[0])
	}
	out := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			t.Fatalf("methodCall 不是对象: %T", x)
		}
		out = append(out, m)
	}
	return out
}

func subParams(t *testing.T, m map[string]any) []any {
	t.Helper()
	p, ok := m["params"].([]any)
	if !ok {
		t.Fatalf("methodCall.params 不是数组: %T", m["params"])
	}
	return p
}

func TestMultiCallInjectsTokenIntoEverySubCall(t *testing.T) {
	cl, st := newStubClient("sec", func(_ rpcRequest, _ int) (int, string, error) {
		return http.StatusOK, okResult(`[["g1"],["g2"]]`), nil
	})
	res, err := cl.MultiCall([]MethodCall{
		{MethodName: "aria2.pause", Params: []any{"g1"}},
		{MethodName: "aria2.unpause", Params: []any{"g2"}},
	})
	if err != nil {
		t.Fatalf("MultiCall 失败: %v", err)
	}
	if len(res) != 2 || !res[0].OK || !res[1].OK {
		t.Fatalf("两条都应成功，实际 %+v", res)
	}
	calls := multiCallFrom(t, st.at(0))
	if len(calls) != 2 {
		t.Fatalf("应有 2 个 methodCall，实际 %d", len(calls))
	}
	for i, want := range []string{"aria2.pause", "aria2.unpause"} {
		if calls[i]["methodName"] != want {
			t.Errorf("第 %d 个方法 = %v，应为 %q", i, calls[i]["methodName"], want)
		}
		// token 必须在每个子调用的 params 里，放外层会让全部子调用鉴权失败。
		p := subParams(t, calls[i])
		if len(p) != 2 || p[0] != "token:sec" {
			t.Errorf("第 %d 个子调用 token 不对: %v", i, p)
		}
	}
}

func TestMultiCallParsesMixedFaults(t *testing.T) {
	body := okResult(`[["g1"],{"faultCode":1,"faultString":"GID not found"},{"status":"active"}]`)
	cl, _ := newStubClient("", func(_ rpcRequest, _ int) (int, string, error) {
		return http.StatusOK, body, nil
	})
	res, err := cl.MultiCall([]MethodCall{
		{MethodName: "aria2.pause", Params: []any{"g1"}},
		{MethodName: "aria2.pause", Params: []any{"g2"}},
		{MethodName: "aria2.tellStatus", Params: []any{"g3", []string{"status"}}},
	})
	if err != nil {
		t.Fatalf("MultiCall 失败: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("应返回 3 项，实际 %d", len(res))
	}
	if !res[0].OK {
		t.Error("第 1 项（数组结果）应成功")
	}
	if res[1].OK || res[1].Error != "GID not found" {
		t.Errorf("第 2 项应解析成 fault，实际 %+v", res[1])
	}
	// 成功的对象结果也带键，但不能因为它能解析成 map 就误判成 fault。
	if !res[2].OK {
		t.Errorf("第 3 项（状态对象）应成功，实际 %+v", res[2])
	}
}

func TestMultiCallEmptyDoesNotHitNetwork(t *testing.T) {
	cl, st := newStubClient("", func(_ rpcRequest, _ int) (int, string, error) {
		return http.StatusOK, okResult(`[]`), nil
	})
	res, err := cl.MultiCall(nil)
	if err != nil || len(res) != 0 {
		t.Fatalf("空批量应直接返回空结果，实际 res=%v err=%v", res, err)
	}
	if got := st.count(); got != 0 {
		t.Errorf("空批量不该发请求，实际 %d 次", got)
	}
}

func TestMultiCallIsNeverRetried(t *testing.T) {
	// 批量里常混写方法，重试会重复执行已生效的那一半。
	cl, st := newStubClient("", func(_ rpcRequest, _ int) (int, string, error) {
		return 0, "", errors.New("connection reset")
	})
	if _, err := cl.MultiCall([]MethodCall{{MethodName: "aria2.pause", Params: []any{"g1"}}}); err == nil {
		t.Fatal("网络失败应返回错误")
	}
	if got := st.count(); got != 1 {
		t.Errorf("multicall 绝不能重试，实际调用 %d 次", got)
	}
}

// RemoveMany 无论多少 gid 都只发两次往返，且按状态分派到两套删除 API。
func TestRemoveManyUsesTwoRoundTrips(t *testing.T) {
	cl, st := newStubClient("", func(_ rpcRequest, seq int) (int, string, error) {
		if seq == 0 {
			return http.StatusOK, okResult(`[[{"status":"active"}],[{"status":"error"}],[{"status":"complete"}]]`), nil
		}
		return http.StatusOK, okResult(`[["g1"],["g2"],["g3"]]`), nil
	})
	res, err := cl.RemoveMany([]string{"g1", "g2", "g3"}, false)
	if err != nil {
		t.Fatalf("RemoveMany 失败: %v", err)
	}
	if got := st.count(); got != 2 {
		t.Fatalf("应恰好 2 次往返，实际 %d", got)
	}
	first := multiCallFrom(t, st.at(0))
	if len(first) != 3 {
		t.Fatalf("第一批应有 3 个 tellStatus，实际 %d", len(first))
	}
	for i, m := range first {
		if m["methodName"] != "aria2.tellStatus" {
			t.Errorf("第 %d 个应为 tellStatus，实际 %v", i, m["methodName"])
		}
	}
	second := multiCallFrom(t, st.at(1))
	want := []string{"aria2.remove", "aria2.removeDownloadResult", "aria2.removeDownloadResult"}
	if len(second) != len(want) {
		t.Fatalf("第二批应有 %d 个删除指令，实际 %d", len(want), len(second))
	}
	for i, m := range second {
		if m["methodName"] != want[i] {
			t.Errorf("第 %d 个删除指令 = %v，应为 %q", i, m["methodName"], want[i])
		}
	}
	for i, r := range res {
		if !r.OK {
			t.Errorf("第 %d 项应成功，实际 %+v", i, r)
		}
	}
}

func TestRemoveManySkipsVanishedGID(t *testing.T) {
	cl, st := newStubClient("", func(_ rpcRequest, seq int) (int, string, error) {
		if seq == 0 {
			return http.StatusOK, okResult(`[{"faultCode":1,"faultString":"Active Download not found for GID#gone"},[{"status":"active"}]]`), nil
		}
		return http.StatusOK, okResult(`[["g2"]]`), nil
	})
	res, err := cl.RemoveMany([]string{"gone", "g2"}, false)
	if err != nil {
		t.Fatalf("RemoveMany 失败: %v", err)
	}
	// 已不存在的 gid 不该再收到删除指令。
	second := multiCallFrom(t, st.at(1))
	if len(second) != 1 || second[0]["methodName"] != "aria2.remove" {
		t.Fatalf("第二批只该删 g2，实际 %v", second)
	}
	if len(res) != 2 || !res[0].OK || !res[1].OK {
		t.Errorf("查不到的 gid 应视为删除成功，实际 %+v", res)
	}
}
