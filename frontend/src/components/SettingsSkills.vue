<script setup lang="ts">
/**
 * 设置 → AI → 技能。
 *
 * 技能是数据目录 `skills/<名字>/SKILL.md`，跟着文件走；提示词是 `ai_prompts` 表里的
 * 一段正文，跟着库走（见 SettingsPrompts.vue 文件头）。两者入口也不同：技能是输入框
 * 里的 `/名字` 胶囊，由模型自己去读全文；提示词是直接填进输入框的正文。
 *
 * 这里只管「默认启用」，即 skills.json。它不是技能总开关：
 *   - 没在会话里选过技能的新/老对话 → 回落到这里勾的默认清单；
 *   - 本次对话要换技能 → 在输入框打 /名字 单独挑，记在 ai_conversations.skills。
 * 口径来自 services/ai/skills.go 文件头与 AISetSkillEnabled 的注释，文案不能写反。
 *
 * 技能本体一律以磁盘为准：增删改都交给资源管理器，界面不写 SKILL.md，
 * 免得「列表里改了描述、磁盘上还是旧的」两份状态打架。
 */
import { computed, inject, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { FolderOpen, RefreshCw } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import type { AISkill } from '../types/ai'
import type { ToastAPI } from '../types'
import { AIGetSkills, AISetSkillEnabled } from '../../bindings/quickdock/services/ai/aiservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')

// SettingsModal 常驻挂载、只切 visible；父组件还要叠加「切到本分段」，
// 所以这里不能在 onMounted 就拉（那一刻数据库可能还没开）。
const props = defineProps<{ visible: boolean }>()

type SkillsResult = { dir?: string; skills?: AISkill[] }

const list = ref<AISkill[]>([])
const dir = ref('')
const loading = ref(false)
// 正在提交的技能名。后端每次都是「读 skills.json → 改一项 → 整份写回」，
// 两个开关并发会互相覆盖，所以提交期间把所有开关一起锁住，串行执行。
const busy = ref('')

const enabledCount = computed(() => list.value.filter((s) => s.enabled && !s.error).length)

// AIGetSkills 与 AISetSkillEnabled 都返回全量清单；若刷新请求先发后回，
// 会把较新的开关结果盖掉。用序号只认「最后发出的那次」。
let seq = 0
let applied = 0
function apply(data: SkillsResult | null, n: number) {
  if (!data || n < applied) return
  applied = n
  dir.value = data.dir || dir.value
  list.value = data.skills || []
}

async function load() {
  if (loading.value) return
  const n = ++seq
  loading.value = true
  try {
    apply(unwrap(await AIGetSkills()) as SkillsResult | null, n)
  } catch (e) {
    toast?.error(getErrorMessage(e))
    logErr('SettingsSkills:load', e)
  } finally {
    loading.value = false
  }
}

async function toggle(s: AISkill) {
  if (busy.value || s.error) return
  const n = ++seq
  busy.value = s.name
  try {
    // 不本地翻转：以回执里的磁盘扫描结果为准，失败时界面保持原状。
    apply(unwrap(await AISetSkillEnabled(s.name, !s.enabled)) as SkillsResult | null, n)
  } catch (e) {
    toast?.error(getErrorMessage(e))
    logErr('SettingsSkills:toggle', e)
  } finally {
    busy.value = ''
  }
}

// 安装技能 = 打开目录让用户拷进去。技能是纯文本说明，先看清楚再装；
// 做成一个「一键导入 zip」反而让人不知道刚往数据目录里放了什么（与 AIPage.installSkill 同口径）。
async function openDir() {
  try {
    if (!dir.value) await load()
    if (!dir.value) return
    unwrap(await RevealInExplorer(dir.value))
    toast?.success(t('aiInstallSkillHint'))
  } catch (e) {
    toast?.error(getErrorMessage(e))
    logErr('SettingsSkills:openDir', e)
  }
}

// 单个 SKILL.md：RevealInExplorer 对文件走 revealFile（资源管理器里选中），
// 文件不存在时会退回打开父目录，正好覆盖「缺 SKILL.md」的技能。
async function openFile(s: AISkill) {
  try {
    unwrap(await RevealInExplorer(s.path))
  } catch (e) {
    toast?.error(getErrorMessage(e))
    logErr('SettingsSkills:openFile', e)
  }
}

// 每次切进这一页都重拉：技能目录可能被用户在资源管理器里改过，缓存会看不到新拷进来的。
watch(() => props.visible, (v) => {
  if (v) void load()
}, { immediate: true })
</script>

<template>
  <div class="section">
    <h3 class="section-title">{{ t('aiSkillsTitle') }}</h3>
    <p class="section-desc">{{ t('aiSkillsDesc') }}</p>

    <div class="action-row skill-actions">
      <button class="btn btn-secondary" :disabled="loading" @click="load()">
        <RefreshCw :size="13" /> {{ t('refresh') }}
      </button>
      <button class="btn btn-secondary" @click="openDir">
        <FolderOpen :size="13" /> {{ t('aiSkillsSetOpenDir') }}
      </button>
      <span class="skill-count">{{ t('aiSkillsSetCount', { n: list.length, m: enabledCount }) }}</span>
    </div>

    <p class="skill-hint">{{ t('aiSkillsSetScopeHint') }}</p>
    <p class="skill-hint">{{ t('aiSkillsSetInstallHint') }}</p>

    <div v-if="loading" class="result-hint">{{ t('loading') }}</div>
    <div v-else-if="!list.length" class="result-hint">{{ t('aiSkillsEmpty') }}</div>

    <div v-for="s in list" :key="s.path || s.name" class="skill-row" :class="{ 'is-broken': !!s.error }">
      <div class="skill-main">
        <div class="skill-name">
          {{ s.name }}
          <span v-if="s.error" class="skill-error">{{ s.error }}</span>
        </div>
        <div class="skill-desc">{{ s.description || '—' }}</div>
        <div class="skill-path" :title="s.path">{{ s.path }}</div>
      </div>
      <div class="skill-ops">
        <label class="skill-toggle" :title="t('aiSkillsSetDefaultTip')">
          <input
            type="checkbox"
            class="skill-check"
            :checked="s.enabled"
            :disabled="!!busy || !!s.error"
            @change="toggle(s)"
          />
          <span>{{ t('aiSkillsSetDefault') }}</span>
        </label>
        <button class="skill-act" :title="t('aiSkillsSetOpenFile')" @click="openFile(s)">
          <FolderOpen :size="13" />
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 按钮沿用 .btn / .action-row 公共类，这里只排布工具行 */
.skill-actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-3);
}
.skill-count {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--color-text-muted);
}
.skill-hint {
  margin: var(--space-4) 0 0;
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--color-text-muted);
}
.skill-row {
  display: flex;
  align-items: flex-start;
  gap: var(--space-4);
  margin-top: var(--space-3);
  padding: var(--space-4) var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
}
.skill-row.is-broken {
  border-color: var(--color-warning);
}
.skill-main {
  flex: 1;
  min-width: 0;
}
.skill-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--color-text-primary);
  word-break: break-all;
}
.skill-error {
  margin-left: var(--space-3);
  padding: 1px var(--space-2);
  border-radius: var(--radius-xs);
  background: var(--color-danger-bg);
  font-size: 10px;
  font-weight: 500;
  color: var(--color-danger);
}
.skill-desc {
  margin-top: 2px;
  overflow: hidden;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--color-text-secondary);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.skill-path {
  margin-top: 2px;
  overflow: hidden;
  font-size: 10.5px;
  line-height: 1.4;
  color: var(--color-text-muted);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.skill-ops {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  flex: none;
  padding-top: 2px;
}
.skill-toggle {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-size: 11.5px;
  white-space: nowrap;
  color: var(--color-text-secondary);
  cursor: pointer;
}
.skill-check {
  width: 13px;
  height: 13px;
  margin: 0;
  accent-color: var(--color-accent);
  cursor: pointer;
}
.skill-check:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.skill-act {
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
.skill-act:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
</style>
