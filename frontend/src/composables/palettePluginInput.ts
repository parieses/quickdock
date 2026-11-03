// 「这条插件命令该带什么文本、走哪条执行通道」——从 usePaletteExecutor 里抽出来的纯逻辑。
//
// 原来这段是 executeSelected 里的一串 if / let（inputText 被赋了六七次），
// 分支之间还会互相影响：想验证「敲 keyword 回车会不会静默复用上次输入」得先把
// 137 行的执行器读一遍。抽成纯函数后可以直接对着表打用例。
//
// 三个概念要分清：
//   inlineInput —— 命令声明 acceptsInput 时，作为**后端参数**带进去的文本
//   hitText     —— 正则 / 内容嗅探命中的原文，与 acceptsInput 无关（前端插件自己读 plugin:init）
//   lastInput   —— 上一次执行时的输入，用于一键重跑

export interface PluginInputOpts {
  /** 命令是否声明 acceptsInput（后端参数语义） */
  acceptsInput?: boolean
  /** 插件是否有前端界面（有则走内联/独立窗口，没有则直接调后端） */
  pluginHasFrontend?: boolean
  /** 命中类型：'slash' 表示用户显式写了 `/prefix 参数` */
  matchType?: string
  /** 打分阶段给出的参数文本（只有 acceptsInput 命令会有） */
  inlineInput?: string
  /** 从结果 label（「命令标题: 输入」）里反解出来的输入 */
  labelInput?: string
  /** 命中文本原文（正则命中 / 内容嗅探命中） */
  hitText?: string
  /** 上一次的输入（持久化 store 优先，其次 usage 表） */
  lastInput?: string
  /** 面板处于「最近使用」列表模式（输入框为空），此时点一条 = 一键重跑 */
  isRecentMode?: boolean
}

export type PluginInputAction =
  /** 带着 text（可能为空）直接调后端执行 */
  | 'execute'
  /** 打开插件的界面，text 通过 plugin:init 传过去 */
  | 'open-inline'
  /** acceptsInput 命令没有任何输入，又没有界面可补：提示用户先填参数 */
  | 'need-input'

export interface PluginInputPlan {
  text?: string
  action: PluginInputAction
}

/**
 * 决定插件命令这次执行带什么文本。
 *
 * 关键规则：**「上一次的输入」只在用户显式表达「重跑」时才回退**——
 *   - matchType === 'slash'：`/sql` 这种只敲了前缀的显式调用，参数留空就是上次那个；
 *   - 最近使用列表：点历史记录本来就是重跑。
 * 敲 keyword（`json` 回车）不属于这两类：那次回退会让一个从来没给过参数的命令
 * 静默带着几个月前的输入执行，用户完全无从察觉。
 */
export function resolvePluginInput(o: PluginInputOpts): PluginInputPlan {
  let text: string | undefined

  if (o.acceptsInput) {
    text = o.inlineInput || o.labelInput || undefined
    if (!text && (o.matchType === 'slash' || o.isRecentMode)) {
      text = o.lastInput || undefined
    }
  }

  // 内容通道：命中的原文交给插件自己决定怎么用。前端插件能读 plugin:init.text，
  // 哪怕命令根本没声明 acceptsInput（json-toolbox 就是这种「仅界面」入口）；
  // 无前端插件不享受这条 —— 凭空多塞一个参数会改变后端命令的语义。
  if (!text && o.pluginHasFrontend) {
    text = o.hitText || undefined
  }

  if (o.acceptsInput && !text) {
    return {
      text: undefined,
      action: o.pluginHasFrontend ? 'open-inline' : 'need-input',
    }
  }
  return { text, action: o.pluginHasFrontend ? 'open-inline' : 'execute' }
}
