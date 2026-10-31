// 运行时弹窗：PHP / Go / Redis / 通用配置 / FTP 启动参数 / 通用日志
//
// 这些弹窗的共同点是「同一种数据（配置文本或日志文本）在不同运行时上的不同呈现」，
// 所以集中在页面里，靠 openXxx(r, ins) 打开。搬出来的价值不在于省行数，
// 而在于「配置读写口径」只有一处可查：都是 EnvConfigGet/Set + 乐观 toast。
//
// 与页面的耦合有三处，全部用回调/引用注入：
//   closeMenu()      操作菜单还开着时打开弹窗要收起菜单
//   toggleEnvVar()   Go 弹窗里「bin 是否在 PATH」复用菜单那套写入/注销逻辑
//   onConfigSaved()  保存成功后通知 ConfigEditor 刷新「已修改」基线
import { reactive, ref } from 'vue'
import type { Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from './useToast'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { binDirOf } from '../utils/envPaths'
import {
  EnvConfigGet,
  EnvConfigSet,
  EnvConfigSupport,
  EnvGoEnvGet,
  EnvGoEnvKeys,
  EnvGoEnvSet,
  EnvLogGet,
  EnvPHPConfigGet,
  EnvPHPConfigSet,
} from '../../bindings/quickdock/services/env/environmentservice'

interface InstallLike {
  version: string
  scope: string
  path: string
  active: boolean
  inSystemPath: boolean
}
interface RuntimeLike {
  id: string
  name: string
  installed: InstallLike[]
}

export interface PHPExt {
  name: string
  file: string
  enabled: boolean
}
export type GoEnvScope = 'version' | 'global'
export interface GoEnvKey {
  key: string
  label: string
  desc: string
  placeholder: string
  choices: string[]
  secret: boolean
}

interface Deps {
  runtimes: Ref<RuntimeLike[]>
  /** 打开弹窗时收起操作菜单 */
  closeMenu: () => void
  /** 复用菜单里的「写/注销用户 PATH」逻辑（参数按页面自己的 RuntimeInfo/Install 传，故不窄化类型） */
  toggleEnvVar: (r: any, ins: any) => Promise<void>
  /** 配置保存成功后刷新编辑器「已修改」基线 */
  onConfigSaved?: () => void
}

// PHP / Redis / FTP 各有专用弹窗（PHP 扩展开关、Redis 日志、FTP 结构化启动参数），
// 不复用裸文本配置弹窗，故排除。
const SPECIAL_CONFIG_UI = ['php', 'redis', 'ftp']

export function useEnvRuntimeModals(deps: Deps) {
  const { t } = useI18n()
  const toast = useToast()

  // ---------------- PHP 配置（php.ini / 禁用函数 / 错误日志 / 扩展） ----------------
  const phpModal = reactive<{
    open: boolean
    runtime: string
    version: string
    tab: string
    raw: string
    disableFunctions: string
    errorLog: string
    errorLogContent: string
    extensions: PHPExt[]
  }>({
    open: false,
    runtime: '',
    version: '',
    tab: 'ini',
    raw: '',
    disableFunctions: '',
    errorLog: '',
    errorLogContent: '',
    extensions: [],
  })

  async function openPHPConfig(r: RuntimeLike, ins: InstallLike, tab: string) {
    try {
      const cfg = unwrap<any>(await EnvPHPConfigGet(r.id, ins.version))
      phpModal.runtime = r.id
      phpModal.version = ins.version
      phpModal.tab = tab
      phpModal.raw = cfg.raw || ''
      phpModal.disableFunctions = cfg.disableFunctions || ''
      phpModal.errorLog = cfg.errorLog || ''
      phpModal.errorLogContent = cfg.errorLogContent || ''
      phpModal.extensions = (cfg.extensions || []).map((e: any) => ({
        name: e.name,
        file: e.file,
        enabled: !!e.enabled,
      }))
      phpModal.open = true
      deps.closeMenu()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  function toggleExt(i: number) {
    phpModal.extensions[i].enabled = !phpModal.extensions[i].enabled
  }

  async function savePHPConfig() {
    try {
      const patch: any = {}
      if (phpModal.tab === 'ini') {
        patch.raw = phpModal.raw
      } else {
        patch.disableFunctions = phpModal.disableFunctions
        patch.errorLog = phpModal.errorLog
        patch.extensions = phpModal.extensions.filter((e) => e.enabled).map((e) => e.name)
      }
      unwrap(await EnvPHPConfigSet(phpModal.runtime, phpModal.version, patch))
      toast.success(t('saved'))
      phpModal.open = false
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  // ---------------- Go 环境变量（go env -w / -u） ----------------
  // 作用域（scope）：
  //   version → 该版本专属 go.env（runtime/go/<ver>/gopath/env），QuickDock 运行该版本时读的就是它
  //   global  → 系统全局即用户级 go.env（%APPDATA%\go\env），对没做版本隔离的 go 生效
  // 两个作用域互不覆盖，后端用 version 参数区分（空串 = 全局），因此不需要新接口。
  const goEnvModal = reactive<{
    open: boolean
    runtime: string
    version: string
    scope: GoEnvScope
    keys: GoEnvKey[]
    values: Record<string, string>
    loading: boolean
    saving: boolean
    error: string
    // PATH 与 go.env 是两件事，但用户常把它们混为一谈：
    // 前者决定「go 命令能否被找到」，后者决定「go 的行为（GOPATH/代理/缓存）」。
    // 弹窗里把前者显式摆出来（目录 + 状态 + 一键切换），避免再去菜单里翻。
    inPath: boolean
    binDir: string
    pathBusy: boolean
  }>({
    open: false,
    runtime: '',
    version: '',
    scope: 'version',
    keys: [],
    values: {},
    loading: false,
    saving: false,
    error: '',
    inPath: false,
    binDir: '',
    pathBusy: false,
  })

  async function loadGoEnvValues() {
    goEnvModal.loading = true
    goEnvModal.error = ''
    goEnvModal.values = {}
    try {
      // 全局作用域传空版本号，后端据此读写用户级 go.env
      const ver = goEnvModal.scope === 'global' ? '' : goEnvModal.version
      const vs = unwrap<Record<string, string>>(await EnvGoEnvGet(goEnvModal.runtime, ver))
      goEnvModal.values = vs || {}
    } catch (e: any) {
      goEnvModal.error = getErrorMessage(e)
    } finally {
      goEnvModal.loading = false
    }
  }

  async function switchGoEnvScope(scope: GoEnvScope) {
    if (goEnvModal.scope === scope || goEnvModal.saving) return
    goEnvModal.scope = scope
    await loadGoEnvValues()
  }

  async function openGoEnv(r: RuntimeLike, ins: InstallLike) {
    goEnvModal.runtime = r.id
    goEnvModal.version = ins.version
    goEnvModal.scope = 'version'
    goEnvModal.keys = []
    goEnvModal.values = {}
    goEnvModal.error = ''
    goEnvModal.loading = true
    goEnvModal.open = true
    goEnvModal.inPath = !!ins.inSystemPath
    goEnvModal.binDir = binDirOf(ins)
    deps.closeMenu()
    try {
      const ks = unwrap<GoEnvKey[]>(await EnvGoEnvKeys(r.id))
      goEnvModal.keys = ks || []
    } catch (e: any) {
      goEnvModal.error = getErrorMessage(e)
      goEnvModal.loading = false
      return
    }
    await loadGoEnvValues()
  }

  // toggleGoPath 在弹窗内直接切换「该版本 bin 是否在用户 PATH」。
  // 复用页面菜单里的 toggleEnvVar（同一套写入/注销逻辑），切换后页面会重建
  // runtimes，所以必须重新取一次 install 才能拿到最新 inSystemPath。
  async function toggleGoPath() {
    if (goEnvModal.pathBusy) return
    goEnvModal.pathBusy = true
    try {
      const r = deps.runtimes.value.find((x) => x.id === goEnvModal.runtime)
      const ins = r?.installed?.find((i) => i.version === goEnvModal.version)
      if (!r || !ins) return
      await deps.toggleEnvVar(r, ins)
      const r2 = deps.runtimes.value.find((x) => x.id === goEnvModal.runtime)
      const ins2 = r2?.installed?.find((i) => i.version === goEnvModal.version)
      goEnvModal.inPath = !!ins2?.inSystemPath
      goEnvModal.binDir = ins2 ? binDirOf(ins2) : goEnvModal.binDir
      toast.success(goEnvModal.inPath ? t('goPathAdded') : t('goPathRemoved'))
    } catch (e: any) {
      goEnvModal.error = getErrorMessage(e)
      logErr('env/goenv', e)
    } finally {
      goEnvModal.pathBusy = false
    }
  }

  async function saveGoEnv() {
    goEnvModal.saving = true
    goEnvModal.error = ''
    try {
      const ver = goEnvModal.scope === 'global' ? '' : goEnvModal.version
      unwrap(await EnvGoEnvSet(goEnvModal.runtime, ver, { ...goEnvModal.values }))
      toast.success(t('saved'))
      goEnvModal.open = false
    } catch (e: any) {
      goEnvModal.error = getErrorMessage(e)
    } finally {
      goEnvModal.saving = false
    }
  }

  // GOPROXY 一键镜像：国内网络拉模块常常超时，提供常用镜像直选。
  const goProxyPresets = [
    { label: '官方', value: 'https://proxy.golang.org,direct' },
    { label: '七牛 goproxy.cn', value: 'https://goproxy.cn,direct' },
    { label: '阿里云', value: 'https://mirrors.aliyun.com/goproxy/,direct' },
    { label: '腾讯云', value: 'https://mirrors.tencent.com/go/,direct' },
    { label: '关闭(直连)', value: 'direct' },
  ]
  function applyGoProxy(value: string) {
    goEnvModal.values['GOPROXY'] = value
  }

  // ---------------- Redis 配置 / 日志 ----------------
  // Redis 已实现通用 ConfigProvider，故配置读写走 EnvConfigGet/Set（与 Nginx/Apache 一致）；
  // 日志查询走通用 EnvLogGet（原 EnvRedisLog 已废弃，仅 Redis 可用 → 现所有实现 LogProvider 的运行时可用）。
  const redisModal = reactive<{
    open: boolean
    runtime: string
    version: string
    tab: string
    path: string
    raw: string
    log: string
    logAuto: boolean
  }>({
    open: false,
    runtime: '',
    version: '',
    tab: 'config',
    path: '',
    raw: '',
    log: '',
    logAuto: false,
  })
  let redisLogTimer: ReturnType<typeof setInterval> | null = null

  async function fetchRedisLog() {
    try {
      redisModal.log = unwrap<string>(await EnvLogGet(redisModal.runtime, redisModal.version)) || ''
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  function startLogAuto() {
    if (redisLogTimer || !redisModal.logAuto || redisModal.tab !== 'log') return
    redisLogTimer = setInterval(() => {
      void fetchRedisLog()
    }, 2000)
  }
  function stopLogAuto() {
    if (redisLogTimer) {
      clearInterval(redisLogTimer)
      redisLogTimer = null
    }
  }

  async function openRedisConfig(r: RuntimeLike, ins: InstallLike, tab: string) {
    try {
      const cfg = unwrap<any>(await EnvConfigGet(r.id, ins.version))
      redisModal.runtime = r.id
      redisModal.version = ins.version
      redisModal.tab = tab
      redisModal.path = cfg.path || ''
      redisModal.raw = cfg.raw || ''
      if (tab === 'log') {
        try {
          redisModal.log = unwrap<string>(await EnvLogGet(r.id, ins.version)) || ''
        } catch {
          redisModal.log = ''
        }
      }
      redisModal.open = true
      deps.closeMenu()
      startLogAuto()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  const refreshRedisLog = fetchRedisLog

  async function saveRedisConfig() {
    try {
      unwrap(await EnvConfigSet(redisModal.runtime, redisModal.version, redisModal.raw))
      toast.success(t('saved'))
      closeRedisModal()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  function toggleLogAuto() {
    redisModal.logAuto = !redisModal.logAuto
    if (redisModal.logAuto) {
      void fetchRedisLog()
      startLogAuto()
    } else {
      stopLogAuto()
    }
  }
  function closeRedisModal() {
    stopLogAuto()
    redisModal.open = false
  }
  function openLogTab() {
    redisModal.tab = 'log'
    void fetchRedisLog()
    startLogAuto()
  }

  // ---------------- 通用配置编辑（Nginx / Apache / Caddy 等） ----------------
  const configSupported = ref(false)
  const configModal = reactive<{ open: boolean; runtime: string; version: string; path: string; raw: string }>({
    open: false,
    runtime: '',
    version: '',
    path: '',
    raw: '',
  })

  async function loadConfigSupport(id: string) {
    try {
      configSupported.value = !!unwrap<boolean>(await EnvConfigSupport(id)) && !SPECIAL_CONFIG_UI.includes(id)
    } catch (e) {
      console.warn('[env] 查询配置编辑支持状态失败', e)
      configSupported.value = false
    }
  }

  async function openConfig(r: RuntimeLike, ins: InstallLike) {
    try {
      const cfg = unwrap<any>(await EnvConfigGet(r.id, ins.version))
      configModal.runtime = r.id
      configModal.version = ins.version
      configModal.path = cfg.path || ''
      configModal.raw = cfg.raw || ''
      configModal.open = true
      deps.closeMenu()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  async function saveConfig() {
    try {
      unwrap(await EnvConfigSet(configModal.runtime, configModal.version, configModal.raw))
      deps.onConfigSaved?.() // 保存成功后刷新「已修改」基线
      toast.success(t('saved'))
      configModal.open = false
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  // ---------------- FTP 启动参数（结构化表单，底层仍是 ftpdmin.args） ----------------
  // FTPDMIN 无配置文件，启动参数持久化在 ftpdmin.args（每行一个令牌）。这里把原始 args
  // 解析成 端口/根目录/只读 三个字段，避免用户手改裸文本写错（如 -p abc）只能启动时才发现。
  const ftpModal = reactive<{
    open: boolean
    runtime: string
    version: string
    port: number
    rootDir: string
    readOnly: boolean
    path: string
  }>({
    open: false,
    runtime: '',
    version: '',
    port: 21,
    rootDir: '',
    readOnly: false,
    path: '',
  })

  function parseFtpArgs(raw: string): { port: number; rootDir: string; readOnly: boolean } {
    const knownVal = ['-p', '-tp', '-ha']
    const tokens: string[] = []
    for (const line of raw.split('\n')) {
      const l = line.trim()
      if (!l) continue
      const i = l.indexOf(' ')
      if (i > 0 && knownVal.includes(l.slice(0, i))) {
        tokens.push(l.slice(0, i), l.slice(i + 1).trim())
      } else {
        tokens.push(l)
      }
    }
    let port = 21
    let rootDir = ''
    let readOnly = false
    for (let k = 0; k < tokens.length; k++) {
      const tk = tokens[k]
      if (tk === '-p') {
        const v = parseInt(tokens[k + 1], 10)
        if (!isNaN(v)) port = v
        k++
      } else if (tk === '-g') {
        readOnly = true
      } else if (!tk.startsWith('-')) {
        rootDir = tk
      }
    }
    return { port, rootDir, readOnly }
  }

  function serializeFtpArgs(a: { port: number; rootDir: string; readOnly: boolean }): string {
    const lines = ['-p', String(a.port), a.rootDir]
    if (a.readOnly) lines.push('-g')
    return lines.join('\n') + '\n'
  }

  async function openFTPConfig(r: RuntimeLike, ins: InstallLike) {
    try {
      const cfg = unwrap<any>(await EnvConfigGet(r.id, ins.version))
      const parsed = parseFtpArgs(cfg.raw || '')
      ftpModal.runtime = r.id
      ftpModal.version = ins.version
      ftpModal.path = cfg.path || ''
      ftpModal.port = parsed.port
      ftpModal.rootDir = parsed.rootDir
      ftpModal.readOnly = parsed.readOnly
      ftpModal.open = true
      deps.closeMenu()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  async function saveFTPConfig() {
    try {
      const raw = serializeFtpArgs({
        port: ftpModal.port,
        rootDir: ftpModal.rootDir,
        readOnly: ftpModal.readOnly,
      })
      unwrap(await EnvConfigSet(ftpModal.runtime, ftpModal.version, raw))
      toast.success(t('saved'))
      ftpModal.open = false
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  // ---------------- 通用日志弹窗（任意实现了 LogProvider 的运行时） ----------------
  // 筛选 / 高亮 / 跟随末尾在 RuntimeLogView 里，这里只维护「哪条运行时、自动刷新开关」。
  const logModal = reactive<{
    open: boolean
    runtime: string
    version: string
    log: string
    logAuto: boolean
  }>({
    open: false,
    runtime: '',
    version: '',
    log: '',
    logAuto: false,
  })
  let logModalTimer: ReturnType<typeof setInterval> | null = null

  async function openLog(r: RuntimeLike, ins: InstallLike) {
    try {
      logModal.runtime = r.id
      logModal.version = ins.version
      logModal.log = unwrap<string>(await EnvLogGet(r.id, ins.version)) || ''
      logModal.logAuto = false
      logModal.open = true
      deps.closeMenu()
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  async function refreshLogModal() {
    try {
      logModal.log = unwrap<string>(await EnvLogGet(logModal.runtime, logModal.version)) || ''
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }

  function startLogModalAuto() {
    if (logModalTimer || !logModal.logAuto) return
    logModalTimer = setInterval(() => {
      void refreshLogModal()
    }, 2000)
  }
  function stopLogModalAuto() {
    if (logModalTimer) {
      clearInterval(logModalTimer)
      logModalTimer = null
    }
  }
  function toggleLogModalAuto() {
    logModal.logAuto = !logModal.logAuto
    if (logModal.logAuto) {
      void refreshLogModal()
      startLogModalAuto()
    } else stopLogModalAuto()
  }
  function closeLogModal() {
    stopLogModalAuto()
    logModal.open = false
  }

  // 页面卸载时统一停掉两个自动刷新定时器（页面 onUnmounted 调一次即可）
  function stopAllTimers() {
    stopLogAuto()
    stopLogModalAuto()
  }

  return {
    phpModal,
    openPHPConfig,
    toggleExt,
    savePHPConfig,
    goEnvModal,
    loadGoEnvValues,
    switchGoEnvScope,
    openGoEnv,
    toggleGoPath,
    saveGoEnv,
    goProxyPresets,
    applyGoProxy,
    redisModal,
    openRedisConfig,
    refreshRedisLog,
    saveRedisConfig,
    startLogAuto,
    stopLogAuto,
    toggleLogAuto,
    closeRedisModal,
    openLogTab,
    configSupported,
    configModal,
    loadConfigSupport,
    openConfig,
    saveConfig,
    ftpModal,
    openFTPConfig,
    saveFTPConfig,
    logModal,
    openLog,
    refreshLogModal,
    toggleLogModalAuto,
    closeLogModal,
    stopAllTimers,
  }
}
