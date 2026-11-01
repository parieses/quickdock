<script setup lang="ts">
/**
 * 带「技能胶囊」的输入框：技能是**行内胶囊**，嵌在文字中间，不单独占一行。
 *
 * 为什么不是 textarea：textarea 里只能有纯文本，胶囊要么退回成 `/名字`（用户已经
 * 否掉了），要么只能放到框外单独一行（同样被否掉）。要在「文字流里」放一个原子块，
 * 只能是 contenteditable。
 *
 * 几条不变量（改之前先看，否则很容易把光标做坏）：
 * - **输入方向是单向的**：DOM → 文本，再把文本 emit 出去。输入过程中绝不回写
 *   `innerHTML`，所以光标不会被 Vue 重渲染甩到行尾。
 * - **只在外部真的改了值时才重建**（发送后清空、点示例填词、切会话）：用 `lastText`
 *   区分「自己刚发出去的」与「外面塞进来的」。
 * - **胶囊是 contenteditable=false 的原子节点**：光标贴着胶囊时按退格/Delete 会整颗删掉，
 *   删完 emit 一次 `update:skills`，父组件的 picked 列表跟着变——不强制用鼠标点 ×。
 *   WebView2 的 plaintext-only 下原生删除会失灵，这里在 keydown 里手动处理边界。
 * - `contenteditable="plaintext-only"`：粘贴带格式的文本不会把富文本样式带进输入框，
 *   换行仍按块/`<br>` 处理（序列化时统一折成 `\n`）。
 */
import { onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  /** 纯文本（不含胶囊）：与原来的 textarea 同一个值，父组件不需要知道胶囊的存在 */
  modelValue: string
  placeholder: string
  disabled?: boolean
  /** 高度上下限（px）：落地页高一些，聊天区矮一些 */
  minHeight?: number
  maxHeight?: number
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'update:skills', v: string[]): void
  (e: 'key', ev: KeyboardEvent): void
  (e: 'paste', ev: ClipboardEvent): void
}>()

const { t } = useI18n()

const el = ref<HTMLDivElement | null>(null)
const isEmpty = ref(true)
// 最近一次由本组件抛出去的文本：用来区分「自己输入」与「外部赋值」
let lastText = ''
// 上一次抛出去的技能名（JSON）：内容没变就不重复 emit，省得面板每个按键都重渲染
let lastSkillsJson = '[]'

// —— 序列化：DOM → 文本 + 技能名 ——

function serialize(): { text: string; skills: string[] } {
  const root = el.value
  const skills: string[] = []
  if (!root) return { text: '', skills }
  let text = ''
  const walk = (node: Node) => {
    for (const child of Array.from(node.childNodes)) {
      if (child.nodeType === Node.TEXT_NODE) {
        text += child.textContent || ''
        continue
      }
      if (!(child instanceof HTMLElement)) continue
      // 胶囊不参与正文：发送时只把名字带过去（正文由模型自己 file_read）
      if (child.dataset.skill) {
        skills.push(child.dataset.skill)
        continue
      }
      if (child.tagName === 'BR') {
        text += '\n'
        continue
      }
      // plaintext-only 下换行会变成 <div>/<p>：块边界折成 \n
      if ((child.tagName === 'DIV' || child.tagName === 'P') && text && !text.endsWith('\n')) text += '\n'
      walk(child)
    }
  }
  walk(root)
  return { text: text.replace(/\u00a0/g, ' '), skills }
}

function emitAll(force = false) {
  const { text, skills } = serialize()
  lastText = text
  isEmpty.value = text === '' && skills.length === 0
  emit('update:modelValue', text)
  const json = JSON.stringify(skills)
  // 内容没变就不重复 emit（每个按键都发一次会让面板白重渲染）；
  // force 用于重建：DOM 刚被清空，技能一定没了，必须让父组件知道。
  if (force || json !== lastSkillsJson) {
    lastSkillsJson = json
    emit('update:skills', skills)
  }
}

function onInput() {
  emitAll()
}

// —— 外部改值：重建 DOM（清空 / 填示例 / 切会话） ——

function rebuild(text: string) {
  const root = el.value
  if (!root) return
  // 重建会把选区连同 DOM 一起丢掉：本来就在输入框里打字的话（比如刚点完 @ 引用，
  // 父组件把 token 从正文里摘掉），重建后要把光标放回末尾，否则用户得再点一下。
  const hadFocus = document.activeElement === root
  root.textContent = ''
  if (text) {
    text.split('\n').forEach((line, i) => {
      if (i > 0) root.appendChild(document.createElement('br'))
      if (line) root.appendChild(document.createTextNode(line))
    })
  }
  // 重建会顺手清掉胶囊：一并把「已选技能」同步回去，别让面板还标着「已选」
  emitAll(true)
  if (hadFocus) placeCaretAtEnd()
}

watch(() => props.modelValue, (v) => {
  if (v === lastText) return
  rebuild(v)
})

onMounted(() => rebuild(props.modelValue))

// —— 胶囊 ——

function domSkills(): string[] {
  return Array.from(el.value?.querySelectorAll<HTMLElement>('[data-skill]') || [])
    .map((n) => n.dataset.skill || '')
    .filter(Boolean)
}

function buildChip(name: string): HTMLElement {
  const chip = document.createElement('span')
  chip.className = 'ai-rich-chip'
  chip.contentEditable = 'false'
  chip.dataset.skill = name
  chip.title = name

  const icon = document.createElement('span')
  icon.className = 'ai-rich-chip-icon'
  icon.textContent = '✦'

  const label = document.createElement('span')
  label.className = 'ai-rich-chip-name'
  label.textContent = name

  const del = document.createElement('button')
  del.type = 'button'
  del.className = 'ai-rich-chip-x'
  del.textContent = '×'
  del.title = t('delete')
  // mousedown 就收掉，避免拖选/双击把胶囊里的文字选起来
  del.addEventListener('mousedown', (e) => {
    e.preventDefault()
    e.stopPropagation()
    removeChip(name)
  })

  chip.append(icon, label, del)
  return chip
}

function placeCaretAfter(node: Node) {
  const sel = window.getSelection()
  if (!sel) return
  const r = document.createRange()
  r.setStartAfter(node)
  r.collapse(true)
  sel.removeAllRanges()
  sel.addRange(r)
}

function placeCaretAtEnd() {
  const root = el.value
  if (!root) return
  root.focus()
  const sel = window.getSelection()
  if (!sel) return
  const r = document.createRange()
  r.selectNodeContents(root)
  r.collapse(false)
  sel.removeAllRanges()
  sel.addRange(r)
}

/** 摘掉末尾正在打字的 `/query`：它是「我要找哪个技能」的检索词，不是正文 */
function stripTailToken(): boolean {
  const root = el.value
  if (!root) return false
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  // 只记「最后一个非空文本节点」：plaintext-only 在交互后常在末尾残留空文本节点，
  // 若取「最后一个遍历到的」会拿到空节点，导致 /query 漏删（胶囊前仍残留 /）。
  let last: Text | null = null
  let n: Node | null
  while ((n = walker.nextNode())) {
    if ((n as Text).data.length > 0) last = n as Text
  }
  if (!last) return false
  const m = /(^|\s)\/([^\s/]*)$/.exec(last.data)
  if (!m) return false
  last.data = last.data.slice(0, last.data.length - m[2].length - 1)
  // 删空了就摘掉这个节点，避免空文本节点累积、下次又来干扰
  if (last.data.length === 0) last.remove()
  return true
}

function removeChip(name: string) {
  const root = el.value
  if (!root) return
  for (const chip of Array.from(root.querySelectorAll<HTMLElement>('[data-skill]'))) {
    if (chip.dataset.skill !== name) continue
    // 胶囊后面那个占位空格一起删，否则会留下「   」双空格
    const next = chip.nextSibling
    if (next && next.nodeType === Node.TEXT_NODE && /^[ \u00a0]$/.test(next.textContent || '')) next.remove()
    chip.remove()
  }
  emitAll()
  // 点 × 之后最自然的下一步是接着打字，别让焦点留在被删掉的按钮上
  root.focus()
}

function insideChip(node: Node | null): boolean {
  if (!node) return false
  const elNode = node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as HTMLElement)
  return !!elNode?.closest?.('[data-skill]')
}

function insertChipAtCaret(chip: HTMLElement) {
  const root = el.value
  if (!root) return
  const sel = window.getSelection()
  const inRoot = !!sel && sel.rangeCount > 0 && !!sel.anchorNode
    && root.contains(sel.anchorNode) && !insideChip(sel.anchorNode)
  if (!inRoot) {
    root.appendChild(chip)
    const space = document.createTextNode(' ')
    root.appendChild(space)
    placeCaretAfter(space)
    return
  }
  const range = sel!.getRangeAt(0)
  // 有选区就落到选区末尾：选中一段话再点技能，不该把那段话删掉
  if (!range.collapsed) range.collapse(false)
  range.insertNode(chip)
  // 胶囊后面必须留一个空格，否则光标没有落点，下一个字会跑到胶囊前面去
  const space = document.createTextNode(' ')
  chip.after(space)
  placeCaretAfter(space)
}

/**
 * 选中/取消一个技能（对外由 + 面板与 / 候选调用）。
 *
 * 已在正文里 → 摘掉（同一个入口既加又减，不用再记「上次点的是哪个」）；
 * 否则插到光标处：末尾正打着的 `/query` 先摘掉，再插胶囊 + 一个空格。
 */
function insertSkill(name: string) {
  if (props.disabled) return
  if (!el.value) return
  if (domSkills().includes(name)) {
    removeChip(name)
    return
  }
  if (stripTailToken()) placeCaretAtEnd()
  insertChipAtCaret(buildChip(name))
  emitAll()
  // 插入时选区已经落在胶囊后面，这里只是把焦点要回来（点 + 面板时焦点在按钮上）
  el.value?.focus()
}

defineExpose({
  // 聚焦并落到末尾：调用方（点示例、新建任务）要的是「接着打字」，不是「从头开始」
  focus: () => placeCaretAtEnd(),
  insertSkill,
})

// —— 键盘删除（退格 / Delete）——
//
// WebView2 的 contenteditable="plaintext-only" 有两个坑，都得在这里手动兜底：
//   1. 原生「删不动」—— 尤其删胶囊（contenteditable=false 原子节点）以及某些边界；
//   2. 光标经常被表示为**元素/根容器节点**而非文本节点，所以「只看文本节点」的删除判断
//      会直接失效（这也是之前「新建任务删不掉 / 聊天要按两次」的根因）。
//
// 这里统一按「光标紧邻的前/后节点」来判定要删什么：文本就删那个字符，胶囊就整颗摘掉。
// 输入法组合（isComposing / keyCode 229）一律放行给原生，绝不干预，否则拼音回车会被吞。

function isChip(n: Node | null | undefined): n is HTMLElement {
  return !!n && n.nodeType === Node.ELEMENT_NODE && !!(n as HTMLElement).dataset?.skill
}

const SPACE_RE = /^[ \u00a0]$/

/**
 * 返回光标在 `forward`（Delete=之后 / Backspace=之前）方向上紧邻的那个节点，以及要删的字符下标：
 * - `{ node: Text, charIndex }`：删该文本节点的第 charIndex 个字符；
 * - `{ node: 胶囊, charIndex: -1 }`：整颗删该胶囊。
 * 找不到（已在最边界）返回 null。会跳过 <br>，并把「胶囊 + 尾随空格」当成一个整体（退格时优先删胶囊）。
 */
function neighbor(forward: boolean): { node: Node; charIndex: number } | null {
  const sel = window.getSelection()
  const root = el.value
  if (!sel || !sel.isCollapsed || sel.rangeCount === 0 || !root) return null
  const range = sel.getRangeAt(0)
  let node: Node | null = range.startContainer
  let offset = range.startOffset
  const TEXT = Node.TEXT_NODE

  // 1) 光标在文本节点内：先看「本节点内」有没有可删的字符
  if (node.nodeType === TEXT) {
    const tn = node as Text
    if (forward) {
      if (offset < tn.length) return { node: tn, charIndex: offset }
      node = tn.nextSibling
    } else {
      if (offset > 0) return { node: tn, charIndex: offset - 1 }
      node = tn.previousSibling
    }
  } else {
    // 2) 光标在元素/根容器内：offset 是子节点下标，取前/后那个子节点
    const kids = (node as Element).childNodes
    node = forward ? kids[offset] : kids[offset - 1]
  }

  // 3) 跨节点：沿方向找到第一个文本或胶囊（跳过 <br> 和空文本）
  while (node) {
    if (node.nodeType === TEXT) {
      const tn = node as Text
      // 退格时，若紧挨着的是胶囊的尾随空格，直接把胶囊一并删掉，少按一次
      if (!forward && SPACE_RE.test(tn.textContent || '') && isChip(tn.previousSibling)) {
        return { node: tn.previousSibling as HTMLElement, charIndex: -1 }
      }
      if (tn.length === 0) { node = forward ? tn.nextSibling : tn.previousSibling; continue }
      return { node: tn, charIndex: forward ? 0 : tn.length - 1 }
    }
    if (isChip(node)) return { node, charIndex: -1 }
    if (node.nodeName === 'BR') { node = forward ? node.nextSibling : node.previousSibling; continue }
    node = forward ? node.nextSibling : node.previousSibling
  }
  return null
}

/** 整颗删除一枚胶囊，并把光标放到胶囊原本占据的空隙处（之前/之后那段文本边上）。 */
function deleteChip(chip: HTMLElement) {
  const root = el.value
  if (!root) return
  const prev = chip.previousSibling
  const tail = chip.nextSibling
  const hasTail = !!tail && tail.nodeType === Node.TEXT_NODE && SPACE_RE.test(tail.textContent || '')
  const after = hasTail ? tail!.nextSibling : chip.nextSibling

  chip.remove()
  if (hasTail) tail!.remove()

  const sel = window.getSelection()
  if (sel) {
    sel.removeAllRanges()
    const r = document.createRange()
    if (prev && prev.nodeType === Node.TEXT_NODE) {
      const tn = prev as Text
      r.setStart(tn, tn.length)
    } else if (prev && prev.nodeType === Node.ELEMENT_NODE && root.contains(prev)) {
      r.setStartAfter(prev)
    } else if (after && after.nodeType === Node.TEXT_NODE) {
      r.setStart(after as Text, 0)
    } else if (after && after.nodeType === Node.ELEMENT_NODE && root.contains(after)) {
      r.setStartBefore(after)
    } else {
      r.selectNodeContents(root)
      r.collapse(false)
    }
    r.collapse(true)
    sel.addRange(r)
  }
  emitAll()
  root.focus()
}

/** 真正的删除：返回 true 表示已处理（调用方应 preventDefault）。 */
function handleDelete(forward: boolean): boolean {
  const nb = neighbor(forward)
  if (!nb) return false
  if (isChip(nb.node)) {
    deleteChip(nb.node)
    return true
  }
  const tn = nb.node as Text
  const idx = nb.charIndex
  if (idx < 0 || idx >= tn.length) return false
  tn.deleteData(idx, 1)
  const sel = window.getSelection()
  if (sel) {
    const r = document.createRange()
    r.setStart(tn, idx) // 删掉 idx 处字符后，后面的字符左移，光标落在 idx 即可
    r.collapse(true)
    sel.removeAllRanges()
    sel.addRange(r)
  }
  return true
}

/**
 * 在光标处插入一段纯文本（用于空格兜底，以及将来可能的其它字符插入）。
 * 兼容三种光标位置：文本节点内（split 后插入）、元素/根容器内（按 child 下标插入）。
 * 若当前有选区，先删掉选区（空格替换选区是合理行为）。
 */
function insertTextAtCaret(str: string) {
  const sel = window.getSelection()
  const root = el.value
  if (!sel || sel.rangeCount === 0 || !root) return
  const range = sel.getRangeAt(0)
  range.deleteContents() // 有选中则删掉；无选中是 no-op
  const tn = document.createTextNode(str)
  range.insertNode(tn) // 文本节点内会自动 split 后插入；元素内插到 kids[offset] 之前
  const r2 = document.createRange()
  r2.setStartAfter(tn)
  r2.collapse(true)
  sel.removeAllRanges()
  sel.addRange(r2)
}

function onKeyDown(e: KeyboardEvent) {
  if ((e.key === 'Backspace' || e.key === 'Delete') && !e.isComposing && e.keyCode !== 229) {
    if (handleDelete(e.key === 'Delete')) {
      e.preventDefault()
      emitAll()
      return
    }
  }
  // WebView2 的 plaintext-only 下，空格（空元素 / 行首 / 连续空格）常被原生吞掉，
  // 表现为「打不进空格」。这里在非组词态手动插入一个空格字符并阻止原生（避免重复）。
  // 组词态（中文输入法选词，空格是上屏键）一律放行给原生，绝不干预。
  if (e.key === ' ' && !e.isComposing && e.keyCode !== 229
      && !e.ctrlKey && !e.metaKey && !e.altKey) {
    e.preventDefault()
    insertTextAtCaret(' ')
    emitAll()
    return
  }
  emit('key', e)
}
</script>

<template>
  <div
    ref="el"
    class="ai-rich"
    :class="{ 'is-empty': isEmpty, 'is-disabled': props.disabled }"
    :contenteditable="props.disabled ? 'false' : 'plaintext-only'"
    :data-ph="props.placeholder"
    spellcheck="false"
    autocapitalize="off"
    autocorrect="off"
    :style="{ minHeight: (props.minHeight || 44) + 'px', maxHeight: (props.maxHeight || 200) + 'px' }"
    role="textbox"
    aria-multiline="true"
    @input="onInput"
    @keydown="onKeyDown"
    @paste="emit('paste', $event)"
  ></div>
</template>

<style scoped>
.ai-rich {
  width: 100%;
  overflow-y: auto;
  padding: 0;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text-primary);
  font-size: 13px;
  font-family: inherit;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  cursor: text;
}

.ai-rich.is-disabled { opacity: 0.6; cursor: not-allowed; }

/* 占位提示用伪元素：contenteditable 没有 placeholder 属性。
   撑满整行的空格提示放在有胶囊时不显示（那时已有内容，isEmpty 为 false）。 */
.ai-rich.is-empty::before {
  content: attr(data-ph);
  color: var(--color-text-disabled);
  pointer-events: none;
}
</style>
