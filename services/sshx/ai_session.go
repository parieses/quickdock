package sshx

import (
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// SSH 终端的 AI 问答落库。
//
// 以前的形态是纯粹的一次性问答：问答只活在面板的内存里（最近 12 条历史 +
// 一段滚动摘要），面板一关就没了，AI 页也看不到。用户的原话是
// 「连接成功一次就创建一个助手任务，这次的所有问答都放在这个任务里」，
// 所以现在每条 SSH 会话对应一条 AI 任务（ai_conversations），问答成对写进
// ai_messages，AI 页能直接打开回看、接着问。
//
// 为什么懒创建（首次提问才建）而不是 Connect 成功就建：连上就退、
// 连上只为看一眼端口这类场景很多，提前建会给侧栏塞一堆空任务。
// 语义上没有区别 —— 一条连接只可能对应一个任务，第一次提问时才落地。
//
// 这些任务统一收进 kind='ssh' 的「SSH 助手」空间（见 ensureAIConv）：
// 连接史是「同一类东西」，混在用户自己的对话里只会让它更难找。

// sshAiConvTitle 任务标题：带上主机标识，用户在多条 SSH 任务之间能分得清。
//
// 前缀用「SSH」而不是 emoji/图标：AI 页侧栏是纯文本列表，前缀是最省事
// 也最容易被搜索到的区分方式。
const sshAiConvTitle = "SSH"

// aiConvTitleMaxRune 标题长度上限，与 ai_conversation.go 的自动标题口径一致。
const aiConvTitleMaxRune = 60

// ensureAIConv 返回该 SSH 会话对应的 AI 任务 id；没有就建一条。
//
// 返回空串表示「这次落不了库」（DB 未就绪 / 建任务失败）。调用方直接跳过落库：
// 问答本身已经成功了，不能因为存档失败把答案吞掉。
func (s *SshService) ensureAIConv(sessionID, hostLabel string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s.App == nil || s.App.DB == nil {
		return ""
	}

	s.aiConvMu.Lock()
	defer s.aiConvMu.Unlock()
	if id := s.aiConvs[sessionID]; id != "" {
		return id
	}

	// 统一收进「SSH 助手」空间（见 internal/db/ai_ssh_space.go）：
	// 不这么做，每连一次机器就有一条「SSH · user@host」混在用户自己的对话里，
	// 连十次就是十条，找自己的任务得先翻过一整屏。
	//
	// 空间取不到不是致命错误：任务照建，只是落到「任务」组。
	// 相反的处理（空间建不出来就不建任务）会让一段已经答完的问答凭空消失。
	spaceID := ""
	if sp, err := s.App.DB.EnsureSSHSpace(); err != nil {
		logger.W("[ssh-ai] 获取 SSH 空间失败: %v", err)
	} else if sp != nil {
		spaceID = sp.ID
	}

	conv, err := s.App.DB.CreateAIConversation(db.NewAIConversation{
		Title:   convTitleFor(hostLabel),
		Mode:    "chat",
		SpaceID: spaceID,
	})
	if err != nil {
		logger.W("[ssh-ai] 创建助手任务失败: %v", err)
		return ""
	}
	s.aiConvs[sessionID] = conv.ID
	return conv.ID
}

// convTitleFor 由主机描述拼任务标题。
//
// hostLabel 里可能带「（生产环境）」这类后缀（见 describeHost），留着有用：
// 侧栏里一眼能看出这条任务对应的机器属于什么环境。
func convTitleFor(hostLabel string) string {
	label := strings.TrimSpace(hostLabel)
	if label == "" {
		return sshAiConvTitle + " 助手"
	}
	title := sshAiConvTitle + " · " + label
	if r := []rune(title); len(r) > aiConvTitleMaxRune {
		title = string(r[:aiConvTitleMaxRune])
	}
	return title
}

// recordAiTurn 把一轮问答写进任务。
//
// 只在模型答完之后调用：调用失败时什么都不写，任务里就不会留下一条
// 「有问无答」的半截记录 —— 失败信息已经在面板上 toast 过了，再进库只是噪音。
//
// 落库失败只记日志：能不能回看是次要收益，不能让它影响这次提问的结果。
func (s *SshService) recordAiTurn(convID, question, answer, reason string) {
	if convID == "" || s.App == nil || s.App.DB == nil {
		return
	}
	if _, err := s.App.DB.AddAIMessageUser(convID, question, "", ""); err != nil {
		logger.W("[ssh-ai] 写入提问失败: %v", err)
		return
	}
	if _, err := s.App.DB.AddAIMessageFull(convID, "assistant", answer, reason); err != nil {
		logger.W("[ssh-ai] 写入回答失败: %v", err)
		return
	}
	// 推进任务更新时间：侧栏按 updated_at 排序，不写的话这条任务会一直沉在
	// 创建那一刻的位置，用户刚问完却在列表最底下找不到。
	if err := s.App.DB.TouchAIConversation(convID); err != nil {
		logger.W("[ssh-ai] 更新任务时间失败: %v", err)
	}
}

// dropAIConv 在 SSH 会话被主动关闭时摘掉映射。
//
// 任务本身不删：用户关掉终端之后仍然可能想回看这几轮问答，
// 而「删一个任务」应该是用户在 AI 页里的显式动作。映射摘掉即可，
// 下次重连同一台机器就是一条新任务。
func (s *SshService) dropAIConv(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.aiConvMu.Lock()
	delete(s.aiConvs, sessionID)
	s.aiConvMu.Unlock()
}
