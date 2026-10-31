// useEnvOllama — Ollama 模型库管理（列表 / 拉取 / 删除 / 运行中 / 版本检测 / 官方模型库联想）
//
// 从 EnvironmentPage 的 script 里原样搬出来的第一块：这里动的是「模型库」
// （几十 GB，全局共享一份，默认 ~/.ollama/models），与 runtime/ollama/<version>
// 下的「程序版本」互不越界——删程序版本不碰模型，删模型也不动程序。
//
// 只把两个跟页面强绑定的东西作为参数传进来：
//   selectedId —— 切分类时才拉列表（模型列表不参与 3s 轮询）
//   baseURL    —— 供 OpenAI 兼容客户端填的地址，端口要从「当前运行版本」推，
//                 那段逻辑依赖页面的 runtimes 状态，留在页面侧算好再传
import { computed, ref, watch } from 'vue'
import type { Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from './useToast'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logWarn } from '../utils/logger'
import { copyToClipboard } from '../utils/clipboard'
import {
  EnvOllamaModels,
  EnvOllamaRunningModels,
  EnvOllamaPullModel,
  EnvOllamaDeleteModel,
  EnvOllamaSearchLibrary,
  EnvOllamaCheckUpdate,
} from '../../bindings/quickdock/services/env/environmentservice'

export interface OllamaModel {
  name: string
  size: number
  modified_at: string
  details: { family: string; parameter_size: string; quantization_level: string }
}
export interface OllamaRunningModel { name: string; size: number; size_vram: number; expires_at: string }
export interface OllamaUpdateInfo { installed: string; latest: string; hasUpdate: boolean }

export interface EnvOllamaDeps {
  /** 当前选中的分类 id */
  selectedId: Ref<string>
  /** 当前 Ollama 的 OpenAI 兼容 Base URL（端口依赖页面侧的运行时状态） */
  baseURL: () => string
}

export function useEnvOllama(deps: EnvOllamaDeps) {
  const { t } = useI18n()
  const toast = useToast()

  const ollamaModels = ref<OllamaModel[]>([])
  const ollamaRunning = ref<OllamaRunningModel[]>([])
  const ollamaLoading = ref(false)
  const ollamaErr = ref('')
  const ollamaPullName = ref('')
  const ollamaPulling = ref(false)
  // ollamaProgress 拉取进度。status 是 Ollama 的协议文案（pulling manifest / downloading…），
  // 与界面语言无关，原样展示不做翻译。
  const ollamaProgress = ref<{ percent: number; completed: number; total: number; status: string } | null>(null)
  const ollamaDelTarget = ref<OllamaModel | null>(null)

  // 与页面里的 newestUpdate 不是一回事：那个比的是「受管目录里已装的最高版本 vs 上游」，
  // 而 Ollama 常常是用官方安装包/包管理器装的、根本不在 runtime 目录下，
  // 于是 newestUpdate 对它永远显示「未安装」。EnvOllamaCheckUpdate 走的是
  // DetectInstalledVersion()（系统里实际生效的那个），两套判定各管一段，别合并。
  const ollamaUpdate = ref<OllamaUpdateInfo | null>(null)
  const ollamaChkBusy = ref(false)

  async function checkOllamaUpdate() {
    if (ollamaChkBusy.value) return
    ollamaChkBusy.value = true
    try {
      ollamaUpdate.value = unwrap<OllamaUpdateInfo>(await EnvOllamaCheckUpdate()) ?? null
    } catch (e) {
      logWarn('env/ollamaUpdate', e)
      ollamaUpdate.value = null
    } finally {
      ollamaChkBusy.value = false
    }
  }

  // ---- 拉取输入框的官方模型库联想 ----
  // 数据源 ollama.com/api/tags（支持 ?q=），非本机已装模型——已装的不需要再拉。
  const ollamaLibModels = ref<{ name: string; size: number }[]>([])
  const ollamaLibOpen = ref(false)
  const ollamaLibLoading = ref(false)
  let ollamaLibTimer: ReturnType<typeof setTimeout> | null = null
  // 请求序号：慢的旧响应回来时若已发起新请求，直接丢弃，避免候选列表被旧结果覆盖。
  let ollamaLibSeq = 0

  /** 拉取候选。空关键字返回官方热门（默认排序）。 */
  async function searchOllamaLibrary(kw: string) {
    const seq = ++ollamaLibSeq
    ollamaLibLoading.value = true
    try {
      const ms = unwrap<{ name: string; size: number }[]>(await EnvOllamaSearchLibrary(kw)) || []
      if (seq !== ollamaLibSeq) return // 已有更新的请求，丢弃本次结果
      ollamaLibModels.value = ms
    } catch {
      if (seq === ollamaLibSeq) ollamaLibModels.value = []
    } finally {
      if (seq === ollamaLibSeq) ollamaLibLoading.value = false
    }
  }

  /** 输入防抖 300ms 后查询，避免逐字符打满官方接口。 */
  function onOllamaPullInput() {
    if (ollamaLibTimer) clearTimeout(ollamaLibTimer)
    ollamaLibTimer = setTimeout(() => searchOllamaLibrary(ollamaPullName.value.trim()), 300)
  }

  /** 聚焦即展开；首次展开时拉一次热门，让空输入也有候选可点。 */
  function openOllamaLib() {
    ollamaLibOpen.value = true
    if (!ollamaLibModels.value.length) searchOllamaLibrary(ollamaPullName.value.trim())
  }

  function pickOllamaLib(name: string) {
    ollamaPullName.value = name
    ollamaLibOpen.value = false
  }

  function closeOllamaLibSoon() {
    // 延迟关闭：否则 mousedown 选中项时列表已被 blur 收起，点击落空。
    setTimeout(() => { ollamaLibOpen.value = false }, 150)
  }

  // 端口取实际侦听端口（用户可能在 ollama.env 改过 OLLAMA_HOST），未取到时回退 11434；
  // 末尾带 /v1 是因为 AI 助手等客户端按 OpenAI 协议接入。
  const ollamaBaseURL = computed(() => deps.baseURL())

  async function copyOllamaText(text: string) {
    if (!text) return
    if (await copyToClipboard(text)) {
      toast.success(t('copied'))
    } else {
      toast.error(t('copyFailed'))
    }
  }

  function copyOllamaModel(name: string) { copyOllamaText(name) }

  /** 把字节格式化成人类可读体积。模型都以 GB 计，小于 1 GB 才降到 MB。 */
  function ollamaSize(n: number): string {
    if (!n) return '—'
    const gb = n / 1024 ** 3
    return gb >= 1 ? gb.toFixed(2) + ' GB' : (n / 1024 ** 2).toFixed(0) + ' MB'
  }
  function ollamaTime(s: string): string {
    if (!s) return '—'
    const d = new Date(s)
    return isNaN(d.getTime()) ? s : d.toLocaleString()
  }

  /**
   * 拉取模型列表与运行中模型。未装/未启动时后端返回明确文案，
   * 落在 ollamaErr 里就地展示，不弹 toast 打扰（切到这个分类就会看到，属预期状态而非异常）。
   */
  async function loadOllama() {
    ollamaLoading.value = true
    ollamaErr.value = ''
    try {
      const [ms, rs] = await Promise.all([EnvOllamaModels(), EnvOllamaRunningModels()])
      ollamaModels.value = unwrap<OllamaModel[]>(ms) || []
      ollamaRunning.value = unwrap<OllamaRunningModel[]>(rs) || []
    } catch (e) {
      ollamaModels.value = []
      ollamaRunning.value = []
      ollamaErr.value = getErrorMessage(e)
    } finally {
      ollamaLoading.value = false
    }
  }

  /**
   * 在服务启停/更新后刷新模型面板。模型/运行中列表不参与轮询，
   * 只在切分类或显式调用时重取——否则启停 Ollama 后要「切走再切回」才刷新。
   * delay 给刚启动的服务留出就绪时间（启动过程 ollama serve 需数百 ms 起监听）。
   */
  function refreshOllamaPanel(delay = 0) {
    const run = () => {
      if (deps.selectedId.value !== 'ollama') return
      loadOllama()
    }
    if (delay > 0) setTimeout(run, delay)
    else run()
  }

  /** 拉取模型：后端异步执行、立即返回，进度经事件推送。 */
  async function pullOllamaModel() {
    const name = ollamaPullName.value.trim()
    if (!name || ollamaPulling.value) return
    ollamaPulling.value = true
    ollamaErr.value = ''
    ollamaProgress.value = { percent: 0, completed: 0, total: 0, status: '' }
    try {
      unwrap(await EnvOllamaPullModel(name))
    } catch (e) {
      // 请求本身没发出去（如服务未启动）才算失败；发出去了就等事件收尾。
      ollamaPulling.value = false
      ollamaProgress.value = null
      ollamaErr.value = getErrorMessage(e)
    }
  }

  async function deleteOllamaModel() {
    const m = ollamaDelTarget.value
    if (!m) return
    try {
      unwrap(await EnvOllamaDeleteModel(m.name))
      toast.success(t('ollamaDeleted', { name: m.name }))
      ollamaDelTarget.value = null
      await loadOllama()
    } catch (e) {
      toast.error(getErrorMessage(e))
    }
  }

  /** 拉取进度事件。Wails v3 的 payload 在 e.data。 */
  function onOllamaPull(e: any) {
    const p = e?.data ?? e
    if (!p || typeof p !== 'object') return
    if (p.stage === 'progress') {
      ollamaProgress.value = {
        percent: p.percent || 0,
        completed: p.completed || 0,
        total: p.total || 0,
        status: p.status || '',
      }
      return
    }
    // done / error 都是本次拉取的终态
    ollamaPulling.value = false
    ollamaProgress.value = null
    if (p.stage === 'done') {
      toast.success(t('ollamaPullDone', { name: p.model || '' }))
      ollamaPullName.value = ''
      loadOllama()
    } else {
      toast.error(p.message || t('ollamaPullFailed'))
    }
  }

  watch(deps.selectedId, (id) => {
    if (id === 'ollama') { loadOllama(); void checkOllamaUpdate() }
  })

  return {
    ollamaModels, ollamaRunning, ollamaLoading, ollamaErr,
    ollamaPullName, ollamaPulling, ollamaProgress, ollamaDelTarget,
    ollamaUpdate, ollamaChkBusy,
    ollamaLibModels, ollamaLibOpen, ollamaLibLoading, ollamaBaseURL,
    checkOllamaUpdate, searchOllamaLibrary, onOllamaPullInput,
    openOllamaLib, pickOllamaLib, closeOllamaLibSoon,
    copyOllamaText, copyOllamaModel, ollamaSize, ollamaTime,
    loadOllama, refreshOllamaPanel, pullOllamaModel, deleteOllamaModel, onOllamaPull,
  }
}
