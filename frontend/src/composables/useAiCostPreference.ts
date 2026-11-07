/**
 * 「AI 单条费用显示」偏好：默认开，可在设置里关掉。
 *
 * 为什么是模块级单例而不是各存一份：设置页（SettingsAI）与消息列表
 * （AiMessageList）是兄弟组件，中间隔着 AIPage。用单例 ref 就不必给那个
 * 4200 行的父模板再加一个 prop / 事件——拨一次，两边同一帧一起生效，
 * 也不会出现「这里关了、那里还开着」。
 *
 * 范式与 useAiStreamPreference 一致：localStorage 持久化，隐私模式 / 存储
 * 被禁时退化为「仅本次运行有效」，不影响功能。
 *
 * 默认开：这条费用本来就以另一种形式出现在消息统计行里（AIPage.formatStat
 * 的 aiStatCost），新做的气泡标签不该让用户升级后反而看不到钱。
 */
import { ref, watch } from 'vue'

const KEY = 'quickdock.ai.showMsgCost'

function readInitial(): boolean {
  if (typeof localStorage === 'undefined') return true
  try {
    // 只认显式的 '0'：首次使用、存储损坏都按默认「显示」处理。
    return localStorage.getItem(KEY) !== '0'
  } catch {
    return true
  }
}

const showMsgCost = ref(readInitial())

watch(showMsgCost, (v) => {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(KEY, v ? '1' : '0')
  } catch {
    // 写不进去就退化为「仅本次运行有效」，不影响功能
  }
})

export function useAiCostPreference() {
  return { showMsgCost }
}
