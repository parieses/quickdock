<script setup lang="ts">
/**
 * 工具审批卡：浮在输入框上方、盖住输入框，而不是挂在消息流里。
 *
 * 原来审批卡是流式工具卡片内部的一块 `<div class="ai-approval">`，位置跟着消息走：
 * 工具调用一多、或者用户往上翻了两屏，按钮就不在视野里了。而这期间整个会话是
 * 停住的（后端在等这个答复），「找不到按钮」的直接体感就是「卡死」。
 *
 * 现在做成卡片式选择菜单（标题 → 目标/命令 → 编号选项）：这期间输入框本来就不可用，
 * 直接盖上去既省地方，又让「现在只能做这个选择」这件事一目了然。
 * 消息流里仍保留「等待确认」的状态字样，只是不再放按钮：两处各有一套按钮，
 * 点了一处另一处的状态同步容易慢半拍，反而让人以为没点上。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowRight } from '@lucide/vue'
import type { AIToolCall } from '../types/ai'
import { toolTarget, toolVerb as aiToolVerb } from '../utils/aiTools'

const props = defineProps<{
  call: AIToolCall
  /** 同时待审的条数。>1 时标出来，否则点完一条又冒出一条会以为是没生效 */
  count?: number
}>()

const emit = defineEmits<{
  (e: 'allow'): void
  (e: 'remember'): void
  (e: 'deny'): void
  (e: 'open-rules'): void
}>()

const { t } = useI18n()

/** i18n 的 t 由本组件提供，工具动词的翻译在这里接上 */
function toolVerb(name: string): string {
  return aiToolVerb(name, t)
}

/** 参数对象：解析失败给空对象——审批卡不能因为模型写了坏 JSON 就整块渲染崩掉 */
const args = computed<Record<string, any>>(() => {
  try {
    const v = JSON.parse(props.call.args || '{}')
    return v && typeof v === 'object' ? v : {}
  } catch {
    return {}
  }
})

/** 「当前命令」只对真的在执行命令的工具显示：其余工具显示目标是文件路径，套「命令」是错的 */
const command = computed(() => {
  const c = args.value.command
  if (typeof c === 'string' && c.trim()) return c.trim()
  return ''
})

/** 目标：path / url / pattern / query…（toolTarget 已按优先级挑好并缩短路径） */
const target = computed(() => (command.value ? '' : toolTarget(props.call)))

interface Opt {
  id: 'allow' | 'remember' | 'deny'
  label: string
  danger?: boolean
  run: () => void
}

/** 编号按实际渲染顺序生成：高危工具没有「本次会话都允许」，硬留空号会让人以为少了一项 */
const opts = computed<Opt[]>(() => {
  const list: Opt[] = [
    {
      id: 'allow',
      label: t('aiApprovalAllowVerb', { verb: toolVerb(props.call.name) }),
      run: () => emit('allow'),
    },
  ]
  if (props.call.rememberable) {
    list.push({
      id: 'remember',
      label: props.call.grantScope
        ? t('aiApprovalRememberScope', { scope: props.call.grantScope })
        : t('aiApprovalRemember'),
      run: () => emit('remember'),
    })
  }
  list.push({ id: 'deny', label: t('aiApprovalDeny'), danger: true, run: () => emit('deny') })
  return list
})

/** 当前高亮项（鼠标移入也跟着动，键盘与鼠标共用同一套「选中」语义） */
const active = ref(0)

const cardEl = ref<HTMLElement | null>(null)

// 换一条待审（或 rememberable 变了导致项数变化）时收起高亮，否则可能指向一个不存在的项
watch(
  () => [props.call.id, opts.value.length],
  () => { active.value = 0 },
)

function runAt(i: number) {
  const o = opts.value[i]
  if (o) o.run()
}

/**
 * 数字键 1/2/3 直选、上下移动、回车确认。
 *
 * 只在「焦点不在输入控件里」时接管：审批期间主输入框是 disabled 的，但侧栏搜索框、
 * 设置页输入框仍可能被点到，那里敲数字不能变成「允许执行」。
 */
function onKey(e: KeyboardEvent) {
  const el = document.activeElement as HTMLElement | null
  const tag = (el?.tagName || '').toLowerCase()
  // 真·文本输入处一律不接管，否则在别处打字会变成「批准」
  if (tag === 'input' || tag === 'textarea' || el?.isContentEditable) return

  const n = Number(e.key)
  if (Number.isInteger(n) && n >= 1 && n <= opts.value.length) {
    e.preventDefault()
    runAt(n - 1)
    return
  }
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    const len = opts.value.length
    active.value = (active.value + (e.key === 'ArrowDown' ? 1 : len - 1)) % len
    return
  }
  if (e.key === 'Enter') {
    // 焦点在按钮/链接上时 Enter 是「点它」，不能再顺手批准一次
    if (tag === 'button' || tag === 'a' || tag === 'select') return
    e.preventDefault()
    runAt(active.value)
  }
}

// 挂载即接管：这个组件本身就是「有条目待审才存在」，生命周期天然等于
// 「键盘该生效的时间段」，不需要额外开关。
onMounted(() => {
  window.addEventListener('keydown', onKey)
  // 抢焦点做两件事：① Tab 能从卡片开始走；② 不抢的话焦点可能停在别处输入框，
  // 那时数字键会被我们跳掉，卡片看起来「键盘没反应」。
  cardEl.value?.focus()
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <div ref="cardEl" class="ai-approve-card" tabindex="-1">
    <div class="ai-approve-head">
      <div class="ai-approve-title">
        {{ t('aiApprovalAskTitle', { verb: toolVerb(call.name) }) }}
        <span v-if="(count || 0) > 1" class="ai-approve-count">{{ t('aiApprovalProgress', { n: count }) }}</span>
      </div>
      <button type="button" class="ai-approve-rules" @click="emit('open-rules')">{{ t('aiApprovalViewRules') }}</button>
    </div>

    <!-- 目标 / 命令：先让人看清「它在动什么」，再让它选。 -->
    <div v-if="target || command" class="ai-approve-meta">
      <div v-if="target" class="ai-approve-meta-row">
        <span class="ai-approve-meta-k">{{ t('aiApprovalTargetLabel') }}</span>
        <span class="ai-approve-mono">{{ target }}</span>
      </div>
      <div v-if="command" class="ai-approve-meta-row">
        <span class="ai-approve-meta-k">{{ t('aiApprovalCommandLabel') }}</span>
        <span class="ai-approve-mono">{{ command }}</span>
      </div>
    </div>

    <!-- 风险说明排在选项之前：先让人看见「为什么问你」，再决定点哪个。反过来等于让人盲点。 -->
    <div v-if="call.riskNote" class="ai-approve-risk">{{ call.riskNote }}</div>

    <div class="ai-approve-opts">
      <button
        v-for="(o, i) in opts"
        :key="o.id"
        type="button"
        class="ai-approve-opt"
        :class="{ 'is-active': i === active, 'is-danger': o.danger }"
        @mouseenter="active = i"
        @click="o.run()"
      >
        <span class="ai-approve-num">{{ i + 1 }}</span>
        <span class="ai-approve-label">{{ o.label }}</span>
        <ArrowRight v-if="i === active" :size="13" class="ai-approve-arrow" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.ai-approve-card {
  padding: 11px 13px 9px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  /* 与输入框同色（都是 tertiary）：靠边框 + 阴影 + 选中行更亮来分层。
     用更暗的 bg-secondary 会读成「凹进输入框里」，不像弹出来的。 */
  background: var(--color-bg-tertiary);
  box-shadow: var(--shadow-lg);
  animation: ai-approve-in 0.16s ease-out;
  max-height: 60vh;
  overflow: auto;
  /* 卡片会被程序抢焦点（数字键/回车要能直接生效），不能出现一圈焦点描边 */
  outline: none;
}

@keyframes ai-approve-in {
  from { opacity: 0; transform: translateY(6px); }
  to { opacity: 1; transform: none; }
}

.ai-approve-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
}

.ai-approve-title {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
  line-height: 1.4;
}

.ai-approve-count {
  margin-left: 6px;
  font-size: 11px;
  font-weight: 400;
  color: var(--color-text-muted);
}

.ai-approve-rules {
  flex: none;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--color-text-muted);
  font-family: inherit;
  font-size: 11.5px;
  cursor: pointer;
  white-space: nowrap;
}

.ai-approve-rules:hover { color: var(--color-accent); text-decoration: underline; }

.ai-approve-meta {
  margin-top: 7px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--color-text-muted);
}

.ai-approve-meta-row { display: flex; gap: 4px; min-width: 0; }

.ai-approve-meta-k { flex: none; }

.ai-approve-mono {
  min-width: 0;
  font-family: var(--font-mono);
  color: var(--color-text-secondary);
  word-break: break-all;
}

.ai-approve-risk {
  margin-top: 7px;
  font-size: 11.5px;
  line-height: 1.55;
  color: var(--color-warning);
  word-break: break-word;
}

/* 选项：编号 + 文案 + 当前项右侧箭头。整行可点，行内不留按钮框——
   卡片本身就是「正在等你选」的语境，再加三个描边按钮只会更吵。 */
.ai-approve-opts {
  margin-top: 9px;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.ai-approve-opt {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 7px 9px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 12.5px;
  text-align: left;
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-approve-opt.is-active {
  /* 比卡片底色再亮一档（卡片本身是 bg-tertiary），选中项才「跳出来」 */
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}

.ai-approve-opt.is-danger.is-active { color: var(--color-danger); }

.ai-approve-num {
  flex: none;
  width: 12px;
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
  color: var(--color-text-muted);
}

.ai-approve-label { flex: 1; min-width: 0; }

.ai-approve-arrow { flex: none; color: var(--color-text-muted); }
</style>
