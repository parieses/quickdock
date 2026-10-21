import { inject, ref, type Ref } from 'vue'

export interface ThemeControl {
  /** 当前主题偏好：'dark' | 'light' | 'system' */
  current: Ref<string>
  /** 设置主题：同步 currentTheme + applyTheme + 持久化 + 跨窗口广播（由 App.vue 实现） */
  set: (theme: string) => Promise<void>
}

/**
 * 设置主题的权威消费入口。
 * App.vue 通过 provide('theme') 注入统一实现（含持久化与跨窗口广播）；
 * 此处仅做消费与兜底，避免在多个组件里重复实现主题写入逻辑（**不要引入第三份实现**）。
 * 主题「当前解析值」的响应式读取请用 useThemeName。
 */
export function useTheme(): ThemeControl {
  const injected = inject<ThemeControl | null>('theme', null)
  if (injected) return injected

  // 兜底：理论上 SettingsModal 等总在 App.vue 的 provide 之下，这里仅在注入缺失时退化。
  const current = ref('system')
  return {
    current,
    set: async (theme: string) => {
      current.value = theme
      const prefersDark = window.matchMedia('(prefers-color-scheme: dark)')
      const isDark = theme === 'dark' || (theme === 'system' && prefersDark.matches)
      document.documentElement.setAttribute('data-theme', isDark ? 'dark' : 'light')
    },
  }
}
