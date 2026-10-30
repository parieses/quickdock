<script lang="ts" setup>
/**
 * SSH 终端的 AI 助手面板。
 *
 * 形态是刻意的：只给命令，不自动执行。
 * 模型返回的命令渲染成带复制按钮的卡片，用户自己粘贴进终端。
 * 这既符合"我看着办"的使用预期，也是安全底线——远程生产机上
 * 一个基于错误假设的自动执行无法挽回。
 *
 * 上下文来自终端屏显（父组件通过 readScreen 传入的 getter 现取），
 * 所以 AI 能看到用户正在看的画面——这正是"这段报错什么意思"这类
 * 问题能得到准确答案的原因。
 *
 * 面板骨架与分段渲染在通用组件 AiPanel.vue 里（剪贴板场景共用），
 * 本文件只保留 SSH 特有的部分：环境徽章、终端选区引用条、
 * 「填入终端 / 直接执行」两个动作，以及生产环境的二次确认。
 */
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { Check, Monitor, Play, Terminal, TextSelect, X } from '@lucide/vue'
import { Ask } from '../../bindings/quickdock/services/sshx/sshservice'
import { apiErrorCode, unwrap } from '../utils/api'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useAIAvailability } from '../composables/useAIAvailability'
import { useAiStreamPreference } from '../composables/useAiStreamPreference'
import { useToast } from '../composables/useToast'
import AiPanel, { type AiPanelCommand, type AiPanelResult } from './AiPanel.vue'

const props = defineProps<{
  sessionId: string
  /**
   * 当前主机的环境：prod / staging / test。
   *
   * 后端已经知道它（据此约束模型输出），但用户也必须知道——
   * 同一个「直接执行」按钮，点在生产机和测试机上的后果完全不同。
   */
  hostEnv?: string
  /**
   * 取终端屏显纯文本。由父组件转发 SshTerminal 的 readScreen。
   *
   * 只在用户的问题里提到屏显时才调用（见 wantsScreen）：屏显是整屏画面，
   * 每轮都送既费 token，又会让模型把注意力落到与问题无关的画面上。
   */
  readScreen: () => string
  /**
   * 把内容写入终端输入缓冲。
   * execute=true 时连回车一起发出（远端立即执行）。
   */
  sendToTerm: (data: string, execute: boolean) => void
  /**
   * 终端右键「问 AI」带来的一次性载荷：用户在终端里选中的那段输出。
   * seq 用于区分连续两次选中，null 表示当前没有待办的提问。
   */
  pendingAsk: { text: string; seq: number } | null
}>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const { error: toastError } = useToast()

const busy = ref(false)
const errMsg = ref('')
/** 用户中途点了「停止」：用于丢弃迟到的本轮结果。 */
const cancelled = ref(false)
const result = ref<AiPanelResult | null>(null)
/** 最近几轮的纯文本，用于多轮追问。够用即可：再多模型会被旧问题带偏。 */
const history = ref<string[]>([])
/**
 * 历史摘要：后端把旧轮次压缩后的精炼摘要。
 *
 * SSH 历史是前端内存态（不落库），所以压缩态要在前后端之间传递：
 * 本轮把上一轮的 summary 作为 prevSummary 回传，后端只对「本轮新晋升为旧的」
 * 那一轮做增量摘要，返回新的 summary 存回这里。这样长对话的历史保持有界，
 * 不再随轮数线性膨胀（根治「越问越卡」）。
 */
const summary = ref('')
/** 刚发送到终端的命令文本，用于给按钮一个短暂反馈。 */
const sent = ref('')

const panelRef = ref<InstanceType<typeof AiPanel> | null>(null)

/**
 * 输入框内容的镜像（由通用面板经 update:question 抛上来）。
 *
 * 输入框归通用面板所有，但「这一轮要不要带屏显」必须由本组件判定——
 * 判定依据就是用户写的字，所以在面板里输入时同步一份到这里。
 */
const questionText = ref('')

/**
 * 屏显触发词。
 *
 * 为什么不再每次都送屏显：屏显是整屏画面（上限 3000 rune），而大部分问题
 * （"磁盘还剩多少"、"nginx 怎么重载"）根本不需要看屏幕。每轮都塞一份既费 token，
 * 又会把模型注意力引到无关画面上。用户只要在问题里提一句「屏显」，
 * 就说明他确实想让 AI 看屏幕——这是唯一可靠的意图信号。
 *
 * 判定放在前端而不是后端：screenText 入参的语义就是"要送进 prompt 的屏显"，
 * 由调用方决定带不带、后端拿到就用。这样判定只有一份实现，
 * 上面的「本轮将附带屏显」提示也不会和后端实际行为分叉。
 */
const SCREEN_KEYWORDS = ['屏显', '当前画面', '当前屏幕', '屏幕内容', 'current screen', 'screen']

/** 本轮是否会让 AI 看到屏幕内容。关键字命中即可，后面不必再写别的内容。 */
const wantsScreen = computed(() => {
  const q = questionText.value.toLowerCase()
  return SCREEN_KEYWORDS.some((k) => q.includes(k))
})

function onQuestionInput(v: string) {
  questionText.value = v
}

/** 当前激活的 AI 档案：多 Key 场景下「问 AI 实际用的是哪一个」由它回答。 */
const { profile: activeProfile, model: activeModel, check: checkAI } = useAIAvailability()

/** 回答方式：关掉流式就传空 streamId，后端退化为一次性返回（请求/返回结构不变）。 */
const { streamEnabled } = useAiStreamPreference()

/**
 * 流式增量与它所属的请求。
 *
 * 为什么按 id 过滤：ai:ask-delta 是全局广播的，同一时刻可能还有别处
 * （剪贴板问 AI、另一个 SSH 会话）在流式输出。不过滤就会把别人的内容
 * 拼进自己的回答里——而且看起来就像模型"答串了"。
 */
const liveText = ref('')
const streamId = ref('')
let offDelta: (() => void) | null = null

onMounted(() => {
  void checkAI()
  offDelta = Events.On('ai:ask-delta', (ev: any) => {
    const d = ev?.data ?? ev
    if (!d || !d.id || d.id !== streamId.value) return
    liveText.value += d.delta ?? ''
  })
})
onUnmounted(() => {
  offDelta?.()
  offDelta = null
})

/** 每次提问一个独立 id：复用同一个 id 会让上一轮迟到的增量混进新一轮。 */
function newStreamId(): string {
  return `ssh-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

/**
 * 是否在生产机上提问。
 *
 * 后端用同一个值约束模型的输出口径（见 sshEnvGuidance），前端用它在界面上
 * 加一道确认——两侧读的是同一个事实，不会出现"模型以为在测试机、
 * 界面提示是生产机"这种分叉。
 */
const isProd = computed(() => props.hostEnv === 'prod')

/** 头部环境徽章：只标注已知取值，未标注的机器不硬凑一个标签。 */
const envBadge = computed(() => {
  switch (props.hostEnv) {
    case 'prod':
      return { text: t('sshAiEnvProd'), cls: 'is-prod' }
    case 'staging':
      return { text: t('sshAiEnvStaging'), cls: 'is-staging' }
    case 'test':
      return { text: t('sshAiEnvTest'), cls: 'is-test' }
    default:
      return null
  }
})

/**
 * 生产机上待二次确认的命令。
 *
 * 为什么只针对生产环境加这一道：风险规则表是有限收录的，
 * `sed -i` 改配置、一条没写完的 `DELETE`、删错名字的容器——这些都匹配不到
 * 任何规则，界面上看起来"很安全"，一点就在生产机上跑了。
 * 测试/预发保持一步到位：那里本来就是用来试错的，多一道确认只会添堵。
 */
const confirmRun = ref('')

async function ask() {
  const q = panelRef.value?.getQuestion()?.trim()
  if (!q || busy.value) return
  busy.value = true
  cancelled.value = false
  errMsg.value = ''
  // 新一轮开始：清掉上一轮的增量，换一个 streamId（旧 id 迟到的增量不会再匹配上）。
  liveText.value = ''
  streamId.value = newStreamId()
  try {
    const r = unwrap<AiPanelResult>(
      await Ask({
        sessionId: props.sessionId,
        question: q,
        // 屏显只在问题提到它时才送：没有意图信号就送，等于让模型猜我们在看什么。
        screenText: wantsScreen.value ? props.readScreen() : '',
        history: history.value.slice(-12),
        // 把上一轮的摘要回传，后端据此做增量压缩（见后端 compressSshHistory）。
        prevSummary: summary.value,
        selection: selection.value,
        // 关掉流式就传空：后端据此不推增量，等于一次性返回。
        streamId: streamEnabled.value ? streamId.value : '',
      })
    )
    // 中途点了「停止」：本轮结果已无意义，丢弃即可（Go 侧调用仍在后台跑完，无害）。
    if (cancelled.value) return
    // unwrap 只在成功时返回数据，失败已 throw；此处 null 说明调用没返回内容。
    if (!r) throw new Error(t('sshAiFailed'))
    result.value = r
    // 回存历史摘要：下一场提问作为 prevSummary 回传，长对话的历史因此保持有界。
    summary.value = r.summary ?? ''
    // 按后端报告的 historyKept 收敛本地历史：未压缩时=全部，压缩后=最近若干条。
    // 不这么做本地数组会一直增长；而前端硬编码上限又会和后端 keepRecent 口径漂移。
    const kept = typeof r.historyKept === 'number' ? r.historyKept : history.value.length
    const base = kept > 0 ? history.value.slice(history.value.length - kept) : []
    history.value = [...base, q, `AI: ${r.answer}`].slice(-12)
    panelRef.value?.setQuestion('')
    // 选中输出（右键「问 AI」带来的上下文）随本轮提问一起消费，发送后从引用条移除：
    // 不移除会一直挂在侧栏，既占空间又让用户误以为这轮还没发出。
    selection.value = ''
    // 换了一轮回答，上一轮待确认的命令必须作废：
    // 留着会让用户在新的答案里点「执行」时，把旧命令直接发出去。
    confirmRun.value = ''
    await nextTick()
  } catch (e) {
    logErr('ssh.ai.ask', e)
    errMsg.value = getErrorMessage(e)
    // AI 未配置是很常见的首访状态，给出可点击的引导而不是一句"失败"。
    if (apiErrorCode(e) === 'no_profile' || apiErrorCode(e) === 'no_endpoint') {
      errMsg.value = t('sshAiNotConfigured')
    } else {
      toastError(errMsg.value)
    }
  } finally {
    busy.value = false
  }
}

async function copyCmd(text: string) {
  if (await copyToClipboard(text)) {
    panelRef.value?.markCopied(text)
    return
  }
  toastError(t('copyFailed'))
}

/**
 * 把命令写进终端。
 *
 * execute=false：只填入缓冲，远端 readline 会把命令回显出来但不执行，
 * 用户审视后自己按回车——这是所有命令都有的保底动作。
 *
 * execute=true：连带回车发出。高风险命令由模板挡掉，这里再兜一层，
 * 避免将来有人绕过 CSS/圆角判定直接调。
 */
function sendCmd(text: string, execute: boolean) {
  if (!text) return
  // 生产环境的「执行」分两步：第一次只把按钮切成「再点一次确认」，不发字节。
  // 做成点击态而不是弹确认框——弹框会打断"看命令 → 发送"这条顺手的链路，
  // 而点击态零常驻视觉成本，误点的人会在第二次点之前重新读一遍命令。
  if (execute && isProd.value && confirmRun.value !== text) {
    confirmRun.value = text
    return
  }
  confirmRun.value = ''
  props.sendToTerm?.(text, execute)
  sent.value = text
  window.setTimeout(() => {
    if (sent.value === text) sent.value = ''
  }, 1500)
}

function clearAll() {
  result.value = null
  history.value = []
  summary.value = ''
  errMsg.value = ''
  selection.value = ''
  confirmRun.value = ''
  liveText.value = ''
  streamId.value = ''
  cancelled.value = false
  panelRef.value?.setQuestion('')
}

/**
 * 停止本轮问答。
 *
 * 后端 Go 调用无法从前端真正中断（一次性的 HTTP 请求），但这里先把视觉态复位：
 * 清除「思考中 / 转圈」并作废迟到的增量与结果，让面板不再卡在转圈不动的状态。
 * 后台那次调用会自行跑完（最多 30s 超时），不对用户造成副作用。
 */
function stop() {
  if (!busy.value) return
  cancelled.value = true
  busy.value = false
  liveText.value = ''
  // 清空 streamId：后端仍在推的增量因此匹配不上而被忽略，不会污染下一轮。
  streamId.value = ''
}

/** 空状态里的示例问题：点一下填进输入框（不直接发送——
 *  用户往往想改几个字，误触就发一次请求也不合适）。
 *  sshAiSample4 特意点出「结合当前屏显」：它同时示范了屏显的触发方式——
 *  问题里出现「屏显」，这一轮才会带上正在看的画面，不用自己粘贴终端内容。 */
const samples = computed(() => [t('sshAiSample1'), t('sshAiSample2'), t('sshAiSample3'), t('sshAiSample4')])

/**
 * 终端右键「问 AI」带来的选中输出。
 *
 * 它与屏显的区别：屏显是"我看到的整屏"，选区是"我特意圈出来的那一段"。
 * 后者是用户的意图表达，模型应优先看它——所以在 prompt 里单独成节、
 * 并且即使屏显被裁剪也不受影响（预算上它排在屏显之前）。
 */
const selection = ref('')

/**
 * 传给通用面板的预填内容。
 *
 * 用 prop 而非直接调面板方法：首次右键「问 AI」时，本组件的 watch 先于
 * 通用面板挂载执行，此刻拿不到面板实例（见 draft prop 的注释）。
 * 带 seq 是因为预填文案是固定的，只传字符串时连续两次右键第二次不会触发。
 */
const draft = ref<{ text: string; seq: number } | undefined>(undefined)

/** 选区预览：只给用户看前两行 + 总行数，别把整段报错再铺一遍占满侧栏。 */
const selectionPreview = computed(() => {
  const lines = selection.value.split('\n')
  if (!selection.value) return { lines: 0, head: [] as string[] }
  return {
    lines: lines.length,
    head: lines.slice(0, 2).map((l) => (l.length > 46 ? l.slice(0, 46) + '…' : l)),
  }
})

// immediate: 面板是 v-if 挂载的。首次右键时 aiOpen 由 false 翻 true，
// 面板全新挂载，此时 pendingAsk 已是 { text, seq:1 } 的初始值——
// 不带 immediate 的 watch 不认初始值，会导致「面板打开了但选区没进去」。
// 手动打开面板时 pendingAsk 为 null，回调会提前 return，不会误聚焦。
//
// 预填的默认问题通过 draft prop 传给通用面板，而不是调它的方法——
// 首次右键时本组件的 watch 先于子组件挂载执行，调方法会打在 null 上。
watch(
  () => props.pendingAsk?.seq ?? 0,
  () => {
    const p = props.pendingAsk
    if (!p) {
      // 手动打开面板（没有待处理的选区）：清掉上一轮残留的预填内容，
      // 否则关闭再打开时会凭空多出一句默认问题。
      draft.value = undefined
      return
    }
    selection.value = p.text
    // 预填一个默认问题而不是留空：选中一段报错后最常见的诉求就是"这什么意思、
    // 怎么修"，让用户可以一个回车就走完；想问别的直接改掉即可。
    draft.value = { text: t('sshAiDefaultQuestion'), seq: p.seq }
  },
  { immediate: true }
)

function dropSelection() {
  selection.value = ''
}
</script>

<template>
  <!--
    侧栏容器：宽度与左边框由这里定，通用面板自身不管形态
    （剪贴板场景把它当覆盖弹层用，宽度由那边的容器决定）。
  -->
  <div class="ssh-ai-wrap">
    <AiPanel
      ref="panelRef"
      :title="t('sshAiTitle')"
      :hint="t('sshAiHint')"
      :samples="samples"
      :empty-tip="t('sshAiSelectionTip')"
      :placeholder="t('sshAiPlaceholder')"
      :result="result"
      :busy="busy"
      :err-msg="errMsg"
      :draft="draft"
      :live-text="liveText"
      :stoppable="true"
      @ask="ask"
      @stop="stop"
      @clear="clearAll"
      @close="emit('close')"
      @copy="copyCmd"
      @update:question="onQuestionInput"
    >
      <!-- 环境徽章：让用户随时知道自己在问哪一类机器。
           生产用 danger 色——它不该是"又一个标签"，而要能在余光里被认出来。 -->
      <template #badge>
        <span v-if="envBadge" class="ssh-ai-env" :class="envBadge.cls">{{ envBadge.text }}</span>
        <!-- 当前使用的 AI 档案：配置了多个 Key 时，用户能一眼看出「这一问走的是哪个」。
             模型名放 title 而不是并排显示——300px 侧栏里塞不下两段文字。 -->
        <span
          v-if="activeProfile"
          class="ssh-ai-profile"
          :title="t('aiUsingProfile', { profile: activeProfile, model: activeModel })"
        >{{ activeProfile }}</span>
      </template>

      <!-- 来自终端右键的选中输出：让它可见、可移除。
           不显示的话用户不知道这轮提问带了上下文，AI 答偏了也无从排查。 -->
      <template #context>
        <div v-if="selection" class="ssh-ai-quote">
          <div class="ssh-ai-quote-head">
            <TextSelect :size="12" />
            <span>{{ t('sshAiQuoteLabel', { n: selectionPreview.lines }) }}</span>
            <button class="ssh-ai-quote-x" :title="t('sshAiQuoteDrop')" @click="dropSelection">
              <X :size="11" />
            </button>
          </div>
          <div v-for="(l, i) in selectionPreview.head" :key="i" class="ssh-ai-quote-line">{{ l }}</div>
        </div>
        <!-- 屏显提示：让用户当场知道这一轮会不会把整屏内容发出去。
             不提示的话，"带不带屏显"是个看不见的状态——用户既不知道为什么
             某次答得准、某次答偏，也无从确认自己没把整屏内容送出去。 -->
        <div v-if="wantsScreen" class="ssh-ai-screen-note">
          <Monitor :size="12" />
          <span>{{ t('sshAiScreenOn') }}</span>
        </div>
        <!-- 历史压缩提示：让用户知道更早的轮次已被摘要化、上下文仍在延续，
             而不是以为前面的对话被丢了。 -->
        <div v-if="summary" class="ssh-ai-summary-note">{{ t('sshAiSummaryNote') }}</div>
      </template>

      <!--
        发送二选一：
        - 「填入」只把命令写进远端输入缓冲，用户看到的命令出现在屏幕上，
          由他亲自按回车 —— 任何风险等级都可用。
        - 「执行」连带回车一起发出，只有非高风险命令才有这个按钮。
          判断依据不是"命令短不短"，而是后端的风险规则：
          rm -rf / dd / mkfs / iptables -F / reboot 这类一旦自动跑起来，
          在生产机上没有撤销机会，必须留一步人工确认。
      -->
      <template #cmdActions="{ command, riskUnknown }">
        <button class="ssh-ai-btn" :class="{ ok: sent === command.text }" @click="sendCmd(command.text, false)">
          <Terminal v-if="sent !== command.text" :size="12" />
          <Check v-else :size="12" />
          {{ t('sshAiInsert') }}
        </button>
        <button
          v-if="!riskUnknown && command.risk !== 'high'"
          class="ssh-ai-btn run"
          :class="{ confirm: confirmRun === command.text }"
          @click="sendCmd(command.text, true)"
        >
          <Play :size="12" />
          {{ confirmRun === command.text ? t('sshAiRunConfirm') : t('sshAiRun') }}
        </button>
        <span v-else-if="command.risk === 'high'" class="ssh-ai-cmd-lock">
          {{ t('sshAiManualConfirm') }}
        </span>
      </template>
    </AiPanel>
  </div>
</template>

<style scoped>
/*
 * 侧栏容器：宽度与左边框在此定义。
 * 通用面板自身只管内容，不管形态——剪贴板场景把它当覆盖弹层用。
 */
.ssh-ai-wrap {
  display: flex;
  flex-direction: column;
  width: 300px;
  min-width: 300px;
  border-left: 1px solid var(--color-border);
  overflow: hidden;
}

/*
 * 环境徽章。
 *
 * 生产用 danger 底色，预发用 warning，测试只留灰字——三档差异必须一眼分得开：
 * 徽章的意义是"在余光里被认出来"，如果三档长得差不多，它就只是装饰。
 * 生产上底色而不是纯红字：侧栏很窄，纯文字色在快速扫视时容易被忽略。
 */
.ssh-ai-env {
  padding: 0 4px;
  font-size: 10px;
  line-height: 15px;
  border-radius: var(--radius-xs);
  border: 1px solid transparent;
}

.ssh-ai-env.is-prod {
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
}

.ssh-ai-env.is-staging {
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-color: var(--color-warning-border);
}

.ssh-ai-env.is-test {
  color: var(--color-text-disabled);
  background: var(--color-border);
}

/*
 * 当前 AI 档案徽章。
 *
 * 与环境徽章不同，它用中性色：档案名不是风险信号，只是"这一问走的是哪个 Key"。
 * 给它 accent 色会和「生产环境」抢注意力，反而弱化了真正的告警。
 */
.ssh-ai-profile {
  max-width: 76px;
  padding: 0 4px;
  font-size: 10px;
  line-height: 15px;
  border-radius: var(--radius-xs);
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 终端选区的引用条：让用户看见这轮提问挂了什么上下文。
   只放两行预览——侧栏只有 300px，把整段报错铺一遍会让输入框被挤到看不见。 */
.ssh-ai-quote {
  padding: 5px 7px;
  border: 1px solid var(--color-accent-border);
  border-left: 2px solid var(--color-accent);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}

.ssh-ai-quote-head {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--color-accent);
}

.ssh-ai-quote-x {
  display: flex;
  align-items: center;
  margin-left: auto;
  padding: 1px;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ssh-ai-quote-x:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.ssh-ai-quote-line {
  margin-top: 2px;
  font-family: var(--font-mono);
  font-size: 10.5px;
  line-height: 1.45;
  color: var(--color-text-muted);
  white-space: pre;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 屏显提示：与选区引用条同排，但用中性色——它只是"这轮多带了什么"的说明，
   不是需要用户处理的东西。accent 色留给选区（那是用户主动圈的重点）。 */
.ssh-ai-screen-note {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 3px 6px;
  font-size: 10.5px;
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

/* 历史压缩提示：一行弱化的说明，不抢输入区的注意力。 */
.ssh-ai-summary-note {
  padding: 2px 2px 0;
  font-size: 10.5px;
  line-height: 1.4;
  color: var(--color-text-disabled);
}

/*
 * 命令操作按钮。
 *
 * 这些按钮渲染在通用面板的 #cmdActions 插槽里 —— 插槽内容属于本组件
 * 的作用域，所以样式也归本组件（写在 AiPanel 里会因 scoped 而不生效）。
 * 基础样式（尺寸/字体/边框）与变体（run / ok）必须写在一起：
 * 拆开后基础样式容易被当成"通用面板的"而漏掉，按钮就变成没有样式的裸文字。
 */
.ssh-ai-btn {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 6px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.ssh-ai-btn:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

/* 「执行」是少数会真正改动的动作，用 accent 文字色标出层级即可，
   不必上实心块——它并不比「填入」高一档，只是更省一步。 */
.ssh-ai-btn.run:hover {
  color: var(--color-accent);
}

/*
 * 待确认态（仅生产环境出现）：按钮文案已变成「再点一次确认」，
 * 这里再给一档 warning 底色把它标成"这一步还没发出去"。
 * 用底色而不是只换文字色：文字色变化在 12px 上太弱，
 * 而这一步的代价是命令直接跑在生产机上。
 */
.ssh-ai-btn.run.confirm,
.ssh-ai-btn.run.confirm:hover {
  color: var(--color-warning);
  background: var(--color-warning-bg);
}

.ssh-ai-btn.ok,
.ssh-ai-btn.ok:hover {
  color: var(--color-success);
}

/* 替代「执行」的说明。用 disabled 灰而不是 warning 黄：
   这里不是报错，只是"这类命令留给你自己确认"，不该看起来像出了问题。 */
.ssh-ai-cmd-lock {
  padding-left: 3px;
  font-size: 11px;
  color: var(--color-text-disabled);
}
</style>
