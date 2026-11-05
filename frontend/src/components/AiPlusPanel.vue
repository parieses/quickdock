<script setup lang="ts">
/**
 * 「MCP / 技能 / 提示词」级联面板：输入框左下角 + 号打开。
 *
 * 两个输入框（新建任务的 hero、对话里的 composer）都要这一份，所以独立成组件：
 * 各自的栏位不同（hero 那行已经有空间 / 模型两个下拉），但弹出来的内容完全一致。
 *
 * 布局是**两个独立的浮层**，不是同一个 flex 容器的两列：
 *   - 左：四个分类，高度只由自己这四项决定，位置永远贴着输入框不动；
 *   - 右：子面板，固定高度 + 内部滚动，悬停左列切换它的内容。
 * 为什么不并排放一个盒子里：那样盒子的高度会跟着右列内容变，而盒子是 bottom
 * 定位向上长的，右列一变高，左列四项就被一起顶到上面去——鼠标刚划到「提示词」
 * 上，分类行就跑了，还会误触另一项。两个浮层各自定位，左列就再也动不了。
 *
 * 四组的作用域**不一样**，交互也就不一样，别看成同一套：
 *   - 引用：点一下 = 挂一枚 @ 引用 chip，与在输入框里手打 @ 选文件**完全等价**
 *     （同一个 pickMention、同一份后端展开逻辑）。列表分「本对话的文件」和
 *     「工作目录」两段，后者就是 AISuggestMentions 的原样结果。
 *   - 技能：点一下 = 在输入框里插一枚行内技能胶囊。选择状态只有那份胶囊数组
 *     （见 useAiSlashSkill），所以行尾不是勾选框，只有一个「已选」文字标记；
 *     再点一次即摘掉。
 *   - 提示词：点一下 = 把正文填进输入框（普通文本，插完还能改）。
 *   - 连接器：开关 = 「这台机器上启用这个 server」，是全局配置（mcp.json 的 disabled）。
 *
 * 定位靠最近的 position:relative 祖先（hero 的 .ai-hero-box、composer 的 .ai-input-area），
 * 两个容器都已经满足，这里只管往上弹。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { AtSign, ChevronRight, FileText, FolderPlus, MessageSquare, Network, Search, Settings2 } from '@lucide/vue'
import type { AIConnector, AIMentionItem, AIPrompt, AISkill } from '../types/ai'

// 四个列表都给默认值：模板里直接读 `props.prompts.length`，一旦某个父组件漏传
// （或者 dev 热更新时组件实例还是旧的、新 prop 尚未注入），就会在这里抛 TypeError，
// 表现是**整个面板白掉**——连没动过的技能分组也一起消失，看起来像「技能没了」。
const props = withDefaults(defineProps<{
  skills: AISkill[]
  connectors: AIConnector[]
  // 提示词库：内置 + 自建，点一下把正文填进输入框（不是胶囊，插完可继续改）
  prompts: AIPrompt[]
  // 输入框里已经写进去的技能名（由父组件从文本解析出来）
  picked: string[]
  // 引用（@）：本对话出现过的文件，与工作目录候选（AISuggestMentions 的原样结果）。
  // 分两组的意义：产物是本轮刚碰过的，通常就是要接着问的那个；工作目录那组与
  // 手打 @ 看到的列表同源，用来补「不在本对话里」的文件。
  files: AIMentionItem[]
  cwdFiles: AIMentionItem[]
  // 已经挂上 chip 的 path：面板里标「已引用」，再点一次摘掉（与技能胶囊同口径）
  pickedFiles: string[]
}>(), {
  skills: () => [],
  connectors: () => [],
  prompts: () => [],
  picked: () => [],
  files: () => [],
  cwdFiles: () => [],
  pickedFiles: () => [],
})

const emit = defineEmits<{
  (e: 'pick-skill', sk: AISkill): void
  (e: 'pick-prompt', p: AIPrompt): void
  (e: 'pick-file', it: AIMentionItem): void
  (e: 'toggle-connector', c: AIConnector): void
  (e: 'install-skill'): void
  (e: 'manage-connectors'): void
  (e: 'manage-prompts'): void
  (e: 'close'): void
}>()

const { t } = useI18n()

/** 当前展开的分类。恒有值：面板打开就有内容，不用先猜点哪儿。 */
type PlusSection = 'file' | 'connector' | 'skill' | 'prompt'
// 默认停在「引用」：这一项跟着当前对话走，是四类里唯一每次打开内容都不一样的，
// 也是 + 号最常被用来做的事（把刚生成 / 刚改过的文件接进下一句提问）。
const section = ref<PlusSection>('file')

/**
 * 产物路径是绝对路径，AISuggestMentions 给的是相对 cwd 的路径，同一个文件
 * 两组里字符串并不相等。按「尾串」判同，避免列表里出现两行一样的 README.md。
 */
function sameFile(a: AIMentionItem, b: AIMentionItem): boolean {
  const pa = (a.path || '').replace(/\\/g, '/')
  const pb = (b.path || '').replace(/\\/g, '/')
  if (!pa || !pb) return false
  return pa === pb || pa.endsWith('/' + pb) || pb.endsWith('/' + pa)
}

const cwdFiles = computed(() =>
  props.cwdFiles.filter((c) => !props.files.some((f) => sameFile(f, c))),
)

/** 已引用：chip 里的 path 与列表项也可能一个是绝对路径一个是相对路径。 */
function isFilePicked(it: AIMentionItem): boolean {
  return props.pickedFiles.some((q) => sameFile({ kind: 'file', name: '', path: q }, it))
}

// 数量写在分类右边：不展开也知道那一类里有没有东西。图标全用项目里已在用的，
// 不引新图标名（lucide 各版本导出名有出入，拼错是运行时空组件）。
const navItems = computed(() => [
  { key: 'file' as const, icon: AtSign, label: t('aiPlusFilesGroup'), count: props.files.length + cwdFiles.value.length },
  { key: 'connector' as const, icon: Network, label: t('aiConnectorsGroup'), count: props.connectors.length },
  { key: 'skill' as const, icon: FileText, label: t('aiSkillsGroup'), count: props.skills.length },
  { key: 'prompt' as const, icon: MessageSquare, label: t('aiPromptsGroup'), count: props.prompts.length },
])

// —— 子面板搜索 ——
const query = ref('')
const searchEl = ref<HTMLInputElement | null>(null)

const searchPlaceholder = computed(() => ({
  file: t('aiPlusSearchFiles'),
  connector: t('aiPlusSearchConnectors'),
  skill: t('aiPlusSearchSkills'),
  prompt: t('aiPlusSearchPrompts'),
}[section.value]))

/** 一次过滤四组：切换分类时 query 会清空，另外几组的计算结果只是留着不用。 */
const shown = computed(() => {
  const q = query.value.trim().toLowerCase()
  const has = (...parts: (string | undefined)[]) =>
    !q || parts.some((p) => (p || '').toLowerCase().includes(q))
  const matchFile = (f: AIMentionItem) => has(f.name, f.path)
  return {
    connectors: props.connectors.filter((c) => has(c.name, c.error)),
    skills: props.skills.filter((s) => has(s.name, s.description, s.dir, s.error)),
    prompts: props.prompts.filter((p) => has(p.name, p.content, p.category)),
    convFiles: props.files.filter(matchFile),
    cwdFiles: cwdFiles.value.filter(matchFile),
  }
})

function preview(content: string): string {
  const flat = (content || '').replace(/\s+/g, ' ').trim()
  return flat.length > 60 ? flat.slice(0, 60) + '…' : flat
}

function isPicked(sk: AISkill): boolean {
  return (props.picked || []).includes(sk.name)
}

/**
 * 悬停切换留 100ms 缓冲。
 *
 * + 号在左下、子面板在右上，鼠标从按钮斜着走到右列时会穿过左列的好几行；
 * 没有延迟，每经过一行就把右列内容换一次，等鼠标真到右边时看到的可能已经不是
 * 刚才想点的那一类。停留满 100ms 才算「我想看这个」，快速掠过不改。
 * focus / click 仍然立刻切，键盘用户不该等这个缓冲。
 */
let hoverTimer: number | null = null
function onNavEnter(key: PlusSection) {
  cancelHover()
  hoverTimer = window.setTimeout(() => {
    section.value = key
    hoverTimer = null
  }, 100)
}
function cancelHover() {
  if (hoverTimer !== null) {
    window.clearTimeout(hoverTimer)
    hoverTimer = null
  }
}
/** 键盘 focus / 点击是明确意图：立刻切，并撤掉还在排队的悬停切换。 */
function selectNow(key: PlusSection) {
  cancelHover()
  section.value = key
}

/** 首字方块：三类的条目没有图标字段，用中性色块给列表一个对齐的左边界。 */
function badge(name: string): string {
  return (name || '?').trim().slice(0, 1).toUpperCase()
}

/**
 * 子面板高度跟着「输入框上方还剩多少空间」算。
 *
 * 写死 vh 不准：hero 页的输入框在窗口中部，composer 页贴着底部，两者上方
 * 差好几百像素；按大的给会顶出窗口（.ai-main 有 overflow:hidden，直接被切），
 * 按小的给又让 13 条提示词永远要滚。
 *
 * 子面板是 bottom 定位、向上生长且高度固定，下边缘不随内容变化，所以量它到
 * 视口顶部的距离就是可用高度，切分类 / 搜索都不用重算。
 */
const popEl = ref<HTMLElement | null>(null)
const listEl = ref<HTMLElement | null>(null)
const panelH = ref(0)

function measurePanelHeight() {
  const el = popEl.value
  if (!el) return
  const bottom = el.getBoundingClientRect().bottom
  if (!bottom || bottom < 0) return
  // 顶部留 12px 呼吸；上限 520 让提示词一屏能看全，下限 200 是极端矮窗口的兜底
  panelH.value = Math.max(200, Math.min(520, Math.floor(bottom - 12)))
}

// 换分类：清掉上一类的搜索词，列表滚回顶部。否则从 13 条提示词切到 3 个连接器，
// 会停在一个空白的滚动位置，用户以为「东西没了」。
watch(section, () => {
  query.value = ''
  nextTick(() => { if (listEl.value) listEl.value.scrollTop = 0 })
})

/**
 * Esc：搜索框里有字先清空，否则收起面板。
 *
 * 用 capture 是为了抢在输入框自己的 keydown 之前拿到——面板开着的时候，
 * 用户按 Esc 想关的是面板，不是输入框里的东西。
 *
 * 输入法组词时必须放行（isComposing / keyCode 229）：中文用户按 Esc 取消候选，
 * 顺手把面板关掉就等于「换个字，结果列表重新翻一遍」。
 */
function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (e.isComposing || e.keyCode === 229) return
  if (e.target === searchEl.value && query.value) {
    query.value = ''
    e.stopPropagation()
    return
  }
  emit('close')
  e.stopPropagation()
}

onMounted(() => {
  nextTick(measurePanelHeight)
  window.addEventListener('resize', measurePanelHeight)
  window.addEventListener('keydown', onKeydown, true)
})
onBeforeUnmount(() => {
  cancelHover()
  window.removeEventListener('resize', measurePanelHeight)
  window.removeEventListener('keydown', onKeydown, true)
})
</script>

<template>
  <!-- 透明遮罩：只负责「点外面就收起」。两个面板 z-index 比它高，内部点击不受影响。 -->
  <div class="ai-plus-backdrop" @click="emit('close')"></div>

  <!-- 左：一级分类。独立浮层，高度只由这四项决定，切分类时位置纹丝不动。 -->
  <div class="ai-plus-navpanel" @mouseleave="cancelHover">
    <button
      v-for="it in navItems"
      :key="it.key"
      class="ai-plus-nav"
      :class="{ 'is-active': section === it.key }"
      @mouseenter="onNavEnter(it.key)"
      @focus="selectNow(it.key)"
      @click="selectNow(it.key)"
    >
      <component :is="it.icon" :size="13" class="ai-plus-nav-icon" />
      <span class="ai-plus-nav-name">{{ it.label }}</span>
      <span class="ai-plus-nav-count">{{ it.count }}</span>
      <ChevronRight :size="13" class="ai-plus-nav-arrow" />
    </button>
  </div>

  <!-- 右：子面板。固定高度，搜索框 / 列表 / 底部操作三段互不挤占。 -->
  <div
    ref="popEl"
    class="ai-plus-detail"
    :style="panelH ? { height: panelH + 'px' } : undefined"
    @mouseenter="cancelHover"
  >
    <div class="ai-plus-search">
      <Search :size="13" class="ai-plus-search-icon" />
      <input
        ref="searchEl"
        v-model="query"
        class="ai-plus-search-input"
        :placeholder="searchPlaceholder"
        spellcheck="false"
      />
    </div>

    <div ref="listEl" class="ai-plus-list">
      <!-- 引用：本对话碰过的文件在前，工作目录候选在后（与手打 @ 同源）。
           点任一条 = 走 pickMention 挂 chip，发送时由后端把内容展开进上下文。 -->
      <template v-if="section === 'file'">
        <template v-if="shown.convFiles.length">
          <div class="ai-plus-group">{{ t('aiPlusConvFiles') }}</div>
          <button
            v-for="f in shown.convFiles"
            :key="'conv-' + (f.path || f.name)"
            class="ai-plus-item"
            :class="{ 'is-picked': isFilePicked(f) }"
            :title="f.path || f.name"
            @click="emit('pick-file', f)"
          >
            <span class="ai-plus-badge">{{ badge(f.name) }}</span>
            <span class="ai-plus-item-body">
              <span class="ai-plus-item-name">{{ f.name }}</span>
              <span class="ai-plus-item-desc">{{ f.path || f.name }}</span>
            </span>
            <span v-if="isFilePicked(f)" class="ai-plus-state">{{ t('aiPlusFilePicked') }}</span>
          </button>
        </template>
        <template v-if="shown.cwdFiles.length">
          <div class="ai-plus-group">{{ t('aiPlusCwdFiles') }}</div>
          <button
            v-for="f in shown.cwdFiles"
            :key="'cwd-' + (f.path || f.name)"
            class="ai-plus-item"
            :class="{ 'is-picked': isFilePicked(f) }"
            :title="f.path || f.name"
            @click="emit('pick-file', f)"
          >
            <span class="ai-plus-badge" :class="{ 'is-dir': f.kind === 'dir' }">{{ badge(f.name) }}</span>
            <span class="ai-plus-item-body">
              <span class="ai-plus-item-name">{{ f.name }}</span>
              <span class="ai-plus-item-desc">
                {{ f.path || f.name }}<template v-if="f.kind === 'dir'"> · {{ t('aiPlusDirTag') }}</template>
              </span>
            </span>
            <span v-if="isFilePicked(f)" class="ai-plus-state">{{ t('aiPlusFilePicked') }}</span>
          </button>
        </template>
        <div v-if="!shown.convFiles.length && !shown.cwdFiles.length" class="ai-plus-empty">
          {{ query ? t('aiPlusNoMatch') : t('aiPlusFilesEmpty') }}
        </div>
      </template>

      <!-- MCP 连接器 -->
      <template v-else-if="section === 'connector'">
        <button
          v-for="c in shown.connectors"
          :key="c.name"
          class="ai-plus-item"
          :class="{ 'is-bad': c.status === 'error' }"
          :title="c.error || c.name"
          @click="emit('toggle-connector', c)"
        >
          <span class="ai-plus-badge">{{ badge(c.name) }}</span>
          <span class="ai-plus-item-body">
            <span class="ai-plus-item-name">{{ c.name }}</span>
            <span class="ai-plus-item-desc">
              {{ c.status === 'error' ? (c.error || t('aiConnectorErr')) : t('aiConnectorTools', { n: c.toolCount }) }}
            </span>
          </span>
          <span class="ai-plus-switch" :class="{ 'is-on': !c.disabled }"></span>
        </button>
        <div v-if="!shown.connectors.length" class="ai-plus-empty">
          {{ query ? t('aiPlusNoMatch') : t('aiConnectorsEmpty') }}
        </div>
      </template>

      <!-- 技能 -->
      <template v-else-if="section === 'skill'">
        <button
          v-for="sk in shown.skills"
          :key="sk.dir"
          class="ai-plus-item"
          :class="{ 'is-picked': isPicked(sk), 'is-bad': !!sk.error }"
          :title="sk.error || sk.path"
          @click="emit('pick-skill', sk)"
        >
          <span class="ai-plus-badge">{{ badge(sk.name) }}</span>
          <span class="ai-plus-item-body">
            <span class="ai-plus-item-name">/{{ sk.name }}</span>
            <span class="ai-plus-item-desc">{{ sk.error || sk.description || sk.dir }}</span>
          </span>
          <span v-if="isPicked(sk)" class="ai-plus-state">{{ t('aiSkillPicked') }}</span>
        </button>
        <div v-if="!shown.skills.length" class="ai-plus-empty">
          {{ query ? t('aiPlusNoMatch') : t('aiSkillsEmpty') }}
        </div>
      </template>

      <!-- 提示词 -->
      <template v-else>
        <button
          v-for="p in shown.prompts"
          :key="p.id"
          class="ai-plus-item"
          :title="p.content"
          @click="emit('pick-prompt', p)"
        >
          <span class="ai-plus-badge">{{ badge(p.name) }}</span>
          <span class="ai-plus-item-body">
            <span class="ai-plus-item-name">{{ p.name }}</span>
            <span class="ai-plus-item-desc">{{ preview(p.content) }}</span>
          </span>
        </button>
        <div v-if="!shown.prompts.length" class="ai-plus-empty">
          {{ query ? t('aiPlusNoMatch') : t('aiPromptsEmpty') }}
        </div>
      </template>
    </div>

    <!-- 底部操作固定在面板底部：列表再长也不把它顶出可视区 -->
    <div class="ai-plus-actions">
      <div v-if="section === 'file'" class="ai-plus-hint">{{ t('aiPlusFileHint') }}</div>
      <button v-else-if="section === 'connector'" class="ai-plus-link" @click="emit('manage-connectors')">
        <Settings2 :size="12" /> {{ t('aiManageConnectors') }}
      </button>
      <button v-else-if="section === 'skill'" class="ai-plus-link" @click="emit('install-skill')">
        <FolderPlus :size="12" /> {{ t('aiInstallSkill') }}
      </button>
      <button v-else class="ai-plus-link" @click="emit('manage-prompts')">
        <Settings2 :size="12" /> {{ t('aiManagePrompts') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.ai-plus-backdrop {
  position: fixed;
  inset: 0;
  z-index: 40;
}

/* —— 左：分类浮层 ——
   只写 bottom、不写 height：高度就是四项内容高，右子面板怎么变都影响不到它。 */
.ai-plus-navpanel {
  position: absolute;
  left: 12px;
  bottom: calc(100% - 4px);
  z-index: 41;
  width: 148px;
  max-width: calc(100% - 24px);
  padding: 5px;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}

.ai-plus-nav {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 8px 6px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 12.5px;
  text-align: left;
  cursor: pointer;
}

.ai-plus-nav:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

/* 当前展开的那一类：底色常驻。没有这个高亮，用户会以为右列内容是「上面那行」的 */
.ai-plus-nav.is-active {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.ai-plus-nav-icon { flex: none; color: var(--color-text-muted); }

.ai-plus-nav:hover .ai-plus-nav-icon,
.ai-plus-nav.is-active .ai-plus-nav-icon { color: var(--color-accent); }

.ai-plus-nav-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-plus-nav-count {
  flex: none;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--color-text-disabled);
}

.ai-plus-nav.is-active .ai-plus-nav-count { color: var(--color-text-muted); }

.ai-plus-nav-arrow { flex: none; color: var(--color-text-disabled); }

/* —— 右：子面板 ——
   独立浮层贴在左列右边。高度由 measurePanelHeight 写进行内 style，
   这里给的是首帧兜底，避免测量前闪一下。 */
.ai-plus-detail {
  position: absolute;
  left: 166px; /* 12 + 148 + 6：左列右缘再留一道缝 */
  bottom: calc(100% - 4px);
  z-index: 41;
  display: flex;
  flex-direction: column;
  width: 300px;
  max-width: calc(100% - 178px);
  height: 320px;
  padding: 5px 0 0;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  overflow: hidden;
}

/* 搜索框：固定在顶部，不跟着列表滚 */
.ai-plus-search {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: none;
  margin: 0 8px 5px;
  padding: 5px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}

.ai-plus-search:focus-within { border-color: var(--color-border-focus-soft); }

.ai-plus-search-icon { flex: none; color: var(--color-text-muted); }

.ai-plus-search-input {
  flex: 1;
  min-width: 0;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: 12px;
}

.ai-plus-search-input::placeholder { color: var(--color-text-disabled); }

/* 列表：唯一可滚动的区域。min-height:0 不能省——flex 子项默认不肯矮于内容，
   少了它整列会顶穿面板，被外层 overflow:hidden 直接裁掉。 */
.ai-plus-list {
  flex: 1;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
  padding: 0 5px 4px;
  scrollbar-gutter: stable;
}

/* 两行式条目：名称一行、说明一行。比单行「名字 + 截断描述」好认，
   提示词和技能的描述本来就比名字长。 */
.ai-plus-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 8px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.ai-plus-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-plus-badge {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 18px;
  height: 18px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-active);
  color: var(--color-text-secondary);
  font-size: 10px;
  font-weight: 600;
}

/* 目录与文件在 @ 候选里混排，给方块一点区分（描述里也带了「目录」字样） */
.ai-plus-badge.is-dir {
  border-radius: 3px;
  background: var(--color-bg-hover);
  color: var(--color-accent);
}

/* 引用列表的分组标题：两组一样式的条目挨在一起时，不写清哪组是哪组会看串 */
.ai-plus-group {
  padding: 7px 8px 3px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.04em;
  color: var(--color-text-disabled);
}

.ai-plus-item-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.ai-plus-item-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
  color: var(--color-text-primary);
}

.ai-plus-item-desc {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 10.5px;
  line-height: 1.4;
  color: var(--color-text-muted);
}

.ai-plus-item.is-bad .ai-plus-item-desc { color: var(--color-danger); }

.ai-plus-item.is-picked .ai-plus-item-name { color: var(--color-accent); }

/* 已选只是文字标记：选择本身是输入框上方那排胶囊（点一下挂上/摘下），
   这里再放一个勾选框，就会出现两套「选了什么」要对账。 */
.ai-plus-state {
  flex: none;
  font-size: 10px;
  color: var(--color-accent);
}

.ai-plus-empty {
  padding: 12px 10px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-disabled);
}

/* 底部操作：不参与列表滚动 */
.ai-plus-actions {
  flex: none;
  padding: 4px 5px 5px;
  border-top: 1px solid var(--color-border);
}

/* 「引用」这组没有可跳转的设置页，底部改成一句等价性说明 */
.ai-plus-hint {
  padding: 5px 8px;
  font-size: 10.5px;
  line-height: 1.4;
  color: var(--color-text-disabled);
}

.ai-plus-link {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 6px 8px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 11.5px;
  cursor: pointer;
}

.ai-plus-link:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

/* 开关：自绘而不是 <input type=checkbox>，与全站其它 toggle 观感一致；
   整行是按钮，点哪儿都切换。 */
.ai-plus-switch {
  flex: none;
  width: 26px;
  height: 15px;
  border-radius: 999px;
  background: var(--color-bg-active);
  position: relative;
  transition: background-color var(--transition-fast);
}

.ai-plus-switch::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 11px;
  height: 11px;
  border-radius: 50%;
  background: var(--color-text-disabled);
  transition: transform var(--transition-fast), background-color var(--transition-fast);
}

.ai-plus-switch.is-on { background: var(--color-accent); }

.ai-plus-switch.is-on::after { transform: translateX(11px); background: #fff; }
</style>
