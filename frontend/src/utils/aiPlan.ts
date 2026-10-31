// 计划模式的结构解析：把一条回答按 `### 章节 + 列表项` 拆成小节，
// 让「步骤」变成可勾的清单、把「按此计划执行」摆在计划旁边，而不是埋在正文末尾。
//
// 抽成独立文件的原因：这一整套是纯函数（输入字符串 + 消息 id，输出小节数组），
// 与页面状态无关，是最容易写单测、也最容易被后续改动破坏的一块。
// 之前它和流式渲染、工具卡片混在一个 5000 行的 SFC 里，改一行不敢确认影响面。
import { stripDsmlMarkup } from './dsmlMarkup'

export interface PlanSection {
  title: string
  items: string[]
  text: string
}

// 步骤章节的标题口径。中文为主（system prompt 是中文的），英文只是兜底。
const PLAN_STEP_TITLE = /计划|步骤|plan|steps/i

/**
 * 解析回答里的章节结构。少于两节返回 null —— 普通回答里也常有零星 ## 小标题，
 * 那种情况应回落到普通 markdown 渲染，而不是给一张空的计划卡片。
 */
export function parsePlanSections(src: string): PlanSection[] | null {
  const out: PlanSection[] = []
  let cur: PlanSection | null = null
  // 围栏里的 `# 注释` 不是标题、`- 参数` 不是列表项：不认围栏会把一段命令示例
  // 拆成十几个假章节。围栏原样留在 text 里，交给 markdown 渲染成代码块。
  let inFence = false
  for (const raw of src.split('\n')) {
    const line = raw.trimEnd()
    const fence = line.trimStart().startsWith('```')
    if (fence) inFence = !inFence
    if (!inFence && !fence) {
      const h = /^#{1,6}\s+(.+?)\s*$/.exec(line)
      if (h) {
        cur = { title: h[1], items: [], text: '' }
        out.push(cur)
        continue
      }
    }
    if (!cur) continue
    const item = !inFence && !fence ? /^\s*(?:\d+[.、)]|[-*+])\s+(.+)$/.exec(line) : null
    if (item) cur.items.push(item[1])
    else if (line.trim()) cur.text += (cur.text ? '\n' : '') + line
  }
  return out.length >= 2 ? out : null
}

// 按消息 id 记忆化：内容不可变，src 用于兜底校验（内容被改动则重新解析）。
const planCache = new Map<string, { src: string; secs: PlanSection[] | null }>()

interface PlanSource {
  id: string
  role: string
  content?: string
}

/** 这条消息的计划小节；不是 assistant 或解析不出结构时返回 null。 */
export function planOf(m: PlanSource): PlanSection[] | null {
  if (m.role !== 'assistant') return null
  const src = m.content ?? ''
  const hit = planCache.get(m.id)
  if (hit && hit.src === src) return hit.secs
  const secs = parsePlanSections(stripDsmlMarkup(src))
  planCache.set(m.id, { src, secs })
  return secs
}

// 模板里每个调用点写 `planOf(m) ?? []` 会各造一个空数组，diff 时被当成新值；
// 空态共用一个常量引用。
export const EMPTY_PLAN: PlanSection[] = []

/** 模板友好的版本：解析不出结构时返回同一个空数组引用。 */
export function planSecs(m: PlanSource): PlanSection[] {
  return planOf(m) ?? EMPTY_PLAN
}

/**
 * 哪一节是「步骤」：标题命中的那一节，否则取第一个有列表项的。
 * 勾选框只给这一节——涉及文件、风险也用列表写，全变成勾选框就没有主次了。
 */
export function planStepIndex(secs: PlanSection[]): number {
  const byTitle = secs.findIndex((s) => PLAN_STEP_TITLE.test(s.title) && s.items.length > 0)
  if (byTitle >= 0) return byTitle
  return secs.findIndex((s) => s.items.length > 0)
}
