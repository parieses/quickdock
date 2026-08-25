import { computed, ref, shallowRef, type ComputedRef, type Ref } from 'vue'
import { useFloating, autoUpdate, offset, flip, shift, type Placement, type Strategy } from '@floating-ui/vue'

export interface FloatMenuOptions {
  /** 浮层相对锚点的位置，右键菜单默认右下（right-start），普通浮层可传 'top' / 'bottom' */
  placement?: Placement
  /** 与锚点的像素偏移，默认 0 */
  offset?: number | { mainAxis?: number; crossAxis?: number }
  /** 定位策略，默认 fixed（随滚动即时跟随） */
  strategy?: Strategy
}

/**
 * 通用浮层定位：沿锚点（真实元素 / 鼠标坐标 / 选区矩形）放置，
 * 自动防溢出（shift）与空间不足翻转（flip）。
 */
export function useFloatMenu(opts: FloatMenuOptions = {}) {
  const placement = opts.placement ?? 'right-start'
  const strategy = opts.strategy ?? 'fixed'
  const off = opts.offset ?? 0

  // 锚点矩形；rect 存在 = 浮层可见
  const rect = shallowRef<{ x: number; y: number; width: number; height: number } | null>(null)
  const visible = computed(() => rect.value !== null)

  // 虚拟元素：把坐标/矩形包装成 floating-ui 的 reference
  const reference = computed(() => {
    const r = rect.value
    if (!r) return null
    return {
      // floating-ui 只读 left/top/width/height/right/bottom
      getBoundingClientRect: () => ({
        left: r.x, top: r.y, width: r.width, height: r.height,
        right: r.x + r.width, bottom: r.y + r.height,
        x: r.x, y: r.y,
        toJSON: () => ({}),
      }),
    }
  })

  const floatingRef: Ref<HTMLElement | null> = ref(null)
  const { floatingStyles, update } = useFloating(reference as ComputedRef, floatingRef, {
    placement,
    strategy,
    middleware: [offset(off), flip({ padding: 8, crossAxis: true, fallbackAxisSideDirection: 'start' }), shift({ padding: 8 })],
    whileElementsMounted: autoUpdate,
  })

  /** 在鼠标坐标处显示（右键菜单） */
  function showAt(x: number, y: number) {
    rect.value = { x, y, width: 0, height: 0 }
  }

  /** 跟随某个 DOM 矩形显示（如选区范围） */
  function showRect(r: DOMRect) {
    rect.value = { x: r.left, y: r.top, width: r.width, height: r.height }
  }

  /** 更新已显示浮层的锚点（高频移动场景，如图表 tooltip）；原地改字段避免重建对象 */
  function updateAt(x: number, y: number) {
    const cur = rect.value
    if (!cur) return
    cur.x = x
    cur.y = y
    update()
  }

  function hide() {
    rect.value = null
  }

  return { visible, floatingRef, floatingStyles, showAt, showRect, updateAt, hide }
}