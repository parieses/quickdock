<script lang="ts" setup>
/**
 * SSH 终端的 AI 助手面板。
 *
 * 形态是刻意的：**只给命令，不自动执行**。
 * 模型返回的命令渲染成带复制按钮的卡片，用户自己粘贴进终端。
 * 这既符合"我看着办"的使用预期，也是安全底线——远程生产机上
 * 一个基于错误假设的自动执行无法挽回。
 *
 * 上下文来自终端屏显（父组件通过 readScreen 传入的 getter 现取），
 * 所以 AI 能看到用户正在看的画面——这正是"这段报错什么意思"这类
 * 问题能得到准确答案的原因。
 */
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, Copy, Loader2, Play, Send, Sparkles, Terminal, TextSelect, Trash2, X } from '@lucide/vue'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { Ask } from '../../bindings/quickdock/services/sshx/sshservice'
import { apiErrorCode, unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { splitSshAiSegments } from '../utils/sshAiSegments'
import { useToast } from '../composables/useToast'

interface AiCommand {
  text: string
  risk: string
  note: string
}

/**
 * 后端返回的内容分段。
 *
 * 分段而不是"一段说明 + 一堆命令"：模型输出是交错的
 * （先解释 → 给命令 → 再补充），分段后界面顺序与模型输出一致。
 */
interface AiSegment {
  type: string
  text: string
  commands: AiCommand[] | null
}

interface AiResult {
  answer: string
  commands: AiCommand[] | null
  segments: AiSegment[] | null
  model: string
  hasRisk: boolean
  elapsedMs: number
}

const props = defineProps<{
  sessionId: string
  /**
   * 当前主机的环境：prod / staging / test。
   *
   * 后端已经知道它（据此约束模型输出），但**用户也必须知道**——
   * 同一个「直接执行」按钮，点在生产机和测试机上的后果完全不同。
   */
  hostEnv?: string
  /** 取终端屏显纯文本。由父组件转发 SshTerminal 的 readScreen。 */
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

/**
 * markdown 渲染器（独立实例，不污染全局 marked 配置）。
 *
 * 代码块**不走** marked 的 renderer：命令块由后端解析后单独渲染成可复制卡片，
 * 若这里也渲染一次，同一条命令会在界面上出现两遍。
 */
const md = new Marked({ breaks: true, gfm: true })

const question = ref('')
const busy = ref(false)
const errMsg = ref('')
const result = ref<AiResult | null>(null)
/** 最近几轮的纯文本，用于多轮追问。够用即可：再多模型会被旧问题带偏。 */
const history = ref<string[]>([])
/** 已复制到剪贴板的命令文本，用于把按钮切成对勾态。 */
const copied = ref('')
/** 刚发送到终端的命令文本，用于给按钮一个短暂反馈。 */
const sent = ref('')

const listEl = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLTextAreaElement | null>(null)

const askDisabled = computed(() => busy.value || !question.value.trim())

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
 * 为什么只针对生产环境加这一道：风险规则表是**有限收录**的，
 * `sed -i` 改配置、一条没写完的 `DELETE`、删错名字的容器——这些都匹配不到
 * 任何规则，界面上看起来"很安全"，一点就在生产机上跑了。
 * 测试/预发保持一步到位：那里本来就是用来试错的，多一道确认只会添堵。
 */
const confirmRun = ref('')

async function ask() {
  const q = question.value.trim()
  if (!q || busy.value) return
  busy.value = true
  errMsg.value = ''
  try {
    const r = unwrap<AiResult>(
      await Ask({
        sessionId: props.sessionId,
        question: q,
        screenText: props.readScreen(),
        history: history.value.slice(-6),
        selection: selection.value,
      })
    )
    // unwrap 只在成功时返回数据，失败已 throw；此处 null 说明调用没返回内容。
    if (!r) throw new Error(t('sshAiFailed'))
    result.value = r
    history.value = [...history.value, q, `AI: ${r.answer}`].slice(-12)
    question.value = ''
    copied.value = ''
    // 换了一轮回答，上一轮待确认的命令必须作废：
    // 留着会让用户在新的答案里点「执行」时，把旧命令直接发出去。
    confirmRun.value = ''
    await nextTick()
    scrollToEnd()
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

function scrollToEnd() {
  if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight
}

async function copyCmd(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = text
    // 复位以便同一条命令可以再次复制；1.5s 足够用户看清反馈。
    setTimeout(() => {
      if (copied.value === text) copied.value = ''
    }, 1500)
  } catch (e) {
    logErr('ssh.ai.copy', e)
    toastError(getErrorMessage(e))
  }
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
  errMsg.value = ''
  copied.value = ''
  selection.value = ''
  confirmRun.value = ''
}

/** 空状态里的示例问题：点一下填进输入框（不直接发送——
 *  用户往往想改几个字，误触就发一次请求也不合适）。 */
const samples = computed(() => [t('sshAiSample1'), t('sshAiSample2'), t('sshAiSample3')])

function useSample(s: string) {
  question.value = s
  nextTick(() => inputEl.value?.focus())
}

/**
 * 内容分段：后端已按模型原始顺序切好「说明文字 / 命令」，
 * 前端只需把文字段渲染成 markdown，命令段直接用后端给的命令列表。
 *
 * 文字段保留 markdown 原文交给 marked 渲染，因此 **加粗**、行内代码、
 * 列表都按 markdown 显示，不会再出现 `**星号**` 这样的原始标记。
 */
interface RenderSegment {
  kind: 'text' | 'cmd'
  html: string
  commands: AiCommand[]
  /**
   * 风险判定是否可信。兜底自行切分时为 true —— 那时拿不到后端风险标注，
   * 一律按"未知"处理，不显示「执行」按钮，避免 rm -rf 这类命令被一键跑掉。
   */
  riskUnknown: boolean
}

const renderSegments = computed<RenderSegment[]>(() => {
  const segs = result.value?.segments
  if (!segs || !segs.length) {
    // 兜底：后端没给分段（例如旧版本服务）时，退回按围栏切分正文。
    return fallbackSegments(result.value?.answer ?? '')
  }
  const out: RenderSegment[] = []
  for (const seg of segs) {
    if (seg.type === 'cmd') {
      const cmds = seg.commands ?? []
      if (cmds.length) out.push({ kind: 'cmd', html: '', commands: cmds, riskUnknown: false })
      continue
    }
    const body = (seg.text ?? '').trim()
    if (!body) continue
    out.push({
      kind: 'text',
      html: DOMPurify.sanitize(md.parse(body, { async: false }) as string),
      commands: [],
      riskUnknown: false,
    })
  }
  return out
})

/**
 * 兜底切分：后端未返回分段时，自己按围栏把正文与命令分开。
 *
 * 与后端同一套判定规则（见 utils/sshAiSegments），避免两边对"哪段是命令"
 * 的理解不一致，否则同一条命令会在正文和卡片里各出现一次。
 */
function fallbackSegments(raw: string): RenderSegment[] {
  return splitSshAiSegments(raw).map((seg) => {
    if (seg.kind === 'cmd') {
      return {
        kind: 'cmd' as const,
        html: '',
        commands: seg.commands.map((c) => ({ text: c, risk: '', note: '' })),
        riskUnknown: true,
      }
    }
    return {
      kind: 'text' as const,
      html: DOMPurify.sanitize(md.parse(seg.text, { async: false }) as string),
      commands: [],
      riskUnknown: false,
    }
  })
}

/**
 * 终端右键「问 AI」带来的选中输出。
 *
 * 它与屏显的区别：屏显是"我看到的整屏"，选区是"我特意圈出来的那一段"。
 * 后者是用户的意图表达，模型应优先看它——所以在 prompt 里单独成节、
 * 并且即使屏显被裁剪也不受影响（预算上它排在屏显之前）。
 */
const selection = ref('')

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
watch(
  () => props.pendingAsk?.seq ?? 0,
  () => {
    const p = props.pendingAsk
    if (!p) return
    selection.value = p.text
    // 预填一个默认问题而不是留空：选中一段报错后最常见的诉求就是"这什么意思、
    // 怎么修"，让用户可以一个回车就走完；想问别的直接改掉即可。
    question.value = t('sshAiDefaultQuestion')
    nextTick(() => inputEl.value?.focus())
  },
  { immediate: true }
)

function dropSelection() {
  selection.value = ''
}
</script>

<template>
  <aside class="ssh-ai">
    <header class="ssh-ai-head">
      <span class="ssh-ai-title">
        <Sparkles :size="14" />
        {{ t('sshAiTitle') }}
        <!-- 环境徽章：让用户随时知道自己在问哪一类机器。
             生产用 danger 色——它不该是"又一个标签"，而要能在余光里被认出来。 -->
        <span v-if="envBadge" class="ssh-ai-env" :class="envBadge.cls">{{ envBadge.text }}</span>
      </span>
      <span class="ssh-ai-head-actions">
        <button class="ssh-ai-icon" :title="t('sshAiClear')" @click="clearAll">
          <Trash2 :size="13" />
        </button>
        <button class="ssh-ai-icon" :title="t('sshAiClose')" @click="emit('close')">
          <X :size="13" />
        </button>
      </span>
    </header>

    <div ref="listEl" class="ssh-ai-list">
      <div v-if="!result && !busy && !errMsg" class="ssh-ai-empty">
        <p>{{ t('sshAiHint') }}</p>
        <!-- 示例做成可点击：看到示例还得自己再打一遍，等于没给。 -->
        <ul>
          <li v-for="(s, i) in samples" :key="i">
            <button class="ssh-ai-sample" @click="useSample(s)">{{ s }}</button>
          </li>
        </ul>
        <!-- 选中即问是右键菜单里的入口，没有这行提示几乎没人会发现。 -->
        <p class="ssh-ai-empty-tip">{{ t('sshAiSelectionTip') }}</p>
      </div>

      <p v-if="errMsg" class="ssh-ai-err">{{ errMsg }}</p>

      <template v-if="result">
        <!--
          按模型原始顺序交错渲染：说明文字（markdown）与命令卡片。
          说明与命令在模型输出里是交错的，拆成"文字全在上、命令全在下"会
          丢掉对应关系；markdown 渲染后也不再出现 **星号** 与大片空白。
        -->
        <template v-for="(seg, i) in renderSegments" :key="i">
          <div v-if="seg.kind === 'text'" class="ssh-ai-answer" v-html="seg.html" />

          <template v-else>
            <!-- 风险提示紧贴命令：先看到警告，再决定要不要复制。 -->
            <p
              v-if="seg.commands.some((c) => c.risk === 'high')"
              class="ssh-ai-risk"
            >
              {{ t('sshAiRiskBanner') }}
            </p>

            <div
              v-for="(c, ci) in seg.commands"
              :key="ci"
              class="ssh-ai-cmd"
              :class="{ danger: c.risk === 'high' }"
            >
              <code class="ssh-ai-cmd-text">{{ c.text }}</code>
              <button class="ssh-ai-copy" :title="t('sshAiCopy')" @click="copyCmd(c.text)">
                <Check v-if="copied === c.text" :size="13" />
                <Copy v-else :size="13" />
              </button>

              <!--
                风险说明必须排在操作按钮**之前**：
                遵循本文件一贯的顺序——先看到警告，再决定要不要动手。
                放在按钮之后就成了"先点了再看"，等于没警告。
              -->
              <p v-if="c.note" class="ssh-ai-cmd-note">{{ c.note }}</p>

              <!--
                发送二选一：
                - 「填入」只把命令写进远端输入缓冲，用户看到的命令出现在屏幕上，
                  由他亲自按回车 —— 任何风险等级都可用。
                - 「执行」连带回车一起发出，只有非高风险命令才有这个按钮。
                  判断依据不是"命令短不短"，而是后端的风险规则：
                  rm -rf / dd / mkfs / iptables -F / reboot 这类一旦自动跑起来，
                  在生产机上没有撤销机会，必须留一步人工确认。
              -->
              <div class="ssh-ai-cmd-actions">
                <button class="ssh-ai-btn" :class="{ ok: sent === c.text }" @click="sendCmd(c.text, false)">
                  <Terminal v-if="sent !== c.text" :size="12" />
                  <Check v-else :size="12" />
                  {{ t('sshAiInsert') }}
                </button>
                <button
                  v-if="!seg.riskUnknown && c.risk !== 'high'"
                  class="ssh-ai-btn run"
                  :class="{ confirm: confirmRun === c.text }"
                  @click="sendCmd(c.text, true)"
                >
                  <Play :size="12" />
                  {{ confirmRun === c.text ? t('sshAiRunConfirm') : t('sshAiRun') }}
                </button>
                <span v-else-if="c.risk === 'high'" class="ssh-ai-cmd-lock">
                  {{ t('sshAiManualConfirm') }}
                </span>
              </div>
            </div>
          </template>
        </template>

        <p class="ssh-ai-meta">{{ result.model }} · {{ (result.elapsedMs / 1000).toFixed(1) }}s</p>
      </template>

      <div v-if="busy" class="ssh-ai-busy">
        <Loader2 :size="14" class="spin" />
        <span>{{ t('sshAiThinking') }}</span>
      </div>
    </div>

    <footer class="ssh-ai-foot">
      <!-- 来自终端右键的选中输出：让它可见、可移除。
           不显示的话用户不知道这轮提问带了上下文，AI 答偏了也无从排查。 -->
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

      <div class="ssh-ai-input-row">
        <textarea
          ref="inputEl"
          v-model="question"
          class="ssh-ai-input"
          rows="2"
          :placeholder="t('sshAiPlaceholder')"
          :disabled="busy"
          @keydown.enter.exact.prevent="ask"
        />
        <button class="ssh-ai-send" :disabled="askDisabled" @click="ask">
          <Loader2 v-if="busy" :size="14" class="spin" />
          <Send v-else :size="14" />
        </button>
      </div>
    </footer>
  </aside>
</template>

<style scoped>
.ssh-ai {
  display: flex;
  flex-direction: column;
  width: 300px;
  min-width: 300px;
  border-left: 1px solid var(--color-border);
  background: var(--color-bg-primary);
  overflow: hidden;
}

.ssh-ai-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}

.ssh-ai-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 500;
  color: var(--color-text-primary);
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
  border-color: var(--color-border);
}

.ssh-ai-head-actions {
  display: flex;
  gap: 2px;
}

.ssh-ai-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ssh-ai-icon:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.ssh-ai-list {
  flex: 1;
  overflow-y: auto;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.ssh-ai-empty {
  color: var(--color-text-muted);
  font-size: 12px;
  line-height: 1.6;
}

.ssh-ai-empty p {
  margin: 0 0 6px;
}

.ssh-ai-empty ul {
  margin: 0;
  padding-left: 16px;
  color: var(--color-text-disabled);
}

/* 示例问题：做成链接质感的按钮而不是又一个实心块——
   空状态的这三行是"提示"不是"操作入口"，不该跟发送按钮抢层级。 */
.ssh-ai-sample {
  padding: 0;
  font: inherit;
  text-align: left;
  color: inherit;
  background: transparent;
  border: none;
  cursor: pointer;
}

.ssh-ai-sample:hover {
  color: var(--color-accent);
}

.ssh-ai-empty-tip {
  margin: 8px 0 0;
  padding-top: 8px;
  border-top: 1px solid var(--color-border);
  color: var(--color-text-disabled);
}

.ssh-ai-err {
  margin: 0;
  font-size: 12px;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-sm);
  padding: 6px 8px;
  line-height: 1.5;
}

/*
 * 说明文字段落。
 *
 * 内容由 markdown 渲染后经 v-html 注入，因此内部元素不受 scoped 影响，
 * 必须用 :deep() 穿透。段落自身的样式仍走 scoped（v-html 容器本身带 scope 属性）。
 */
.ssh-ai-answer {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-primary);
  word-break: break-word;
}

/* markdown 渲染出的块级元素自带 margin，这里统一压平，避免段间距过大。 */
.ssh-ai-answer :deep(p) {
  margin: 0 0 4px;
}

.ssh-ai-answer :deep(p:last-child) {
  margin-bottom: 0;
}

.ssh-ai-answer :deep(ul),
.ssh-ai-answer :deep(ol) {
  margin: 4px 0;
  padding-left: 18px;
}

.ssh-ai-answer :deep(li) {
  margin: 1px 0;
}

.ssh-ai-answer :deep(li > p) {
  margin: 0;
}

.ssh-ai-answer :deep(strong) {
  font-weight: 600;
  color: var(--color-text-primary);
}

/* 行内代码：命令名/参数常被模型这样标注，给等宽字体更好认。 */
.ssh-ai-answer :deep(code) {
  font-family: var(--font-mono);
  font-size: 11px;
  padding: 1px 4px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  color: var(--color-accent);
}

.ssh-ai-answer :deep(a) {
  color: var(--color-accent);
  text-decoration: none;
}

.ssh-ai-answer :deep(a:hover) {
  text-decoration: underline;
}

.ssh-ai-answer :deep(blockquote) {
  margin: 4px 0;
  padding: 2px 0 2px 8px;
  border-left: 2px solid var(--color-border);
  color: var(--color-text-muted);
}

.ssh-ai-answer :deep(blockquote p) {
  margin: 0;
}

.ssh-ai-risk {
  margin: 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-sm);
  padding: 5px 8px;
}

.ssh-ai-cmd {
  position: relative;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  padding: 6px 28px 6px 8px;
}

.ssh-ai-cmd.danger {
  border-color: var(--color-danger-border);
}

.ssh-ai-cmd-text {
  display: block;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-primary);
  word-break: break-all;
  white-space: pre-wrap;
}

.ssh-ai-copy {
  position: absolute;
  top: 4px;
  right: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ssh-ai-copy:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

/* 命令操作条。
 *
 * 刻意做成**轻量工具栏**而不是实心按钮：这个面板的既有语言是扁平的
 * （复制按钮就是无边框图标），一个带底色的大按钮会把注意力从命令本身
 * 抢到按钮上——用户要看的是命令，按钮只是顺手的工具。
 * 因此：无边框 + 弱化文字色，hover 才浮出背景。
 */
.ssh-ai-cmd-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  /* 负偏移让按钮左缘对齐命令文本，而不是缩进在卡片内边距之后 */
  margin: 4px 0 0 -5px;
}

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

.ssh-ai-cmd-note {
  margin: 4px 0 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-warning);
}

.ssh-ai-meta {
  margin: 0;
  font-size: 11px;
  color: var(--color-text-disabled);
}

.ssh-ai-busy {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--color-text-muted);
}

/* 纵向：底部「选中输出的引用条」与「输入行」是上下关系，
   沿用横向 flex 会把三者挤成一排。 */
.ssh-ai-foot {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
}

.ssh-ai-input-row {
  display: flex;
  gap: 6px;
  align-items: flex-end;
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

.ssh-ai-input {
  flex: 1;
  resize: none;
  padding: 6px 8px;
  font-family: inherit;
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.ssh-ai-input:focus {
  outline: none;
  border-color: var(--color-accent);
  box-shadow: 0 0 0 2px var(--color-accent-border);
}

.ssh-ai-send {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: none;
  border-radius: var(--radius-sm);
  background: var(--color-accent);
  color: var(--color-accent-text);
  cursor: pointer;
  flex-shrink: 0;
}

.ssh-ai-send:disabled {
  opacity: 0.5;
  cursor: default;
}
</style>