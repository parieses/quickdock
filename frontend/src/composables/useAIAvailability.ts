/**
 * 跨「问 AI」入口的 AI 档案可用性封装。
 *
 * 两个职责：
 *  1. `check()`：调后端 AIAvailable()，拿到「是否已配置可用档案」以及当前激活的档案名/模型。
 *  2. `goToConfig()`：跨窗口跳转到 AI 设置页（浮窗里点「问 AI」发现没配 Key 时用）。
 *
 * 为什么是组合式而不是在各自面板里各写一遍：SSH 右键、剪贴板列表、以及未来
 * 任何「问 AI」入口都要做同一套「前置校验 + 跳转」，散落各组件迟早会分叉
 * （一个入口忘了校验、另一个跳转写错了事件名）。
 *
 * 「用的是哪个 Key」：AIAvailable() 返回的就是后端当前激活档案（services/ai 里
 * aiActiveKey 决定），多 Key 场景下「问 AI」一律走激活档，这里把名字/模型透出给界面，
 * 用户能直接看到，要换档去设置页切换即可。
 */
import { ref } from 'vue'
import { AIAvailable } from '../../bindings/quickdock/services/ai/aiservice'
import { OpenAISettings } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'

export function useAIAvailability() {
  const available = ref(false)
  /** 当前激活档案名（多 Key 场景下即「问 AI」实际使用的那一个）。 */
  const profile = ref('')
  /** 当前激活档案的模型名。 */
  const model = ref('')
  let checking = false

  async function check(): Promise<boolean> {
    if (checking) return available.value
    checking = true
    try {
      const r = unwrap<{ available: boolean; profile?: string; model?: string }>(await AIAvailable())
      if (r) {
        available.value = r.available
        profile.value = r.profile ?? ''
        model.value = r.model ?? ''
      } else {
        available.value = false
      }
    } catch {
      available.value = false
    } finally {
      checking = false
    }
    return available.value
  }

  /**
   * 未配置时跳转到 AI 设置。
   *
   * 走 Go 侧 OpenAISettings() 而非前端直接 Emit：浮窗里的跳转必须把主窗口带上来，
   * 这一步落在宿主（Go）才能做，集中处理比前端各自 Emit 两次更不易出错。
   */
  function goToConfig() {
    try {
      void OpenAISettings()
    } catch {
      /* 极端：Go 侧未就绪，忽略即可 */
    }
  }

  return { available, profile, model, check, goToConfig }
}
