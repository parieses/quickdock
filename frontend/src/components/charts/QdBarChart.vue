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
  /** 柱条最小高度 / 最大宽度（px）。最小高度用于给 0 值日留一条贴底短线，
   *  否则「哪天闲了」在图上完全消失。 */
  barMinHeight?: number
  barMaxWidth?: number
  /** 柱身不透明度：紧凑场景用半透明，免得一块实心色压过旁边的数字。 */
  barOpacity?: number
  /** 全 0 视为无数据（默认关：迷你图要靠 0 值短线表达空日子，不能被遮住）。 */
  zeroAsEmpty?: boolean
  axisNameX?: string
  axisNameY?: string
  valueFormatter?: (v: number) => string
  /** tooltip 首行标题：轴标签放不下完整日期时，由父组件给全量文本。 */
  tooltipTitle?: (index: number) => string
  /** 附加 tooltip 行（已 i18n 的 HTML 片段），由父组件提供，图表组件不拉数据。 */
  tooltipExtra?: (index: number) => string
  loading?: boolean
}>(), {
  height: 160,
  stacked: false,
  horizontal: false,
  showLegend: false,
  minimal: false,
  barMinHeight: 0,
  barMaxWidth: 28,
  barOpacity: 1,
  zeroAsEmpty: false,
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
const isEmpty = computed(() => {
  if (props.labels.length === 0 || props.series.length === 0) return true
  if (props.series.every((s) => s.values.length === 0)) return true
  return props.zeroAsEmpty && props.series.every((s) => s.values.every((v) => !v))
})

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
  // yAxis.name 默认画在轴顶端上方 nameGap 处，矮图里会压到最上面那档刻度。
  // 有轴名时把 grid.top 让开一行，并减少刻度档数，单位与数字才不会挤成一团。
  const hasAxisNameY = !props.minimal && !!props.axisNameY
  const valueAxis = {
    type: 'value',
    name: hasAxisNameY ? props.axisNameY : undefined,
    nameLocation: 'end',
    nameGap: 10,
    nameTextStyle: { color: c.textMuted, fontSize: 10 },
    splitNumber: 4,
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
      if (pc) return { value: v, itemStyle: { color: resolveChartColor(c, pc, i) } }
      // 空日子留一条浅色短线，和有量的柱子明确区分（重写前纯 CSS 柱条的语义）。
      // 用 borderLight 而不是 bgTertiary：实测深色主题下卡片底 #171b22 与
      // bgTertiary #1b202a 只差 4~5 级亮度，2px 短线整根融进背景，
      // 看上去像「柱子缺了两天」而不是「那两天没调用」。
      if (props.barMinHeight > 0 && !v) {
        return { value: v, itemStyle: { color: c.borderLight } }
      }
      return v
    })
    return {
      id: `qd-bar-${i}`,
      name: s.name,
      type: 'bar',
      data,
      stack: props.stacked ? (s.stack ?? 'total') : undefined,
      barMinHeight: props.barMinHeight,
      barMaxWidth: props.barMaxWidth,
      itemStyle: { color, opacity: props.barOpacity },
      emphasis: { focus: 'series', itemStyle: { opacity: 1 } },
    }
  })

  return {
    color: c.palette,
    textStyle: { color: c.textSecondary },
    grid: {
      left: props.minimal ? 0 : 8,
      right: props.minimal ? 0 : 12,
      top: props.minimal ? 0 : (props.showLegend ? 28 : 12) + (hasAxisNameY ? 14 : 0),
      bottom: props.minimal ? 0 : 8,
      containLabel: !props.minimal,
    },
    tooltip: {
      trigger: 'axis',
      // 迷你图（五六十像素高）里浮层比容器还高，不 confine 时它会顶出卡片，
      // 第一行直接被裁掉（实测 AI 用量的 tooltip 只剩「请求数」往下三行）。
      // 只在 minimal 时开：正常高度的图表留 overflow 放行，tooltip 出容器更宽松。
      confine: props.minimal,
      // 不挂 body：挂到 body 后定位要跨容器换算，窄卡里容易算到视口外「整个看不到」。
      // 改为留在图表容器（ECharts 默认，定位最稳），由容器 overflow:visible 放行，
      // 再用高 z-index 压住相邻卡片。pointer-events:none 免得它自己吃掉 hover 闪烁。
      extraCssText: 'z-index:2147483647;box-shadow:0 6px 20px rgba(0,0,0,.22);border-radius: var(--radius-sm);padding:6px 9px;line-height:1.6;white-space:nowrap;pointer-events:none;',
      axisPointer: { type: 'shadow', shadowStyle: { color: c.border } },
      backgroundColor: c.tooltipBg,
      borderColor: c.border,
      borderWidth: 1,
      textStyle: { color: c.textPrimary },
      formatter: (params: unknown) => {
        const arr = (Array.isArray(params) ? params : [params]) as TooltipParam[]
        if (!arr.length) return ''
        const heading = props.tooltipTitle?.(arr[0].dataIndex) ?? arr[0].name
        let html = `<div>${escapeHtml(heading)}</div>`
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
    () => props.barMinHeight,
    () => props.barMaxWidth,
    () => props.barOpacity,
    () => props.valueFormatter,
    () => props.tooltipTitle,
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
  /* tooltip 要能画出容器，不能裁；canvas 本身就是 inset:0，不需要靠 hidden 收边。 */
  overflow: visible;
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
