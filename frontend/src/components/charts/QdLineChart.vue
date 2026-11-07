<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { createChart, escapeHtml } from '../../utils/echarts'
import type { EChartsType } from '../../utils/echarts'
import { useChartTheme, resolveChartColor } from '../../composables/useChartTheme'
import type { ChartColorKey } from '../../composables/useChartTheme'
import { useChartResize } from '../../composables/useChartResize'

interface QdLineSeries {
  name: string
  /** [时间戳(ms), 数值][]，时间正序。 */
  points: [number, number][]
  color?: ChartColorKey
  area?: boolean
}
interface QdAnomaly {
  x: number
  y: number
}

const props = withDefaults(defineProps<{
  series: QdLineSeries[]
  height?: number | string
  showLegend?: boolean
  axisNameX?: string
  axisNameY?: string
  /** 超时阈值（Y 轴数值），渲染成 markLine。 */
  threshold?: number
  thresholdName?: string
  /** 异常点（如探测失败），渲染成醒目的独立点。 */
  anomalies?: QdAnomaly[]
  anomaliesName?: string
  dataZoom?: boolean
  valueFormatter?: (v: number) => string
  /** 附加 tooltip 行（已 i18n 的 HTML 片段），由父组件提供。 */
  tooltipExtra?: (x: number, y: number) => string
  loading?: boolean
}>(), {
  height: 220,
  showLegend: false,
  dataZoom: true,
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
  props.series.length === 0 || props.series.every((s) => s.points.length === 0),
)

function fmtValue(v: number): string {
  return props.valueFormatter ? props.valueFormatter(v) : String(v)
}

interface TooltipParam {
  seriesName: string
  value: [number, number] | number
  marker?: string
}

function buildOption() {
  const c = theme.value
  // 上千点：关动画、不画 symbol、lttb 降采样，避免每帧全量重排。
  const series: Record<string, unknown>[] = props.series.map((s, i) => {
    const color = resolveChartColor(c, s.color, i)
    return {
      id: `qd-line-${i}`,
      name: s.name,
      type: 'line',
      data: s.points,
      showSymbol: false,
      symbol: 'circle',
      sampling: 'lttb',
      animation: false,
      lineStyle: { width: 2, color },
      itemStyle: { color },
      areaStyle: s.area ? { color, opacity: 0.12 } : undefined,
      markLine:
        i === 0 && props.threshold !== undefined
          ? {
              silent: true,
              symbol: 'none',
              data: [{ yAxis: props.threshold, name: props.thresholdName }],
              lineStyle: { color: c.danger, type: 'dashed', width: 1 },
              label: {
                show: true,
                formatter: props.thresholdName ?? '',
                color: c.danger,
                position: 'insideEndTop',
              },
            }
          : undefined,
    }
  })

  if (props.anomalies?.length) {
    // 异常点复用 line series：不连线、只画点、silent 不进 tooltip，
    // 这样无需额外注册 ScatterChart / MarkPointComponent。
    series.push({
      id: 'qd-line-anomaly',
      name: props.anomaliesName ?? t('chartTooltipStatus'),
      type: 'line',
      data: props.anomalies.map((a) => [a.x, a.y]),
      showSymbol: true,
      symbol: 'circle',
      symbolSize: 8,
      lineStyle: { width: 0 },
      itemStyle: { color: c.danger },
      silent: true,
      animation: false,
      z: 5,
    })
  }

  return {
    color: c.palette,
    animation: false,
    textStyle: { color: c.textSecondary },
    grid: {
      left: 8, right: 12, top: props.showLegend ? 28 : 12,
      bottom: props.dataZoom ? 30 : 8, containLabel: true,
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: {
        type: 'cross',
        lineStyle: { color: c.borderLight },
        crossStyle: { color: c.borderLight },
      },
      backgroundColor: c.tooltipBg,
      borderColor: c.border,
      borderWidth: 1,
      textStyle: { color: c.textPrimary },
      formatter: (params: unknown) => {
        const arr = (Array.isArray(params) ? params : [params]) as TooltipParam[]
        const first = arr.find((p) => Array.isArray(p.value))
        if (!first) return ''
        const pair = first.value as [number, number]
        const ts = pair[0]
        const time = Number.isFinite(ts) ? new Date(ts).toLocaleString() : ''
        let html = `<div>${t('chartTooltipTime')}: ${escapeHtml(time)}</div>`
        for (const p of arr) {
          const v = Array.isArray(p.value) ? p.value[1] : p.value
          html += `<div>${p.marker ?? ''}${escapeHtml(p.seriesName)}: ${escapeHtml(fmtValue(Number(v)))}</div>`
        }
        if (props.tooltipExtra) html += props.tooltipExtra(ts, Number(pair[1]))
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
    xAxis: {
      type: 'time',
      name: props.axisNameX ?? t('chartAxisTime'),
      nameTextStyle: { color: c.textMuted },
      axisLabel: { color: c.textMuted, hideOverlap: true },
      axisLine: { lineStyle: { color: c.axisLine } },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      name: props.axisNameY ?? t('chartAxisLatency'),
      nameTextStyle: { color: c.textMuted },
      axisLabel: { color: c.textMuted, formatter: (v: number) => fmtValue(v) },
      axisLine: { lineStyle: { color: c.axisLine } },
      splitLine: { lineStyle: { color: c.grid } },
    },
    dataZoom: props.dataZoom
      ? [{ id: 'qd-line-zoom', type: 'inside', xAxisIndex: 0, filterMode: 'none' }]
      : [],
    series,
  }
}

function render() {
  if (!chart) return
  // replaceMerge 只替换 series：数据/主题刷新时保留用户当前的 dataZoom 缩放窗口。
  chart.setOption(buildOption(), { replaceMerge: ['series'] })
}

function resetZoom() {
  chart?.dispatchAction({ type: 'dataZoom', start: 0, end: 100 })
}

onMounted(() => {
  if (!canvasHost.value) return
  chart = createChart(canvasHost.value)
  render()
})
useChartResize(host, () => chart)
watch(
  [
    () => props.series,
    () => props.anomalies,
    () => props.threshold,
    () => props.thresholdName,
    () => props.anomaliesName,
    () => props.axisNameX,
    () => props.axisNameY,
    () => props.showLegend,
    () => props.dataZoom,
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
  <div ref="host" class="qd-chart">
    <div ref="canvasHost" class="qd-chart__canvas"></div>
    <div v-if="loading" class="qd-chart__state">{{ t('chartLoading') }}</div>
    <div v-else-if="isEmpty" class="qd-chart__state">{{ t('chartNoData') }}</div>
    <div v-if="dataZoom && !loading && !isEmpty" class="qd-chart__zoom">
      <span>{{ t('chartZoomHint') }}</span>
      <button type="button" @click="resetZoom">{{ t('chartResetZoom') }}</button>
    </div>
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
.qd-chart__zoom {
  position: absolute; right: var(--space-2); bottom: 2px; z-index: 2;
  display: flex; align-items: center; gap: var(--space-2);
  font-size: 10px; color: var(--color-text-muted);
}
.qd-chart__zoom button {
  padding: 1px 6px; font-size: 10px; font-family: inherit; cursor: pointer;
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
}
.qd-chart__zoom button:hover { color: var(--color-accent); border-color: var(--color-accent); }
</style>
