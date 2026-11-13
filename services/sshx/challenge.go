package sshx

// 交互式问答（keyboard-interactive）的 UI 桥接：服务端下发「密码 + OTP」这类
// 多轮问题时，由这里挂起连接、把问题推给前端弹窗、再把答案回填给引擎。
//
// 状态刻意放在包级变量而不是 SshService 字段：service.go 定义的结构体本轮不改
// （主流程负责把 challengeFor 注入连接），往结构体加字段就得动那个共享文件。
// 服务是单例，且每次问答用全局唯一的 UUID 作 key，即便将来有多个 SshService
// 实例共用这张表也不会串台。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

// EventSshChallenge 推给前端的一次交互式问答。
const EventSshChallenge = "quickdock:ssh:challenge"

// challengeTimeout 等人作答的上限。
//
// 引擎层拨号有 dialTimeout(15s)，但问答是「人」在输入，15s 不够；
// 90s 覆盖手输 OTP 的节奏，又不至于让用户走开后连接永久挂住。
const challengeTimeout = 90 * time.Second

// SshChallengeEvent 交给前端弹窗渲染的一问。
//
// 字段都是「问题」不含「答案」：事件会跨进 JS 层并可能被日志系统看到，
// 答案只能由 AnswerSshChallenge 走 IPC 单独回传，绝不塞进事件里。
type SshChallengeEvent struct {
	ID          string   `json:"id"`
	HostID      string   `json:"hostId"`
	Name        string   `json:"name"`
	Instruction string   `json:"instruction"`
	Questions   []string `json:"questions"`
	Echos       []bool   `json:"echos"`
}

type challengeAnswer struct {
	answers  []string
	canceled bool
}

var (
	challengeMu      sync.Mutex
	challengePending = make(map[string]chan challengeAnswer)
)

// challengeCtxKey 把「本次拨号是否允许弹窗问答」挂在 context 上。
//
// 为什么走 context 而不是给 dial 加参数：dial 被 Connect / Exec / SFTP /
// 批量 / 端口转发 / 连通性测试 / MCP 共用，只有 Connect 有 UI 能收答案。
// 逐条调用链加参数会让「谁能弹窗」散落到 7 个入口，漏一个就是把没有 UI
// 的后台路径挂起 90 秒。context 让这个能力默认关闭、只在唯一入口显式打开。
type challengeCtxKey struct{}

func withChallenge(ctx context.Context, fn func(string, string, []string, []bool) ([]string, error)) context.Context {
	return context.WithValue(ctx, challengeCtxKey{}, fn)
}

func challengeFrom(ctx context.Context) func(string, string, []string, []bool) ([]string, error) {
	fn, _ := ctx.Value(challengeCtxKey{}).(func(string, string, []string, []bool) ([]string, error))
	return fn
}

// challengeFor 造一个可直接赋给 sshcore.AuthSpec.Challenge 的回调。
//
// hostID 只用于事件里展示主机归属，不参与挂起表的索引——索引用一次性 UUID，
// 这样同一台主机同时开多条连接、各自弹出问答也不会互相顶掉。
func (s *SshService) challengeFor(hostID string) func(string, string, []string, []bool) ([]string, error) {
	return func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		// 事件发不出去就别挂起、也别白等 90 秒：没有 UI 能收答案时，
		// 直接按「问答协议对不上」收场，让上层立刻失败而不是卡住整条连接。
		if s.App == nil {
			return nil, sshcore.ErrChallengeRequired
		}
		a := s.App.App()
		if a == nil {
			return nil, sshcore.ErrChallengeRequired
		}

		id := uuid.NewString()
		// 缓冲 1：AnswerSshChallenge 可能恰好在超时分支之后才送达，此时已无人接收；
		// 缓冲能让那次发送立即返回，不把 Wails 调用线程挂死。channel 随后随引用
		// 消失被 GC，所以这里不 close（见 defer 的说明）。
		ch := make(chan challengeAnswer, 1)

		challengeMu.Lock()
		challengePending[id] = ch
		challengeMu.Unlock()

		// 为什么 defer 只从表里删、不 close(ch)：AnswerSshChallenge 是「锁内取出并
		// 摘除 channel」之后才发送的，与这里的清理存在窗口。若 defer 把 ch close 掉，
		// 那次发送就会 panic（send on closed channel）。删表已足以让重复提交拿不到
		// channel，缓冲 1 的 ch 不需要 close 也不会泄漏。
		defer func() {
			challengeMu.Lock()
			delete(challengePending, id)
			challengeMu.Unlock()
		}()

		a.Event.Emit(EventSshChallenge, SshChallengeEvent{
			ID:          id,
			HostID:      hostID,
			Name:        name,
			Instruction: instruction,
			Questions:   questions,
			Echos:       echos,
		})

		timer := time.NewTimer(challengeTimeout)
		defer timer.Stop()

		select {
		case ans := <-ch:
			// 取消即「用户关了弹窗」：立刻回 ErrChallengeRequired，别让连接等到超时。
			if ans.canceled {
				return nil, sshcore.ErrChallengeRequired
			}
			// 引擎层还会按 questions 长度再兜一次；这里先拦下并给出更明确的原因，
			// 但用 %w 包住同一个哨兵，保证上层 errors.Is 的判定不变。只报条数，
			// 绝不带答案内容。
			if len(ans.answers) != len(questions) {
				return nil, fmt.Errorf("%w：答案条数不符（问题 %d 条，收到 %d 条）",
					sshcore.ErrChallengeRequired, len(questions), len(ans.answers))
			}
			return ans.answers, nil
		case <-timer.C:
			return nil, sshcore.ErrChallengeRequired
		}
	}
}

// AnswerSshChallenge 前端提交答案（Wails 门面）。
//
// canceled=true 表示用户关了弹窗，答案忽略、直接让挂起的连接以 ErrChallengeRequired
// 失败；这样连接不会一直卡到 90 秒超时。
func (s *SshService) AnswerSshChallenge(id string, answers []string, canceled bool) *services.ApiResult {
	id = strings.TrimSpace(id)
	if id == "" {
		return failCode(CodeInvalidInput, "交互式问答 id 为空")
	}

	// 「取出 + 摘除」必须在同一把锁里完成：一次问答只允许一次提交，否则重复提交
	// （连点、或超时后弹窗才关）会拿到同一个 channel 发两次，第二次要么填满缓冲
	// 阻塞，要么被误当成下一轮的答案。
	challengeMu.Lock()
	ch, ok := challengePending[id]
	if ok {
		delete(challengePending, id)
	}
	challengeMu.Unlock()

	if !ok {
		// 挂起项已经不在了：多半是 90 秒超时，或这一问已经答过。
		// 不能往没人接收的 channel 里发，也不能让这次 IPC 卡住。
		return failCode(CodeChallengeRequired, "该交互式问答已超时或已结束")
	}

	// ch 缓冲为 1 且刚从表里摘除，只会有这一次发送，必然立即返回；
	// 即便对端已因超时退出 select，也不会阻塞 Wails 调用。
	ch <- challengeAnswer{answers: answers, canceled: canceled}
	return services.Ok(nil)
}
