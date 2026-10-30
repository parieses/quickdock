package aitools

// 写盘前的快照钩子（方案 A3 的 tools 侧一半）。
//
// 分工：这个包只定义「什么时候该快照」，不知道快照存在哪、长什么样——那是宿主
// （services/ai）的事。用接口 + ctx 而不是给 Deps 加字段，理由与审批闸门一样：
// Deps 是单例，而快照归属「这一次会话的这一次工具调用」，挂单例必然串台
// （A 会话的编辑记到 B 的回滚点里）。接口放这里、实现放 services/ai，
// 是因为 tools 不能反向依赖 ai（那会成环）。

import "context"

type checkpointHookKey struct{}
type toolCallIDKey struct{}

// CheckpointHook 由宿主注入：写盘前把 path 的原状态记下来。
//
// 返回错误时必须**放弃这次写入**。快照没成功还照写，等于「改了却撤不回」——
// 比不改更糟，因为界面上的回滚按钮会让人以为还有救。
type CheckpointHook interface {
	Snapshot(toolCallID, tool, path string) error
}

func WithCheckpointHook(ctx context.Context, h CheckpointHook) context.Context {
	return context.WithValue(ctx, checkpointHookKey{}, h)
}

func CheckpointHookFrom(ctx context.Context) CheckpointHook {
	h, _ := ctx.Value(checkpointHookKey{}).(CheckpointHook)
	return h
}

// WithToolCallID 把当前工具调用 id 挂到 ctx。
//
// 由执行核心在每次调用前设置：快照要能与「哪一条工具调用改的」对上，
// 否则界面上只能显示「第 3 次改动」而说不清是哪一步。
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

func ToolCallIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}

// snapshotBeforeWrite 两个写类工具的统一入口。
//
// 没有 hook 时静默跳过：外部 MCP 客户端调用、单测直接调工具函数这两种路径
// 本来就没有「会话」与「回滚」的概念，为它们造一个空实现只会掩盖真正的缺失。
func snapshotBeforeWrite(ctx context.Context, tool, path string) error {
	h := CheckpointHookFrom(ctx)
	if h == nil {
		return nil
	}
	return h.Snapshot(ToolCallIDFrom(ctx), tool, path)
}
