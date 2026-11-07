import { ref, watch, type Ref } from 'vue'
import { useThemeName } from './useThemeName'

/**
 * 图表 option 里的颜色一律来自这里，禁止在组件里写死具体色值。
 * 语义色键对应 style.css 两套主题里同名的设计 token。
 */
export type ChartColorKey =
  | 'accent' | 'success' | 'warning' | 'danger' | 'info'
  | 'primary' | 'secondary' | 'muted'

export interface ChartThemeColors {
  themeName: 'dark' | 'light'
  accent: string
  success: string
  warning: string
  danger: string
  info: string
  textPrimary: string
  textSecondary: string
  textMuted: string
  textDisabled: string
  border: string
  borderLight: string
  grid: string
  bgPrimary: string
  bgSecondary: string
  bgTertiary: string
  surface: string
  tooltipBg: string
  axisLine: string
  /** 多系列默认色板，按索引取用。 */
  palette: string[]
}

type ColorField =
  | 'accent' | 'success' | 'warning' | 'danger' | 'info'
  | 'textPrimary' | 'textSecondary' | 'textMuted'

const COLOR_FIELD: Record<ChartColorKey, ColorField> = {
  accent: 'accent',
  success: 'success',
  warning: 'warning',
  danger: 'danger',
  info: 'info',
  primary: 'textPrimary',
  secondary: 'textSecondary',
  muted: 'textMuted',
}

/**
 * getComputedStyle 读设计 token → 具体色值（canvas 渲染器不吃 CSS var）。
 * style.css 在 :root / [data-theme="light"] 两套里都定义了同名 token，
 * 因此读不到的兜底留空串：ECharts 会回退到内置默认，不会残留硬编码色值。
 */
function cssVar(name: string): string {
  if (typeof document === 'undefined') return ''
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

function readColors(themeName: 'dark' | 'light'): ChartThemeColors {
  const accent = cssVar('--color-accent')
  const success = cssVar('--color-success')
  const warning = cssVar('--color-warning')
  const danger = cssVar('--color-danger')
  const info = cssVar('--color-log-info')
  return {
    themeName,
    accent, success, warning, danger, info,
    textPrimary: cssVar('--color-text-primary'),
    textSecondary: cssVar('--color-text-secondary'),
    textMuted: cssVar('--color-text-muted'),
    textDisabled: cssVar('--color-text-disabled'),
    border: cssVar('--color-border'),
    borderLight: cssVar('--color-border-light'),
    grid: cssVar('--color-border'),
    bgPrimary: cssVar('--color-bg-primary'),
    bgSecondary: cssVar('--color-bg-secondary'),
    bgTertiary: cssVar('--color-bg-tertiary'),
    surface: cssVar('--color-surface'),
    tooltipBg: cssVar('--color-surface-elevated'),
    axisLine: cssVar('--color-border-light'),
    palette: [accent, success, warning, danger, info, cssVar('--color-accent-light')],
  }
}

/**
 * 响应式主题色表：<html data-theme> 变化时（useThemeName 的 MutationObserver）
 * 重读整套 token。组件 watch 这个 ref 后重新 setOption 即可，不用 dispose 实例。
 */
export function useChartTheme(): Ref<ChartThemeColors> {
  const themeName = useThemeName()
  const colors = ref<ChartThemeColors>(readColors(themeName.value)) as Ref<ChartThemeColors>
  watch(themeName, (name) => {
    colors.value = readColors(name)
  })
  return colors
}

/** 把语义色键解析成当前主题的具体色值；未指定时回退到默认色板。 */
export function resolveChartColor(
  colors: ChartThemeColors,
  key: ChartColorKey | undefined,
  index = 0,
): string {
  if (key) return colors[COLOR_FIELD[key]]
  return colors.palette[index % colors.palette.length]
}
