package ai

// WP4c：压缩阈值下放到档案后的守卫。
//
// 这一批最怕的不是「阈值不生效」，而是**默认值悄悄变了**：用户从没动过那几个
// 数字，升级后却突然压得更早或更晚，而凭据只有「我也没改啊」。
// 所以第一组用例专门钉默认值。

import (
	"testing"

	"quickdock/internal/db"
)

func TestThresholdDefaultsUnchanged(t *testing.T) {
	// 空档案 = 全部走默认，必须与 WP4c 之前的常量逐字一致。
	if got := softCapOf(AIProfile{}); got != aiContextSoftCap {
		t.Errorf("软上限默认值 = %d，期望 %d", got, aiContextSoftCap)
	}
	if got := preflightMarginOf(AIProfile{}); got != 85 {
		t.Errorf("触发线默认值 = %d，期望 85", got)
	}
	lo, hi := keepRecentOf(AIProfile{})
	if lo != aiKeepRecentMin || hi != aiKeepRecentMax {
		t.Errorf("保留条数默认值 = %d/%d，期望 %d/%d", lo, hi, aiKeepRecentMin, aiKeepRecentMax)
	}
}

func TestThresholdOverrides(t *testing.T) {
	cfg := AIProfile{ContextSoftCap: 50000, PreflightMargin: 75, KeepRecentMin: 4, KeepRecentMax: 12}
	if got := softCapOf(cfg); got != 50000 {
		t.Errorf("软上限覆盖无效: %d", got)
	}
	if got := preflightMarginOf(cfg); got != 75 {
		t.Errorf("触发线覆盖无效: %d", got)
	}
	lo, hi := keepRecentOf(cfg)
	if lo != 4 || hi != 12 {
		t.Errorf("保留条数覆盖无效: %d/%d", lo, hi)
	}
	// 换个模型就该换阈值：同一个档案结构体，不同窗口给出不同上限。
	small := contextLimitsOf(AIProfile{ContextWindow: 8000, ContextSoftCap: 50000})
	big := contextLimitsOf(AIProfile{ContextWindow: 400000, ContextSoftCap: 50000})
	if small.History > big.History {
		t.Errorf("小窗口的历史预算不该比大窗口还多: %d vs %d", small.History, big.History)
	}
	// 覆盖值必须真的压住：大窗口按七成本可以带更多，但软上限是 5 万。
	if big.History > 50000 {
		t.Errorf("历史预算超过了软上限: %d", big.History)
	}
}

// 越界的值一律夹回来，而不是拒绝保存——这些是调参，不是安全开关。
func TestThresholdClamping(t *testing.T) {
	if got := softCapOf(AIProfile{ContextSoftCap: 1}); got != aiContextSoftCapMin {
		t.Errorf("过小的软上限应夹到 %d，实际 %d", aiContextSoftCapMin, got)
	}
	if got := softCapOf(AIProfile{ContextSoftCap: 1 << 30}); got != aiContextSoftCapMax {
		t.Errorf("过大的软上限应夹到 %d，实际 %d", aiContextSoftCapMax, got)
	}
	if got := preflightMarginOf(AIProfile{PreflightMargin: 10}); got != preflightMarginMin {
		t.Errorf("过小的触发线应夹到 %d，实际 %d", preflightMarginMin, got)
	}
	if got := preflightMarginOf(AIProfile{PreflightMargin: 100}); got != preflightMarginMax {
		t.Errorf("触发线不该到 100%%（那就永远不压了），应夹到 %d，实际 %d", preflightMarginMax, got)
	}
	// 上下限填反：夹成同一个值，压缩仍能工作。
	lo, hi := keepRecentOf(AIProfile{KeepRecentMin: 30, KeepRecentMax: 10})
	if lo != 30 || hi != 30 {
		t.Errorf("填反时应夹成同一个值，实际 %d/%d", lo, hi)
	}
}

// 保留条数是「最多保留多少条」，短历史不能被它撑大。
func TestKeepRecentWithOverrideDoesNotInflate(t *testing.T) {
	// 直接验行为：给 3 条历史、上限 40，结果必须是 3。
	msgs := make([]db.AIMessage, 0, 3)
	for i := 0; i < 3; i++ {
		msgs = append(msgs, db.AIMessage{Role: "user", Content: "x"})
	}
	if got := keepRecentCount(msgs, 200000, AIProfile{KeepRecentMax: 40}); got != 3 {
		t.Errorf("短历史应保留全部 3 条，实际 %d", got)
	}
}
