import { ref, watch, type Ref } from 'vue'

/**
 * 为对话框添加焦点陷阱（Focus Trap）：
 * - 打开时自动聚焦第一个可聚焦元素
 * - Tab/Shift+Tab 循环在对话框内元素之间
 * - Escape 关闭（由组件自身实现）
 * - 关闭时恢复之前聚焦的元素
 */
export function useFocusTrap(visible: Ref<boolean>, panelRef: Ref<HTMLElement | null>) {
  let previousActive: HTMLElement | null = null
  const firstFocusable = ref<HTMLElement | null>(null)

  function getFocusable(el: HTMLElement): HTMLElement[] {
    const selectors = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
    return Array.from(el.querySelectorAll<HTMLElement>(selectors))
  }

  watch(visible, (v) => {
    if (v) {
      previousActive = document.activeElement as HTMLElement
      // 等 DOM 渲染完成后聚焦
      requestAnimationFrame(() => {
        if (!panelRef.value) return
        const focusable = getFocusable(panelRef.value)
        if (focusable.length > 0) {
          firstFocusable.value = focusable[0]
          focusable[0].focus()
        } else {
          panelRef.value.focus()
        }
      })
    } else {
      // 恢复之前的焦点
      if (previousActive && typeof previousActive.focus === 'function') {
        previousActive.focus()
      }
      previousActive = null
      firstFocusable.value = null
    }
  })

  function onKeydown(e: KeyboardEvent) {
    if (!visible.value || !panelRef.value) return

    if (e.key === 'Escape') return // 由组件自行处理

    if (e.key === 'Tab') {
      const focusable = getFocusable(panelRef.value)
      if (focusable.length === 0) {
        e.preventDefault()
        return
      }

      const first = focusable[0]
      const last = focusable[focusable.length - 1]

      if (e.shiftKey) {
        if (document.activeElement === first) {
          e.preventDefault()
          last.focus()
        }
      } else {
        if (document.activeElement === last) {
          e.preventDefault()
          first.focus()
        }
      }
    }
  }

  return { onKeydown }
}
