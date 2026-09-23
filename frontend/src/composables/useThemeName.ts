import { onBeforeUnmount, ref, type Ref } from 'vue'

/** 主题的唯一真相是 <html data-theme>：App.vue 的 applyTheme 把 'system' 解析成具体值后写入。
 *  所以不能读用户偏好设置（可能是 'system'，取值会不对），必须监听这个属性。 */
function readThemeName(): 'dark' | 'light' {
  return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark'
}

/**
 * 响应式当前主题名（'dark' | 'light'）。
 * 插件图标分深浅两档，需要按主题取用并在切换时重新拉取，故做成可复用的响应式来源。
 */
export function useThemeName(): Ref<'dark' | 'light'> {
  const name = ref<'dark' | 'light'>(readThemeName())
  let observer: MutationObserver | null = null
  if (typeof MutationObserver !== 'undefined') {
    observer = new MutationObserver(() => {
      name.value = readThemeName()
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  }
  onBeforeUnmount(() => {
    observer?.disconnect()
    observer = null
  })
  return name
}
