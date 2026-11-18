package ai

// run 终态收口（finalRunStatus）的单测。
//
// 这段逻辑此前缺失，导致 agent_runs.status 永远停在 running：
// 轨迹抽屉的轮询停不下来，回放里每条历史都显示「运行中」。
// 判定抽成纯函数后单独测，不必跑需要 API Key Network 的 runAgent 全链路。

import (
	"errors"
	"testing"

	"quickdock/internal/db"
)

func TestFinalRunStatus(t *testing.T) {
	upstream := errors.New("上游 500")
	cases := []struct {
		name       string
		res        AgentRunResult
		wantStatus string
		wantErrMsg string
	}{
		{"正常结束", AgentRunResult{}, db.AgentStatusDone, ""},
		{"上游报错", AgentRunResult{Err: upstream}, db.AgentStatusFailed, "上游 500"},
		{"用户停止", AgentRunResult{Stopped: true, Err: upstream}, db.AgentStatusCanceled, ""},
		{"断连停止优先于错误", AgentRunResult{Stopped: true}, db.AgentStatusCanceled, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, msg := finalRunStatus(c.res)
			if got != c.wantStatus {
				t.Errorf("status = %q，期望 %q", got, c.wantStatus)
			}
			if msg != c.wantErrMsg {
				t.Errorf("errMsg = %q，期望 %q", msg, c.wantErrMsg)
			}
		})
	}
}
