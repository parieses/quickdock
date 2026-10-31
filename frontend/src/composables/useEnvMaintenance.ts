// 维护面板：源体检 / 磁盘占用 / 环境清单 / 用户环境变量 / 包源镜像 / GitHub 加速
//
// 这六件事都属于「跨运行时」的整体运维，不属于任何一个运行时卡片，
// 故在页面上收拢成一个弹窗按标签页组织（maint.tab）。
//
// 页面耦合只有一处：清单「应用激活版本」之后要重建运行时列表，
// 用 onEnvChanged 回调注入，不把 load() 硬塞进来。
import { reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from './useToast'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import {
  EnvDiskUsage,
  EnvGitHubAccelSet,
  EnvGitHubAccelState,
  EnvManifestExport,
  EnvManifestImport,
  EnvMirrorState,
  EnvMirrorToggle,
  EnvSourceCheck,
  EnvUserEnv,
  EnvUserEnvDeleteVar,
  EnvUserEnvSetPath,
  EnvUserEnvSetVar,
} from '../../bindings/quickdock/services/env/environmentservice'

interface SourceCheckItem {
  runtime: string
  name: string
  sourceId: string
  source: string
  version: string
  url: string
  ok: boolean
  code: number
  error: string
  latencyMs: number
  mirrorOk: boolean
  mirrorMs: number
}
interface VersionCheckItem {
  runtime: string
  name: string
  url: string
  ok: boolean
  code: number
  error: string
  latencyMs: number
  count: number
  latest: string
  recommended: string
  stale: boolean
}
interface DiskUsageItem {
  runtime: string
  name: string
  bytes: number
  versions: { version: string; bytes: number }[]
}
interface MirrorStateItem {
  key: string
  label: string
  available: boolean
  enabled: boolean
  current: string
  target: string
  file: string
  detail: string
}

const MAINT_TABS = ['check', 'disk', 'manifest', 'envvar'] as const
export type MaintTab = (typeof MAINT_TABS)[number]

export interface UserEnvVarItem {
  name: string
  value: string
  expand: boolean
  managed: boolean
}

interface Deps {
  /** 清单应用激活版本后需要重建运行时列表 */
  onEnvChanged?: () => void | Promise<void>
}

export function useEnvMaintenance(deps: Deps = {}) {
  const { t } = useI18n()
  const toast = useToast()

  const maint = reactive({ open: false, tab: 'check' as MaintTab })

  // ---- 源体检 ----
  const sourceCheck = reactive({
    running: false,
    done: false,
    error: '',
    sources: [] as SourceCheckItem[],
    versions: [] as VersionCheckItem[],
    report: null as null | { durationMs: number; ok: number; mirrorOnly: number; failed: number },
  })

  const diskUsage = reactive({ loading: false, loaded: false, totalBytes: 0, items: [] as DiskUsageItem[] })

  // ---- 运行时级包源镜像（Node→npm / Python→pip / Composer→composer）----
  // 入口在各自运行时的操作菜单里：用户要加速的通常是某一个包管理器，
  // 不再用一个全局「一次性切四项」的开关。Go 不在此列——它的 GOPROXY 在
  // 「Go 环境配置」弹窗里有快捷预设，且要按版本写各自的 go.env，语义不同。
  const MIRROR_RUNTIMES = new Set(['node', 'python', 'composer'])
  const runtimeMirror = reactive<Record<string, MirrorStateItem | undefined>>({})
  const mirrorBusy = ref('')

  async function fetchRuntimeMirror(rt: string) {
    if (!MIRROR_RUNTIMES.has(rt)) return
    try {
      const st = unwrap<MirrorStateItem>(await EnvMirrorState(rt))
      if (st) runtimeMirror[rt] = st
    } catch (e: any) {
      // 状态读不到不影响菜单可用性，点击时后端还会再校验一次
      logErr('env/mirror', e)
    }
  }

  async function toggleRuntimeMirror(r: { id: string }) {
    if (mirrorBusy.value) return
    const st = runtimeMirror[r.id]
    const enable = !st?.enabled
    mirrorBusy.value = r.id
    try {
      const next = unwrap<MirrorStateItem>(await EnvMirrorToggle(r.id, enable))
      if (next) runtimeMirror[r.id] = next
      toast.success(enable ? t('mirrorEnabledToast') : t('mirrorDisabledToast'))
    } catch (e: any) {
      toast.error(getErrorMessage(e))
      logErr('env/mirror', e)
    } finally {
      mirrorBusy.value = ''
    }
  }

  // mirrorMenuLabel 菜单项文案：知道当前状态时按状态提示下一步动作。
  function mirrorMenuLabel(rt: string): string {
    const st = runtimeMirror[rt]
    if (!st) return t('mirrorEnable')
    return st.enabled ? t('mirrorDisable') : t('mirrorEnable')
  }

  function mirrorMenuTitle(rt: string): string {
    const st = runtimeMirror[rt]
    if (!st) return t('mirrorEnableHint')
    return st.enabled ? t('mirrorDisableHint', { v: st.current || st.target }) : t('mirrorEnableHint')
  }

  // ---- GitHub 下载加速（gh-proxy）----
  // 源体检里「需加速」的项要靠这个开关才真正生效，否则体检说能下、实际下载仍走直连。
  const githubAccel = reactive({ loading: false, enabled: false, busy: false })
  async function loadGithubAccel() {
    githubAccel.loading = true
    try {
      const d = unwrap<{ enabled: boolean }>(await EnvGitHubAccelState())
      githubAccel.enabled = !!d?.enabled
    } catch (e: any) {
      logErr('env/maint', e)
    } finally {
      githubAccel.loading = false
    }
  }
  async function toggleGithubAccel() {
    if (githubAccel.busy) return
    githubAccel.busy = true
    const next = !githubAccel.enabled
    try {
      unwrap(await EnvGitHubAccelSet(next))
      githubAccel.enabled = next
      toast.success(next ? t('ghAccelOn') : t('ghAccelOff'))
    } catch (e: any) {
      toast.error(getErrorMessage(e))
      logErr('env/maint', e)
    } finally {
      githubAccel.busy = false
    }
  }

  // ---- 环境清单（导出 / 导入比对）----
  const manifestInput = ref<HTMLInputElement | null>(null)
  const manifestPanel = reactive({
    busy: false,
    error: '',
    text: '',
    report: null as any,
    applied: [] as string[],
  })

  // ---- 用户环境变量（HKCU\Environment）----
  // 激活某个版本时后端会把它的 bin 目录写进用户 PATH，并写入 GOPATH/GOROOT 等变量。
  // 这些是「系统环境变量」，不在 go.env 里，此前界面上看不到也改不了——本面板补这个缺口。
  const userEnv = reactive({
    loading: false,
    saving: false,
    loaded: false,
    error: '',
    pathEntries: [] as string[],
    vars: [] as UserEnvVarItem[],
    newPathEntry: '',
    newName: '',
    newValue: '',
    pendingDelete: '', // 二次确认：同一行再点一次才真的删
    // 就地编辑：后端 EnvUserEnvSetVar 对同名变量就是覆盖，所以「改值」不需要新接口。
    // 之前只能新增/删除，想改一个已存在变量的值，得先删再建——中间那个空档里
    // 依赖它的程序读到的是「变量不存在」。
    editingName: '',
    editingValue: '',
  })

  function isQuickdockEntry(p: string): boolean {
    const low = p.toLowerCase()
    return low.includes('.quickdock\\') || low.includes('.quickdock/')
  }

  async function loadUserEnv() {
    userEnv.loading = true
    userEnv.error = ''
    try {
      const rep: any = unwrap(await EnvUserEnv())
      userEnv.pathEntries = rep?.path?.entries || []
      userEnv.vars = rep?.vars || []
      userEnv.loaded = true
    } catch (e: any) {
      userEnv.error = getErrorMessage(e)
      logErr('env/envvar', e)
    } finally {
      userEnv.loading = false
    }
  }

  async function saveUserPath(next: string[]) {
    userEnv.saving = true
    userEnv.error = ''
    try {
      unwrap(await EnvUserEnvSetPath(next))
      await loadUserEnv()
    } catch (e: any) {
      userEnv.error = getErrorMessage(e)
      logErr('env/envvar', e)
    } finally {
      userEnv.saving = false
    }
  }

  async function addPathEntry() {
    const v = userEnv.newPathEntry.trim()
    if (!v) return
    if (userEnv.pathEntries.some((x) => x.toLowerCase() === v.toLowerCase())) {
      userEnv.error = t('maintEnvDupEntry')
      return
    }
    userEnv.newPathEntry = ''
    await saveUserPath([...userEnv.pathEntries, v])
  }

  async function removePathEntry(p: string) {
    await saveUserPath(userEnv.pathEntries.filter((x) => x !== p))
  }

  async function saveUserVar() {
    const name = userEnv.newName.trim().toUpperCase()
    const value = userEnv.newValue.trim()
    if (!name) {
      userEnv.error = t('maintEnvNameRequired')
      return
    }
    if (!value) {
      userEnv.error = t('maintEnvValueRequired')
      return
    }
    userEnv.saving = true
    userEnv.error = ''
    try {
      unwrap(await EnvUserEnvSetVar(name, value))
      userEnv.newName = ''
      userEnv.newValue = ''
      toast.success(t('saved'))
      await loadUserEnv()
    } catch (e: any) {
      userEnv.error = getErrorMessage(e)
      logErr('env/envvar', e)
    } finally {
      userEnv.saving = false
    }
  }

  // ---- 就地编辑已有变量 ----
  // EnvUserEnvSetVar 对同名变量即覆盖，所以「改值」不需要新接口。
  // 此前只能新增/删除，想改一个已存在变量的值必须先删再建 ——
  // 中间那个空档里，依赖它的程序读到的是「变量不存在」。
  function startEditVar(v: UserEnvVarItem) {
    userEnv.editingName = v.name
    userEnv.editingValue = v.value
    userEnv.pendingDelete = ''
  }

  function cancelEditVar() {
    userEnv.editingName = ''
    userEnv.editingValue = ''
  }

  async function commitEditVar() {
    const name = userEnv.editingName
    const value = userEnv.editingValue.trim()
    if (!name) return
    if (!value) {
      userEnv.error = t('maintEnvValueRequired')
      return
    }
    userEnv.saving = true
    userEnv.error = ''
    try {
      unwrap(await EnvUserEnvSetVar(name, value)) // 同名 = 覆盖
      userEnv.editingName = ''
      userEnv.editingValue = ''
      toast.success(t('saved'))
      await loadUserEnv()
    } catch (e: any) {
      userEnv.error = getErrorMessage(e)
      logErr('env/envvar', e)
    } finally {
      userEnv.saving = false
    }
  }

  // deleteUserVar 两段式：第一次点亮「确认删除」，再点一次才执行。
  // 删的是系统环境变量，误删会影响其它程序，不值得为省一次点击冒险。
  async function deleteUserVar(name: string) {
    if (userEnv.pendingDelete !== name) {
      userEnv.pendingDelete = name
      return
    }
    userEnv.saving = true
    userEnv.error = ''
    try {
      unwrap(await EnvUserEnvDeleteVar(name))
      await loadUserEnv()
    } catch (e: any) {
      userEnv.error = getErrorMessage(e)
      logErr('env/envvar', e)
    } finally {
      userEnv.saving = false
      userEnv.pendingDelete = ''
    }
  }

  function openMaintenance(tab: MaintTab) {
    maint.open = true
    maint.tab = tab
    if (tab === 'disk' && !diskUsage.loaded) loadDiskUsage()
    if (tab === 'check' && !githubAccel.loading) loadGithubAccel()
    if (tab === 'envvar' && !userEnv.loaded) loadUserEnv()
  }

  // ---- 源体检（逐条事件 + 汇总）----
  async function runSourceCheck() {
    if (sourceCheck.running) return
    sourceCheck.running = true
    sourceCheck.done = false
    sourceCheck.error = ''
    sourceCheck.sources = []
    sourceCheck.versions = []
    sourceCheck.report = null
    try {
      unwrap(await EnvSourceCheck())
    } catch (e: any) {
      sourceCheck.running = false
      sourceCheck.error = getErrorMessage(e)
      logErr('env/maint', e)
    }
  }

  // onSourceCheckEvent 逐条接收体检结果。重复点体检时同一目标只替换不叠加。
  // 注意：Wails 事件的回调参数是 {name, data} 包装体，必须取 data
  // （EnvironmentPage 的 onProgress / onOllamaPull 都是 `e?.data ?? e` 这个口径）。
  function onSourceCheckEvent(payload: any) {
    const ev = payload?.data ?? payload
    if (!ev) return
    if (ev.kind === 'source' && ev.source) {
      const item = ev.source as SourceCheckItem
      const i = sourceCheck.sources.findIndex((x) => x.runtime === item.runtime && x.sourceId === item.sourceId)
      if (i >= 0) sourceCheck.sources.splice(i, 1, item)
      else sourceCheck.sources.push(item)
      return
    }
    if (ev.kind === 'version' && ev.version) {
      const item = ev.version as VersionCheckItem
      const i = sourceCheck.versions.findIndex((x) => x.runtime === item.runtime)
      if (i >= 0) sourceCheck.versions.splice(i, 1, item)
      else sourceCheck.versions.push(item)
    }
  }

  // localSourceStats 按已收到的明细统计，作为 done 事件的兜底。
  // 后端报告字段缺失（版本不一致/序列化差异）时不至于让界面显示全 0。
  function localSourceStats() {
    let ok = 0
    let mirror = 0
    let failed = 0
    for (const it of sourceCheck.sources) {
      if (it.ok) ok++
      else if (it.mirrorOk) mirror++
      else failed++
    }
    return { ok, mirror, failed }
  }

  function onSourceCheckDone(payload: any) {
    const rep = payload?.data ?? payload
    sourceCheck.running = false
    sourceCheck.done = true
    if (rep && rep.error) {
      sourceCheck.error = String(rep.error)
      return
    }
    // 汇总报告带完整列表，先回填明细，兜住事件丢失/顺序问题
    if (Array.isArray(rep?.sources) && rep.sources.length) sourceCheck.sources = rep.sources
    if (Array.isArray(rep?.versions) && rep.versions.length) sourceCheck.versions = rep.versions
    const local = localSourceStats()
    const num = (v: any, fallback: number) => {
      const n = Number(v)
      return Number.isFinite(n) && n > 0 ? n : fallback
    }
    sourceCheck.report = {
      durationMs: Number(rep?.durationMs) || 0,
      ok: num(rep?.ok, local.ok),
      mirrorOnly: num(rep?.mirrorOnly, local.mirror),
      failed: num(rep?.failed, local.failed),
    }
  }

  async function loadDiskUsage() {
    diskUsage.loading = true
    try {
      const data = unwrap<{ totalBytes: number; runtimes: DiskUsageItem[] }>(await EnvDiskUsage())
      diskUsage.totalBytes = data?.totalBytes || 0
      diskUsage.items = data?.runtimes || []
      diskUsage.loaded = true
    } catch (e: any) {
      toast.error(t('maintDiskFail') + ': ' + getErrorMessage(e))
      logErr('env/maint', e)
    } finally {
      diskUsage.loading = false
    }
  }

  // exportManifest 导出环境清单：后端给 JSON 文本，前端落成文件下载。
  async function exportManifest() {
    if (manifestPanel.busy) return
    manifestPanel.busy = true
    manifestPanel.error = ''
    try {
      const text = unwrap<string>(await EnvManifestExport()) || ''
      const blob = new Blob([text], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = 'quickdock-env-' + new Date().toISOString().slice(0, 10) + '.json'
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      toast.success(t('maintManifestExported'))
    } catch (e: any) {
      manifestPanel.error = getErrorMessage(e)
      logErr('env/maint', e)
    } finally {
      manifestPanel.busy = false
    }
  }

  function pickManifestFile() {
    manifestInput.value?.click()
  }

  // onManifestFile 选文件后先只做比对（不动本机环境），
  // 需要切激活版本时再由用户点一次「应用激活版本」。
  async function onManifestFile(e: Event) {
    const input = e.target as HTMLInputElement
    const file = input.files?.[0]
    input.value = '' // 允许重复选同一个文件
    if (!file) return
    manifestPanel.busy = true
    manifestPanel.error = ''
    manifestPanel.report = null
    manifestPanel.applied = []
    try {
      const text = await file.text()
      const res = unwrap<any>(await EnvManifestImport(text, false))
      manifestPanel.text = text
      manifestPanel.report = res?.report || null
    } catch (err: any) {
      manifestPanel.error = getErrorMessage(err)
      logErr('env/maint', err)
    } finally {
      manifestPanel.busy = false
    }
  }

  // applyManifestActive 按清单把「已装且要求激活」的版本设为激活版本
  async function applyManifestActive() {
    if (!manifestPanel.text || manifestPanel.busy) return
    manifestPanel.busy = true
    try {
      const res = unwrap<any>(await EnvManifestImport(manifestPanel.text, true))
      manifestPanel.report = res?.report || manifestPanel.report
      manifestPanel.applied = res?.appliedActive || []
      if (manifestPanel.applied.length)
        toast.success(t('maintManifestApplied', { n: manifestPanel.applied.length }))
      await deps.onEnvChanged?.()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
      logErr('env/maint', e)
    } finally {
      manifestPanel.busy = false
    }
  }

  // formatSize 人性化字节数（磁盘统计面板用；与进程占用的 formatBytes 分开，
  // 后者只输出 MB/GB 且 0 值显示为「—」，语义不同）
  function formatSize(n: number): string {
    if (!n || n <= 0) return '0 B'
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    let i = 0
    let v = n
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024
      i++
    }
    return (i === 0 ? v.toFixed(0) : v.toFixed(v >= 100 ? 0 : 1)) + ' ' + units[i]
  }

  return {
    MIRROR_RUNTIMES,
    maint,
    sourceCheck,
    diskUsage,
    runtimeMirror,
    mirrorBusy,
    githubAccel,
    manifestPanel,
    manifestInput,
    userEnv,
    fetchRuntimeMirror,
    toggleRuntimeMirror,
    mirrorMenuLabel,
    mirrorMenuTitle,
    loadGithubAccel,
    toggleGithubAccel,
    isQuickdockEntry,
    loadUserEnv,
    saveUserPath,
    addPathEntry,
    removePathEntry,
    saveUserVar,
    startEditVar,
    cancelEditVar,
    commitEditVar,
    deleteUserVar,
    openMaintenance,
    runSourceCheck,
    onSourceCheckEvent,
    onSourceCheckDone,
    loadDiskUsage,
    exportManifest,
    pickManifestFile,
    onManifestFile,
    applyManifestActive,
    formatSize,
  }
}
