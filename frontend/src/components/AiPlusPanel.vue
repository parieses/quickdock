<script setup lang="ts">
/**
 * 「技能和连接器」面板：输入框左下角 + 号打开。
 *
 * 两个输入框（新建任务的 hero、对话里的 composer）都要这一份，所以独立成组件：
 * 各自的栏位不同（hero 那行已经有空间 / 模型两个下拉），但弹出来的内容完全一致。
 *
 * 两组的作用域**不一样**，交互也就不一样，别看成同一套：
 *   - 技能：点一下 = 在输入框里插一枚行内技能胶囊。选择状态只有那份胶囊数组
 *     （见 useAiSlashSkill），所以行尾不是勾选框，只有一个「已选」文字标记；
 *     再点一次即摘掉。这样「我选了什么」永远看得见、也随时能改。
 *   - 连接器：开关 = 「这台机器上启用这个 server」。装着进程、占着工具名额，
 *     是全局配置（mcp.json 的 disabled），所以行尾是开关。
 *
 * 定位靠最近的 position:relative 祖先（hero 的 .ai-hero-box、composer 的 .ai-input-area），
 * 两个容器都已经满足，这里只管往上弹。
 */
import { useI18n } from 'vue-i18n'
import { FolderPlus, Settings2 } from '@lucide/vue'
import type { AIConnector, AISkill } from '../types/ai'

const props = defineProps<{
  skills: AISkill[]
  connectors: AIConnector[]
  // 输入框里已经写进去的技能名（由父组件从文本解析出来）
  picked: string[]
}>()

const emit = defineEmits<{
  (e: 'pick-skill', sk: AISkill): void
  (e: 'toggle-connector', c: AIConnector): void
  (e: 'install-skill'): void
  (e: 'manage-connectors'): void
  (e: 'close'): void
}>()

const { t } = useI18n()

function isPicked(sk: AISkill): boolean {
  return (props.picked || []).includes(sk.name)
}
</script>

<template>
  <!-- 透明遮罩：只负责「点外面就收起」。面板 z-index 比它高，内部点击不受影响。 -->
  <div class="ai-plus-backdrop" @click="emit('close')"></div>
  <div class="ai-plus-pop">
    <div class="ai-plus-sec">
      <div class="ai-plus-sec-title">{{ t('aiSkillsGroup') }}</div>
      <div class="ai-plus-sec-hint">{{ t('aiSkillsScopeHint') }}</div>
      <div v-if="!props.skills.length" class="ai-plus-empty">{{ t('aiSkillsEmpty') }}</div>
      <button
        v-for="sk in props.skills"
        :key="sk.dir"
        class="ai-plus-row"
        :class="{ 'is-picked': isPicked(sk), 'is-bad': !!sk.error }"
        :title="sk.error || sk.path"
        @click="emit('pick-skill', sk)"
      >
        <span class="ai-plus-name">/{{ sk.name }}</span>
        <span class="ai-plus-desc">{{ sk.error || sk.description || sk.dir }}</span>
        <span v-if="isPicked(sk)" class="ai-plus-state">{{ t('aiSkillPicked') }}</span>
      </button>
      <button class="ai-plus-link" @click="emit('install-skill')">
        <FolderPlus :size="12" /> {{ t('aiInstallSkill') }}
      </button>
    </div>
    <div class="ai-plus-sec">
      <div class="ai-plus-sec-title">{{ t('aiConnectorsGroup') }}</div>
      <div class="ai-plus-sec-hint">{{ t('aiConnectorsScopeHint') }}</div>
      <div v-if="!props.connectors.length" class="ai-plus-empty">{{ t('aiConnectorsEmpty') }}</div>
      <button
        v-for="c in props.connectors"
        :key="c.name"
        class="ai-plus-row"
        :class="{ 'is-on': !c.disabled, 'is-bad': c.status === 'error' }"
        :title="c.error || c.name"
        @click="emit('toggle-connector', c)"
      >
        <span class="ai-plus-name">{{ c.name }}</span>
        <span class="ai-plus-desc">
          {{ c.status === 'error' ? (c.error || t('aiConnectorErr')) : t('aiConnectorTools', { n: c.toolCount }) }}
        </span>
        <span class="ai-plus-switch" :class="{ 'is-on': !c.disabled }"></span>
      </button>
      <button class="ai-plus-link" @click="emit('manage-connectors')">
        <Settings2 :size="12" /> {{ t('aiManageConnectors') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
/* 面板本体：贴着输入框上沿向上展开（与 @ 补全弹层同一套定位，
   输入框贴着窗口底部，往下弹会被切掉）。 */
.ai-plus-pop {
  position: absolute;
  left: 12px;
  bottom: calc(100% - 4px);
  z-index: 41;
  width: 320px;
  max-width: calc(100% - 24px);
  /* 窗口很矮时不能让面板顶出 .ai-main（那里有 overflow:hidden，会被切掉）。 */
  max-height: min(380px, 55vh);
  overflow: auto;
  padding: 6px;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}

.ai-plus-backdrop {
  position: fixed;
  inset: 0;
  z-index: 40;
}

.ai-plus-sec + .ai-plus-sec {
  margin-top: 4px;
  padding-top: 4px;
  border-top: 1px solid var(--color-border);
}

.ai-plus-sec-title {
  padding: 4px 8px 2px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--color-text-muted);
}

/* 作用域说明：两组的生效范围不同（会话级 / 全局），不写出来就只能靠猜 */
.ai-plus-sec-hint {
  padding: 0 8px 3px;
  font-size: 10.5px;
  line-height: 1.4;
  color: var(--color-text-disabled);
}

.ai-plus-empty {
  padding: 4px 8px 6px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-disabled);
}

.ai-plus-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 5px 8px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.ai-plus-row:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-plus-row.is-bad .ai-plus-desc { color: var(--color-danger); }

.ai-plus-row.is-picked .ai-plus-name { color: var(--color-text-primary); }

/* 已选只是文字标记：选择本身是输入框上方那排胶囊（点一下挂上/摘下），
   这里再放一个勾选框，就会出现两套「选了什么」要对账。 */
.ai-plus-state {
  flex: none;
  font-size: 10px;
  color: var(--color-accent);
}

.ai-plus-name {
  flex-shrink: 0;
  max-width: 40%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-size: 11.5px;
}

.ai-plus-desc {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  color: var(--color-text-muted);
}

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

.ai-plus-link {
  display: flex;
  align-items: center;
  gap: 5px;
  width: 100%;
  margin-top: 2px;
  padding: 5px 8px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-accent);
  font-family: inherit;
  font-size: 11.5px;
  cursor: pointer;
}

.ai-plus-link:hover { background: var(--color-accent-bg); }
</style>
