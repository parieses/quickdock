import { onBeforeUnmount, onMounted, type Ref } from 'vue'
import type { EChartsType } from '../utils/echarts'

/**
 * 通用容器尺寸监听：尺寸变化时调 chart.resize()。
 *
 * 图表组件常在 v-show / 折叠面板里挂载，init 那一刻容器可能是 0 宽高；
 * ResizeObserver 在容器变为可见时会补一次 resize，避免留下空白画布。
 * 组件卸载时断连，防止对已 dispose 的实例调用 resize。
 */
export function useChartResize(
  container: Ref<HTMLElement | null>,
  getChart: () => EChartsType | null,
): void {
  let observer: ResizeObserver | null = null
  onMounted(() => {
    if (typeof ResizeObserver === 'undefined' || !container.value) return
    observer = new ResizeObserver(() => {
      getChart()?.resize()
    })
    observer.observe(container.value)
  })
  onBeforeUnmount(() => {
    observer?.disconnect()
    observer = null
  })
}
