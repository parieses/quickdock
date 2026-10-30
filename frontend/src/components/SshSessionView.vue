<script lang="ts" setup>
/**
 * 单个 SSH 会话视图：终端 + 会话头 + 连接编排。
 *
 * 连接顺序是刻意的（与 SshTerminal 的约定配套）：
 *   挂载终端 → 拿到 cols/rows → 调 Connect → 把 sessionId 交回终端。
 * 必须先挂终端再连：Connect 一返回远端就会吐 banner/提示符，若此时还没挂监听，
 * 首批输出会整段丢掉，用户看到的是一个黑屏，直到他敲了一下键才出现内容。
 *
 * 多标签下的生命周期（#13）：本组件只管连，不管断。
 * 卸载时不 Disconnect —— 切标签走 v-show 不卸载，但父级重建标签栏、
 * 删除主机等场景会卸载，那时直接断线会让其它标签之外的东西莫名断掉。
 * 断开统一由 SshPage 在「用户明确要关这个标签」时调用。
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, RotateCw, Sparkles, Unplug } from '@lucide/vue'
import { Connect, Disconnect, ListSessions, Resize } from '../../bindings/quickdock/services/sshx/sshservice'
import { apiErrorCode, apiErrorData, unwrap } from '../utils/api'
import { useAIAvailability } from '../composables/useAIAvailability'
import { useToast } from '../composables/useToast'
import {
  formatBytes,
  formatDuration,
  isFingerprintError,
  sshErrorText,
  sshTargetFull,
  type SshConnectResult,
  type SshHost,
  type SshHostKeyInfo,
  type SshSessionInfo,
} from '../utils/ssh'
import SshTerminal from './SshTerminal.vue'
import SshAiPanel from './SshAiPanel.vue'
import SshFingerprintDialog from './SshFingerprintDialog.vue'
import SshPasteDialog from './SshPasteDialog.vue'

const { t } = useI18n()
const { error: toastError } = useToast()

const props = defineProps<{
  host: SshHost
  /**
   * 要接回的既有会话（#12）。
   *
   * 有值时不发起新连接，直接把 sessionId 交给终端并回放历史输出——
   * 这就是「关窗不断连」的兑现方式。单行粘贴确认等交互都照常可用，
   * 因为写路径与会话来源无关。
   */
  resume?: SshSessionInfo | null
  /**
   * 是否为当前可见的标签。由父页面在切标签时置位。
   *
   * 切标签后必须把焦点交还给终端：点击标签栏时焦点落在 <button> 上，
   * 终端没有焦点的话用户敲的键全被浏览器吃掉，看起来就是「终端卡死」。
   */
  focused?: boolean
}>()
const emit = defineEmits<{
  /** 连接状态变化：标签栏据此显示转圈 / 清除忙碌态 */
  busy: [busy: boolean]
  /** 拿到 sessionId：标签栏据此在关标签时知道该断哪条会话 */
  session: [sessionId: string]
}>()

type Phase = 'idle' | 'connecting' | 'connected' | 'closed' | 'error'

const termRef = ref<InstanceType<typeof SshTerminal> | null>(null)
const sessionId = ref('')
const phase = ref<Phase>('idle')
const errMsg = ref('')
let lastSize: { cols: number; rows: number } | null = null
/** 防止 onReady 与手动重试并发进入同一次连接 */
let inFlight = false

// 指纹确认状态
const fpVisible = ref(false)
const fpMode = ref<'unknown' | 'changed'>('unknown')
const fpInfo = ref<SshHostKeyInfo>({})

// 多行粘贴确认
const pasteData = ref('')

// 会话元信息（状态条）
const meta = ref<SshSessionInfo | null>(null)
/** 断开原因：区分「用户主动断开」和「远端/网络断开」，两者给的话术不同 */
const endReason = ref<'user' | 'remote' | 'lost' | ''>('')
/** 当前秒，用于让「已连接 3m12s」自己走动 */
const now = ref(Date.now())
let ticker: ReturnType<typeof setInterval> | null = null
let metaTimer: ReturnType<typeof setInterval> | null = null

const stateText = computed(() => {
  switch (phase.value) {
    case 'connecting':
      return t('sshConnecting')
    case 'connected':
      return t('sshConnected')
    case 'closed':
      return endReason.value === 'user' ? t('sshSessionClosedByUser') : t('sshDisconnected')
    case 'error':
      return t('sshConnectFailed')
    default:
      return ''
  }
})

/** 状态条：连接时长。没有 meta（刚建连还没回填）就不显示，避免闪一下。 */
const durationText = computed(() => {
  const started = meta.value?.started
  if (!started || phase.value !== 'connected') return ''
  return t('sshDuration', { time: formatDuration(now.value - started) })
})

/** RTT：0 表示保活还没跑过（默认 45s 一次），如实说「测量中」而不是显示 0ms。 */
const rttText = computed(() => {
  const rtt = meta.value?.rttMs ?? 0
  if (phase.value !== 'connected') return ''
  return rtt > 0 ? t('sshRtt', { ms: rtt }) : t('sshRttPending')
})

const bytesText = computed(() => {
  const n = meta.value?.received ?? 0
  if (!n) return ''
  return t('sshBytes', { size: formatBytes(n) })
})

/** 断开横幅上的解释文案：三种原因给三种说法，不混成一句「连接断开」。 */
const endHint = computed(() => {
  switch (endReason.value) {
    case 'user':
      return ''
    case 'remote':
      return t('sshSessionClosedRemote')
    case 'lost':
      return t('sshReconnectHint')
    default:
      return ''
  }
})

function currentSize() {
  const s = termRef.value?.getSize?.()
  if (s && s.cols > 0 && s.rows > 0) return s
  return lastSize ?? { cols: 80, rows: 24 }
}

async function doConnect() {
  if (inFlight) return
  const size = currentSize()
  inFlight = true
  phase.value = 'connecting'
  errMsg.value = ''
  endReason.value = ''
  emit('busy', true)
  try {
    const res = unwrap<SshConnectResult>(await Connect(props.host.id, size.cols, size.rows))
    if (!res?.sessionId) throw new Error(t('sshConnectFailed'))
    sessionId.value = res.sessionId
    // 父级要靠它才知道「关这个标签该断哪条会话」——
    // 组件内部的 sessionId 出了作用域就没了。
    emit('session', res.sessionId)
    phase.value = 'connected'
    // 立刻回填一次元信息，状态条不必等下一次轮询才有内容。
    meta.value = {
      id: res.sessionId,
      hostId: props.host.id,
      state: 'connected',
      cols: size.cols,
      rows: size.rows,
      received: 0,
      label: `${res.user}@${res.host}:${res.port}`,
      started: Date.now(),
      rttMs: 0,
    }
  } catch (e) {
    const code = apiErrorCode(e)
    const data = apiErrorData<SshHostKeyInfo>(e) || {}
    if (isFingerprintError(code)) {
      // 未知/变更的服务器身份交给弹窗处理；信任后自动重连，
      // 用户不需要「连一次失败 → 再点一次连接」。
      openFingerprint(code === SSH_ERR_HOSTKEY_CHANGED ? 'changed' : 'unknown', data)
      phase.value = 'idle'
      return
    }
    errMsg.value = sshErrorText(e, code, t)
    phase.value = 'error'
  } finally {
    inFlight = false
    emit('busy', false)
  }
}

// 与 utils/ssh.ts 的 SSH_ERR.HOSTKEY_CHANGED 同值；这里单独常量是为了
// 避免为一个比较就把整个常量表引进模板层。
const SSH_ERR_HOSTKEY_CHANGED = 'ssh_hostkey_changed'

function openFingerprint(mode: 'unknown' | 'changed', data: SshHostKeyInfo) {
  fpMode.value = mode
  fpInfo.value = {
    host: data.host || props.host.host,
    port: data.port || props.host.port,
    keyType: data.keyType || '',
    fingerprint: data.newFingerprint || data.fingerprint || '',
    oldFingerprint: data.oldFingerprint || '',
    knownTypes: data.knownTypes || [],
  }
  fpVisible.value = true
}

function onReady(size: { cols: number; rows: number }) {
  lastSize = size
  // 接管既有会话时不能再 Connect：那条 TCP 还活着，重连会多开一条连接，
  // 旧会话变成没人管的孤儿。把 sessionId 交给终端即可，它会回放历史输出。
  if (props.resume?.id) {
    sessionId.value = props.resume.id
    emit('session', props.resume.id)
    meta.value = props.resume
    phase.value = props.resume.state === 'connected' ? 'connected' : 'closed'
    if (phase.value === 'closed') {
      endReason.value = 'lost'
      errMsg.value = t('sshRestoredDisconnected')
    }
    // 尺寸可能变了（用户把窗口拖大过），补一次同步让远端 TUI 重排。
    void syncTerminalSize()
    return
  }
  void doConnect()
}

async function syncTerminalSize() {
  const size = currentSize()
  try {
    unwrap(await Resize(sessionId.value, size.cols, size.rows))
  } catch {
    // 会话可能已经断了；状态事件会负责把 UI 切到 closed，这里静默即可。
  }
}

function onTrusted() {
  fpVisible.value = false
  void doConnect()
}

/** 用户放弃信任指纹：连接没建立，要给出明确结论而不是留一个空白终端。 */
function onFingerprintCancel() {
  fpVisible.value = false
  if (sessionId.value) return
  phase.value = 'error'
  errMsg.value = t('sshHostKeyCanceled')
}

function onState(ev: { sessionId: string; state: string; error?: string; errorCode?: string }) {
  if (!sessionId.value || ev.sessionId !== sessionId.value) return
  // 远端主动断开（用户敲 exit、网络掉线、服务器重启）都归到这里：
  // 会话还在管理器里保留着，这里只改展示，不销毁——画面留在回放缓冲里，
  // 用户可以先看最后输出再决定要不要重连。
  if (ev.state === 'disconnected' || ev.state === 'closed') {
    phase.value = 'closed'
    errMsg.value = ev.error || ''
    // 没有 errorCode 说明是正常的 shell 退出（用户敲了 exit / 登出），
    // 不该被当成故障来提示。
    endReason.value = ev.errorCode ? 'lost' : 'remote'
    stopTicker()
  }
}

/**
 * 低频刷新状态条。
 *
 * RTT 与累计流量都只在服务端算得到，前端没有事件可订阅，所以只能轮询。
 * 5 秒一次足够（这两个数字的变化尺度是「分钟」，不是「秒」），
 * 调更密只是白烧 IPC。
 */
async function refreshMeta() {
  const sid = sessionId.value
  if (!sid || phase.value !== 'connected') return
  try {
    const list = unwrap<SshSessionInfo[]>(await ListSessions())
    if (!list) return
    const hit = list.find(s => s.id === sid)
    // 会话可能刚被移除（用户断开），拿不到就保持原样，不必报错。
    if (hit) meta.value = hit
  } catch {
    // 轮询失败静默：状态事件才是断线的权威来源，这里只是刷新展示数字。
  }
}

async function dropSession() {
  const sid = sessionId.value
  sessionId.value = ''
  stopTicker()
  if (!sid) return
  try {
    unwrap(await Disconnect(sid))
  } catch {
    // 会话可能已经因为断网消失，这里失败不影响用户继续操作
  }
}

async function reconnect() {
  endReason.value = ''
  meta.value = null
  await dropSession()
  void doConnect()
}

/** 主动断开当前标签的会话。会话已关，标签留着让用户看最后的输出。 */
async function onDisconnectClick() {
  endReason.value = 'user'
  await dropSession()
  // phase 必须自己落到 closed。
  //
  // dropSession 在 await Disconnect() 之前就把 sessionId 清空了，于是 Go 侧
  // 随后推来的 state=closed 会被 onState 开头的 sessionId 校验挡掉；少了这一句，
  // 用户点完「断开」界面还会保持绿色「已连接」，按钮也仍是「断开」
  // （再点只会空转），而「重连」入口永远不会出现——等于没有出路。
  phase.value = 'closed'
  emit('session', '')
}

function onTermError(msg: string) {
  toastError(msg)
}

// ---- 多行粘贴确认 ----

function onPaste(data: string) {
  pasteData.value = data
}

function onPasteConfirm() {
  const data = pasteData.value
  pasteData.value = ''
  // 走终端暴露的 send 而不是自己调 Write：按键与粘贴必须共用同一条
  // 串行发送链，否则用户「粘完马上敲键」时两路字节会交叉，远端收到的顺序就乱了。
  termRef.value?.send?.(data)
}

function onPasteCancel() {
  pasteData.value = ''
}

// ---- AI 面板 ----

// 默认收起：300px 对小窗口是实打实的损失，而 AI 是偶发需求。
const aiOpen = ref(false)

/** 转发给 AI 面板的屏显读取：必须在调用时才取，不能缓存——
 *  用户提问前可能刚敲了新命令，缓存的旧屏显会让 AI 答非所问。 */
function readScreen(): string {
  return termRef.value?.readScreen?.() ?? ''
}

/**
 * 转发给 AI 面板的「写入终端」能力。
 *
 * 走终端暴露的 send 而不是另开一条通路：按键、粘贴、AI 发送必须共用同一条
 * 串行输入链，否则用户「发完命令马上敲键」时两路字节会交叉，远端收到的是乱序。
 *
 * execute=false 时把命令写进远端输入缓冲但不发回车：用户在屏幕上能看到这条命令，
 * 由他亲自按回车执行。这是刻意的——AI 建议的命令一旦自动跑起来，
 * 在生产机上没有撤销的机会。
 */
function sendToTerm(data: string, execute: boolean): void {
  const text = execute ? data + '\r' : data
  termRef.value?.send?.(text)
}

/**
 * 终端右键「问 AI」：把选中的输出交给 AI 面板。
 *
 * 两个动作：先开面板，再把选区推给面板。但光"顺序对"还不够，
 * 时序也必须对：两行若在同一个同步块里连续执行，Vue 要到异步 flush 才挂载
 * 面板，而面板的 watch 默认不对初始值触发——挂载时 pendingAsk 早已就位，
 * 那次赋值不构成"变化"，watch 永远不会跑。
 *
 * 表现很隐蔽：面板默认是收起的，所以用户第一次右键时面板刚被打开、
 * 选区却没进去，看到的是空输入框，只会以为右键没生效。
 * 因此这里 await nextTick() 等面板挂载完成再注入。
 *
 * 不能改用 `watch(..., { immediate: true })` 兜底：那样每次点右下角 AI 按钮
 * 重新打开面板时，都会把上一次的旧选区再挂上来。
 *
 * 选区与屏显的关系：两者都进 prompt。屏显是"整屏画面"，选区是"我圈出来的
 * 那一段"，后者权重更高，模型会优先看它（见 Ask 里的 selection）。
 */
async function askAIAbout(selection: string): Promise<void> {
  // 入口前置：没配 Key 就先带去设置页，而不是打开一个注定失败的面板。
  // 这一步必须在 aiOpen 之前——面板一旦打开，用户对着输入框发问才发现没配置。
  if (!(await ai.check())) {
    ai.goToConfig()
    return
  }
  aiOpen.value = true
  // 等这一帧渲染完（面板挂载、watch 注册完毕），再注入一次性载荷。
  await nextTick()
  pendingAsk.value = { text: selection, seq: ++askSeq }
}

/** 右下角 AI 按钮：与右键「问 AI」走同一套前置校验。 */
async function openAiPanel(): Promise<void> {
  if (!(await ai.check())) {
    ai.goToConfig()
    return
  }
  aiOpen.value = true
}

const ai = useAIAvailability()

/** 待注入 AI 面板的提问（右键选中后的一次性载荷）。
 *  带 seq 是为了让用户连续两次选中不同内容时，面板能识别这是新的一次；
 *  seq 只是自增计数器，不需要响应式，故用普通变量（写成 ref 会误导后续维护者
 *  以为界面依赖它的变化）。 */
let askSeq = 0
const pendingAsk = ref<{ text: string; seq: number } | null>(null)

// ---- 状态条时钟 ----

/**
 * 状态条刷新。
 *
 * 时钟 1 秒一跳（让「已连接 3m12s」平滑走动），会话元信息 5 秒一跳
 * （RTT 保活默认 45s 才出一次值，更密地轮询没有新信息）。
 */
function startTicker() {
  stopTicker()
  now.value = Date.now()
  ticker = setInterval(() => {
    now.value = Date.now()
  }, 1000)
  metaTimer = setInterval(() => {
    void refreshMeta()
  }, 5000)
}

function stopTicker() {
  if (ticker !== null) {
    clearInterval(ticker)
    ticker = null
  }
  if (metaTimer !== null) {
    clearInterval(metaTimer)
    metaTimer = null
  }
}

// 被切到前台时把焦点交回终端。
//
// 父页面只负责置位 focused，真正 focus 由本组件做——它才持有终端实例。
// nextTick 是必需的：切标签时 v-show 刚翻转为 true，这一帧 DOM 还不可聚焦。
watch(
  () => props.focused,
  v => {
    if (v) nextTick(() => termRef.value?.focus())
  },
)

// 时钟由 phase 驱动，不能只在 setup 里起一次：
// 断开时会 stopTicker，若不在连上时重启，重连成功后「已连接」时长与 RTT
// 就永久冻结在断开那一刻（用户只能靠关掉标签重开才能恢复）。
watch(phase, p => {
  if (p === 'connected') startTicker()
  else stopTicker()
})

onBeforeUnmount(() => {
  stopTicker()
  // 刻意不断连：多标签下本组件可能因为「删除主机」「重建标签栏」被卸载，
  // 那些场景的断开由 SshPage 统一处理。这里断开会让用户毫不知情地丢掉会话。
  // 真正退出应用时 Go 侧 Shutdown 会关掉全部连接。
  sessionId.value = ''
})

// 父页面在切主机前要问一句「这会断开当前会话吗」——不暴露状态的话，
// 用户误点一下左侧列表就会静默丢掉一条正在跑的命令。
// 已经断开的会话不算「活动」：它只剩回放价值，切走不需要确认。
defineExpose({
  hasSession: () => phase.value === 'connected' || phase.value === 'connecting',
  sessionId: () => sessionId.value,
})
</script>

<template>
  <div class="sess">
    <header class="sess-head">
      <div class="sess-meta">
        <span class="sess-name">{{ host.name || host.host }}</span>
        <span class="sess-target">{{ sshTargetFull(host) }}</span>
      </div>
      <span class="sess-state" :class="`is-${phase}`">
        <i class="sess-dot" />
        {{ stateText }}
      </span>
      <button v-if="phase === 'connected'" class="sess-btn" @click="onDisconnectClick">
        <Unplug :size="13" />
        <span>{{ t('sshDisconnect') }}</span>
      </button>
      <button v-else-if="phase === 'error' || phase === 'closed'" class="sess-btn" @click="reconnect">
        <RotateCw :size="13" />
        <span>{{ t('sshReconnect') }}</span>
      </button>
    </header>

    <!-- 状态条：连接时长 / RTT / 累计流量。空值不渲染，
         免得刚建连时这些字段闪一下再消失。 -->
    <div v-if="durationText || rttText || bytesText" class="sess-status">
      <span v-if="durationText">{{ durationText }}</span>
      <span v-if="rttText">{{ rttText }}</span>
      <span v-if="bytesText">{{ bytesText }}</span>
    </div>

    <!-- 终端 + AI 面板并列。面板默认收起：占掉 300px 对小窗口来说是实打实的损失，
         而 AI 是偶发需求（遇到报错、想不起来某条命令时才用），不该常驻占位。 -->
    <div class="sess-row">
      <div class="sess-body">
        <SshTerminal
          ref="termRef"
          :session-id="sessionId"
          :replay-on-attach="!!resume?.id"
          @ready="onReady"
          @state="onState"
          @error="onTermError"
          @paste="onPaste"
          @askAI="askAIAbout"
        />

        <!-- AI 入口按钮：只在会话已建立时出现 -->
        <button
          v-if="phase === 'connected' && !aiOpen"
          class="sess-ai-fab"
          :title="t('sshAiTitle')"
          @click="openAiPanel"
        >
          <Sparkles :size="15" />
        </button>

        <!-- 连接中：盖住终端，避免用户对着空白屏以为连上了 -->
        <div v-if="phase === 'connecting'" class="sess-overlay">
          <Loader2 :size="18" class="spin" />
          <span>{{ t('sshConnecting') }}</span>
        </div>

        <!-- 连接失败：给出原因，不留死路（可重试） -->
        <div v-else-if="phase === 'error'" class="sess-overlay">
          <div class="sess-card">
            <p class="sess-card-title">{{ t('sshConnectFailed') }}</p>
            <p class="sess-card-msg">{{ errMsg }}</p>
            <div class="sess-card-actions">
              <button class="sess-btn" @click="reconnect">{{ t('sshReconnect') }}</button>
            </div>
          </div>
        </div>

        <!-- 已断开：用底部横条而不是全屏遮罩，让用户还能看到最后的输出 -->
        <div v-else-if="phase === 'closed'" class="sess-banner">
          <div class="sess-banner-text">
            <span>{{ errMsg || stateText }}</span>
            <em v-if="endHint" class="sess-banner-hint">{{ endHint }}</em>
          </div>
          <button class="sess-btn sess-btn--sm" @click="reconnect">{{ t('sshReconnect') }}</button>
        </div>

        <SshPasteDialog
          :visible="!!pasteData"
          :data="pasteData"
          @confirm="onPasteConfirm"
          @cancel="onPasteCancel"
        />
      </div>

      <SshAiPanel
        v-if="aiOpen && phase === 'connected'"
        :session-id="sessionId"
        :host-env="host.env"
        :read-screen="readScreen"
        :send-to-term="sendToTerm"
        :pending-ask="pendingAsk"
        @close="aiOpen = false"
      />
    </div>

    <SshFingerprintDialog
      :visible="fpVisible"
      :mode="fpMode"
      :host="fpInfo.host || ''"
      :port="fpInfo.port || 22"
      :key-type="fpInfo.keyType || ''"
      :fingerprint="fpInfo.fingerprint || ''"
      :old-fingerprint="fpInfo.oldFingerprint || ''"
      :known-types="fpInfo.knownTypes || []"
      @trusted="onTrusted"
      @cancel="onFingerprintCancel"
    />
  </div>
</template>

<style scoped>
.sess {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
  min-height: 0;
  background: var(--color-terminal-bg);
}

.sess-head {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  flex: 0 0 auto;
  padding: var(--space-3) var(--space-5);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.sess-meta {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  flex: 1 1 auto;
}

.sess-name {
  font-size: 12.5px;
  font-weight: 500;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sess-target {
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sess-state {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--color-text-muted);
}

.sess-dot {
  width: 6px;
  height: 6px;
  border-radius: var(--radius-full);
  background: var(--color-text-disabled);
}

.sess-state.is-connecting .sess-dot {
  background: var(--color-warning);
  animation: sess-pulse 1.2s ease-in-out infinite;
}

.sess-state.is-connected .sess-dot {
  background: var(--color-success);
}

.sess-state.is-closed .sess-dot,
.sess-state.is-error .sess-dot {
  background: var(--color-danger);
}

@keyframes sess-pulse {
  50% {
    opacity: 0.25;
  }
}

.sess-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 0 auto;
  padding: var(--space-2) var(--space-5);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast),
    border-color var(--transition-fast);
}

.sess-btn:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
  border-color: var(--color-border-light);
}

.sess-btn--ghost {
  border-color: transparent;
}

.sess-btn--sm {
  padding: 2px var(--space-4);
}

/* 状态条紧贴会话头下沿，不占终端空间。
   字号刻意压到 10.5px：这是「参考信息」不是「当前任务」，
   跟终端正文抢注意力会让密集输出更难读。 */
.sess-status {
  display: flex;
  align-items: center;
  gap: var(--space-5);
  flex: 0 0 auto;
  padding: 3px var(--space-5);
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.sess-body {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
}

/* 终端与 AI 面板的横向容器。min-width: 0 是必需的：
   flex 子项默认 min-width:auto，终端内容过宽会把这一行撑破，
   AI 面板被挤出可视区（表现为面板"消失"而不是布局变窄）。 */
.sess-row {
  display: flex;
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
}

/* 悬浮入口：贴在终端右下角，不占布局空间。 */
.sess-ai-fab {
  position: absolute;
  right: 14px;
  bottom: 14px;
  z-index: 5;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
  cursor: pointer;
}

.sess-ai-fab:hover {
  background: var(--color-bg-hover);
  color: var(--color-accent);
}

.sess-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-4);
  font-size: 12px;
  color: var(--color-text-muted);
  background: var(--color-terminal-bg);
}

.sess-card {
  width: 340px;
  max-width: 80%;
  padding: var(--space-6);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}

.sess-card-title {
  margin: 0 0 var(--space-3);
  font-size: 13px;
  font-weight: 600;
  color: var(--color-danger);
}

.sess-card-msg {
  margin: 0 0 var(--space-6);
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-secondary);
  word-break: break-word;
  user-select: text;
}

.sess-card-actions {
  display: flex;
  gap: var(--space-4);
}

.sess-banner {
  position: absolute;
  left: 50%;
  bottom: var(--space-6);
  display: flex;
  align-items: center;
  gap: var(--space-5);
  padding: var(--space-3) var(--space-6);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  box-shadow: var(--shadow-toast);
  transform: translateX(-50%);
}

.sess-banner-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 11.5px;
  color: var(--color-text-secondary);
}

.sess-banner-hint {
  font-size: 10.5px;
  font-style: normal;
  color: var(--color-text-muted);
}

.spin {
  animation: sess-spin 900ms linear infinite;
}

@keyframes sess-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
