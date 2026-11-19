// 剪贴板 / 便签面板「翻译 / 总结」按钮的显示名单一事实源。
//
// 聊天页曾有一排 chip（解释 / 翻译 / 总结 / 审阅）、落地页曾有一排模式 pill，两处
// 摆的都是同样的意图。这些已经全部收口：聊天页和落地页默认就是普通对话，话术统一
// 从输入框左侧 + 面板的「提示词」取（internal/db/prompt.go，用户能改能加）。
// 硬编码模板与提示词库必然重复，别再往界面加第二份。
//
// 这里只剩一件事：按 action key 给那两个面板取显示名。key 与后端 ai_actions.go 的
// 常量一致，实际指令仍在后端 prompts/actions/*.md。

/** 一个动作：key 与后端 ai_actions.go 的常量一一对应。 */
interface AiActionChip {
  key: 'chat' | 'explain' | 'translate' | 'summarize' | 'review'
  labelKey: string
}

const AI_ACTION_CHIPS: AiActionChip[] = [
  { key: 'chat', labelKey: 'aiModeChat' },
  { key: 'explain', labelKey: 'aiModeExplain' },
  { key: 'translate', labelKey: 'aiModeTranslate' },
  { key: 'summarize', labelKey: 'aiModeSummarize' },
  { key: 'review', labelKey: 'aiActionReview' },
]

/** 按 key 取 label（面板复用；找不到返回 key 本身，别把界面弄成空白）。 */
export function aiActionLabelKey(key: string): string {
  return AI_ACTION_CHIPS.find((a) => a.key === key)?.labelKey || key
}
