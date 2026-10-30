/**
 * 「AI 回答方式」偏好：流式（默认）/ 一次性返回。
 *
 * 为什么是模块级单例而不是每个面板各存一份：
 * 这是用户对「AI 怎么出字」的偏好，不是某个面板的属性。SSH 里关掉流式、
 * 换到剪贴板又从头流一遍，只会让人以为设置没生效。
 *
 * 为什么存 localStorage 而不是后端 DB：
 * 它只影响前端何时渲染——后端两种模式都能服务（带 streamId 即流式），
 * 不存在跨设备语义，也不值得为它进设置页来回切。
 *
 * 默认流式：只有用户显式关过才是一次性，读不到存储时也按流式走。
 */
import { ref, watch } from 'vue'

const KEY = 'quickdock.ai.stream'

function readInitial(): boolean {
  if (typeof localStorage === 'undefined') return true
  try {
    const saved = localStorage.getItem(KEY)
    // 只认显式的 '0'：首次使用（null）与存储损坏都按默认流式处理。
    return saved !== '0'
  } catch {
    return true
  }
}

const streamEnabled = ref(readInitial())

watch(streamEnabled, (v) => {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(KEY, v ? '1' : '0')
  } catch {
    // 隐私模式/存储被禁：写不进去就退化为「仅本次运行有效」，不影响功能
  }
})

export function useAiStreamPreference() {
  return { streamEnabled }
}
