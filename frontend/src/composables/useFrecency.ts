import { ref } from 'vue'
import { RecordUsage, RecordUsageEx, GetAllUsage } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'

interface FrecencyEntry { count: number; lastUsed: number }

// 内存缓存 — 启动时从后端 GetAllUsage 一次性加载
let frecencyCache: Record<string, FrecencyEntry> = {}
const frecencyTick = ref(0) // 递增以触发 computed 重新计算

async function loadFrecency(): Promise<void> {
  if (frecencyTick.value > 0) return // 已经加载过
  try {
    const raw = await GetAllUsage()
    const entries = unwrap<{ key: string; count: number; lastUsed: number }[]>(raw)
    frecencyCache = {}
    if (entries) {
      for (const e of entries) {
        frecencyCache[e.key] = { count: e.count, lastUsed: e.lastUsed }
      }
    }
  } catch {
    frecencyCache = {}
  }
  frecencyTick.value++ // 触发所有依赖 frecency 的 computed 重算
}

function recordUsage(key: string, type_?: string, label?: string, desc?: string, input?: string) {
  const now = Date.now()
  frecencyCache[key] = { count: (frecencyCache[key]?.count || 0) + 1, lastUsed: now }
  if (type_) {
    try { RecordUsageEx(key, type_, label || '', desc || '', input || '').catch(e => console.warn('[CmdPalette] RecordUsageEx:', e)) } catch {}
  } else {
    try { RecordUsage(key).catch(e => console.warn('[CmdPalette] RecordUsage:', e)) } catch {}
  }
}

function frecencyScore(key: string): number {
  if (frecencyTick.value === 0) return 0
  const entry = frecencyCache[key]
  if (!entry) return 0
  const now = Date.now()
  const recencyDays = (now - entry.lastUsed) / 86400000
  return entry.count * 10 + Math.max(0, 30 - recencyDays)
}

export function useFrecency() {
  return { frecencyTick, loadFrecency, recordUsage, frecencyScore }
}
