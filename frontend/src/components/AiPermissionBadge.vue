<script setup lang="ts">
/**
 * 输入框右下角的「当前权限」标记：默认权限 / 完全访问。
 *
 * 为什么要把这件事摆在输入框上，而不是只藏在设置页：审批卡出现的那一刻，
 * 用户唯一的疑问就是「这破玩意为什么每次都拦我」。如果答案在三级菜单里，
 * 他的选择永远是「去设置里把确认全关掉」；摆在眼前，他才知道这是一档可以
 * 随对话切换的东西，用完能切回来。
 *
 * 两档的差异只有一处：**问不问**。
 *   - 默认权限：写命令、发外部消息之类会停下来等确认（读文件、改授权目录里的文件不拦）；
 *   - 完全访问：不再问，直接做。工具层的硬底线仍然生效——高危命令（rm -rf /、
 *     格式化磁盘）照样拒绝执行，写操作照样只能在授权目录里（fsguard）。
 *     这两条是能力边界，不是确认步骤，放开确认步骤不该连带放开它们。
 *
 * 切到完全访问要过一道确认卡（勾选风险确认才能点确定），切回默认权限
 * 永远是一步到位：加摩擦只该加在「更危险」的那一侧。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, Folder, Globe, ShieldAlert, ShieldCheck, Terminal, X } from '@lucide/vue'

const props = defineProps<{
  /** 当前是否完全访问 */
  full: boolean
  /** 正在生成时不允许切换：这一轮已经按旧口径在跑，切了也不生效，只会造成误解 */
  disabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'change', full: boolean): void
}>()

const { t } = useI18n()

// 菜单与确认卡各自一份状态：菜单是轻量弹层，确认卡是模态框，
// 合成一个「当前步骤」枚举反而要处理「菜单关掉但确认卡没关」这类中间态。
const menuOpen = ref(false)
const confirmOpen = ref(false)
const agreed = ref(false)

function toggleMenu() {
  if (props.disabled) return
  menuOpen.value = !menuOpen.value
}

function pickDefault() {
  menuOpen.value = false
  if (!props.full) return
  emit('change', false)
}

function pickFull() {
  menuOpen.value = false
  if (props.full) return
  // 每次打开都重置勾选：把上次的勾留在这里，等于第二次确认变成一次点击。
  agreed.value = false
  confirmOpen.value = true
}

function confirmFull() {
  // 勾选是唯一的门槛，按钮的 disabled 只是同一件事的视觉提示——
  // 这里再判一次，免得键盘/程序化触发绕过它。
  if (!agreed.value) return
  confirmOpen.value = false
  emit('change', true)
}

function cancelConfirm() {
  confirmOpen.value = false
  agreed.value = false
}

// Esc 关自己这一层（捕获阶段拦下）：不加这一条，按 Esc 会被页面的全局快捷键
// 顺手处理掉，而这层模态框还留在屏幕上——「按了没反应」比「关不掉」更让人困惑。
function onEsc(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (confirmOpen.value) {
    e.stopPropagation()
    cancelConfirm()
    return
  }
  if (menuOpen.value) {
    e.stopPropagation()
    menuOpen.value = false
  }
}

onMounted(() => document.addEventListener('keydown', onEsc, true))
onBeforeUnmount(() => document.removeEventListener('keydown', onEsc, true))
</script>

<template>
  <div class="ai-perm">
    <div v-if="menuOpen" class="ai-perm-backdrop" @click="menuOpen = false"></div>
    <button
      class="ai-perm-badge"
      :class="{ 'is-full': props.full, 'is-disabled': props.disabled }"
      :title="t('aiPermTip')"
      @click="toggleMenu"
    >
      <component :is="props.full ? ShieldAlert : ShieldCheck" :size="12" />
      <span>{{ props.full ? t('aiPermFull') : t('aiPermDefault') }}</span>
    </button>

    <div v-if="menuOpen" class="ai-perm-menu">
      <div class="ai-perm-menu-title">{{ t('aiPermMenuTitle') }}</div>
      <button class="ai-perm-opt" :class="{ 'is-active': !props.full }" @click="pickDefault">
        <ShieldCheck :size="14" />
        <span class="ai-perm-opt-body">
          <span class="ai-perm-opt-head">
            {{ t('aiPermDefault') }}
            <span v-if="!props.full" class="ai-perm-opt-now">{{ t('aiPermCurrent') }}</span>
          </span>
          <span class="ai-perm-opt-desc">{{ t('aiPermDefaultDesc') }}</span>
        </span>
      </button>
      <button class="ai-perm-opt is-risk" :class="{ 'is-active': props.full }" @click="pickFull">
        <ShieldAlert :size="14" />
        <span class="ai-perm-opt-body">
          <span class="ai-perm-opt-head">
            {{ t('aiPermFull') }}
            <span v-if="props.full" class="ai-perm-opt-now">{{ t('aiPermCurrent') }}</span>
          </span>
          <span class="ai-perm-opt-desc">{{ t('aiPermFullDesc') }}</span>
        </span>
      </button>
    </div>

    <Teleport to="body">
      <div v-if="confirmOpen" class="ai-perm-modal" @click.self="cancelConfirm">
        <div class="ai-perm-dialog" role="dialog" aria-modal="true">
          <div class="ai-perm-dialog-head">
            <ShieldAlert :size="16" class="ai-perm-dialog-icon" />
            <div class="ai-perm-dialog-title">{{ t('aiPermConfirmTitle') }}</div>
            <button class="ai-perm-dialog-x" :title="t('aiPermCancel')" @click="cancelConfirm">
              <X :size="14" />
            </button>
          </div>
          <div class="ai-perm-dialog-desc">{{ t('aiPermConfirmDesc') }}</div>

          <div class="ai-perm-risks">
            <div class="ai-perm-risk-row">
              <Folder :size="14" />
              <div>
                <div class="ai-perm-risk-name">{{ t('aiPermFileOps') }}</div>
                <div class="ai-perm-risk-desc">{{ t('aiPermFileOpsDesc') }}</div>
              </div>
            </div>
            <div class="ai-perm-risk-row">
              <Terminal :size="14" />
              <div>
                <div class="ai-perm-risk-name">{{ t('aiPermTerminal') }}</div>
                <div class="ai-perm-risk-desc">{{ t('aiPermTerminalDesc') }}</div>
              </div>
            </div>
            <div class="ai-perm-risk-row">
              <Globe :size="14" />
              <div>
                <div class="ai-perm-risk-name">{{ t('aiPermInternet') }}</div>
                <div class="ai-perm-risk-desc">{{ t('aiPermInternetDesc') }}</div>
              </div>
            </div>
          </div>

          <!-- 整行可点：勾选框自己画（原生 checkbox 在不同主题里深一块浅一块），
               外层用 div 而不是 label —— label 套 button 的激活规则在各家浏览器
               细节不一，容易出现「点一下翻两次」。 -->
          <div
            class="ai-perm-agree"
            role="checkbox"
            :aria-checked="agreed"
            tabindex="0"
            @click="agreed = !agreed"
            @keydown.space.prevent="agreed = !agreed"
            @keydown.enter.prevent="agreed = !agreed"
          >
            <span class="ai-perm-check" :class="{ 'is-on': agreed }">
              <Check v-if="agreed" :size="11" :stroke-width="3" />
            </span>
            <span>{{ t('aiPermAgree') }}</span>
          </div>

          <div class="ai-perm-dialog-note">{{ t('aiPermNote') }}</div>

          <div class="ai-perm-dialog-foot">
            <button class="ai-perm-btn" @click="cancelConfirm">{{ t('aiPermCancel') }}</button>
            <button class="ai-perm-btn is-danger" :disabled="!agreed" @click="confirmFull">
              {{ t('aiPermConfirm') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.ai-perm {
  position: relative;
  display: inline-flex;
  flex-shrink: 0;
}

/* 徽标：和 git 分支 chip 同尺寸，靠图标 + 文字区分两档；完全访问用警示色，
   不是因为「危险」要吓人，而是它确实和默认权限不是一个档次的后果。 */
.ai-perm-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 22px;
  padding: 0 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-muted);
  font-family: inherit;
  font-size: 11px;
  white-space: nowrap;
  cursor: pointer;
  transition: border-color var(--transition-fast), color var(--transition-fast), background-color var(--transition-fast);
}

.ai-perm-badge:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }

.ai-perm-badge.is-full {
  border-color: color-mix(in srgb, var(--color-warning) 55%, transparent);
  background: color-mix(in srgb, var(--color-warning) 12%, transparent);
  color: var(--color-warning);
}

.ai-perm-badge.is-disabled { opacity: 0.5; cursor: default; }

/* 点外面收起菜单。z-index 比菜单低一层，菜单内部点击不受影响。 */
.ai-perm-backdrop {
  position: fixed;
  inset: 0;
  z-index: 40;
}

.ai-perm-menu {
  position: absolute;
  right: 0;
  bottom: calc(100% + 8px);
  z-index: 41;
  width: 260px;
  padding: 6px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-bg-primary);
  box-shadow: var(--shadow-lg);
}

.ai-perm-menu-title {
  padding: 2px 8px 4px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.06em;
  
  color: var(--color-text-muted);
}

.ai-perm-opt {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  width: 100%;
  padding: 7px 8px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
}

.ai-perm-opt:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-perm-opt.is-active { background: var(--color-bg-active); color: var(--color-text-primary); }

.ai-perm-opt.is-risk svg { color: var(--color-warning); }

.ai-perm-opt-body { display: flex; flex-direction: column; gap: 1px; min-width: 0; }

.ai-perm-opt-head { display: flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 500; }

.ai-perm-opt-now { font-size: 10px; font-weight: 400; color: var(--color-accent); }

.ai-perm-opt-desc { font-size: 11px; line-height: 1.45; color: var(--color-text-muted); }

/* ── 确认卡 ── */

.ai-perm-modal {
  position: fixed;
  inset: 0;
  z-index: 3000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: rgba(0, 0, 0, 0.45);
}

.ai-perm-dialog {
  width: 100%;
  max-width: 420px;
  max-height: 86vh;
  overflow: auto;
  padding: 16px 18px 14px;
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-lg);
  background: var(--color-popover-bg);
  box-shadow: var(--shadow-lg);
}

.ai-perm-dialog-head { display: flex; align-items: center; gap: 8px; }

.ai-perm-dialog-icon { flex: none; color: var(--color-danger); }

.ai-perm-dialog-title { flex: 1; min-width: 0; font-size: var(--text-base); font-weight: 600; color: var(--color-text-primary); }

.ai-perm-dialog-x {
  flex: none;
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

.ai-perm-dialog-x:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-perm-dialog-desc {
  margin-top: 10px;
  font-size: 12px;
  line-height: 1.65;
  color: var(--color-text-secondary);
}

.ai-perm-risks {
  margin-top: 12px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-primary);
}

.ai-perm-risk-row { display: flex; align-items: flex-start; gap: 8px; color: var(--color-text-secondary); }

.ai-perm-risk-row > svg { flex: none; margin-top: 2px; }

.ai-perm-risk-name { font-size: 12px; font-weight: 500; color: var(--color-text-primary); }

.ai-perm-risk-desc { margin-top: 1px; font-size: 11px; line-height: 1.5; color: var(--color-text-muted); }

/* 勾选行：整行可点，复选框自己画——原生 checkbox 在不同主题下深一块浅一块。 */
.ai-perm-agree {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 12px;
  font-size: 12px;
  color: var(--color-text-primary);
  cursor: pointer;
}

.ai-perm-check {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 15px;
  height: 15px;
  padding: 0;
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-xs);
  background: transparent;
  color: #fff;
  cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast);
}

.ai-perm-check.is-on { background: var(--color-danger); border-color: var(--color-danger); }

.ai-perm-dialog-note {
  margin-top: 10px;
  font-size: 11px;
  line-height: 1.55;
  color: var(--color-text-muted);
}

.ai-perm-dialog-foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }

.ai-perm-btn {
  padding: 5px 14px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
}

.ai-perm-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.ai-perm-btn.is-danger {
  border-color: var(--color-danger);
  background: var(--color-danger);
  color: #fff;
}

.ai-perm-btn.is-danger:hover { background: color-mix(in srgb, var(--color-danger) 85%, #000); }

.ai-perm-btn.is-danger:disabled {
  border-color: var(--color-border);
  background: var(--color-bg-active);
  color: var(--color-text-disabled);
  cursor: default;
}
</style>
