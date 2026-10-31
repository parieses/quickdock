<script setup lang="ts">
// 右侧面板：标签页 + 大预览区（仿常见 AI 工作台）。
//
// 与弹层预览互补——弹层适合看一眼就关，面板适合「边聊边对着产物改」。
// 首页（新标签页）列本任务的全部产物，点文件开成文件标签页；HTML 渲染成
// 页面，文本/代码走 CodeMirror 打开即编辑，图片/二进制只读提示。
//
// 为什么整簇（标签状态 + CodeMirror 生命周期 + 模板 + 样式）一起搬进组件：
// 它们是同一件事的内部实现，留在页面里会与「消息区产物条点击」这类外部入口
// 混在一起；对外只暴露 ensureTab / openArtifact 两个动作。
import { computed, inject, nextTick, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { FileText, FolderOpen, Plus, Save, X } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { AIReadPreviewFile, AISavePreviewFile } from '../../bindings/quickdock/services/ai/aiservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import type { AIArtifact } from '../types/ai'
import type { ToastAPI } from '../types'

const props = defineProps<{
  // 面板开合。状态留在父组件：工具栏按钮的 is-active 与 Ctrl+Shift+B 都要读它
  open: boolean
  // 本任务全部回复的产物合集（父组件按消息算好传入）
  artifacts: AIArtifact[]
}>()
const emit = defineEmits<{ (e: 'update:open', v: boolean): void }>()

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 一次预览读取的结果（后端 AIReadPreviewFile 的 JSON）。
interface AIFilePreview {
  path: string; name: string; size: number; kind: 'text' | 'image' | 'html' | 'binary'
  content?: string; dataUrl?: string; truncated?: boolean; modTime?: string
}

interface SideTab {
  id: number
  kind: 'home' | 'file'
  // file 标签页的标题（文件名）；home 标题走 i18n，不落在这里（语言切换能跟上）
  title: string
  path?: string
  data?: AIFilePreview | null
  loading?: boolean
  // HTML 文件当前视图：false = 渲染页面（默认），true = 源码
  source?: boolean
  // editing = 正在改；draft = 编辑框内容（确认保存前不污染 data.content）
  editing?: boolean
  saving?: boolean
  draft?: string
}

// 面板每次启动都收起：开合是「这次会话要不要边看边聊」的临时决定，
// 不是布局偏好——上次开着不代表这次还想开。
const tabs = ref<SideTab[]>([])
const activeTabId = ref(0)
let tabSeq = 1
const activeTab = computed(() => tabs.value.find((x) => x.id === activeTabId.value) || null)

function revealArtifact(p: AIFilePreview) {
  void RevealInExplorer(p.path).catch(() => { /* 打不开多半是目录已不存在，静默即可 */ })
}

function formatPreviewSize(bytes: number): string {
  if (bytes >= 1 << 20) return (bytes / (1 << 20)).toFixed(1) + ' MB'
  if (bytes >= 1 << 10) return (bytes / (1 << 10)).toFixed(1) + ' KB'
  return bytes + ' B'
}

function newTab() {
  const tab: SideTab = { id: tabSeq++, kind: 'home', title: '' }
  tabs.value.push(tab)
  activeTabId.value = tab.id
}

function closeTab(id: number) {
  const i = tabs.value.findIndex((x) => x.id === id)
  if (i < 0) return
  tabs.value.splice(i, 1)
  if (activeTabId.value === id) {
    const next = tabs.value[Math.min(i, tabs.value.length - 1)]
    activeTabId.value = next ? next.id : 0
  }
  // 关掉最后一个标签页 = 用户要收工，连同面板一起收起（不是自动补首页——
  // 那样关 × 永远关不掉，只能再点工具栏按钮，反直觉）
  if (!tabs.value.length) emit('update:open', false)
}

// ensureTab 面板打开时保证有一个首页标签页，避免一块空白（父组件 externally 也能调）。
function ensureTab() {
  if (!tabs.value.length) newTab()
}

async function openArtifact(a: AIArtifact) {
  ensureTab()
  // reactive 而不是裸对象：push 进 ref 数组后，组件读的是数组里那个代理，
  // 而手上一旦握着裸对象，`tab.data = ...` 改得动数据却不触发视图更新——
  // 表现就是「首次点开一直转圈，切到别的标签页再切回来文件才出现」。
  const tab = reactive<SideTab>({ id: tabSeq++, kind: 'file', title: a.name, path: a.path, data: null, loading: true })
  tabs.value.push(tab)
  activeTabId.value = tab.id
  // 先把「正在读取文件…」画出来再发请求：AI 正在流式输出时主线程很忙，
  // 不留这一拍的话，从点击到画面变化之间是一段纯空白（看着像没反应）。
  await nextTick()
  try {
    tab.data = unwrap(await AIReadPreviewFile(a.path)) as AIFilePreview
    tab.loading = false
    // 文本/代码文件打开即编辑（CodeMirror），不再有只读视图：
    // 只读视图那套 hljs 首屏渲染正是「执行中」卡住的主因，而点开产物十次有九次
    // 都是想改。HTML 保持渲染页面优先（源码可切），图片/二进制照旧。
    if (tab.data?.kind === 'text') {
      tab.draft = tab.data.content ?? ''
      tab.editing = true
    }
  } catch (e) {
    tab.loading = false
    toast.error(getErrorMessage(e))
    closeTab(tab.id)
  }
}

// 面板从关到开时补一个首页标签页（父组件按需调用 ensureTab 之外的兜底）。
watch(() => props.open, (v) => { if (v) ensureTab() })

// —— 代码视图 ——
// 只剩 CodeMirror 一种形态：行号/高亮/undo/搜索都是 CM 自带，只读渲染层已移除
// （它用 hljs 同步高亮，几百 KB 的文件正好卡在 loading 消失前那帧）。
// 截断文件（只读到前 512KB）以只读模式打开：保存会丢掉没读到的后半截。
const codeDirty = computed(() => {
  const d = activeTab.value
  return !!d && d.editing && d.draft !== d.data?.content
})

// 编辑态挂载 CodeMirror（动态加载，见 utils/aiCodeEditor.ts）：
// watch「正在编辑的标签页 id」——进编辑态挂载，退出/切走/关标签页销毁。
// draft 是唯一数据源：CM 的 onChange 只更新 draft，保存/脏标记都读它——
// CM 实例销毁重建（切走再切回）时从 draft 恢复，内容不丢。
const cmHostEl = ref<HTMLElement | null>(null)
let cmHandle: { focus(): void; destroy(): void } | null = null
function destroyCm() {
  cmHandle?.destroy()
  cmHandle = null
}

// 组件卸载（或应用退出）时销毁 CodeMirror：CM 持有 DOM 与事件监听，
// 不显式 destroy 会留下指向已卸载节点的实例。
onUnmounted(destroyCm)

watch(
  () => {
    const d = activeTab.value
    return d && d.kind === 'file' && d.editing ? d.id : 0
  },
  async (tabId) => {
    destroyCm()
    if (!tabId) return
    const tab = tabs.value.find((x) => x.id === tabId)
    if (!tab || !tab.data) return
    await nextTick()
    // await 期间用户可能已经切走/取消，挂载前再验一遍
    if (!cmHostEl.value || activeTab.value?.id !== tabId || !tab.editing) return
    try {
      const { createCodeEditor } = await import('../utils/aiCodeEditor')
      if (!cmHostEl.value || activeTab.value?.id !== tabId || !tab.editing) return
      cmHandle = await createCodeEditor(cmHostEl.value, {
        value: tab.draft ?? tab.data.content ?? '',
        filename: tab.data.name,
        readOnly: !!tab.data.truncated,
        onChange: (v) => { tab.draft = v },
        onSave: () => { void saveEdit() },
      })
      if (!tab.data.truncated) cmHandle.focus()
    } catch (e) {
      toast.error(getErrorMessage(e))
      tab.editing = false // 加载失败退回空白提示，别卡在空编辑态
    }
  },
)

// HTML 标签页：渲染视图 ↔ 源码视图。切回渲染前先把未保存的改动存掉，
// 否则 iframe 读的还是 data.content（旧内容），用户会以为改动丢了。
async function toggleHtmlSource() {
  const d = activeTab.value
  if (!d || !d.data || d.data.kind !== 'html') return
  if (d.source) {
    if (codeDirty.value) await saveEdit()
    d.source = false
    d.editing = false
    d.draft = undefined
  } else {
    d.draft = d.data.content ?? ''
    d.editing = true
    d.source = true
  }
}

async function saveEdit() {
  const d = activeTab.value
  if (!d || !d.path || d.saving || !d.editing || d.data?.truncated) return
  if (d.draft === d.data?.content) return // 没有改动，不做无意义的写盘
  d.saving = true
  try {
    const res = unwrap(await AISavePreviewFile(d.path, d.draft ?? '')) as { path: string; size?: number; modTime?: string }
    // 更新标签页里的缓存：内容、体积、时间都要跟上，否则大小显示是旧的
    if (d.data) {
      d.data.content = d.draft ?? ''
      if (typeof res?.size === 'number') d.data.size = res.size
      if (res?.modTime) d.data.modTime = res.modTime
    }
    d.saving = false
    toast.success(t('aiFileSaved'))
  } catch (e) {
    d.saving = false
    toast.error(getErrorMessage(e))
  }
}

defineExpose({ ensureTab, openArtifact })
</script>

<template>
  <!-- 右侧面板：标签页 + 大预览区。放在 ai-main 外面而不是里面：
       与对话侧栏并列、占满整页高度，聊天区不被压缩出横向滚动 -->
  <aside v-if="open" class="ai-side">
    <div class="ai-side-tabs">
      <div
        v-for="tab in tabs"
        :key="tab.id"
        :class="['ai-side-tab', { active: tab.id === activeTabId }]"
        @click="activeTabId = tab.id"
      >
        <FileText v-if="tab.kind === 'file'" :size="11" />
        <span class="ai-side-tab-title">{{ tab.kind === 'home' ? t('aiSideNewTab') : tab.title }}</span>
        <button class="ai-side-tab-close" :title="t('aiSideCloseTab')" @click.stop="closeTab(tab.id)">
          <X :size="11" />
        </button>
      </div>
      <button class="ai-side-new" :title="t('aiSideNewTab')" @click="newTab">
        <Plus :size="13" />
      </button>
    </div>
    <div v-if="activeTab" class="ai-side-body">
      <!-- 首页：本任务的产物列表 -->
      <div v-if="activeTab.kind === 'home'" class="ai-side-home">
        <div v-if="!artifacts.length" class="ai-side-home-empty">{{ t('aiSideHomeEmpty') }}</div>
        <template v-else>
          <div class="ai-side-home-title">{{ t('aiArtifactsCount', { n: artifacts.length }) }}</div>
          <div class="ai-artifacts-list">
            <button v-for="a in artifacts" :key="a.path" class="ai-artifact" :title="a.path" @click="openArtifact(a)">
              <FileText :size="13" />
              <span class="ai-artifact-name">{{ a.name }}</span>
              <span class="ai-artifact-op">{{ t(a.op === 'edit' ? 'aiArtifactEdited' : 'aiArtifactWritten') }}</span>
            </button>
          </div>
        </template>
      </div>
      <!-- 文件标签页：与弹层预览同一套读取结果，只是渲染面更大 -->
      <template v-else>
        <div v-if="activeTab.loading" class="ai-side-home-empty">{{ t('aiPreviewLoading') }}</div>
        <template v-else-if="activeTab.data">
          <div class="ai-side-filebar">
            <span class="ai-side-filebar-name" :title="activeTab.data.path">
              {{ activeTab.data.name }} · {{ formatPreviewSize(activeTab.data.size) }}
              <template v-if="codeDirty"> · {{ t('aiFileUnsaved') }}</template>
              <template v-if="activeTab.data.truncated"> · {{ t('aiPreviewTruncated') }}</template>
            </span>
            <!-- 保存只在有改动时出现：打开即编辑，没有「编辑」按钮可点 -->
            <button
              v-if="activeTab.editing && codeDirty && !activeTab.data.truncated"
              class="ai-file-act" :disabled="activeTab.saving" @click="saveEdit"
            >
              <Save :size="12" /> {{ t('aiFileSave') }}
            </button>
            <button v-if="activeTab.data.kind === 'html'" class="ai-file-act" @click="toggleHtmlSource">
              {{ t(activeTab.source ? 'aiPreviewRendered' : 'aiPreviewSource') }}
            </button>
            <button class="ai-file-act" @click="revealArtifact(activeTab.data)">
              <FolderOpen :size="12" /> {{ t('aiPreviewReveal') }}
            </button>
          </div>
          <iframe
            v-if="activeTab.data.kind === 'html' && !activeTab.source"
            class="ai-side-frame"
            sandbox="allow-scripts"
            :srcdoc="activeTab.data.content"
          ></iframe>
          <div v-else-if="activeTab.data.kind === 'image'" class="ai-side-imgwrap">
            <img class="ai-side-img" :src="activeTab.data.dataUrl" :alt="activeTab.data.name" />
          </div>
          <div v-else-if="activeTab.data.kind === 'binary'" class="ai-file-binary">{{ t('aiPreviewBinary') }}</div>
          <!-- 其余（文本 / HTML 源码）一律 CodeMirror，打开即编辑：
               行号/高亮/undo/搜索都由 CM 提供，见 utils/aiCodeEditor.ts -->
          <div v-else class="ai-code-wrap">
            <div ref="cmHostEl" class="ai-code-cm"></div>
          </div>
        </template>
      </template>
    </div>
  </aside>
</template>

<style scoped>
/* 右侧面板：标签页 + 大预览区 */
.ai-side {
  width: 42%; min-width: 340px; flex-shrink: 0;
  display: flex; flex-direction: column; overflow: hidden;
  border-left: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.ai-side-tabs {
  display: flex; align-items: center; gap: 4px; padding: 6px 8px;
  border-bottom: 1px solid var(--color-border); flex-shrink: 0;
  overflow-x: auto; scrollbar-width: thin;
}
.ai-side-tab {
  display: inline-flex; align-items: center; gap: 6px; flex-shrink: 0;
  max-width: 170px; padding: 4px 8px; border-radius: var(--radius-sm);
  border: none; background: transparent; color: var(--color-text-secondary);
  font-size: 12px; font-family: inherit; cursor: pointer; user-select: none;
}
.ai-side-tab:hover { color: var(--color-text-primary); }
.ai-side-tab.active { background: var(--color-bg-tertiary); color: var(--color-text-primary); }
.ai-side-tab-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ai-side-tab-close {
  display: flex; padding: 1px; border: none; border-radius: 3px;
  background: transparent; color: var(--color-text-secondary); cursor: pointer; flex-shrink: 0;
}
.ai-side-tab-close:hover { color: var(--color-text-primary); background: var(--color-bg-primary); }
.ai-side-new {
  display: flex; padding: 4px; flex-shrink: 0; border: none; border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-secondary); cursor: pointer;
}
.ai-side-new:hover { color: var(--color-text-primary); background: var(--color-bg-tertiary); }
.ai-side-body { flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: hidden; }
.ai-side-home { flex: 1; overflow: auto; padding: 14px; }
.ai-side-home-title { font-size: 12px; color: var(--color-text-secondary); margin-bottom: 8px; }
.ai-side-home-empty { flex: 1; display: flex; align-items: center; justify-content: center; padding: 24px; color: var(--color-text-secondary); font-size: 12px; text-align: center; }
.ai-side-filebar {
  display: flex; align-items: center; gap: 8px; padding: 6px 10px;
  border-bottom: 1px solid var(--color-border); flex-shrink: 0;
}
.ai-side-filebar-name { flex: 1; font-size: 11px; color: var(--color-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* HTML 渲染视图：iframe 铺满面板；白底兜底，页面没写 background 时与浏览器一致 */
.ai-side-frame { flex: 1; width: 100%; border: none; background: var(--color-content-bg); }
.ai-side-imgwrap { flex: 1; display: flex; align-items: center; justify-content: center; padding: 12px; overflow: auto; }
.ai-side-img { max-width: 100%; max-height: 100%; border-radius: var(--radius-md); }
/* 代码视图宿主：只剩 CodeMirror（行号/高亮/滚动都在编辑器内部） */
.ai-code-wrap { flex: 1; min-height: 0; display: flex; overflow: hidden; background: var(--color-bg-secondary); }
.ai-code-cm { flex: 1; min-width: 0; overflow: hidden; display: flex; }
.ai-code-cm .cm-editor { flex: 1; }
/* 产物预览统一在本面板，弹层已删；这里只剩面板文件栏也在用的公共小件 */
.ai-file-binary { flex: 1; display: flex; align-items: center; justify-content: center; padding: 24px; color: var(--color-text-secondary); font-size: 13px; }
.ai-file-act {
  display: inline-flex; align-items: center; gap: 5px; padding: 4px 10px;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-primary); font-size: 11px; cursor: pointer; font-family: inherit;
}
.ai-file-act:hover { border-color: var(--color-accent); }
</style>
