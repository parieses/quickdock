package ai

// 审批待批表的键必须是宿主生成的 approvalKey，不能是模型给的 callID。
//
// 这条一旦错就是安全洞：模型的 fn_call_id 在重试、并行编排时会重复，两张卡
// 落进同一个键之后，用户点「允许」放行的可能是另一条命令，而界面上看起来
// 一切正常。本文件只测键的选取，不跑完整 await 流程（那要 sink / db）。

import "testing"

func newTestGate() *approvalGate {
	return &approvalGate{
		pending: make(map[string]*pendingApproval),
		granted: make(map[string]bool),
	}
}

func TestDecideByApprovalKey(t *testing.T) {
	g := newTestGate()
	ch := make(chan approvalDecision, 1)
	g.pending["ap-1"] = &pendingApproval{ch: ch, callID: "call-x"}

	if !g.decide("ap-1", true, false) {
		t.Fatal("按 approvalKey 应命中")
	}
	select {
	case d := <-ch:
		if !d.Allow {
			t.Error("送达的决定应该是 allow")
		}
	default:
		t.Fatal("决定没有送到 channel")
	}
}

func TestDecideDuplicateCallIDNotCrossed(t *testing.T) {
	g := newTestGate()
	chA := make(chan approvalDecision, 1)
	chB := make(chan approvalDecision, 1)
	// 模型给了同一个 callID，但宿主给的是两个不同的 approvalKey。
	g.pending["ap-a"] = &pendingApproval{ch: chA, callID: "same"}
	g.pending["ap-b"] = &pendingApproval{ch: chB, callID: "same"}

	if !g.decide("ap-b", true, false) {
		t.Fatal("按 approvalKey 应命中第二条")
	}
	select {
	case <-chB:
	default:
		t.Fatal("决定应该送到 ap-b 对应的那张卡")
	}
	select {
	case <-chA:
		t.Fatal("ap-a 不该收到决定：重复 callID 不能串台")
	default:
	}
}

func TestDecideLegacyCallIDFallback(t *testing.T) {
	g := newTestGate()
	ch := make(chan approvalDecision, 1)
	g.pending["ap-1"] = &pendingApproval{ch: ch, callID: "call-old"}

	// 升级前就开着的窗口只会回传模型的 callID，必须仍然能点。
	if !g.decide("call-old", false, false) {
		t.Fatal("旧前端回传 callID 时应兜底命中")
	}
	select {
	case d := <-ch:
		if d.Allow {
			t.Error("送达的决定应该是 deny")
		}
	default:
		t.Fatal("兜底路径没有送出决定")
	}
}

func TestDecideUnknown(t *testing.T) {
	g := newTestGate()
	if g.decide("", true, false) {
		t.Error("空 id 不该命中也别 panic")
	}
	if g.decide("ap-none", true, false) {
		t.Error("不存在的键应返回 false")
	}
}
