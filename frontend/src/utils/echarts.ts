/**
 * ECharts 按需装配（Tree-shakable 入口）。
 *
 * 只从 echarts/core 装配、只 use() 本项目真正用到的图表 / 组件 / 渲染器，
 * 严禁 `import * as echarts from 'echarts'`（会把整包打进产物）。
 * 具名导出以 node_modules/echarts@6.1.0 的 types/dist/*.d.ts 校对为准：
 *   charts.d.ts      -> BarChart / LineChart
 *   components.d.ts  -> GridComponent / TooltipComponent / LegendComponent /
 *                       DataZoomComponent / MarkLineComponent
 *   renderers.d.ts   -> CanvasRenderer
 *   core.d.ts        -> init / use / EChartsType / EChartsInitOpts / ComposeOption
 */
import { init, use } from 'echarts/core'
import { BarChart, LineChart } from 'echarts/charts'
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DataZoomComponent,
  MarkLineComponent,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsType, EChartsInitOpts, ComposeOption } from 'echarts/core'
import type { BarSeriesOption, LineSeriesOption } from 'echarts/charts'
import type {
  GridComponentOption,
  TooltipComponentOption,
  LegendComponentOption,
  DataZoomComponentOption,
  MarkLineComponentOption,
} from 'echarts/components'

use([
  BarChart, LineChart,
  GridComponent, TooltipComponent, LegendComponent,
  DataZoomComponent, MarkLineComponent,
  CanvasRenderer,
])

/**
 * 组合后的强类型 option：只含本项目实际用到的系列与组件。
 * 组件可用它标注自己构造的 option；setOption 本身仍接受宽松的 EChartsCoreOption。
 */
export type EChartsOption = ComposeOption<
  | BarSeriesOption
  | LineSeriesOption
  | GridComponentOption
  | TooltipComponentOption
  | LegendComponentOption
  | DataZoomComponentOption
  | MarkLineComponentOption
>

export type { EChartsType }

/**
 * 创建一个 canvas 渲染的图表实例。
 * 主题不注册到 ECharts：颜色由 useChartTheme() 从设计 token 桥接后写进 option，
 * 这样切换明暗主题只需重新 setOption，无需 dispose 重建。
 */
export function createChart(el: HTMLElement, opts?: EChartsInitOpts): EChartsType {
  return init(el, undefined, { renderer: 'canvas', ...opts })
}

const ESC_MAP: Record<string, string> = {
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
}

/** tooltip formatter 返回 HTML，日期/状态/错误等动态文本必须先转义再拼。 */
export function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ESC_MAP[c] ?? c)
}
