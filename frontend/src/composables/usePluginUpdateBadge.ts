// 插件「可更新」角标共享状态：PluginMarketPage 拉取市场索引后写入，
// Sidebar 读取并在「插件」导航项上显示小红点与数量。用模块级 reactive 单例，
// 避免 PluginMarketPage → PluginManagerPage → App → Sidebar 的逐层 props 透传。
import { reactive } from 'vue'

export const pluginUpdateBadge = reactive({
  // 有可用更新的插件数量（已装且远程版本更高）；0 表示无更新
  count: 0,
})

export function setPluginUpdateBadge(count: number) {
  pluginUpdateBadge.count = count
}
