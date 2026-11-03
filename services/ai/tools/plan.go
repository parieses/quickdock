package aitools

// 任务清单：让「大任务拆开做、做完一个勾一个」有可勾选的落点。
//
// 为什么需要一个工具而不是让模型在正文里写 `- [ ]`：markdown 清单是死的。
// 它一旦流式吐出去就改不了，模型做完第二步时无法回头把第一步标成完成——
// 用户看到的永远是「全都还没做」。清单要能随进度变化，就必须是一个独立状态，
// 由工具调用去更新，由界面去渲染。
//
// 为什么状态只活在 ctx 上、不落库：它描述的是「这一次提问正在做什么」，
// 与回复一起结束。落库会引出「上一轮的清单什么时候清」这种没有正确答案的问题，
// 而用户真正要留档的是最终回复，不是中途的 checklist。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsrv "quickdock/internal/mcp"
)

const (
	// planMaxItems 单份清单最多几条。再长就不是「步骤」而是需求文档了。
	planMaxItems = 30
	// planItemMaxRune 单条文字上限：清单是给人扫一眼的，不是任务说明。
	planItemMaxRune = 160
)

// PlanItem 清单里的一条。
type PlanItem struct {
	Text string `json:"text"`
	// Status 取 todo / doing / done。前端据此决定打勾与删除线。
	Status string `json:"status"`
}

// PlanUpdate 一次清单更新（整份覆盖，不是增量）。
//
// 整份覆盖而不是「改第 3 条」：模型手里的清单本来就只有几行，重发一遍最不容易错；
// 而按序号改在模型重排过顺序之后会改错行，且错了没人看得出来。
type PlanUpdate struct {
	Items []PlanItem `json:"items"`
	Note  string     `json:"note,omitempty"`
}

// PlanHookFn 清单更新的接收方（宿主把它接到 SSE 的 plan 事件上）。
type PlanHookFn func(PlanUpdate)

type planHookKey struct{}

// WithPlanHook 把清单回调挂到 ctx 上。
//
// 与 CmdProgress 同一套做法：工具拿不到 sink（它只认 Deps + args），
// 而「清单变了」这件事必须立刻到前端，所以只能经 ctx 过去。
func WithPlanHook(ctx context.Context, fn PlanHookFn) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, planHookKey{}, fn)
}

// PlanHookFrom 取出 ctx 上的清单回调；没挂返回 nil（子 agent、插件调用）。
func PlanHookFrom(ctx context.Context) PlanHookFn {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(planHookKey{}).(PlanHookFn)
	return fn
}

func registerPlan(d Deps) {
	r := registrar{d: d, category: mcpsrv.CategoryCode}
	str := mcpsrv.Str

	r.addCtx(updatePlan, mcpsrv.LevelRead, "plan_update",
		"维护当前任务的步骤清单（用户界面上会实时显示成一份可勾选的列表）。"+
			"任务需要三步以上动作时，动手前先调一次建立清单：全部 status 用 todo，正在做的那条用 doing；"+
			"每完成一步再调一次，把那条改成 done、下一条改成 doing。"+
			"整份清单每次都要完整重发（不是只发改动的那条）。两步以内能做完的事不要用它",
		[]string{"items"},
		map[string]any{
			"items": map[string]any{
				"type":        "array",
				"description": "完整步骤列表，按执行顺序",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text":   str("一条可执行的动作，动词开头，一句话说完"),
						"status": map[string]any{"type": "string", "enum": []string{"todo", "doing", "done"}, "description": "todo 待做 / doing 正在做 / done 已完成"},
					},
					"required": []string{"text", "status"},
				},
			},
			"note": str("可选。一句话说明当前进展或下一步，会显示在清单下方"),
		})
}

// updatePlan 是 plan_update 的实现：校验 → 归一化 → 交给 hook。
func updatePlan(ctx context.Context, _ Deps, args map[string]any) (any, error) {
	raw, ok := args["items"]
	if !ok {
		return nil, errors.New("items 不能为空")
	}
	// 两种形状都收：内置 AI 走 JSON 解码得到 []any，而直接走 Go 调用
	// （测试、将来的其它宿主）可能是 []map[string]any。只认一种的话，
	// 另一种会以「items 必须是数组」这种莫名其妙的错误失败。
	var list []any
	switch v := raw.(type) {
	case []any:
		list = v
	case []map[string]any:
		list = make([]any, 0, len(v))
		for _, m := range v {
			list = append(list, m)
		}
	default:
		return nil, errors.New("items 必须是数组")
	}
	if len(list) == 0 {
		return nil, errors.New("清单不能是空数组")
	}
	if len(list) > planMaxItems {
		list = list[:planMaxItems]
	}

	upd := PlanUpdate{Note: strings.TrimSpace(mcpsrv.Arg(args, "note"))}
	done, doing := 0, 0
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(m["text"]))
		if text == "" {
			continue
		}
		if n := len([]rune(text)); n > planItemMaxRune {
			text = string([]rune(text)[:planItemMaxRune]) + "…"
		}
		status := normalizePlanStatus(fmt.Sprint(m["status"]))
		switch status {
		case "done":
			done++
		case "doing":
			doing++
		}
		upd.Items = append(upd.Items, PlanItem{Text: text, Status: status})
	}
	if len(upd.Items) == 0 {
		return nil, errors.New("清单里没有有效的步骤")
	}

	// 没有接收方（子 agent / 插件调用）时不能报错：清单对它们没意义，
	// 但「调了一个不存在的界面」不该让一次正常的任务执行失败。
	if hook := PlanHookFrom(ctx); hook != nil {
		hook(upd)
	}

	left := len(upd.Items) - done
	return map[string]any{
		"ok":      true,
		"total":   len(upd.Items),
		"done":    done,
		"doing":   doing,
		"left":    left,
		"message": fmt.Sprintf("清单已更新：共 %d 步，已完成 %d 步，还剩 %d 步", len(upd.Items), done, left),
		// 提醒模型别把清单当进度汇报工具：更新完就该继续动手，
		// 而不是每改一次清单就说一段「我已经完成了第 N 步」。
		"hint": "继续做下一步，不要在正文里复述整份清单",
	}, nil
}

// normalizePlanStatus 把模型可能给出的各种写法收敛到三态。
//
// 不做严格校验（不合法就丢条目）：模型写 "completed"、"已完成"、"in_progress"
// 都表达得清清楚楚，为这个把一条有效的步骤丢掉，比认下来更糟。
func normalizePlanStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "done", "complete", "completed", "finished", "ok", "完成", "已完成":
		return "done"
	case "doing", "in_progress", "in-progress", "running", "active", "进行中", "正在做":
		return "doing"
	default:
		return "todo"
	}
}
