// useEnvEverything — Everything 文件搜索（外部程序）：四态探测 + 启停 + 设置 + 自测搜索
//
// Everything 是外部程序（与 git/ollama 同属「单版本、non-托管更新」语义），
// QuickDock 不负责下载更新主程序，只做：探测、启停 Everything.exe、封装 es.exe 检索、存设置。
// 这段逻辑与页面的唯一耦合是「启停后刷新侧栏绿点」，通过 deps.onStateChanged 回调注入。
import { computed, ref } from 'vue'
import type { Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from './useToast'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import {
  EnvEverythingStatus,
  EnvEverythingGetSettings,
  EnvEverythingSetSettings,
  EnvEverythingStart,
  EnvEverythingStop,
  EnvEverythingInstall,
  EnvEverythingInstallEs,
  EnvEverythingOpenDownload,
  EnvEverythingOpenCliDownload,
  EnvEverythingSearch,
  EnvRefresh,
} from '../../bindings/quickdock/services/env/environmentservice'

export interface EverythingStatusInfo {
  installed: boolean
  hasEsExe: boolean
  running: boolean
  state: 'not_installed' | 'missing_es' | 'stopped' | 'running'
  version: string
  exePath: string
  esExePath: string
  searchDiag?: string
  enabled: boolean
  updatedAt: number
}
export interface EverythingSettings {
  installPath: string
  esExePath: string
  fileSearchEnabled: boolean
}
export interface EvFileResult { path?: string; name?: string; dir?: string; modified?: number }

export interface EnvEverythingDeps {
  /** 当前选中分类 id：切走时不拉数据（避免无谓请求） */
  selectedId: Ref<string>
  /** 启停/安装完成后刷新运行态缓存（侧栏绿点依赖它） */
  onStateChanged: () => void
}

export function useEnvEverything(deps: EnvEverythingDeps) {
  const { t } = useI18n()
  const toast = useToast()

  const evStatus = ref<EverythingStatusInfo | null>(null)
  const evSettings = ref<EverythingSettings>({ installPath: '', esExePath: '', fileSearchEnabled: true })
  const evBusy = ref(false)

  // ---- 测试搜索 ----
  // EnvEverythingSearch 是命令面板与 MCP 的取数口，页面若完全不接，
  // 「诊断说 running、es.exe 也在，但文件搜不出来」这种情况用户无处自查。
  // 这里直接调同一个接口，看到的就是命令面板会拿到的东西。
  const evQuery = ref('')
  const evSearching = ref(false)
  const evSearchErr = ref(false)
  const evSearchMsg = ref('')
  const evResults = ref<EvFileResult[]>([])

  async function runEvSearch() {
    const q = evQuery.value.trim()
    if (!q || evSearching.value) return
    evSearching.value = true
    evSearchErr.value = false
    evSearchMsg.value = ''
    evResults.value = []
    try {
      const rows = unwrap<EvFileResult[]>(await EnvEverythingSearch(q, 20, 'date_modified')) ?? []
      evResults.value = rows
      evSearchMsg.value = rows.length ? t('evTestSearchOk', { n: rows.length }) : t('evTestSearchEmpty')
    } catch (e) {
      evSearchErr.value = true
      evSearchMsg.value = getErrorMessage(e)
      logErr('env/evSearch', e)
    } finally {
      evSearching.value = false
    }
  }

  const evStateText = computed(() => {
    const st = evStatus.value?.state
    if (st === 'running') return t('evStateRunning')
    if (st === 'stopped') return t('evStateStopped')
    if (st === 'missing_es') return t('evStateMissingEs')
    if (st === 'not_installed') return t('evStateNotInstalled')
    return '—'
  })
  const evStateClass = computed(() => {
    const st = evStatus.value?.state
    if (st === 'running') return 'ok'
    if (st === 'missing_es' || st === 'not_installed') return 'fail'
    return 'warn'
  })
  const evStateHint = computed(() => {
    const st = evStatus.value?.state
    if (st === 'running') return t('evRunningReady')
    if (st === 'stopped') return t('evStoppedHint')
    if (st === 'missing_es') return t('evMissingEsHint')
    if (st === 'not_installed') return t('evNotInstalledHint')
    return ''
  })

  async function loadEverything() {
    if (deps.selectedId.value !== 'everything') return
    // 先保留上次数据（消除切回时的闪现），再后台刷新
    try {
      const res = unwrap<EverythingStatusInfo>(await EnvEverythingStatus())
      if (res) evStatus.value = res
    } catch { /* 保留旧值 */ }
    try {
      const s = unwrap<EverythingSettings>(await EnvEverythingGetSettings())
      if (s) evSettings.value = s
    } catch { /* 保留旧值 */ }
  }

  async function evRedetect() {
    if (evBusy.value) return
    evBusy.value = true
    try {
      unwrap(await EnvRefresh()) // 触发后端真实重扫（与刷新按钮一致），再读新状态
    } catch { /* 忽略刷新失败，仍读当前状态 */ }
    await loadEverything()
    evBusy.value = false
  }

  async function evStart() {
    if (evBusy.value) return
    evBusy.value = true
    try {
      unwrap(await EnvEverythingStart())
      toast.success(t('envStarted'))
      await loadEverything()
      deps.onStateChanged()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    } finally { evBusy.value = false }
  }

  async function evStop() {
    if (evBusy.value) return
    evBusy.value = true
    try {
      unwrap(await EnvEverythingStop())
      toast.success(t('envStopped'))
      await loadEverything()
      deps.onStateChanged()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    } finally { evBusy.value = false }
  }

  /** 程序托管下载 Everything 便携版：按系统 32/64 位自动选包，下载+解压到托管目录。 */
  async function evInstall() {
    if (evBusy.value) return
    evBusy.value = true
    try {
      unwrap(await EnvEverythingInstall())
      toast.success(t('evInstallDone'))
      await loadEverything()
      deps.onStateChanged()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    } finally { evBusy.value = false }
  }

  /** 程序托管下载 es.exe 命令行工具（文件检索依赖它），落盘到 Everything 同目录。 */
  async function evInstallEs() {
    if (evBusy.value) return
    evBusy.value = true
    try {
      unwrap(await EnvEverythingInstallEs())
      toast.success(t('evInstallEsDone'))
      await loadEverything()
      deps.onStateChanged()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    } finally { evBusy.value = false }
  }

  // 兜底：跳转官网手动下载（次要入口，主路径是上面的程序托管下载）
  async function evOpenDownload() {
    try { unwrap(await EnvEverythingOpenDownload()) } catch (e: any) { toast.error(getErrorMessage(e)) }
  }
  async function evOpenCliDownload() {
    try { unwrap(await EnvEverythingOpenCliDownload()) } catch (e: any) { toast.error(getErrorMessage(e)) }
  }

  async function evSaveSettings() {
    if (!evSettings.value || evBusy.value) return
    evBusy.value = true
    try {
      unwrap(await EnvEverythingSetSettings(
        evSettings.value.installPath,
        evSettings.value.esExePath,
        evSettings.value.fileSearchEnabled,
      ))
      toast.success(t('evSaved'))
      await loadEverything()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    } finally { evBusy.value = false }
  }

  return {
    evStatus, evSettings, evBusy,
    evQuery, evSearching, evSearchErr, evSearchMsg, evResults,
    evStateText, evStateClass, evStateHint,
    runEvSearch, loadEverything, evRedetect, evStart, evStop,
    evInstall, evInstallEs, evOpenDownload, evOpenCliDownload, evSaveSettings,
  }
}
