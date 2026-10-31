// useDragSort.ts — 原生 HTML5 拖拽排序的公共管道
//
// 为什么抽：场景栏与设置里的导航排序各自手写了一套 dragstart / dragover / drop，
// 逻辑几乎是同一段（preventDefault、dataTransfer 兜底、清态），但两处各写一遍 ——
// 其中一处「dragover 忘记 preventDefault 就永远收不到 drop」这类坑只会在新写的那份里重现。
//
// 边界：只管「拖谁 / 落到谁身上 / 算新顺序」，样式（dragging 透明度、落点指示线）
// 留在各自组件，因为两处视觉不同（场景栏整行淡出，设置页画顶部指示线）。
//
// 不引第三方库：两处列表都很短（场景通常 <20，导航 11 项），原生足够。
import { ref } from 'vue'

export interface DragSortOptions {
  /** 当前可拖拽项的顺序（搜索过滤时传过滤后的顺序，别传全量） */
  ids: () => string[]
  /** 拖拽结束后的新顺序（已按落点 splice 好，元素集合与传入时相同） */
  onReorder: (ids: string[]) => void
}

export function useDragSort(opts: DragSortOptions) {
  const dragId = ref('')
  // 当前悬停的落点下标，仅供模板画「落点指示线」；不参与顺序计算
  const dragOverIndex = ref(-1)

  function onDragStart(id: string, e: DragEvent, index = -1) {
    dragId.value = id
    dragOverIndex.value = index
    e.dataTransfer?.setData('text/plain', id)
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
  }

  function onDragOver(index: number, e: DragEvent) {
    // 必须 preventDefault，否则浏览器不认这里是合法落点、根本不派发 drop
    e.preventDefault()
    dragOverIndex.value = index
  }

  function onDrop(targetId: string, e: DragEvent) {
    e.preventDefault()
    // dragId 正常由 dragstart 写入；跨窗口/异常情况下退回 dataTransfer
    const id = dragId.value || e.dataTransfer?.getData('text/plain') || ''
    dragId.value = ''
    dragOverIndex.value = -1
    if (!id || id === targetId) return

    const ids = opts.ids()
    // 两个下标都必须**在移除之前**算：移除后 from 之后的下标整体左移，
    // 先移除再取 to 会让「向下拖一格」变成原地不动（落点算到了前一个位置）。
    const from = ids.indexOf(id)
    const to = ids.indexOf(targetId)
    if (from < 0 || to < 0) return
    ids.splice(from, 1)
    // 移除后目标下标的变化刚好抵消落点语义，两个方向都可以直接 splice(to)：
    //   向下拖（from < to）：目标左移一格，插在它后面 = 原下标 to
    //   向上拖（from > to）：目标不动，插在它前面   = 原下标 to
    ids.splice(to, 0, id)
    opts.onReorder(ids)
  }

  function onDragEnd() {
    dragId.value = ''
    dragOverIndex.value = -1
  }

  return { dragId, dragOverIndex, onDragStart, onDragOver, onDrop, onDragEnd }
}
