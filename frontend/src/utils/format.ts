/**
 * AI 用量口径的统一格式化。
 *
 * 这三处曾逐字重复在 AIPage / AiMessageList / SettingsAI 里：设置页总量、每日柱、
 * 模型表、聊天气泡的单条统计必须显示成「同一种钱 / 同一种 token 数」，否则用户
 * 在同一屏里会对不上账。口径有变动只改这里。
 *
 * 注意：AgentRunTree / AiTraceDrawer 里的同名函数是有意的不同口径（空串表示
 * 「不显示」、列宽更窄时精度不同），不要误并进来。
 */

/** token 数折算成 k/M：真实对话的 input 常是几万到几十万，原样打印会撑爆列宽。 */
export function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 1 : 2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + 'k'
  return String(n)
}

/** 费用格式化：小额多给两位小数，否则 ¥0.00 会把「几乎不要钱」和「免费」混为一谈。 */
export function fmtCost(n: number): string {
  if (!n) return '¥0'
  if (n >= 1) return '¥' + n.toFixed(2)
  if (n >= 0.01) return '¥' + n.toFixed(3)
  return '¥' + n.toFixed(4)
}
