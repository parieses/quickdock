<script setup lang="ts">
/**
 * 设置 → AI → 提示词库。
 *
 * 这里管的是「用户自己的话术」，与技能（skills/ 目录下的 SKILL.md）不是一套：
 * 技能装进输入框是胶囊、由模型去读全文；提示词是直接填进输入框的一段正文，
 * 用户可以接着改。所以本组件只做增删改查，不碰模型调用。
 *
 * 变量（{{date}} / {{time}} / {{selection}}）一律存原文，插入时由 AIPage 展开：
 * 提前展开会把「上次复制的文本」固化进模板，下次再用就是脏数据。
 */
import { computed, inject, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, Pencil, Plus, Trash2 } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import type { AIPrompt } from '../types/ai'
import type { ToastAPI } from '../types'
import { AIDeletePrompt, AIGetPrompts, AISavePrompt } from '../../bindings/quickdock/services/ai/aiservice'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')

// SettingsModal 是一开始就挂着的（只切 visible），所以这里不能 onMounted 就拉：
// 那一刻数据库可能还没开，会在每次启动时弹一条「数据库未就绪」。
const props = defineProps<{ visible: boolean }>()

const list = ref<AIPrompt[]>([])
const loading = ref(false)
const saving = ref(false)

// 编辑态：id 为空表示新建。内置条目也能编辑（改文案/分类），只是删不掉。
const editing = ref<{ id: string; name: string; category: string; content: string; builtin: number } | null>(null)

// 按分类分组展示：内置条目已经带好分类，用户自建的多半不填，归到「未分类」。
const groups = computed(() => {
  const map = new Map<string, AIPrompt[]>()
  for (const p of list.value) {
    const key = (p.category || '').trim() || t('aiPromptsNoCategory')
    const arr = map.get(key)
    if (arr) arr.push(p)
    else map.set(key, [p])
  }
  return Array.from(map, ([name, items]) => ({ name, items }))
})

async function load() {
  loading.value = true
  try {
    const data = unwrap(await AIGetPrompts()) as { prompts?: AIPrompt[] } | null
    list.value = data?.prompts || []
  } catch (e) {
    toast?.error(getErrorMessage(e))
    logErr('SettingsPrompts:load', e)
  } finally {
    loading.value = false
  }
}

function startNew() {
  editing.value = { id: '', name: '', category: '', content: '', builtin: 0 }
}

function startEdit(p: AIPrompt) {
  editing.value = { id: p.id, name: p.name, category: p.category, content: p.content, builtin: p.builtin }
}

function cancelEdit() {
  editing.value = null
}

async function save() {
  const draft = editing.value
  if (!draft || saving.value) return
  if (!draft.name.trim()) {
    toast?.error(t('aiPromptsNameRequired'))
    return
  }
  if (!draft.content.trim()) {
    toast?.error(t('aiPromptsContentRequired'))
    return
  }
  saving.value = true
  try {
    unwrap(await AISavePrompt(
      draft.id,
      draft.name,
      draft.content,
      draft.category,
      // 新建的排在内置条目之后：内置那批是「通用套路」，自建的更贴身，但顺序不该插到中间
      draft.id ? (list.value.find((p) => p.id === draft.id)?.sort ?? 0) : (list.value.length + 1) * 10,
    ))
    editing.value = null
    await load()
    toast?.success(t('aiPromptsSaved'))
  } catch (e) {
    toast?.error(getErrorMessage(e))
    logErr('SettingsPrompts:save', e)
  } finally {
    saving.value = false
  }
}

// 复制内置条目：这是「想改内置文案又不想动原文」的正路——
// 内置条目每台机器都有，直接改掉原文的话，换台机器就找不回原始版本了。
async function duplicate(p: AIPrompt) {
  try {
    unwrap(await AISavePrompt(
      '',
      `${p.name}${t('aiPromptsCopySuffix')}`,
      p.content,
      p.category,
      (list.value.length + 1) * 10,
    ))
    await load()
    toast?.success(t('aiPromptsSaved'))
  } catch (e) {
    toast?.error(getErrorMessage(e))
    logErr('SettingsPrompts:duplicate', e)
  }
}

async function remove(p: AIPrompt) {
  if (!(await toast?.confirm(t('aiPromptsDeleteConfirm', { name: p.name })))) return
  try {
    unwrap(await AIDeletePrompt(p.id))
    await load()
    toast?.success(t('aiPromptsDeleted'))
  } catch (e) {
    // 内置条目会被后端拒绝，这里把原话透出来就够了（「内置提示词不能删除」）
    toast?.error(getErrorMessage(e))
    logErr('SettingsPrompts:delete', e)
  }
}

// 每次切到这一页都重拉：提示词也可能在别的窗口（AI 页）里改过，
// 缓存一份会让「刚加的那条」看不见。
watch(() => props.visible, (v) => {
  if (v) void load()
}, { immediate: true })
</script>

<template>
  <div class="section">
    <h3 class="section-title">{{ t('aiPromptsTitle') }}</h3>
    <p class="section-desc">{{ t('aiPromptsDesc') }}</p>

    <div class="action-row">
      <button class="btn btn-primary" @click="startNew">
        <Plus :size="13" /> {{ t('create') }}
      </button>
      <span class="prompt-count">{{ t('aiPromptsCount', { n: list.length }) }}</span>
    </div>

    <!-- 编辑器放在列表上方：新建后不用滚动就能开始写 -->
    <div v-if="editing" class="prompt-editor">
      <div class="field-row">
        <label class="field field-half">
          <span class="field-label">{{ t('aiPromptsName') }}</span>
          <input v-model="editing.name" class="field-input" type="text" maxlength="60" :placeholder="t('aiPromptsNamePh')" />
        </label>
        <label class="field field-half">
          <span class="field-label">{{ t('aiPromptsCategory') }}</span>
          <input v-model="editing.category" class="field-input" type="text" maxlength="20" :placeholder="t('aiPromptsCategoryPh')" />
        </label>
      </div>
      <label class="field">
        <span class="field-label">{{ t('aiPromptsContent') }}</span>
        <textarea
          v-model="editing.content"
          class="prompt-textarea"
          maxlength="8000"
          :placeholder="t('aiPromptsContentPh')"
        ></textarea>
      </label>
      <p class="prompt-var-hint">
        {{ t('aiPromptsVarHint') }}
        <code>&#123;&#123;date&#125;&#125;</code>
        <code>&#123;&#123;time&#125;&#125;</code>
        <code>&#123;&#123;selection&#125;&#125;</code>
      </p>
      <div class="action-row">
        <button class="btn btn-primary" :disabled="saving" @click="save">{{ saving ? t('saving') : t('save') }}</button>
        <button class="btn btn-secondary" @click="cancelEdit">{{ t('cancel') }}</button>
      </div>
    </div>

    <div v-if="loading" class="result-hint">{{ t('loading') }}</div>
    <div v-else-if="!list.length" class="result-hint">{{ t('aiPromptsEmpty') }}</div>

    <div v-for="g in groups" :key="g.name" class="prompt-group">
      <div class="prompt-group-title">{{ g.name }}</div>
      <div v-for="p in g.items" :key="p.id" class="prompt-row">
        <div class="prompt-main">
          <div class="prompt-name">
            {{ p.name }}
            <span v-if="p.builtin" class="prompt-tag">{{ t('aiPromptsBuiltin') }}</span>
          </div>
          <div class="prompt-preview">{{ p.content.replace(/\s+/g, ' ') }}</div>
        </div>
        <div class="prompt-acts">
          <button class="prompt-act" :title="t('edit')" @click="startEdit(p)"><Pencil :size="13" /></button>
          <button class="prompt-act" :title="t('copy')" @click="duplicate(p)"><Copy :size="13" /></button>
          <button v-if="!p.builtin" class="prompt-act" :title="t('delete')" @click="remove(p)"><Trash2 :size="13" /></button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.prompt-count {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--color-text-muted);
}

.prompt-group {
  margin-top: var(--space-6);
}

.prompt-group-title {
  margin-bottom: var(--space-3);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--color-text-muted);
}

.prompt-row {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-4) var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
}

.prompt-row + .prompt-row {
  margin-top: var(--space-2);
}

.prompt-main {
  flex: 1;
  min-width: 0;
}

.prompt-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--color-text-primary);
}

/* 内置标记：不是装饰，它解释「为什么这行没有删除按钮」 */
.prompt-tag {
  margin-left: var(--space-3);
  padding: 1px var(--space-2);
  border-radius: var(--radius-xs);
  background: var(--color-bg-hover);
  font-size: 10px;
  font-weight: 500;
  color: var(--color-text-muted);
}

.prompt-preview {
  margin-top: 2px;
  overflow: hidden;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--color-text-muted);
  white-space: nowrap;
  text-overflow: ellipsis;
}

.prompt-acts {
  display: flex;
  gap: var(--space-2);
  flex: none;
}

.prompt-act {
  display: grid;
  place-items: center;
  width: 26px;
  height: 26px;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.prompt-act:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.prompt-editor {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  margin-top: var(--space-5);
  padding: var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
}

/* 名称/分类输入框沿用全局 .field-input，但底色要抬到 bg-primary：
 * 编辑器卡片本身是 bg-tertiary，同色会让输入框只剩一圈边框，看着像没样式。 */
.prompt-editor .field-input {
  background: var(--color-bg-primary);
}

/* 模板正文按代码排版：缩进与换行本身是提示词的一部分 */
.prompt-textarea {
  width: 100%;
  min-height: 132px;
  padding: var(--space-4) var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
  font-family: var(--font-mono);
  font-size: 12.5px;
  line-height: 1.6;
  resize: vertical;
}

.prompt-textarea:focus {
  outline: none;
  border-color: var(--color-border-focus-soft);
}

.prompt-var-hint {
  margin: 0;
  font-size: 11px;
  line-height: 1.6;
  color: var(--color-text-muted);
}

.prompt-var-hint code {
  margin-left: var(--space-2);
  padding: 1px var(--space-2);
  border-radius: var(--radius-xs);
  background: var(--color-bg-hover);
  font-family: var(--font-mono);
  color: var(--color-text-secondary);
}
</style>
