<script setup lang="ts">
/**
 * 消息里的「派活 = 团队成员卡片」。
 *
 * 为什么单独成组件：派活原本混在工具列表的 .ai-tool 里，折叠条一旦换成团队标题，
 * 同一批里的普通工具（如「列出」）就被卷进团队卡内部——用户要的是普通工具在上、
 * 团队卡在下。所以这里把派活抽出来独立渲染，工具列表只留普通工具。
 *
 * 与 AgentRunTree.vue 的区别：那是消息底部按 run_id 从后端拉的完整编排树（含
 * token/步骤/模型）；这里用的是流式/历史 tool_calls 里的 spawn_agent 调用，
 * 流式期就要能看到，数据源和字段都不同，但共用 panels.css 的 qd-run-* 视觉。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight } from '@lucide/vue'
import { fmtElapsed } from '../utils/aiRunTree'
import { agentRoleLabel, spawnArgs, spawnPartial, spawnResultText, spawnTaskBrief } from '../utils/aiTools'
import type { AIToolCall } from '../types/ai'

const props = defineProps<{ calls: AIToolCall[] }>()
const { t } = useI18n()

/** 成员状态：与编排树同口径，expired / 拒绝都归到失败一档。 */
function statusOf(tc: AIToolCall): { text: string; cls: string } {
  if (tc.error || tc.denied || tc.approval === 'expired') return { text: t('aiRunStatusFailed'), cls: 'failed' }
  if (tc.running) return { text: t('aiRunStatusRunning'), cls: 'running' }
  if (spawnResultText(tc)) return { text: t('aiRunStatusDone'), cls: 'done' }
  return { text: t('aiRunStatusPending'), cls: 'pending' }
}

/** 单个子 agent 实时在干什么（第 N 轮 · 正在搜代码 · 2.1s）。 */
function progressOf(tc: AIToolCall): string {
  const c = tc.child
  const bits: string[] = []
  if (c?.round) bits.push(t('aiSpawnRound', { n: c.round }))
  if (c?.phase === 'tool' && c.tool) bits.push(c.tool)
  else if (c?.phase === 'tool_done' && c.ms) bits.push(fmtElapsed(c.ms))
  return bits.join(' · ')
}

const members = computed(() => props.calls.map((tc, i) => {
  const role = (spawnArgs(tc.args)?.role || 'agent').trim()
  const base = role.charAt(0).toUpperCase() + role.slice(1)
  const name = /-\d+$/.test(base) ? base : `${base}-${i + 1}`
  const st = statusOf(tc)
  return {
    tc,
    name,
    role,
    roleLabel: agentRoleLabel(role, t),
    letter: (role.charAt(0) || '?').toUpperCase(),
    tone: ['a', 'b', 'c', 'd'][i % 4],
    task: spawnTaskBrief(spawnArgs(tc.args)?.task || ''),
    result: spawnResultText(tc),
    partial: spawnPartial(tc),
    statusText: st.text,
    statusCls: st.cls,
  }
}))

/**
 * 组进度：按这张卡里成员的真实状态算，不读后端心跳帧的 done/total。
 *
 * 后端心跳是按 parallelGroups 分段统计的（一轮最多 agentMaxParallel=5 个并发）：
 * 7 个派活分两波时，帧里是「第二波 0/3 → 1/3」，而这里展示的是整组 7 位成员。
 * 直接用帧会让「4 个已完成」旁边显示「0/3 完成」——分母和成员数都不是一回事。
 * 等待时间仍用心跳帧：它是当前并发段真实在等的秒数，比本地猜的更准。
 */
const group = computed(() => {
  if (members.value.length < 2) return null
  const done = members.value.filter((m) => m.statusCls === 'done' || m.statusCls === 'failed').length
  const last = props.calls[props.calls.length - 1]
  return { done, total: members.value.length, elapsedMs: last?.groupElapsedMs || 0 }
})
</script>

<template>
  <details class="qd-team">
    <summary class="qd-team-sum">
      <!-- 叠加头像：最多 4 位，超出用 +N -->
      <span class="qd-run-avatars">
        <span
          v-for="(m, i) in members.slice(0, 4)" :key="m.tc.id"
          class="qd-run-avatar qd-run-avatar--stack" :class="'tone-' + m.tone"
        >{{ m.letter }}</span>
        <span v-if="members.length > 4" class="qd-run-avatar qd-run-avatar--more">+{{ members.length - 4 }}</span>
      </span>
      <span>{{ t('aiTeamMembers', { n: members.length }) }}</span>
      <!-- 进度直接挂在折叠条上：收起时也能看到「4/4 完成 · 已等 1m33s」，
           不必点开一张张数状态。口径与展开底部的组进度同源（都是 group）。 -->
      <span v-if="group" class="qd-team-progress">
        <span class="qd-team-progress-sep">·</span>
        {{ t('aiSpawnGroupProgress', { done: group.done, total: group.total }) }}
        <template v-if="group.elapsedMs">
          <span class="qd-team-progress-sep">·</span>
          <span class="qd-team-progress-elapsed">{{ t('aiSpawnGroupElapsed', { t: fmtElapsed(group.elapsedMs) }) }}</span>
        </template>
      </span>
      <ChevronDown :size="13" class="qd-team-chev" />
    </summary>
    <div class="qd-team-body">
      <!-- 一人一张卡：头像 + 名字 + 状态点，点开看任务全文 / 进度 / 结论 -->
      <details v-for="m in members" :key="m.tc.id" class="qd-run-member" :class="'is-' + m.statusCls">
        <summary class="qd-run-member-sum">
          <span class="qd-run-avatar" :class="'tone-' + m.tone">{{ m.letter }}</span>
          <span class="qd-run-member-name">{{ m.name }}</span>
          <!-- 中文职能小标签：名字里的 Writer 是英文角色标识，不展开也能
               看到「写作」两个字，一眼知道这位成员负责哪方面。 -->
          <span v-if="m.roleLabel" class="qd-team-role-chip">{{ m.roleLabel }}</span>
          <!-- 任务简介直接贴在名字后面：不展开也能看出这位成员负责哪块功能。
               完整 task 仍在展开的 body 里，这里只放首行摘要，靠 CSS 截断。 -->
          <span v-if="m.task" class="qd-run-member-task" :title="m.task">{{ m.task }}</span>
          <span class="qd-run-member-status" :class="'is-' + m.statusCls">
            <span class="qd-run-dot" :class="'is-' + m.statusCls"></span>
            {{ m.statusText }}
          </span>
          <ChevronRight :size="13" class="qd-run-member-arrow" />
        </summary>
        <div class="qd-run-member-body">
          <div v-if="m.roleLabel" class="qd-team-role">
            <span class="qd-team-role-tag">{{ t('aiAgentRoleLabel') }}</span>
            <span class="qd-run-role">{{ m.role }}</span>
            <span>· {{ m.roleLabel }}</span>
          </div>
          <p v-if="m.task" class="qd-run-task-full">{{ m.task }}</p>
          <div v-if="m.tc.running && progressOf(m.tc)" class="ai-spawn-progress">{{ progressOf(m.tc) }}</div>
          <div v-if="m.partial" class="ai-spawn-partial">{{ t('aiSpawnPartial') }}</div>
          <pre v-if="m.result" class="qd-team-result">{{ m.result }}</pre>
          <div v-else class="ai-spawn-hint">{{ m.tc.running ? t('aiSpawnPending') : t('aiSpawnNoResult') }}</div>
        </div>
      </details>
      <!-- 整组进度：主 agent 等结论时唯一的「还剩几个」，贴整组底部一条 -->
      <div v-if="group" class="ai-spawn-group">
        <span>{{ t('aiSpawnGroupProgress', { done: group.done, total: group.total }) }}</span>
        <span v-if="group.elapsedMs" class="ai-spawn-group-ms">
          {{ t('aiSpawnGroupElapsed', { t: fmtElapsed(group.elapsedMs) }) }}
        </span>
      </div>
    </div>
  </details>
</template>

<style scoped>
/* 视觉复用 panels.css 的全局 .qd-run-*（头像 / 成员卡 / 状态点 / 进度行），
   这里只定义团队容器自身；颜色一律取既有 token，不新增色值。 */
.qd-team { margin: var(--space-2) 0 var(--space-1); }
.qd-team > summary {
  display: flex; align-items: center; gap: var(--space-3);
  width: fit-content; max-width: 100%;
  padding: 3px var(--space-3); margin-left: -6px;
  border-radius: var(--radius-sm); list-style: none;
  cursor: pointer; user-select: none; outline: none;
  white-space: nowrap;
  font-size: var(--text-sm); color: var(--color-text-secondary);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.qd-team > summary::-webkit-details-marker { display: none; }
.qd-team > summary:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.qd-team-chev {
  margin-left: auto; flex: 0 0 auto;
  color: var(--color-text-muted); transition: transform var(--transition-fast);
}
.qd-team[open] > summary .qd-team-chev { transform: rotate(180deg); }
/* 折叠条上的组进度：次要色小字，不喧宾夺主，但收起时也一眼能看到进度。 */
.qd-team-progress {
  display: inline-flex; align-items: center; gap: 3px;
  font-size: var(--text-xs); color: var(--color-text-muted);
}
.qd-team-progress-sep { color: var(--color-text-muted); }
.qd-team-progress-elapsed { font-family: var(--font-mono); }
/* 展开后把成员卡包成一个整体面板：外壳用 bg-secondary（比成员卡的 card-bg
   暗一档），避免和卡片同一层颜色糊在一起。 */
.qd-team-body {
  display: flex; flex-direction: column; gap: 5px;
  margin-top: 6px; padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
}
/* 成员行上的任务简介：紧跟名字，占满中间剩余宽度，超长省略；
   title 放完整 task，鼠标悬停能看全句。名字与状态都不让它挤变形。 */
.qd-team .qd-run-member-name { flex-shrink: 0; }
.qd-run-member-task {
  flex: 1 1 auto; min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--text-xs); color: var(--color-text-muted);
}
.qd-team .qd-run-member-status { flex-shrink: 0; }
.qd-team-result {
  margin: var(--space-2) 0 0; padding: var(--space-3) var(--space-4); max-height: 260px; overflow: auto;
  font-family: var(--font-mono); font-size: var(--text-xs); line-height: 1.5;
  color: var(--color-text-secondary); background: var(--color-bg-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  white-space: pre-wrap; word-break: break-word;
}
/* 进度 / 残本 / 结论提示 / 组进度：从原 AiMessageList 的派活卡样式迁来，
   颜色一律既有 token。 */
.ai-spawn-progress {
  font-size: var(--text-xs); font-family: var(--font-mono); color: var(--color-text-muted);
}
.ai-spawn-hint { font-size: var(--text-xs); color: var(--color-text-muted); }
.ai-spawn-partial {
  align-self: flex-start;
  font-size: var(--text-xs); padding: 1px var(--space-2);
  border-radius: var(--radius-sm);
  color: var(--color-warning-ink); background: var(--color-warning-bg);
}
.ai-spawn-group {
  display: flex; align-items: center; gap: var(--space-2);
  margin-top: var(--space-2); padding-top: var(--space-2);
  border-top: 1px dashed var(--color-border-light);
  font-size: var(--text-xs); color: var(--color-text-secondary);
}
.ai-spawn-group-ms { color: var(--color-text-muted); font-family: var(--font-mono); }
</style>
