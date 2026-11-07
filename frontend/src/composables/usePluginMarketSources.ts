/**
 * 市场源配置与离线状态。
 *
 * 源列表落在 app_state 表的 qd_ 前缀 key 上，直接复用项目已有的 GetValue/SetValue
 * 绑定（这两个 key 通过 db.validateAppStateKey 白名单），因此不需要新增任何 Go
 * service 方法、也不需要重新生成前端 bindings。后端 GetPluginMarket 读同一个 key。
 *
 * 拉取与缓存全在 Go 侧：WebView2 跟随 WinINET 代理，代理端口没起时前端 fetch 会
 * 全部超时；前端这里只管「有哪些源、哪个启用、是不是离线缓存」。
 */
import { ref } from 'vue'
import { GetValue, SetValue } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'

const SOURCES_KEY = 'qd_plugin_market_sources'

export interface MarketSource {
  id: string
  name: string
  /** 内置官方源留空：地址由后端 defaultPluginMarketURL 补全，前端不重复硬编码。 */
  url: string
  enabled: boolean
  builtin?: boolean
}

export const marketSources = ref<MarketSource[]>([])
export const marketOffline = ref<{ offline: boolean; cachedAt: number }>({ offline: false, cachedAt: 0 })
// 源配置保存后由管理页发出，市场页 watch 到就重拉一次索引：源变了，
// 当前列表（可能还是上一次的合并结果或离线缓存）已经过期。
export const marketRefreshSignal = ref(0)

/** 没有任何已保存配置时的编辑态：只有不可删除的内置官方源。 */
function builtinSource(): MarketSource {
  return { id: 'official', name: '', url: '', enabled: true, builtin: true }
}

export function setMarketOffline(offline: boolean, cachedAt = 0) {
  marketOffline.value = { offline, cachedAt }
}

/** 读取配置；空 / 损坏时回退到内置源，不让一份写坏的 JSON 让市场页打不开。 */
export async function loadMarketSources(): Promise<void> {
  try {
    const raw = unwrap<string>(await GetValue(SOURCES_KEY))
    if (raw && raw.trim()) {
      const parsed = JSON.parse(raw)
      if (Array.isArray(parsed) && parsed.length) {
        marketSources.value = parsed as MarketSource[]
        return
      }
    }
  } catch {
    // 读取失败按「没配置」处理，回退到内置源即可，不打断市场页加载。
  }
  marketSources.value = [builtinSource()]
}

export function isValidSourceUrl(url: string): boolean {
  return /^https:\/\/\S+$/i.test(url.trim())
}

export function newSourceId(): string {
  return 'src-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 7)
}

/** 保存整个源列表（含内置源的启停状态）。返回是否写成功。 */
export async function saveMarketSources(list: MarketSource[]): Promise<boolean> {
  try {
    await unwrap(await SetValue(SOURCES_KEY, JSON.stringify(list)))
    marketSources.value = list
    marketRefreshSignal.value++
    return true
  } catch {
    return false
  }
}

export function usePluginMarketSources() {
  return {
    marketSources, marketOffline, marketRefreshSignal,
    loadMarketSources, saveMarketSources, setMarketOffline,
    isValidSourceUrl, newSourceId,
  }
}
