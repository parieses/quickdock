// AI 输入框的「/ 选技能」补全。
//
// 技能胶囊在**输入框内部**（行内原子节点，见 AiRichInput）：所以这个组合式不再碰
// 文本，只做三件事——识别末尾正在输入的 `/query`、过滤候选、把方向键/回车接给弹层。
// 「插入/摘掉胶囊」交给富输入框（它才知道光标在哪），通过 onPick 回调。
//
// 这样仍然只有一处状态：DOM 里的胶囊就是选择本身。`picked` 只是它的一份只读投影，
// 供面板标「已选」、供发送时取名字。
import { computed, ref, watch, type ComputedRef, type Ref } from 'vue'
import type { AISkill } from '../types/ai'

export interface AiSlashDeps {
  input: Ref<string>
  streaming: Ref<boolean>
  hasKey: Ref<boolean> | ComputedRef<boolean>
  skills: Ref<AISkill[]>
  /** 已选技能名（只读投影，展示用） */
  picked: Ref<string[]>
  /** 选中一个候选：由调用方插到光标处（富输入框的 insertSkill 会自己处理「再点一次=取消」） */
  onPick: (sk: AISkill) => void
}

// 末尾正在输入的 /token：行首或空白之后才算命令。
// 「前面必须是空白」这条限定掉的是路径与 URL 里的斜杠（D:/proj、http://x），
// 否则打开一个工作目录后随手打的路径会把技能菜单顶出来。
const TAIL_RE = /(^|\s)\/([^\s/]*)$/

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

export function useAiSlashSkill(deps: AiSlashDeps) {
  const { input, streaming, hasKey, skills, picked, onPick } = deps

  const slashIdx = ref(0)
  // Esc 收起 / 选中后临时抑制面板用：slashItems 已是只读 computed，不能再手动清空，
  // 所以把「该不该显示」与「过滤结果」拆开——否则 Esc 后 query 还在，computed 会立刻
  // 把面板又算回来。重新打 / 时（query 变非 null）解除抑制。
  const slashHidden = ref(false)

  const slashQuery = computed<string | null>(() => {
    if (streaming.value || !hasKey.value) return null
    const m = TAIL_RE.exec(input.value)
    return m ? m[2] : null
  })

  // 必须是 computed 且依赖 skills：技能清单是首次打 / 时异步懒加载的，
  // 加载完成那一刻 slashQuery 没变（用户停在那儿没再打字），若只看 query 就不会
  // 重新过滤，弹层始终是空的——表现为「概率不弹」（取决于打字有没有赶在加载前）。
  // 依赖 skills 后，清单一到就自动重算。
  const slashItems = computed<AISkill[]>(() => {
    const q = slashQuery.value
    if (q === null) return []
    const ql = q.toLowerCase()
    return skills.value
      .filter((s) => !s.error)
      .filter((s) => !ql
        || s.name.toLowerCase().includes(ql)
        || (s.description || '').toLowerCase().includes(ql))
      .slice(0, 8)
  })

  // query 变化时把高亮项归零（含变 null：收起时也别留着旧下标）；重新打 / 时解除 Esc 抑制
  watch(slashQuery, (q) => {
    slashIdx.value = 0
    if (q !== null) slashHidden.value = false
  })

  const slashOpen = computed(() => !slashHidden.value && slashQuery.value !== null && slashItems.value.length > 0)

  // onSlashKey 处理补全弹层里的方向键与回车。
  // 返回 true 表示这次按键已被弹层消费（调用方不能再当成发送/换行）。
  function onSlashKey(e: KeyboardEvent): boolean {
    if (e.isComposing || e.keyCode === 229) return false
    if (!slashOpen.value) return false
    const n = slashItems.value.length
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      slashIdx.value = (slashIdx.value + 1) % n
      return true
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      slashIdx.value = (slashIdx.value - 1 + n) % n
      return true
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      const sk = slashItems.value[slashIdx.value]
      slashHidden.value = true
      slashIdx.value = 0
      if (sk) onPick(sk)
      return true
    }
    if (e.key === 'Escape') {
      // 只收起弹层、保留已打的 /que：Esc 是「我不想选」，不是「清空我刚打的字」。
      e.preventDefault()
      slashHidden.value = true
      return true
    }
    return false
  }

  /**
   * 正文里万一还留着 `/已选技能` 的 token（粘贴、手打没走候选），发送前兜底剥掉。
   * 正常路径下正文里不会出现技能名——胶囊不参与序列化。
   */
  function stripSkillTokens(text: string): string {
    let out = text
    for (const n of picked.value) {
      out = out.replace(new RegExp('(^|\\s)/' + escapeRe(n) + '(?![^\\s/])', 'g'), '$1')
    }
    return out.replace(/[ \t]{2,}/g, ' ').trim()
  }

  return { slashItems, slashIdx, slashOpen, slashQuery, onSlashKey, slashHidden, stripSkillTokens }
}
