// AI 回复的 markdown「按块增量渲染」内核。
//
// 为什么需要这一层（原来这堆逻辑直接写在 AIPage 的 setup 里）：
// 流式期间若每帧都对全文做 md.parse + v-html 整段替换，成本随回复变长线性上升
// （实测 15KB 全文单帧 md.parse 约 10.5ms，其中 lexer 占 5ms），而 v-html 的
// innerHTML 赋值会销毁并重建该消息的全部 DOM 节点 —— 两者叠加就是「AI 一直在
// 回复但界面发卡」的根因。改为按顶层块渲染后实测降到 0.75ms/帧（约 14×）。
//
// 两种缓存刻意分开（见下方注释），这一点很容易在后续重构里被「合并成一个 LRU」
// 顺手做掉，然后把「越长的对话越卡」重新引回来。
import type { Ref } from 'vue'
import type { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { stripDsmlMarkup } from '../utils/dsmlMarkup'
import { splitMarkdownBlocks as splitBlocks } from '../utils/markdownBlocks'

export interface LiveBlock {
  raw: string
  html: string
}

interface Deps {
  /** 页面自己装配好的 Marked 实例（renderer 里要用 i18n 文案，故不在这里 new） */
  md: Marked
  /** 流式中的块数组，由页面持有（模板直接 v-for 它） */
  streamBlocks: Ref<LiveBlock[]>
}

export function useAiMarkdownBlocks(deps: Deps) {
  // 历史消息的块 HTML：按消息 id 记忆化。
  //
  // 为什么不用「整段文本 → 块数组」的全局 LRU：
  // 之前 mdCache 上限 200 段，淘汰采用「删最旧一条」。当一个会话的消息数超过上限后，
  // 模板每帧遍历历史消息时，最早的那批条目正好被挤在淘汰线上，于是每帧都要把全部
  // 历史消息重新 md.parse 一遍。实测：400 段 × 3 块的会话，稳态帧 md.parse 次数 = 1200
  // （= 消息数 × 块数），也就是「越长的对话越卡」，而且是持续卡，不是只在首帧卡。
  //
  // AI 消息落库后内容不再变化，按消息 id 记忆化即可：
  // 无容量上限（随会话消息数自然增长）、不会抖动、也不会误伤其他会话。
  const msgBlockCache = new Map<string, { src: string; html: string[] }>()

  // 流式块的 HTML 缓存（与历史块分开存放）。
  // 分开的原因：两者共用一个 LRU 时，长对话的历史块会把流式块挤出去，
  // 导致流式期间「已完成的块」每帧都被重新解析 —— 恰好是最需要流畅的时候掉帧。
  // 流式块的规模只与单次回复的段落数有关，不会无限增长，故不设上限。
  const liveBlockCache = new Map<string, string>()

  // 以「围栏代码块之外的空行」为界切块：切分点必须是 markdown 语义上独立的边界，
  // 否则分块渲染与整段渲染结果不一致（表现为流式过程中内容突然变形）。
  // 具体规则与边界场景测试见 utils/markdownBlocks.ts。
  function splitMarkdownBlocks(src: string): string[] {
    return splitBlocks(src)
  }

  // 解析单个块（带缓存）。命中缓存时返回同一字符串引用，
  // Vue 的 patchElement 对 innerHTML 只在值变化时赋值 → 不产生 DOM 写入。
  //
  // 注意：只有已完成的块才写入缓存。流式中最后一个未完成块每帧原文都变，
  // 若也缓存会产生大量一次性条目；未完成块每帧重解析本身开销就很小
  // （实测末块仅占全文 0.2%）。
  function renderBlock(raw: string, cacheable = true): string {
    const cached = liveBlockCache.get(raw)
    if (cached !== undefined) return cached
    const html = DOMPurify.sanitize(deps.md.parse(raw, { async: false }) as string)
    if (cacheable) liveBlockCache.set(raw, html)
    return html
  }

  // 历史消息的块 HTML 数组。消息内容不可变，故可安全缓存；
  // src 用于兜底校验（内容若被改动则重新解析）。
  function renderMessageBlocks(m: { id: string; content?: string }): string[] {
    const hit = msgBlockCache.get(m.id)
    if (hit && hit.src === m.content) return hit.html
    // 渲染前剥掉「被写成正文的工具调用」标记：修复前落库的历史消息里带着这些标记，
    // 直接渲染就是满屏 <｜｜DSML｜｜>（见 utils/dsmlMarkup.ts）。
    const raws = splitMarkdownBlocks(stripDsmlMarkup(m.content ?? ''))
    const html = raws.map((raw) => DOMPurify.sanitize(deps.md.parse(raw, { async: false }) as string))
    msgBlockCache.set(m.id, { src: m.content ?? '', html })
    return html
  }

  // 流式增量渲染：只重新解析最后一个未完成块，其余块直接复用缓存。
  //
  // 对齐方式：只有除最后一块外的块才按原文做公共前缀匹配。
  // 原因是最后一块是「正在写」的块，它的原文每帧都在变，若也要求原文完全相等，
  // 就永远匹配不上 → 每次都从第 0 块重建，导致已渲染内容被顶替（实测复现）。
  // 最后一块无条件重渲染（它的体量小，开销低），前面的块原文一旦稳定就不会再变。
  function renderLiveBlocks(src: string) {
    // 与历史消息同一个口径：先剥掉工具调用标记，再切块渲染。
    // 后端已经在流式层拦过一次（services/ai/ai_dsml.go），这里是幂等的兜底。
    const raws = splitMarkdownBlocks(stripDsmlMarkup(src))
    const arr = deps.streamBlocks.value
    const n = raws.length
    // 已完成块的数量：raws 的前 n-1 块（最后一块是进行中的）
    const doneCount = n > 0 ? n - 1 : 0
    // 计算与上一帧的公共前缀（只比较已完成块）
    let common = 0
    const limit = Math.min(doneCount, arr.length)
    while (common < limit && arr[common].raw === raws[common]) {
      common++
    }
    // 公共前缀的 HTML 必然一致（同一原文 → 同一缓存结果），无需重写
    for (let i = common; i < doneCount; i++) {
      const html = renderBlock(raws[i])
      if (i < arr.length) {
        if (arr[i].html !== html) arr[i].html = html
        arr[i].raw = raws[i]
      } else {
        arr.push({ raw: raws[i], html })
      }
    }
    // 最后一块（进行中）：无条件重渲染，且不写入缓存（原文每帧都变）
    if (n > 0) {
      const html = renderBlock(raws[n - 1], false)
      if (doneCount < arr.length) {
        if (arr[doneCount].html !== html) arr[doneCount].html = html
        arr[doneCount].raw = raws[n - 1]
      } else {
        arr.push({ raw: raws[n - 1], html })
      }
    }
    if (arr.length > n) arr.length = n
  }

  // 流式结束、消息落地时，把已渲染好的块直接交给历史消息缓存。
  // 否则落地瞬间要把整条回复重新 md.parse 一遍（实测 15KB ≈ 10.5ms），
  // 恰好发生在用户刚看到完整回复的时刻 —— 表现为「回复刚出来就卡一下」。
  function primeMessageBlocks(id: string, content: string, html: string[]) {
    msgBlockCache.set(id, { src: content, html })
  }

  // 只保留当前会话仍在显示的消息缓存，丢弃其他会话的条目。
  function pruneMessageBlocks(list: { id: string }[]) {
    if (msgBlockCache.size === 0) return
    const alive = new Set(list.map((m) => m.id))
    for (const id of Array.from(msgBlockCache.keys())) {
      if (!alive.has(id)) msgBlockCache.delete(id)
    }
  }

  // 按回复隔离：一条回复的块原文可能与另一条完全相同（如「好的」这类短回复），
  // 跨回复复用是安全的，但没必要长期持有。
  function clearLiveCache() {
    liveBlockCache.clear()
  }

  return {
    splitMarkdownBlocks,
    renderBlock,
    clearLiveCache,
    renderMessageBlocks,
    renderLiveBlocks,
    primeMessageBlocks,
    pruneMessageBlocks,
  }
}
