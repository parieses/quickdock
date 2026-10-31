// AI 输入框的 @ 引用补全（见后端 ai_mentions.go）。
//
// 输入框里打 @ 弹出补全，选中后变成一个 chip；发送时只把「引用了谁」带给后端，
// 由后端读内容并展开进上下文。
//
// 为什么不在前端读文件内容：读路径必须过 fsguard 白名单，而白名单在 Go 侧；
// 前端读等于绕过它。也不把内容塞进 message 文本——那样用户自己的气泡里会
// 出现几千行文件内容，而且会跟着落库。
import { computed, ref, watch, type ComputedRef, type Ref } from 'vue'
import { AISuggestMentions } from '../../bindings/quickdock/services/ai/aiservice'
import { unwrap } from '../utils/api'
import type { AIConversation, AIMentionItem } from '../types/ai'

// 发送时带给后端的一份引用快照（字段全部拍平，避免把 kind 的可选性传下去）。
export interface MentionSnapshot { kind: string; path: string; convId: string; name: string }

export interface AiMentionDeps {
  input: Ref<string>
  streaming: Ref<boolean>
  hasKey: Ref<boolean> | ComputedRef<boolean>
  conversations: Ref<AIConversation[]>
  // 工作目录：后端按它扫文件（空 = 只能引用会话）
  convCwd: Ref<string>
}

export function useAiMention(deps: AiMentionDeps) {
  const { input, streaming, hasKey, conversations, convCwd } = deps

  const mentions = ref<AIMentionItem[]>([])
  const mentionItems = ref<AIMentionItem[]>([])
  const mentionIdx = ref(0)
  let mentionTimer: number | null = null
  // 上一次发送用过的引用：重试（reuseUserBubble）时输入框已清空，靠它把引用带回去。
  const lastMentions = ref<MentionSnapshot[]>([])

  // 只识别输入框末尾的 @token。
  //
  // 光标在中间时「@」可能是已发出的正文（比如邮箱、@2x 图片名），按末尾判定
  // 能避开绝大多数误触发；代价是中间插入 @ 不弹窗，用户把光标挪到末尾即可。
  const mentionQuery = computed<string | null>(() => {
    if (streaming.value || !hasKey.value) return null
    const m = /(^|\s)@([^\s@]*)$/.exec(input.value)
    return m ? m[2] : null
  })

  const mentionOpen = computed(() => mentionQuery.value !== null && mentionItems.value.length > 0)

  async function loadMentionItems(q: string) {
    const out: AIMentionItem[] = []
    // 会话从本地列表里筛：这些数据页面已经有了，为它再走一次 RPC 只是多一次往返。
    const ql = q.toLowerCase()
    for (const c of conversations.value) {
      const title = (c.title || '').trim()
      if (!title || (ql && !title.toLowerCase().includes(ql))) continue
      out.push({ kind: 'conv', name: title, convId: c.id })
      if (out.length >= 5) break
    }
    try {
      const list = unwrap<AIMentionItem[]>(await AISuggestMentions(convCwd.value, q))
      for (const it of list || []) out.push({ kind: it.kind, name: it.name, path: it.path })
    } catch {
      // 没有工作目录 / 目录已失效：只给会话项。这不是错误，不该弹提示打断输入。
    }
    mentionItems.value = out
    mentionIdx.value = 0
  }

  watch(mentionQuery, (q) => {
    if (mentionTimer) { clearTimeout(mentionTimer); mentionTimer = null }
    if (q === null) {
      mentionItems.value = []
      mentionIdx.value = 0
      return
    }
    // 120ms 防抖：扫目录是同步 IO，每敲一个字都扫一遍会在机械硬盘上明显卡顿。
    mentionTimer = window.setTimeout(() => { void loadMentionItems(q) }, 120)
  })

  function sameMention(a: AIMentionItem, b: AIMentionItem): boolean {
    return a.kind === b.kind && (a.path || '') === (b.path || '') && (a.convId || '') === (b.convId || '')
  }

  function pickMention(it: AIMentionItem) {
    // 把输入框末尾那个 @token 删掉：chip 才是引用的载体，正文里留着 @xxx
    // 会在发给模型的文本里多出一段既不像路径也不像名字的东西。
    input.value = input.value.replace(/@[^\s@]*$/, '')
    if (!mentions.value.some((m) => sameMention(m, it))) mentions.value.push(it)
    mentionItems.value = []
    mentionIdx.value = 0
  }

  function removeMention(i: number) {
    mentions.value.splice(i, 1)
  }

  // onMentionKey 处理补全弹层里的方向键与回车。
  // 返回 true 表示这次按键已被弹层消费。
  function onMentionKey(e: KeyboardEvent): boolean {
    if (!mentionOpen.value) return false
    const n = mentionItems.value.length
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      mentionIdx.value = (mentionIdx.value + 1) % n
      return true
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      mentionIdx.value = (mentionIdx.value - 1 + n) % n
      return true
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      pickMention(mentionItems.value[mentionIdx.value])
      return true
    }
    if (e.key === 'Escape') {
      // 只收起弹层、保留已打的 @xxx：Esc 是「我不想选」，不是「我要清空刚打的字」。
      e.preventDefault()
      mentionItems.value = []
      return true
    }
    return false
  }

  return {
    mentions, mentionItems, mentionIdx, mentionQuery, mentionOpen, lastMentions,
    pickMention, removeMention, onMentionKey,
  }
}
