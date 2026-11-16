<script setup lang="ts">
/**
 * 输入框右下角的「能力档」标记：助手档 / 只读档。
 *
 * 与右边那个「默认权限 / 完全访问」是**两个维度**，刻意不做进同一个菜单：
 *   · 能力档（这里）：AI 有没有这只手。只读档根本不挂写类工具，模型连
 *     「可以改文件」都不知道；
 *   · 权限（隔壁）：有手的时候，动手前问不问。
 *
 * 两件事混成一个下拉，用户就只能看到「危险 / 更危险」这种模糊排序，
 * 而「我不想让它改文件，但懒得每次都确认」这种真实诉求就没法表达。
 *
 * 为什么摆在输入框上：能力档跟着**会话**走，切走再回来要还原。藏在设置页的
 * 全局开关做不到这一点——那是另一层（AI 设置 → 工具），影响的是所有对话。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, Wrench } from '@lucide/vue'

const props = defineProps<{
  /** 'assistant' = 工具全开；'readonly' = 只挂读类工具 */
  scene: string
  /** 正在生成时不允许切换：这一轮已经按旧口径在跑，切了也不生效 */
  disabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'change', scene: string): void
}>()

const { t } = useI18n()

const menuOpen = ref(false)
// 只读档用警示色：不是因为危险，而是它会让 AI 明确地回答「这个我做不了」，
// 用户需要一眼看出「不是它笨，是我把它的手绑住了」。
const readOnly = () => props.scene === 'readonly'

function toggleMenu() {
  if (props.disabled) return
  menuOpen.value = !menuOpen.value
}

function pick(next: string) {
  menuOpen.value = false
  if ((next === 'readonly') === readOnly()) return
  emit('change', next)
}

// Esc 只关自己这一层：不拦的话会被页面的全局快捷键顺手处理掉，
// 弹层还留在屏幕上，「按了没反应」比「关不掉」更让人困惑。
function onEsc(e: KeyboardEvent) {
  if (e.key !== 'Escape' || !menuOpen.value) return
  e.stopPropagation()
  menuOpen.value = false
}

onMounted(() => document.addEventListener('keydown', onEsc, true))
onBeforeUnmount(() => document.removeEventListener('keydown', onEsc, true))
</script>

<template>
  <div class="ai-scene">
    <div v-if="menuOpen" class="ai-scene-backdrop" @click="menuOpen = false"></div>
    <button
      class="ai-scene-badge"
      :class="{ 'is-readonly': readOnly(), 'is-disabled': props.disabled }"
      :title="t('aiSceneTip')"
      @click="toggleMenu"
    >
      <component :is="readOnly() ? Eye : Wrench" :size="12" />
      <span>{{ readOnly() ? t('aiSceneReadOnly') : t('aiSceneAssistant') }}</span>
    </button>

    <div v-if="menuOpen" class="ai-scene-menu">
      <div class="ai-scene-menu-title">{{ t('aiSceneMenuTitle') }}</div>
      <button class="ai-scene-opt" :class="{ 'is-active': !readOnly() }" @click="pick('assistant')">
        <Wrench :size="14" />
        <span class="ai-scene-opt-body">
          <span class="ai-scene-opt-head">
            {{ t('aiSceneAssistant') }}
            <span v-if="!readOnly()" class="ai-scene-opt-now">{{ t('aiSceneCurrent') }}</span>
          </span>
          <span class="ai-scene-opt-desc">{{ t('aiSceneAssistantDesc') }}</span>
        </span>
      </button>
      <button class="ai-scene-opt" :class="{ 'is-active': readOnly() }" @click="pick('readonly')">
        <Eye :size="14" />
        <span class="ai-scene-opt-body">
          <span class="ai-scene-opt-head">
            {{ t('aiSceneReadOnly') }}
            <span v-if="readOnly()" class="ai-scene-opt-now">{{ t('aiSceneCurrent') }}</span>
          </span>
          <span class="ai-scene-opt-desc">{{ t('aiSceneReadOnlyDesc') }}</span>
        </span>
      </button>
      <div class="ai-scene-note">{{ t('aiSceneNote') }}</div>
    </div>
  </div>
</template>

<style scoped>
.ai-scene {
  position: relative;
  display: inline-flex;
  flex-shrink: 0;
}

/* 与隔壁 AiPermissionBadge 同尺寸：两个徽标并排，尺寸不一致会显得一个像补丁。 */
.ai-scene-badge {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  height: 22px;
  padding: 0 var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-muted);
  font-family: inherit;
  font-size: var(--text-xs);
  white-space: nowrap;
  cursor: pointer;
  transition: border-color var(--transition-fast), color var(--transition-fast), background-color var(--transition-fast);
}

.ai-scene-badge:hover {
  color: var(--color-text-primary);
  border-color: var(--color-border-light);
}

.ai-scene-badge.is-readonly {
  border-color: var(--color-accent-border, var(--color-border-light));
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}

.ai-scene-badge.is-disabled {
  opacity: 0.5;
  cursor: default;
}

.ai-scene-backdrop {
  position: fixed;
  inset: 0;
  z-index: 40;
}

/* 面层三级：卡片是 card-bg，浮层必须用 popover-bg 才比卡片亮一层。
   写成 bg-secondary 会盖住卡片后比卡片更暗，像挖了个洞。 */
.ai-scene-menu {
  position: absolute;
  right: 0;
  bottom: calc(100% + 8px);
  z-index: 41;
  width: 264px;
  padding: var(--space-3);
  border: 1px solid var(--popover-border-color, var(--color-border));
  border-radius: var(--radius-lg);
  background: var(--color-popover-bg);
  box-shadow: var(--shadow-lg);
}

.ai-scene-menu-title {
  padding: var(--space-1) var(--space-4) var(--space-2);
  font-size: var(--text-2xs);
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--color-text-muted);
}

.ai-scene-opt {
  display: flex;
  align-items: flex-start;
  gap: var(--space-4);
  width: 100%;
  padding: 7px var(--space-4);
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
}

.ai-scene-opt:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.ai-scene-opt.is-active {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}

.ai-scene-opt-body {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.ai-scene-opt-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  font-size: var(--text-sm);
  font-weight: 500;
}

.ai-scene-opt-now {
  font-size: var(--text-2xs);
  font-weight: 400;
  color: var(--color-accent);
}

.ai-scene-opt-desc {
  font-size: var(--text-xs);
  line-height: 1.45;
  color: var(--color-text-muted);
}

.ai-scene-note {
  margin-top: var(--space-2);
  padding: var(--space-2) var(--space-4) 0;
  border-top: 1px solid var(--color-border);
  font-size: var(--text-2xs);
  line-height: 1.5;
  color: var(--color-text-muted);
}
</style>
