// 快捷提示词（解释/翻译/总结/审阅）的前端单一事实源。
//
// 为什么放这里：输入框的 chip 行、剪贴板面板、便签面板都要列「翻译 / 总结」，
// 各自写一份 label 和提示词就会漂（同一个动作两处叫两个名字、两处填两段不同的指令）。
//
// 交互语义：点按钮 = 把 fillKey 对应的模板填进输入框，光标留在末尾等用户补内容
// （同 Cursor / Claude Code 的快捷 prompt）。不再是「选中一个隐藏动作」——
// 提示词就在正文里，所见即所得，发送时只发普通对话，不走 promptAction 机制。
// 后端的 prompts/actions/*.md 仍保留，供老会话兼容。
//
// 模式与动作分离：落地页「模式」只保留对话（chat）；解释/翻译/总结/审阅是
// 聊天页的快捷提示词，不是模式。执行权限由现有权限徽标（只读/完全访问）承担，
// 不另加 Ask/Agent 按钮——那会和「只读」重复。

/** 一个动作：key 与后端 ai_actions.go 的常量一一对应（老会话/sidebar 图标仍读它）。 */
export interface AiActionChip {
  key: 'chat' | 'explain' | 'translate' | 'summarize' | 'review'
  labelKey: string
  descKey: string
  /** 点击时填入输入框的模板 i18n key；chat 为空（它不是提示词，代表普通对话）。 */
  fillKey: string
}

/** 全量清单：chat 在首位供 sidebar 图标/历史 mode 映射使用，不代表落地页要全显示。 */
export const AI_ACTION_CHIPS: AiActionChip[] = [
  { key: 'chat', labelKey: 'aiModeChat', descKey: 'aiModeChatDesc', fillKey: '' },
  { key: 'explain', labelKey: 'aiModeExplain', descKey: 'aiModeExplainDesc', fillKey: 'aiQuickExplain' },
  { key: 'translate', labelKey: 'aiModeTranslate', descKey: 'aiModeTranslateDesc', fillKey: 'aiQuickTranslate' },
  { key: 'summarize', labelKey: 'aiModeSummarize', descKey: 'aiModeSummarizeDesc', fillKey: 'aiQuickSummarize' },
  { key: 'review', labelKey: 'aiActionReview', descKey: 'aiActionReviewDesc', fillKey: 'aiQuickReview' },
]

/** 聊天页输入区真正展示的快捷提示词：不含 chat（chat 只是落地页唯一入口）。 */
export const AI_QUICK_PROMPTS: AiActionChip[] = AI_ACTION_CHIPS.filter((a) => a.key !== 'chat')

/** 帮助按钮填入的模板 key。注意它是一句完整问题，本身就能直接发送，
 *  和「解释/翻译」那种待补内容的模板不同，不参与空模板拦截。 */
export const AI_HELP_FILL_KEY = 'aiActionHelpAsk'

/** 空模板校验用：这些 key 的译文原样留在输入框里 = 用户点了但没填内容。 */
export const QUICK_PROMPT_FILL_KEYS: string[] = AI_QUICK_PROMPTS.map((a) => a.fillKey)

/** 按 key 取 label（面板复用；找不到返回 key 本身，别把界面弄成空白）。 */
export function aiActionLabelKey(key: string): string {
  return AI_ACTION_CHIPS.find((a) => a.key === key)?.labelKey || key
}