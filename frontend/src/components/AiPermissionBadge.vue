<script setup lang="ts">
/**
 * 输入框右下角的「执行权限」标记，三档：只读 / 默认 / 完全访问。
 *
 * 合并前这里是两档（默认、完全访问），另有一个「能力档」（助手档、只读档）摆在旁边。
 * 两个开关都在回答同一个问题——「AI 这次能动多少」，却要用户在脑子里组合
 * 「有没有这只手」×「动手前问不问」。实际用起来的结果是没人切只读，
 * 而「想让它别动手」只能靠关工具。合成一条从紧到松的轴之后，一个下拉就是全部语义。
 *
 * 三档的差异：
 *   - 只读：写类工具根本不挂，模型连调都调不到（不是挂了再拦）；
 *   - 默认：工具全开，执行命令、装软件、对外发消息前先问；
 *   - 完全访问：不再逐条确认，直接做。硬底线仍然生效——高危命令照样拒绝，
 *     写操作照样只能在授权目录里（fsguard）。那两条是能力边界，不是确认步骤。
 *
 * 切到完全访问要过一道确认卡（勾选风险确认才能点确定），往回收永远是一步到位：
 * 加摩擦只该加在「更危险」的那一侧。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, Folder, Globe, Lock, ShieldAlert, ShieldCheck, Terminal, X } from '@lucide/vue'

const props = defineProps<{
  /** 当前档位：'readonly' | '' （默认）| 'full' */
  level: string
  /** 正在生成时不允许切换：这一轮已经按旧口径在跑，切了也不生效，只会造成误解 */
  disabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'change', level: string): void
}>()

const { t } = useI18n()

// 归一一次：脏值（老前端传的 assistant 之类）当默认档，与后端 aiNormalizePermission 同口径。
const cur = computed(() => {
  const v = (props.level || '').trim().toLowerCase()
  if (v === 'readonly' || v === 'full') return v
  return ''
})

const badgeIcon = computed(() => (cur.value === 'full' ? ShieldAlert : cur.value === 'readonly' ? Lock : ShieldCheck))

const badgeText = computed(() => {
  if (cur.value === 'full') return t('aiPermFull')
  if (cur.value === 'readonly') return t('aiPermReadOnly')
  return t('aiPermDefault')
})

// 菜单与确认卡各自一份状态：菜单是轻量弹层，确认卡是模态框，
// 合成一个「当前步骤」枚举反而要处理「菜单关掉但确认卡没关」这类中间态。
const menuOpen = ref(false)
const confirmOpen = ref(false)
const agreed = ref(false)

function toggleMenu() {
  if (props.disabled) return
  menuOpen.value = !menuOpen.value
}

/** 选一档：只有「完全访问」要过确认卡，往回收一步到位。 */
function pick(level: string) {
  menuOpen.value = false
  if (level === cur.value) return
  if (level === 'full') {
    // 每次打开都重置勾选：把上次的勾留在这里，等于第二次确认变成一次点击。
    agreed.value = false
    confirmOpen.value = true
    return
  }
  emit('change', level)
}

function confirmFull() {
  // 勾选是唯一的门槛，按钮的 disabled 只是同一件事的视觉提示——
  // 这里再判一次，免得键盘/程序化触发绕过它。
  if (!agreed.value) return
  confirmOpen.value = false
  emit('change', 'full')
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
      :class="{ 'is-full': cur === 'full', 'is-readonly': cur === 'readonly', 'is-disabled': props.disabled }"
      :title="t('aiPermTip')"
      @click="toggleMenu"
    >
      <component :is="badgeIcon" :size="12" />
      <span>{{ badgeText }}</span>
    </button>

    <div v-if="menuOpen" class="ai-perm-menu">
      <div class="ai-perm-menu-title">{{ t('aiPermMenuTitle') }}</div>
      <button class="ai-perm-opt" :class="{ 'is-active': cur === 'readonly' }" @click="pick('readonly')">
        <Lock :size="14" />
        <span class="ai-perm-opt-body">
          <span class="ai-perm-opt-head">
            {{ t('aiPermReadOnly') }}
            <span v-if="cur === 'readonly'" class="ai-perm-opt-now">{{ t('aiPermCurrent') }}</span>
          </span>
          <span class="ai-perm-opt-desc">{{ t('aiPermReadOnlyDesc') }}</span>
        </span>
      </button>
      <button class="ai-perm-opt" :class="{ 'is-active': cur === '' }" @click="pick('')">
        <ShieldCheck :size="14" />
        <span class="ai-perm-opt-body">
          <span class="ai-perm-opt-head">
            {{ t('aiPermDefault') }}
            <span v-if="cur === ''" class="ai-perm-opt-now">{{ t('aiPermCurrent') }}</span>
          </span>
          <span class="ai-perm-opt-desc">{{ t('aiPermDefaultDesc') }}</span>
        </span>
      </button>
      <button class="ai-perm-opt is-risk" :class="{ 'is-active': cur === 'full' }" @click="pick('full')">
        <ShieldAlert :size="14" />
        <span class="ai-perm-opt-body">
          <span class="ai-perm-opt-head">
            {{ t('aiPermFull') }}
            <span v-if="cur === 'full'" class="ai-perm-opt-now">{{ t('aiPermCurrent') }}</span>
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

/* 徽标：和 git 分支 chip 同尺寸，靠图标 + 文字区分三档；完全访问用警示色，
   不是因为「危险」要吓人，而是它确实和默认权限不是一个档次的后果。 */
.ai-perm-badge {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  height: 22px;
  padding: 0 var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  /* 权限是「AI 这次能动多少」，底栏上最该看清的一枚：从 muted 提到 secondary，
     完全访问仍靠警示色区分（见下） */
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-xs);
  white-space: nowrap;
  cursor: pointer;
  transition: border-color var(--transition-fast), color var(--transition-fast), background-color var(--transition-fast);
}

.ai-perm-badge:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }

.ai-perm-badge.is-full {
  border-color: var(--color-warning-border);
  background: var(--color-warning-bg);
  color: var(--color-warning);
}

.ai-perm-badge.is-readonly {
  border-color: var(--color-border-light);
  color: var(--color-text-secondary);
}

.ai-perm-badge.is-disabled { opacity: var(--opacity-disabled); cursor: default; }

/* 点外面收起菜单。z-index 比菜单低一层，菜单内部点击不受影响。 */
.ai-perm-backdrop {
  position: fixed;
  inset: 0;
  z-index: var(--z-ctx-backdrop);
}

.ai-perm-menu {
  position: absolute;
  right: 0;
  bottom: calc(100% + 8px);
  z-index: calc(var(--z-ctx-backdrop) + 1);
  width: 260px;
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-popover-bg);
  /* 与空间下拉、worktree 分支下拉同档：贴着底栏向上弹的小菜单，
     lg 的大扩散会压暗旁边的按钮和输入框边缘，看着像浮层把底下挖了个洞。 */
  box-shadow: var(--shadow-md);
}

.ai-perm-menu-title {
  padding: var(--space-1) var(--space-4) var(--space-2);
  font-size: var(--text-2xs);
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--color-text-muted);
}

.ai-perm-opt {
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

.ai-perm-opt:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-perm-opt.is-active { background: var(--color-bg-active); color: var(--color-text-primary); }

.ai-perm-opt.is-risk svg { color: var(--color-warning); }

.ai-perm-opt-body { display: flex; flex-direction: column; gap: 1px; min-width: 0; }

.ai-perm-opt-head { display: flex; align-items: center; gap: var(--space-3); font-size: var(--text-sm); font-weight: 500; }

.ai-perm-opt-now { font-size: var(--text-2xs); font-weight: 400; color: var(--color-accent); }

.ai-perm-opt-desc { font-size: var(--text-xs); line-height: 1.45; color: var(--color-text-muted); }

/* ── 确认卡 ── */

.ai-perm-modal {
  position: fixed;
  inset: 0;
  z-index: var(--z-modal);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-8);
  background: var(--color-bg-overlay);
}

.ai-perm-dialog {
  width: 100%;
  max-width: 420px;
  max-height: 86vh;
  overflow: auto;
  padding: var(--space-6) 18px 14px;
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-lg);
  background: var(--color-popover-bg);
  box-shadow: var(--shadow-lg);
}

.ai-perm-dialog-head { display: flex; align-items: center; gap: var(--space-4); }

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
  font-size: var(--text-sm);
  line-height: 1.65;
  color: var(--color-text-secondary);
}

.ai-perm-risks {
  margin-top: var(--space-5);
  padding: 10px var(--space-5);
  display: flex;
  flex-direction: column;
  gap: 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-primary);
}

.ai-perm-risk-row { display: flex; align-items: flex-start; gap: var(--space-4); color: var(--color-text-secondary); }

.ai-perm-risk-row > svg { flex: none; margin-top: var(--space-1); }

.ai-perm-risk-name { font-size: var(--text-sm); font-weight: 500; color: var(--color-text-primary); }

.ai-perm-risk-desc { margin-top: 1px; font-size: var(--text-xs); line-height: 1.5; color: var(--color-text-muted); }

/* 勾选行：整行可点，复选框自己画——原生 checkbox 在不同主题下深一块浅一块。 */
.ai-perm-agree {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-top: var(--space-5);
  font-size: var(--text-sm);
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
  color: var(--color-text-primary);
  cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast);
}

.ai-perm-check.is-on { background: var(--color-danger); border-color: var(--color-danger); color: var(--color-danger-text); }

.ai-perm-dialog-note {
  margin-top: 10px;
  font-size: var(--text-xs);
  line-height: 1.55;
  color: var(--color-text-muted);
}

.ai-perm-dialog-foot { display: flex; justify-content: flex-end; gap: var(--space-4); margin-top: 14px; }

.ai-perm-btn {
  padding: 5px 14px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-sm);
  cursor: pointer;
}

.ai-perm-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.ai-perm-btn.is-danger {
  border-color: var(--color-danger);
  background: var(--color-danger);
  color: var(--color-danger-text);
}

.ai-perm-btn.is-danger:hover { background: var(--color-danger-hover); }

.ai-perm-btn.is-danger:disabled {
  border-color: var(--color-border);
  background: var(--color-bg-active);
  color: var(--color-text-disabled);
  cursor: default;
}
</style>
