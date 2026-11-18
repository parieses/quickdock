/**
 * 模态弹框的键盘焦点约束：打开时移入、Tab 圈在框内、关闭时归还。
 *
 * 为什么单独抽出来：轨迹抽屉与上下文详情都是 role="dialog" + aria-modal，
 * 之前只有 Esc 与遮罩，Tab 能跑到弹框背后的页面——读屏用户会「看见」一个
 * 并不在焦点上的界面。两个弹框各写一份必然漂移，这里收成一处。
 *
 * 用法：
 *   const dialogEl = ref<HTMLElement | null>(null)
 *   useDialogFocus(() => props.open, dialogEl)
 *   // 模板：<section ref="dialogEl" role="dialog" ...>
 */
import { onBeforeUnmount, watch, type Ref } from 'vue'

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"]), [contenteditable="true"]'

export function useDialogFocus(isOpen: () => boolean, dialogEl: Ref<HTMLElement | null>) {
  let lastFocused: HTMLElement | null = null

  function onKeydown(e: KeyboardEvent) {
    if (!isOpen() || e.key !== 'Tab') return
    const root = dialogEl.value
    if (!root) return
    const items = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
      (el) => el.offsetParent !== null || el === document.activeElement,
    )
    if (!items.length) {
      // 框内没有可聚焦元素时别让 Tab 逃逸到背景页。
      e.preventDefault()
      return
    }
    const first = items[0]
    const last = items[items.length - 1]
    const active = document.activeElement
    if (e.shiftKey && (active === first || !root.contains(active))) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && (active === last || !root.contains(active))) {
      e.preventDefault()
      first.focus()
    }
  }

  watch(
    isOpen,
    (open) => {
      if (open) {
        // 记住触发者，关闭时还回去（通常是那个打开弹框的按钮）。
        lastFocused = document.activeElement as HTMLElement | null
        document.addEventListener('keydown', onKeydown, true)
        // 等 Teleport 的 DOM 落地后再移入，同步查还拿不到 ref。
        requestAnimationFrame(() => {
          const root = dialogEl.value
          if (!root) return
          const target = root.querySelector<HTMLElement>(FOCUSABLE)
          ;(target ?? root).focus()
        })
      } else {
        document.removeEventListener('keydown', onKeydown, true)
        // 归还焦点：触发元素可能已被卸载（切会话），focus 一个 detached 节点无害。
        lastFocused?.focus()
        lastFocused = null
      }
    },
    { immediate: true },
  )

  onBeforeUnmount(() => {
    document.removeEventListener('keydown', onKeydown, true)
  })
}
