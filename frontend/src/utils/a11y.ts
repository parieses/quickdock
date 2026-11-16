import type { App, Directive } from 'vue'

/**
 * 键盘可达性与弹窗无障碍的全局能力。
 *
 * 为什么放在这里而不是每个组件各写一份：项目里有一批 `<div @click>` / `<span @click>`
 * 没有键盘语义，另有一批内联遮罩弹窗既没有焦点陷阱也没有 aria 标记。
 * 逐个改模板既容易漏，又会把同一段 Tab 循环复制到几十个文件里。
 *
 * 11-16 之后 `.modal-overlay` / `.modal-mask` / `.dl-modal-overlay` / `.qd-modal-overlay`
 * / `.ai-modal-overlay` / `.tool-modal-overlay` 这一批已经全部迁到 QdModal（自己带 v-dialog），
 * 白名单里对应条目随之删除 —— 留着只会让新写的同名遮罩悄悄绕过 v-dialog 又拿到一份陷阱。
 *
 * 与 `useFocusTrap` 的关系：那 14 个组件自己接了焦点陷阱、自己处理 Esc，
 * 本文件的白名单**刻意不含**它们的遮罩类名（.confirm-overlay 等），避免双重陷阱。
 *
 * Esc 用「延后一帧兜底」而不是抢注册：项目已有 20+ 处组件级 / 页面级 Esc 链
 * （CommandPalette / GitPage / DownloadPage / AIPage …），再加一层同步 Esc 会导致
 * 「按一次关两层」。这里等组件自己的链先跑完，若弹窗仍在 DOM 中，才模拟一次
 * 点击遮罩（等价于用户点空白处）把它关掉 —— 这样只补真正缺 Esc 的弹窗。
 *
 * Esc 兜底的两个显式开关（写在遮罩元素上，由弹窗自己控制，不抢注册）：
 *   · data-qd-no-esc 或 data-qd-esc="off"：给「点遮罩不关闭」的弹窗用。命中时
 *     本兜底只保留焦点陷阱、不合成点击，关闭交给弹窗自己的按钮 / 链路。
 *   · data-qd-busy（弹窗自身带）或弹窗内任一带 aria-busy="true" 的元素：
 *     异步提交 / 加载中禁止合成关闭，防请求半途把弹窗关掉、用户以为丢了输入。
 */

const FOCUSABLE = [
  'button:not([disabled])',
  'a[href]',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

/** 弹窗打开时优先聚焦表单控件，而不是常驻在右上角的关闭按钮。
 *  .challenge-mask（SshChallengeDialog）这类「打开即要输入」的弹窗尤其依赖它。
 *  hidden / disabled 不算；contenteditable 排除显式标 false 的容器。 */
const FORM_FOCUSABLE = [
  'input:not([type=hidden]):not([disabled])',
  'textarea:not([disabled])',
  'select:not([disabled])',
  '[contenteditable]:not([contenteditable="false"])',
].join(',')

function focusable(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => el.getClientRects().length > 0,
  )
}

/** 模块级浮层栈：Tab 循环只作用于最上层，嵌套弹窗不会互相抢焦点。 */
const openDialogs: HTMLElement[] = []

interface DialogState {
  onKey: (e: KeyboardEvent) => void
  previous: HTMLElement | null
}

const STATE = '__qdDialogState'

function attachDialog(el: HTMLElement) {
  if ((el as any)[STATE]) return
  if (!el.hasAttribute('role')) el.setAttribute('role', 'dialog')
  if (!el.hasAttribute('aria-modal')) el.setAttribute('aria-modal', 'true')

  const previous = document.activeElement as HTMLElement | null
  openDialogs.push(el)

  requestAnimationFrame(() => {
    const list = focusable(el)
    const form = Array.from(el.querySelectorAll<HTMLElement>(FORM_FOCUSABLE)).filter(
      (n) => n.getClientRects().length > 0,
    )
    const target = form[0] ?? list[0]
    if (target) target.focus()
    else {
      el.setAttribute('tabindex', '-1')
      el.focus()
    }
  })

  const onKey = (e: KeyboardEvent) => {
    if (openDialogs[openDialogs.length - 1] !== el) return
    if (e.key === 'Escape') {
      if (e.isComposing || e.keyCode === 229) return
      // 不 preventDefault、不 stopPropagation：先让组件自己的 Esc 链处理。
      // 下一帧弹窗还在，说明这一层没人管，才模拟点遮罩关闭（@click.self /
      // @mousedown.self 都会命中，因为合成事件的 target 就是遮罩本身）。
      // 是否真的合成，还受文件头那两个 opt-out / busy 开关约束。
      requestAnimationFrame(() => {
        if (!document.contains(el)) return
        // 点遮罩不关闭的弹窗：只保留焦点陷阱，不合成点击。
        if (el.hasAttribute('data-qd-no-esc') || el.getAttribute('data-qd-esc') === 'off') return
        // 异步提交 / 加载中别误关：弹窗自带忙标记，或内部有 aria-busy 元素。
        if (el.hasAttribute('data-qd-busy') ||
            el.matches('[aria-busy="true"]') ||
            el.querySelector('[aria-busy="true"]')) return
        el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
        el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
      return
    }
    if (e.key !== 'Tab') return
    const list = focusable(el)
    if (!list.length) {
      e.preventDefault()
      return
    }
    const first = list[0]
    const last = list[list.length - 1]
    const active = document.activeElement
    const outside = !el.contains(active)
    if (e.shiftKey && (active === first || outside)) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && (active === last || outside)) {
      e.preventDefault()
      first.focus()
    }
  }
  document.addEventListener('keydown', onKey, true)
  ;(el as any)[STATE] = { onKey, previous } as DialogState
}

function detachDialog(el: HTMLElement) {
  const state = (el as any)[STATE] as DialogState | undefined
  if (!state) return
  document.removeEventListener('keydown', state.onKey, true)
  delete (el as any)[STATE]
  const i = openDialogs.indexOf(el)
  if (i !== -1) openDialogs.splice(i, 1)
  // 关闭后把焦点还给触发它的元素，否则 Tab 会从 body 开头重新走。
  if (state.previous && document.contains(state.previous)) state.previous.focus()
}

/** 精确白名单：只认「会盖住内容、有明确关闭动作」的模态遮罩。
 *  刻意排除菜单背景（*-backdrop / *-menu）、加载态（install-overlay / sess-overlay）、
 *  图片与 HTML 预览（ai-img-viewer / ai-html-preview）——它们不是对话框。 */
const DIALOG_SELECTOR = [
  '.kb-overlay',
  '.cs-overlay',
  '.challenge-mask',
  '.hdd-overlay',
  '.ai-perm-modal',
  '.sfp-overlay',
  '.sfe-overlay',
  '.gen-overlay',
  '.prompt-dialog-overlay',
  '.note-editor-mask',
].join(',')

/** 给可点击但没有键盘语义的元素补 tabindex / role / Enter+Space 触发 click。 */
export const vKeyClick: Directive<HTMLElement> = {
  mounted(el) {
    if (el.matches('button, a[href], input, select, textarea, [tabindex]')) return
    el.setAttribute('tabindex', '0')
    if (!el.hasAttribute('role')) el.setAttribute('role', 'button')
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Enter' && e.key !== ' ') return
      if (e.isComposing || e.keyCode === 229) return
      e.preventDefault()
      el.click()
    }
    el.addEventListener('keydown', onKey)
    ;(el as any).__qdKeyClick = onKey
  },
  unmounted(el) {
    const onKey = (el as any).__qdKeyClick as ((e: KeyboardEvent) => void) | undefined
    if (onKey) el.removeEventListener('keydown', onKey)
    delete (el as any).__qdKeyClick
  },
}

/** 显式给单个遮罩挂焦点陷阱；新写的弹窗优先用这个指令，不必依赖白名单。 */
export const vDialog: Directive<HTMLElement> = {
  mounted: attachDialog,
  unmounted: detachDialog,
}

/**
 * 存量弹窗的兜底：只盯白名单里的遮罩出现/消失，自动补 aria、焦点陷阱与
 * 「延后一帧」的 Esc 兜底（见 attachDialog 的 onKey，不会抢组件自己的 Esc 链）。
 * 用 MutationObserver 而不是全局 click 代理，是因为 Vue 的 v-if 卸载时机与
 * 组件状态无关，观察 DOM 是唯一可靠入口。
 */
export function installA11y(app: App) {
  app.directive('key-click', vKeyClick)
  app.directive('dialog', vDialog)

  const sync = () => {
    const present = new Set(Array.from(document.querySelectorAll<HTMLElement>(DIALOG_SELECTOR)))
    for (const el of openDialogs.slice()) {
      if (!present.has(el) || !document.contains(el)) detachDialog(el)
    }
    for (const el of present) attachDialog(el)
  }

  const start = () => {
    // AI 流式输出时 DOM 变动很密，一帧内合并成一次扫描，避免每个 token 都全文档 querySelectorAll。
    let pending = false
    const observer = new MutationObserver(() => {
      if (pending) return
      pending = true
      requestAnimationFrame(() => {
        pending = false
        sync()
      })
    })
    observer.observe(document.body, { childList: true, subtree: true })
    // 首次同步：热重载或延迟挂载时，body 里可能已经有遮罩而没有后续 mutation。
    sync()
  }
  // module script 通常已解析完 body，但独立窗口 / 热重载不保证，留一道守卫。
  if (document.body) start()
  else document.addEventListener('DOMContentLoaded', start, { once: true })
}
