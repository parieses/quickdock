<script setup lang="ts">
/**
 * 当前工作目录的 hooks.json 编辑弹窗。
 *
 * 只改命令列表，绝不碰批准状态：AISaveHooks 的 Go 实现也不动批准，保存后用户
 * 仍需在父组件（AIPage）显式批准，这些命令才会在 AI 改文件 / 执行命令时运行。
 * 挂载由父级 v-if 控制；保存成功 emit('saved') 让父级重读 AIGetHooks。
 */
import { inject, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2 } from '@lucide/vue'
import QdModal from './QdModal.vue'
import { AISaveHooks } from '../../bindings/quickdock/services/ai/aiservice'
import type { AIHooksInfo } from '../types/ai'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'

import { reportError } from '../utils/reportError'
const props = defineProps<{ cwd: string; hooks: AIHooksInfo | null }>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'saved'): void
}>()
const { t } = useI18n()
const toast = inject<{ error: (m: string) => void; success: (m: string) => void }>('toast')!

const afterEdit = ref<string[]>([...(props.hooks?.afterEdit || [])])
const beforeCommand = ref<string[]>([...(props.hooks?.beforeCommand || [])])
const busy = ref(false)
const err = ref('')

function addCmd(list: string[]) { list.push('') }
function removeCmd(list: string[], i: number) { list.splice(i, 1) }

async function save() {
  if (busy.value) return
  // trim + 去空行：空命令会被后端当成一条永远在跑空程序的钩子，没意义。
  const cleanAfter = afterEdit.value.map((s) => s.trim()).filter(Boolean)
  const cleanBefore = beforeCommand.value.map((s) => s.trim()).filter(Boolean)
  busy.value = true
  err.value = ''
  try {
    unwrap(await AISaveHooks(props.cwd, cleanAfter, cleanBefore))
    toast.success(t('hooksSaved'))
    emit('saved')
    emit('update:modelValue', false)
  } catch (e) {
    logErr('hooksEditor.save', e)
    err.value = getErrorMessage(e)
    reportError(e, 'HooksEditorDialog.save')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <QdModal
    :model-value="true"
    :title="t('hooksEditorTitle')"
    width="560px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="hooks-meta">
      <div>{{ t('hooksEditorCwd') }}：{{ props.cwd || '-' }}</div>
      <div>{{ t('hooksEditorPath') }}：{{ props.hooks?.path || t('hooksEditorNoFile') }}</div>
      <div>
        {{ t('hooksEditorApproval') }}：
        <span :class="props.hooks?.approved ? 'hooks-badge ok' : 'hooks-badge warn'">
          {{ props.hooks?.approved ? t('hooksEditorApproved') : t('hooksEditorNotApproved') }}
        </span>
      </div>
    </div>

    <p class="hooks-warning">{{ t('hooksEditWarning') }}</p>

    <div class="hooks-list">
      <div class="hooks-list-head">{{ t('hooksEditorAfterEdit') }}</div>
      <div v-for="(cmd, i) in afterEdit" :key="'a' + i" class="hooks-row">
        <input v-model="afterEdit[i]" class="qd-input hooks-input" :placeholder="t('hooksEditorCmdPh')" />
        <button class="qd-icon-btn" :title="t('delete')" @click="removeCmd(afterEdit, i)"><Trash2 :size="14" /></button>
      </div>
      <button class="qd-btn qd-btn-sm hooks-add" @click="addCmd(afterEdit)"><Plus :size="13" />{{ t('add') }}</button>
    </div>

    <div class="hooks-list">
      <div class="hooks-list-head">{{ t('hooksEditorBeforeCommand') }}</div>
      <div v-for="(cmd, i) in beforeCommand" :key="'b' + i" class="hooks-row">
        <input v-model="beforeCommand[i]" class="qd-input hooks-input" :placeholder="t('hooksEditorCmdPh')" />
        <button class="qd-icon-btn" :title="t('delete')" @click="removeCmd(beforeCommand, i)"><Trash2 :size="14" /></button>
      </div>
      <button class="qd-btn qd-btn-sm hooks-add" @click="addCmd(beforeCommand)"><Plus :size="13" />{{ t('add') }}</button>
    </div>

    <p v-if="err" class="hooks-err">{{ err }}</p>

    <template #footer>
      <button class="qd-btn" @click="emit('update:modelValue', false)">{{ t('cancel') }}</button>
      <button class="qd-btn qd-btn-primary" :disabled="busy" @click="save">{{ t('save') }}</button>
    </template>
  </QdModal>
</template>

<style scoped>
.hooks-meta { padding: var(--space-3) var(--space-4); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); font-size: var(--text-xs); line-height: 1.7; color: var(--color-text-secondary); word-break: break-all; }
.hooks-badge { padding: 0 var(--space-2); border-radius: var(--radius-xs); }
.hooks-badge.ok { color: var(--color-success); background: var(--color-success-bg); }
.hooks-badge.warn { color: var(--color-warning); background: var(--color-warning-bg); }
.hooks-warning { margin: var(--space-4) 0; padding: var(--space-3) var(--space-4); border: 1px solid var(--color-danger-border); border-radius: var(--radius-sm); background: var(--color-danger-bg); color: var(--color-text-primary); font-size: var(--text-sm); line-height: 1.6; }
.hooks-list { margin-top: var(--space-5); }
.hooks-list-head { margin-bottom: var(--space-3); font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary); }
.hooks-row { display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2); }
.hooks-input { flex: 1; min-width: 0; font-family: var(--font-mono); }
.hooks-add { margin-top: var(--space-1); }
.hooks-err { margin: var(--space-4) 0 0; padding: var(--space-3) var(--space-4); border-radius: var(--radius-sm); background: var(--color-danger-bg); color: var(--color-danger); font-size: var(--text-sm); word-break: break-all; }
</style>
