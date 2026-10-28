<script setup lang="ts">
import { ref, computed, onMounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Play, Trash2, Pencil, Check, X } from '@lucide/vue'
import {
  ListClipboardRules, CreateClipboardRule, UpdateClipboardRule, DeleteClipboardRule,
  SetClipboardRuleEnabled, GetClipboardRulesEnabled, SetClipboardRulesEnabled, TestClipboardRule,
} from '../../bindings/quickdock/services/clipboard/clipboardservice'
import type { ClipboardRule } from '../../bindings/quickdock/internal/db/models'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')

// 动作清单与后端 internal/cliprule 的常量一一对应，改动要两边同步。
const ACTIONS = [
  'trim', 'remove_blank', 'dedupe_lines', 'sort_lines',
  'json_format', 'json_minify', 'strip_ansi', 'case', 'url_decode',
] as const

const rules = ref<ClipboardRule[]>([])
const globalOn = ref(false)
const busy = ref(false)
const loading = ref(true)

// 编辑态：editing.id 为空表示新建
const editing = ref<ClipboardRule | null>(null)
const sample = ref('')
const sampleApp = ref('')   // 试运行用的来源应用（仅 app 匹配规则需要）
const testOut = ref('')
const testState = ref<'' | 'matched' | 'unchanged' | 'nomatch'>('')

function emptyRule(): ClipboardRule {
  return {
    id: '', name: '', enabled: true, priority: 0,
    matchKind: 'all', matchExpr: '', action: 'trim', actionArg: '',
    writeBack: false, createdTs: 0, updatedTs: 0,
  }
}

async function load() {
  loading.value = true
  try {
    const [list, on] = await Promise.all([
      ListClipboardRules().then((r) => unwrap<ClipboardRule[]>(r)),
      GetClipboardRulesEnabled().then((r) => unwrap<boolean>(r)),
    ])
    rules.value = Array.isArray(list) ? list : []
    globalOn.value = on === true
  } catch (e) {
    logErr('ClipRules', e)
    toast?.error?.(getErrorMessage(e))
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function toggleGlobal() {
  if (busy.value) return
  busy.value = true
  try {
    await SetClipboardRulesEnabled(!globalOn.value)
    globalOn.value = !globalOn.value
  } catch (e) {
    logErr('ClipRules', e)
    toast?.error?.(getErrorMessage(e))
  } finally {
    busy.value = false
  }
}

function startCreate() {
  editing.value = emptyRule()
  sample.value = ''
  sampleApp.value = ''
  testOut.value = ''
  testState.value = ''
}

function startEdit(r: ClipboardRule) {
  editing.value = { ...r }
  sample.value = ''
  sampleApp.value = ''
  testOut.value = ''
  testState.value = ''
}

function cancelEdit() {
  editing.value = null
  testOut.value = ''
  testState.value = ''
}

async function save() {
  const cur = editing.value
  if (!cur) return
  if (!cur.name.trim()) {
    toast?.error?.(t('crNameRequired'))
    return
  }
  busy.value = true
  try {
    if (cur.id) {
      unwrap(await UpdateClipboardRule(cur))
    } else {
      unwrap(await CreateClipboardRule(cur))
    }
    editing.value = null
    await load()
    toast?.success?.(t('crSaveOk'))
  } catch (e) {
    logErr('ClipRules', e)
    toast?.error?.(getErrorMessage(e))
  } finally {
    busy.value = false
  }
}

async function toggleRule(r: ClipboardRule) {
  try {
    unwrap(await SetClipboardRuleEnabled(r.id, !r.enabled))
    await load()
  } catch (e) {
    logErr('ClipRules', e)
    toast?.error?.(getErrorMessage(e))
  }
}

async function remove(r: ClipboardRule) {
  if (!(await toast?.confirm?.(t('crDeleteConfirm', { name: r.name })))) return
  try {
    unwrap(await DeleteClipboardRule(r.id))
    await load()
  } catch (e) {
    logErr('ClipRules', e)
    toast?.error?.(getErrorMessage(e))
  }
}

// 试运行：把样例文本丢给后端同一套引擎，看到的就是真实结果（不是前端另写一遍）
async function runTest() {
  const cur = editing.value
  if (!cur || !sample.value) return
  try {
    const res = unwrap<{ output: string; matched: boolean; changed: boolean }>(
      // 第三参是来源应用（窗口标题）：app 类规则靠它判定，传空串等于必然不命中
      await TestClipboardRule(cur, sample.value, sampleApp.value.trim()),
    )
    testOut.value = res?.output ?? ''
    testState.value = !res?.matched ? 'nomatch' : (res.changed ? 'matched' : 'unchanged')
  } catch (e) {
    logErr('ClipRules', e)
    toast?.error?.(getErrorMessage(e))
  }
}

const showExpr = computed(() => editing.value?.matchKind === 'regex' || editing.value?.matchKind === 'app')
const showArg = computed(() => editing.value?.action === 'case' || editing.value?.action === 'json_format')

// 优先级：DB 里 priority ASC 决定执行顺序，新建时后端自动排到末尾（>0 才生效）。
// 之前这个字段既看不到也改不了 —— 多条规则同时命中时，谁先谁后完全靠运气。
// 用受控文本而非 number 输入：0 在语义上是「还没分配」，显示成空比显示 "0" 清楚。
const priorityText = computed({
  get: () => (editing.value && editing.value.priority > 0 ? String(editing.value.priority) : ''),
  set: (v: string) => {
    if (!editing.value) return
    const n = Number(String(v).replace(/[^\d]/g, ''))
    editing.value.priority = Number.isFinite(n) && n > 0 ? n : 0
  },
})

function actionLabel(a: string): string {
  return t('crAct_' + a)
}

function matchLabel(r: ClipboardRule): string {
  if (r.matchKind === 'regex') return t('crMatchRegex')
  if (r.matchKind === 'app') return t('crMatchApp')
  return t('crMatchAll')
}
</script>

<template>
  <div class="cr-page">
    <h3 class="section-title">{{ t('crTitle') }}</h3>
    <p class="cr-desc">{{ t('crDesc') }}</p>

    <!-- 总开关：默认关，措辞要说清"关着就什么都不做" -->
    <div class="cr-global" :class="{ on: globalOn }">
      <div class="cr-global-text">
        <span class="cr-global-title">{{ globalOn ? t('crGlobalOn') : t('crGlobalOff') }}</span>
        <span class="cr-global-hint">{{ t('crGlobalHint') }}</span>
      </div>
      <button class="btn" :class="globalOn ? 'btn-primary' : 'btn-secondary'" :disabled="busy" @click="toggleGlobal">
        {{ globalOn ? t('crTurnOff') : t('crTurnOn') }}
      </button>
    </div>

    <!-- 编辑表单：就地展开，不弹窗（规则字段少，弹窗反而多点两次） -->
    <section v-if="editing" class="cr-form">
      <div class="cr-form-head">
        <span class="cr-form-title">{{ editing.id ? t('crEditTitle') : t('crNewTitle') }}</span>
        <button class="cr-icon-btn" :title="t('cancel')" @click="cancelEdit"><X :size="14" /></button>
      </div>

      <div class="field">
        <label class="field-label">{{ t('crName') }}</label>
        <input v-model="editing.name" class="field-input" :placeholder="t('crNamePlaceholder')" />
      </div>

      <div class="cr-grid">
        <div class="field">
          <label class="field-label">{{ t('crMatchKind') }}</label>
          <select v-model="editing.matchKind" class="field-input">
            <option value="all">{{ t('crMatchAll') }}</option>
            <option value="regex">{{ t('crMatchRegex') }}</option>
            <option value="app">{{ t('crMatchApp') }}</option>
          </select>
        </div>
        <div v-if="showExpr" class="field">
          <label class="field-label">{{ t('crMatchExpr') }}</label>
          <input v-model="editing.matchExpr" class="field-input" :placeholder="t('crMatchExprHint')" />
        </div>
      </div>

      <div class="cr-grid">
        <div class="field">
          <label class="field-label">{{ t('crAction') }}</label>
          <select v-model="editing.action" class="field-input">
            <option v-for="a in ACTIONS" :key="a" :value="a">{{ actionLabel(a) }}</option>
          </select>
        </div>
        <div v-if="showArg" class="field">
          <label class="field-label">{{ t('crActionArg') }}</label>
          <select v-if="editing.action === 'case'" v-model="editing.actionArg" class="field-input">
            <option value="lower">{{ t('crCaseLower') }}</option>
            <option value="upper">{{ t('crCaseUpper') }}</option>
            <option value="title">{{ t('crCaseTitle') }}</option>
          </select>
          <input v-else v-model="editing.actionArg" class="field-input" placeholder="2" />
        </div>
      </div>

      <div class="cr-grid">
        <div class="field">
          <label class="field-label">{{ t('crPriority') }}</label>
          <input v-model="priorityText" class="field-input" :placeholder="t('crPriorityAuto')" inputmode="numeric" />
          <p class="field-hint">{{ t('crPriorityHint') }}</p>
        </div>
        <div></div>
      </div>

      <label class="cr-check">
        <input v-model="editing.writeBack" type="checkbox" />
        <span>
          <span class="cr-check-title">{{ t('crWriteBack') }}</span>
          <span class="cr-check-hint">{{ t('crWriteBackHint') }}</span>
        </span>
      </label>

      <!-- 试运行：先看它到底会把内容改成什么样，再决定是否保存 -->
      <div class="cr-test">
        <textarea v-model="sample" class="cr-test-input" rows="3" :placeholder="t('crTestPlaceholder')" />
        <!-- 应用匹配规则必须能提供"来源应用"才能验证：后端拿到空串一律判不命中，
             于是这条规则在试运行里永远显示"未匹配"，用户根本没法自查写法对不对。 -->
        <input
          v-if="editing.matchKind === 'app'"
          v-model="sampleApp"
          class="cr-test-input"
          :placeholder="t('crTestAppPlaceholder')"
        />
        <div class="cr-test-actions">
          <button class="btn btn-secondary" :disabled="!sample" @click="runTest">
            <Play :size="12" />
            <span>{{ t('crTest') }}</span>
          </button>
          <span v-if="testState" class="cr-test-state" :class="testState">
            {{ testState === 'nomatch' ? t('crTestNotMatched') : testState === 'unchanged' ? t('crTestUnchanged') : t('crTestMatched') }}
          </span>
        </div>
        <pre v-if="testOut" class="cr-test-out">{{ testOut }}</pre>
      </div>

      <div class="cr-form-actions">
        <button class="btn btn-primary" :disabled="busy" @click="save">
          <Check :size="12" />
          <span>{{ t('save') }}</span>
        </button>
        <button class="btn btn-secondary" @click="cancelEdit">{{ t('cancel') }}</button>
      </div>
    </section>

    <!-- 规则列表 -->
    <div class="cr-list">
      <div v-for="r in rules" :key="r.id" class="cr-row" :class="{ off: !r.enabled }">
        <div class="cr-row-main">
          <span class="cr-row-name">
            <span class="cr-pri" :title="t('crPriorityHint')">#{{ r.priority }}</span>
            {{ r.name }}
          </span>
          <span class="cr-row-meta">
            {{ actionLabel(r.action) }} · {{ matchLabel(r) }}
            <template v-if="r.matchExpr"> · {{ r.matchExpr }}</template>
          </span>
        </div>
        <span v-if="r.writeBack" class="cr-tag">{{ t('crTagWriteBack') }}</span>
        <button class="cr-act" :class="{ on: r.enabled }" @click="toggleRule(r)">
          {{ r.enabled ? t('crEnabled') : t('crDisabled') }}
        </button>
        <button class="cr-icon-btn" :title="t('edit')" @click="startEdit(r)"><Pencil :size="13" /></button>
        <button class="cr-icon-btn danger" :title="t('delete')" @click="remove(r)"><Trash2 :size="13" /></button>
      </div>

      <p v-if="!loading && !rules.length" class="cr-empty">{{ t('crEmpty') }}</p>
    </div>

    <button v-if="!editing" class="btn btn-secondary cr-add" @click="startCreate">
      <Plus :size="13" />
      <span>{{ t('crNew') }}</span>
    </button>
  </div>
</template>

<style scoped>
/* 与 settings.css 的 .section 对齐（同宽、同段间距），合并进「剪贴板」页后视觉连续 */
.cr-page { width: 100%; max-width: 600px; margin-top: 28px; }
.page-title { margin: 0 0 4px; font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.cr-desc { margin: 0 0 16px; font-size: 12px; color: var(--color-text-disabled); line-height: 1.6; }

/* 总开关条：开着时用强调色底，一眼看出"规则现在就生效" */
.cr-global {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  margin-bottom: 14px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.cr-global.on { border-color: var(--color-accent-border); background: var(--color-accent-bg); }
.cr-global-text { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.cr-global-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.cr-global-hint { font-size: 11px; color: var(--color-text-disabled); line-height: 1.5; }

.cr-form {
  padding: 14px;
  margin-bottom: 14px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.cr-form-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.cr-form-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
/* 两列栅格：匹配方式 + 表达式、动作 + 参数各占一半，字段少时不会拉成一条长线 */
.cr-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 12px; }

.cr-check { display: flex; align-items: flex-start; gap: 8px; margin-top: 14px; cursor: pointer; }
.cr-check input { margin-top: 2px; }
.cr-check span { display: flex; flex-direction: column; gap: 2px; }
.cr-check-title { font-size: 12.5px; color: var(--color-text-secondary); }
.cr-check-hint { font-size: 11px; color: var(--color-text-disabled); }

.cr-test { margin-top: 14px; }
.cr-test-input {
  width: 100%;
  padding: 8px 10px;
  font-family: 'Consolas', ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  resize: vertical;
}
.cr-test-input:focus { outline: none; border-color: var(--color-accent); box-shadow: 0 0 0 2px var(--color-accent-border); }
.cr-test-actions { display: flex; align-items: center; gap: 10px; margin-top: 8px; }
.cr-test-state { font-size: 11.5px; color: var(--color-text-disabled); }
.cr-test-state.matched { color: var(--color-success); }
.cr-test-state.nomatch { color: var(--color-warning); }
.cr-test-out {
  margin: 8px 0 0;
  padding: 8px 10px;
  max-height: 160px;
  overflow: auto;
  font-family: 'Consolas', ui-monospace, SFMono-Regular, monospace;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  white-space: pre-wrap;
  word-break: break-all;
}
.cr-form-actions { display: flex; gap: 8px; margin-top: 14px; }

.cr-list {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  overflow: hidden;
  background: var(--color-surface);
}
.cr-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  transition: background-color var(--transition-fast);
}
.cr-row + .cr-row { border-top: 1px solid var(--color-border); }
.cr-row:hover { background: var(--color-bg-hover); }
/* 停用的规则整体压暗，避免和生效中的混在一起 */
.cr-row.off .cr-row-main { opacity: 0.55; }
.cr-row-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.cr-row-name { font-size: 13px; font-weight: 500; color: var(--color-text-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
/* 执行序号：与规则名同一行，弱化处理——它是说明顺序的，不是名字的一部分 */
.cr-pri {
  display: inline-block;
  min-width: 18px;
  margin-right: 4px;
  padding: 0 3px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary);
  color: var(--color-text-disabled);
  font-size: 10px;
  font-weight: 400;
  text-align: center;
}
.cr-row-meta {
  font-size: 11px;
  color: var(--color-text-disabled);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.cr-tag {
  flex-shrink: 0;
  padding: 1px 6px;
  font-size: 10px;
  border-radius: var(--radius-sm);
  color: var(--color-warning);
  background: var(--color-warning-bg);
}
.cr-act {
  flex-shrink: 0;
  height: 22px;
  padding: 0 8px;
  font-size: 11px;
  font-family: inherit;
  color: var(--color-text-disabled);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
}
.cr-act.on { color: var(--color-accent); border-color: var(--color-accent-border); }
.cr-icon-btn {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  background: transparent;
  color: var(--color-text-disabled);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: color var(--transition-fast), background-color var(--transition-fast);
}
.cr-icon-btn:hover { color: var(--color-text-primary); background: var(--color-bg-active); }
.cr-icon-btn.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.cr-empty { margin: 0; padding: 18px 12px; text-align: center; font-size: 12px; color: var(--color-text-disabled); }
.cr-add { display: inline-flex; align-items: center; gap: 6px; margin-top: 12px; }
</style>
