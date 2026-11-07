<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { createChart, escapeHtml } from '../../utils/echarts'
import type { EChartsType } from '../../utils/echarts'
import { useChartTheme, resolveChartColor } from '../../composables/useChartTheme'
import type { ChartColorKey } from '../../composables/useChartTheme'
import { useChartResize } from '../../composables/useChartResize'

interface QdBarSeries {
  name: string
  values: number[]
  color?: ChartColorKey
  stack?: string
  /** 逐点覆盖颜色（如「当天有失败」整根染红）。 */
  pointColors?: (ChartColorKey | undefined)[]
}

const props = withDefaults(defineProps<{
  labels: string[]
  series: QdBarSeries[]
  height?: number | string
  /** 图表容器的原生 title（悬停提示），由父组件用 i18n 传入。 */
  title?: string
  stacked?: boolean
  horizontal?: boolean
  showLegend?: boolean
  /** 迷你模式：隐藏坐标轴/网格，只画柱（用于顶部统计条这类紧凑场景）。 */
  minimal?: boolean
  axisNameX?: string
  axisNameY?: string
  valueFormatter?: (v: number) => string
  /** 附加 tooltip 行（已 i18n 的 HTML 片段），由父组件提供，图表组件不拉数据。 */
  tooltipExtra?: (index: number) => string
  loading?: boolean
}>(), {
  height: 160,
  stacked: false,
  horizontal: false,
  showLegend: false,
  minimal: false,
  loading: false,
})

const { t } = useI18n()
const theme = useChartTheme()
const host = ref<HTMLElement | null>(null)
const canvasHost = ref<HTMLElement | null>(null)
let chart: EChartsType | null = null

/** 运行时高度走 CSS v-bind()，模板里不写内联 style。 */
const heightCss = computed(() =>
  typeof props.height === 'number' ? `${props.height}px` : props.height,
)
const isEmpty = computed(() =>
  props.labels.length === 0 ||
  props.series.length === 0 ||
  props.series.every((s) => s.values.length === 0),
)

function fmtValue(v: number): string {
  return props.valueFormatter ? props.valueFormatter(v) : String(v)
}

interface TooltipParam {
  name: string
  seriesName: string
  value: number | { value: number }
  marker?: string
  dataIndex: number
}

function buildOption() {
  const c = theme.value
  const valueAxis = {
    type: 'value',
    name: props.minimal ? undefined : props.axisNameY,
    nameTextStyle: { color: c.textMuted },
    axisLabel: props.minimal
      ? { show: false }
      : { color: c.textMuted, formatter: (v: number) => fmtValue(v) },
    axisLine: { show: !props.minimal, lineStyle: { color: c.axisLine } },
    splitLine: { show: !props.minimal, lineStyle: { color: c.grid } },
  }
  const categoryAxis = {
    type: 'category',
    name: props.minimal ? undefined : props.axisNameX,
    nameTextStyle: { color: c.textMuted },
    data: props.labels,
    axisLabel: { show: !props.minimal, color: c.textMuted, hideOverlap: true },
    axisLine: { show: !props.minimal, lineStyle: { color: c.axisLine } },
    axisTick: { show: false },
  }
  const series = props.series.map((s, i) => {
    const color = resolveChartColor(c, s.color, i)
    const data = s.values.map((v, j) => {
      const pc = s.pointColors?.[j]
      return pc ? { value: v, itemStyle: { color: resolveChartColor(c, pc, i) } } : v
    })
    return {
      id: `qd-bar-${i}`,
      name: s.name,
      type: 'bar',
      data,
      stack: props.stacked ? (s.stack ?? 'total') : undefined,
      barMaxWidth: 28,
      itemStyle: { color },
      emphasis: { focus: 'series' },
    }
  })

  return {
    color: c.palette,
    textStyle: { color: c.textSecondary },
    grid: {
      left: props.minimal ? 0 : 8,
      right: props.minimal ? 0 : 12,
      top: props.minimal ? 0 : (props.showLegend ? 28 : 12),
      bottom: props.minimal ? 0 : 8,
      containLabel: !props.minimal,
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow', shadowStyle: { color: c.border } },
      backgroundColor: c.tooltipBg,
      borderColor: c.border,
      borderWidth: 1,
      textStyle: { color: c.textPrimary },
      formatter: (params: unknown) => {
        const arr = (Array.isArray(params) ? params : [params]) as TooltipParam[]
        if (!arr.length) return ''
        let html = `<div>${escapeHtml(arr[0].name)}</div>`
        for (const p of arr) {
          const v = typeof p.value === 'object' ? p.value.value : p.value
          html += `<div>${p.marker ?? ''}${escapeHtml(p.seriesName)}: ${escapeHtml(fmtValue(v))}</div>`
        }
        if (props.tooltipExtra) html += props.tooltipExtra(arr[0].dataIndex)
        return html
      },
    },
    legend: props.showLegend
      ? {
          show: true,
          top: 0,
          textStyle: { color: c.textSecondary },
          data: props.series.map((s) => s.name),
        }
      : { show: false },
    xAxis: props.horizontal ? valueAxis : categoryAxis,
    yAxis: props.horizontal ? categoryAxis : valueAxis,
    series,
  }
}

function render() {
  if (!chart) return
  chart.setOption(buildOption(), true)
}

onMounted(() => {
  if (!canvasHost.value) return
  chart = createChart(canvasHost.value)
  render()
})
useChartResize(host, () => chart)
watch(
  [
    () => props.labels,
    () => props.series,
    () => props.axisNameX,
    () => props.axisNameY,
    () => props.stacked,
    () => props.horizontal,
    () => props.showLegend,
    () => props.minimal,
    () => props.valueFormatter,
    () => props.tooltipExtra,
    theme,
  ],
  () => render(),
)
onBeforeUnmount(() => {
  chart?.dispose()
  chart = null
})
</script>

<template>
  <div ref="host" class="qd-chart" :title="title">
    <div ref="canvasHost" class="qd-chart__canvas"></div>
    <div v-if="loading" class="qd-chart__state">{{ t('chartLoading') }}</div>
    <div v-else-if="isEmpty" class="qd-chart__state">{{ t('chartNoData') }}</div>
  </div>
</template>

<style scoped>
.qd-chart {
  position: relative;
  width: 100%;
  height: v-bind(heightCss);
  border-radius: var(--radius-md);
  overflow: hidden;
}
.qd-chart__canvas { position: absolute; inset: 0; }
.qd-chart__state {
  position: absolute; inset: 0; z-index: 1;
  display: flex; align-items: center; justify-content: center;
  font-size: 11px; color: var(--color-text-muted);
  background: var(--color-bg-secondary);
  border-radius: var(--radius-md);
}
</style>
