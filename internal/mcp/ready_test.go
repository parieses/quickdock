package mcp

import (
	"context"
	"strings"
	"testing"
)

// 就绪门：外部环境没准备好的工具不该出现在任何一次装配里。
//
// 以前这类工具照样挂在工具列表上，调用时才报「不可用」——模型先失败一次、
// 再从错误里推断该换哪个工具，白烧一整轮。filter 掉之后它根本看不见，
// 但直接调用（客户端缓存了旧 tools/list）仍要给出具体原因，不能只说「未知工具」。
func TestResolveFiltersNotReadyTool(t *testing.T) {
	ready := false
	Register(Tool{
		Name:        "ready_probe",
		Description: "单测用：依赖外部程序",
		Level:       LevelRead,
		Domain:      CategoryFile,
		Handler:     func(map[string]any) (any, error) { return "ok", nil },
		Ready:       func() bool { return ready },
		NotReadyNote: func() string {
			return "外部程序未运行"
		},
	})

	pol := NewPolicy(LevelRead, nil)
	sf := Resolve(ToolScope{Caller: CallerMCP, Policy: pol})
	if sf.Has("ready_probe") {
		t.Fatal("未就绪的工具不该进工具面")
	}
	reason := ""
	for _, d := range sf.Dropped {
		if d.Name == "ready_probe" {
			reason = d.Reason
		}
	}
	if reason != "外部程序未运行" {
		t.Fatalf("Dropped 原因应是工具自己声明的那句，实际 %q", reason)
	}
	if _, err := Invoke(context.Background(), sf, "ready_probe", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "外部程序未运行") {
		t.Fatalf("直接调用应给出未就绪原因，实际 %v", err)
	}

	ready = true
	sf = Resolve(ToolScope{Caller: CallerMCP, Policy: pol})
	if !sf.Has("ready_probe") {
		t.Fatal("就绪后应重新挂上（不需要重启进程）")
	}
	if v, err := Invoke(context.Background(), sf, "ready_probe", nil, nil); err != nil || v != "ok" {
		t.Fatalf("就绪后应能调用，实际 %v %v", v, err)
	}

	// 面板要能解释「开关开着为什么调不到」：Catalog 里必须标出未就绪。
	ready = false
	found := false
	for _, c := range CatalogWith(pol) {
		if c.Name != "ready_probe" {
			continue
		}
		found = true
		if !c.NotReady || c.NotReadyNote != "外部程序未运行" {
			t.Fatalf("Catalog 应标出未就绪状态，实际 %+v", c)
		}
	}
	if !found {
		t.Fatal("Catalog 是全量清单，未就绪的工具也该在里面（否则面板会以为工具被删了）")
	}
}
