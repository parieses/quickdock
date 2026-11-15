<script setup lang="ts">
/**
 * EnvironmentPage 的弹窗层。
 *
 * 状态仍全部归 EnvironmentPage（与详情区、工具栏共用同一份 ref / reactive），
 * 这里只渲染并把交互写回 ctx。ctx 是父组件传来的 reactive({...})：
 * reactive 会自动 unwrap 其中的 ref，所以 `ctx.x = v` 原样落到父级
 * `x.value`，v-model 双向依然有效；函数则直接调父级实现。
 * 模板 ref 改函数式，保证父组件里的 focus() / markSaved() 仍能拿到实例。
 *
 * 样式随模板一起搬：scoped 选择器穿不进子组件，留在父级会整块失效。
 */
import { useI18n } from 'vue-i18n'
import RuntimeLogView from './RuntimeLogView.vue'
defineProps<{ ctx: any }>()
const { t } = useI18n()
</script>

<template>
<!-- 别名 / 备注编辑弹窗 -->
<div v-if="ctx.editing.open" class="modal-overlay" @click.self="ctx.editing.open = false">
  <div class="modal">
    <div class="modal-title">{{ t('editAliasNote') }} · {{ ctx.editing.version }}</div>
    <label class="modal-label">{{ t('envAlias') }}</label>
    <input v-model="ctx.editing.alias" class="env-input" :placeholder="t('aliasPlaceholder')" />
    <label class="modal-label">{{ t('envNote') }}</label>
    <textarea v-model="ctx.editing.note" class="env-textarea" :placeholder="t('envNotePlaceholder')" rows="3"></textarea>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.editing.open = false">{{ t('cancel') }}</button>
      <button class="env-install-btn" @click="ctx.saveAlias">{{ t('save') }}</button>
    </div>
  </div>
</div>

<!-- 删除确认弹窗 -->
<div v-if="ctx.confirmDelete.open" class="modal-overlay" @click.self="ctx.confirmDelete.open = false">
  <div class="modal">
    <div class="modal-title">{{ t('confirmDeleteTitle') }}</div>
    <p class="modal-text">{{ t('confirmDeleteVersion', { version: ctx.confirmDelete.version }) }}</p>
    <label v-if="ctx.HAS_DATA_DIR.has(ctx.confirmDelete.runtime)" class="modal-check">
      <input type="checkbox" v-model="ctx.confirmDelete.removeData" />
      <span>{{ t('deleteWithData') }}</span>
    </label>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.confirmDelete.open = false">{{ t('cancel') }}</button>
      <button class="env-install-btn danger" @click="ctx.doDelete">{{ t('deleteVersion') }}</button>
    </div>
  </div>
</div>

<!-- 模型删除确认弹窗：与版本删除分开，避免误把「删模型」当成「删程序」 -->
<div v-if="ctx.ollamaDelTarget" class="modal-overlay" @click.self="ctx.ollamaDelTarget = null">
  <div class="modal">
    <div class="modal-title">{{ t('ollamaConfirmDeleteTitle') }}</div>
    <p class="modal-text">{{ t('ollamaConfirmDelete', { name: ctx.ollamaDelTarget.name, size: ctx.ollamaSize(ctx.ollamaDelTarget.size) }) }}</p>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.ollamaDelTarget = null">{{ t('cancel') }}</button>
      <button class="env-install-btn danger" @click="ctx.deleteOllamaModel">{{ t('delete') }}</button>
    </div>
  </div>
</div>

<!-- 通用日志弹窗：任意实现了 LogProvider 的运行时（nginx/redis/caddy/traefik/apache/postgresql/mysql/mariadb/mongodb/memcached/minio/mailpit…）-->
<div v-if="ctx.logModal.open" class="modal-overlay" @click.self="ctx.closeLogModal()">
  <div class="modal php-modal">
    <div class="modal-title">{{ t('viewLog') }} · {{ ctx.logModal.runtime }} · {{ ctx.logModal.version }}</div>
    <div class="php-tab-body">
      <!-- 日志查看器自带搜索高亮 / 级别着色 / 跟随末尾；自动刷新仍由本页负责（要调后端） -->
      <RuntimeLogView
        :text="ctx.logModal.log"
        :empty-text="t('logEmpty')"
        @refresh="ctx.refreshLogModal()"
      >
        <template #actions>
          <button class="log-btn" :class="{ active: ctx.logModal.logAuto }" @click="ctx.toggleLogModalAuto()">
            {{ ctx.logModal.logAuto ? t('logAutoOn') : t('logAutoOff') }}
          </button>
        </template>
      </RuntimeLogView>
    </div>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.closeLogModal()">{{ t('cancel') }}</button>
    </div>
  </div>
</div>

<!-- 特色操作输出弹窗：各语言生态的只读查询 / 可逆写操作 / 清理动作共用一个文本视图 -->
<div v-if="ctx.taskModal.open" class="modal-overlay" @click.self="ctx.taskModal.open = false">
  <div class="modal task-modal">
    <div class="modal-title">
      {{ t('task_' + ctx.taskKey(ctx.taskModal.taskId)) }}
      <span class="task-ver">{{ ctx.taskModal.runtime }} · {{ ctx.taskModal.version }}</span>
    </div>
    <p class="modal-text">{{ t('task_' + ctx.taskKey(ctx.taskModal.taskId) + '_desc') }}</p>

    <div v-if="ctx.taskModal.needsConfirm" class="task-confirm">
      <span class="task-confirm-icon">!</span>
      <div>
        <div class="task-confirm-title">{{ t('taskDangerTitle') }}</div>
        <p class="task-confirm-text">{{ t('taskDangerDesc') }}</p>
      </div>
    </div>

    <div v-if="ctx.taskModal.busy" class="env-msg">{{ t('taskRunning') }}</div>
    <div v-else-if="ctx.taskModal.error" class="env-msg error">{{ ctx.taskModal.error }}</div>

    <pre v-if="ctx.taskModal.output" class="task-output">{{ ctx.taskModal.output }}</pre>
    <p v-if="ctx.taskModal.truncated" class="task-hint">{{ t('taskTruncated') }}</p>
    <p v-if="ctx.taskModal.durationMs" class="task-hint">{{ t('taskDuration', { ms: ctx.taskModal.durationMs }) }}</p>

    <div class="modal-actions">
      <button class="op-btn" @click="ctx.taskModal.open = false">{{ t('close') }}</button>
      <button v-if="ctx.taskModal.output" class="op-btn" @click="ctx.copyTaskOutput">{{ t('copy') }}</button>
      <button
        v-if="ctx.taskModal.needsConfirm || ctx.taskModal.error"
        class="env-install-btn"
        :class="{ busy: ctx.taskModal.busy, danger: ctx.taskModal.kind === 'danger' }"
        :disabled="ctx.taskModal.busy"
        @click="ctx.runTask"
      >{{ ctx.taskModal.needsConfirm ? t('taskRun') : t('taskRetry') }}</button>
    </div>
  </div>
</div>

<!-- 端口全景弹窗：所有运行中服务的端口与控制台一览 -->
<!-- 项目版本要求检查：探测目录里的版本声明，与已装/激活对比 -->
<div v-if="ctx.projectCheck.open" class="modal-overlay" @click.self="ctx.projectCheck.open = false">
  <div class="modal path-modal path-modal--760">
    <div class="modal-actions-top">
      <div>
        <div class="modal-title">{{ t('projectCheck') }}</div>
        <p class="modal-text">{{ t('projectCheckDesc') }}</p>
      </div>
      <button class="cfg-close" type="button" :title="t('close')" @click="ctx.projectCheck.open = false">×</button>
    </div>

    <div class="pc-row">
      <input
        v-model="ctx.projectCheck.dir"
        class="env-input"
        :placeholder="t('projectCheckPh')"
        @keyup.enter="ctx.runProjectCheck"
      />
      <button class="op-btn" @click="ctx.pickProjectDir">{{ t('projectCheckPick') }}</button>
      <button
        class="op-btn"
        :disabled="ctx.projectCheck.loading || !ctx.projectCheck.dir.trim()"
        @click="ctx.runProjectCheck"
      >{{ ctx.projectCheck.loading ? t('checking') : t('projectCheckRun') }}</button>
    </div>

    <p v-if="ctx.projectCheck.error" class="env-msg error">{{ ctx.projectCheck.error }}</p>
    <div v-else-if="!ctx.projectCheck.rows.length && !ctx.projectCheck.deps.length" class="empty-state">
      <div class="empty-text">{{ ctx.projectCheck.dir ? t('projectCheckNone') : t('projectCheckEmpty') }}</div>
    </div>
    <template v-else>
      <!-- 版本要求：可能为空（项目没写 engines / require.php），但依赖侧仍能识别出项目 -->
      <template v-if="ctx.projectCheck.rows.length">
        <div class="pc-section-head">{{ t('projectDepsVersionSection') }}</div>
        <p v-if="ctx.projectMissing" class="env-msg error">{{ t('projectCheckMissing', { n: ctx.projectMissing }) }}</p>
        <table class="panorama-table">
          <thead>
            <tr>
              <th>{{ t('runtime') }}</th>
              <th>{{ t('projectCheckRequire') }}</th>
              <th>{{ t('projectCheckMatched') }}</th>
              <th>{{ t('projectCheckActive') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in ctx.projectCheck.rows" :key="row.runtime + '|' + row.require">
              <td>{{ ctx.projectRuntimeName(row.runtime) }}</td>
              <td>
                <span class="mono">{{ row.require || '—' }}</span>
                <span v-if="row.source" class="muted"> · {{ row.source }}</span>
              </td>
              <td>
                <span v-if="row.matched" class="pc-ok">{{ row.matched }}</span>
                <span v-else class="pc-bad">{{ t('projectCheckNoMatch') }}</span>
              </td>
              <td>
                <span v-if="row.active" class="mono">{{ row.active }}</span>
                <span v-else class="muted">—</span>
                <select
                  v-if="row.installed.length"
                  class="pc-select"
                  :value="row.active"
                  @change="ctx.setActiveFromProject(row, ($event.target as HTMLSelectElement).value)"
                >
                  <option value="">{{ t('projectCheckSwitch') }}</option>
                  <option v-for="v in row.installed" :key="v" :value="v">{{ v }}</option>
                </select>
              </td>
            </tr>
          </tbody>
        </table>
      </template>
      <p v-else class="pc-note">{{ t('projectDepsNoVersion') }}</p>

      <!-- 依赖状态：PHP / Composer 与 Node 各一张卡，全部来自 EnvProjectDeps 的只读快照 -->
      <template v-if="ctx.projectCheck.deps.length">
        <div class="pc-section-head">
          {{ t('projectDepsSection') }}
          <span class="pc-section-hint">{{ t('projectDepsReadonly') }}</span>
        </div>
        <div class="pd-list">
          <div v-for="d in ctx.projectCheck.deps" :key="d.kind" class="pd-card">
            <div class="pd-head">
              <span class="pd-kind">{{ d.kind === 'php' ? t('projectDepsKindPhp') : t('projectDepsKindNode') }}</span>
              <span class="pd-manager mono">{{ d.manager }}<span v-if="d.managerSpec" class="pd-manager-ver"> {{ d.managerSpec }}</span></span>
              <span class="pd-manifest mono" :title="d.manifestDir">{{ d.manifest }}</span>
            </div>
            <div class="pd-grid">
              <div class="pd-item">
                <span class="pd-label">{{ t('projectDepsLock') }}</span>
                <span v-if="d.lockPresent" class="pc-ok">{{ d.lockFile }}</span>
                <span v-else class="pc-bad">{{ t('projectDepsLockMissing') }}</span>
                <span v-if="d.lockDeps >= 0" class="muted"> · {{ t('projectDepsLockCount', { n: d.lockDeps }) }}</span>
                <span v-if="d.lockStale" class="pd-stale" :title="t('projectDepsStale')">!</span>
              </div>
              <div class="pd-item">
                <span class="pd-label">{{ t('projectDepsDir') }}</span>
                <span v-if="d.depsInstalled" class="pc-ok mono">{{ d.depsDir }}/</span>
                <span v-else class="pc-bad">{{ t('projectDepsNotInstalled', { dir: d.depsDir }) }}</span>
              </div>
              <div class="pd-item">
                <span class="pd-label">{{ t('projectDepsDeclared') }}</span>
                <span class="mono">{{ d.manifestDeps }}</span>
              </div>
            </div>
            <div class="pd-path mono" :title="d.manifestDir">{{ d.manifestDir }}</div>
          </div>
        </div>
      </template>
    </template>
  </div>
</div>

<div v-if="ctx.panorama.open" class="modal-overlay" @click.self="ctx.panorama.open = false">
  <div class="modal path-modal path-modal--640">
    <div class="modal-actions-top">
      <div>
        <div class="modal-title">{{ t('portPanorama') }}</div>
        <p class="modal-text">{{ t('portPanoramaDesc') }}</p>
      </div>
      <button class="cfg-close" type="button" :title="t('close')" @click="ctx.panorama.open = false">×</button>
    </div>
    <div v-if="!ctx.panoramaEntries.length" class="empty-state">
      <div class="empty-icon">∅</div>
      <div class="empty-text">{{ t('noRunningService') }}</div>
    </div>
    <table v-else class="panorama-table">
      <thead>
        <tr>
          <th>{{ t('runtime') }}</th>
          <th>{{ t('version') }}</th>
          <th>{{ t('svcPort') }}</th>
          <th>{{ t('console') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="p in ctx.panoramaEntries" :key="p.id + p.version">
          <td><span class="pano-dot" :style="{ background: ctx.avatarColor(p.id) }"></span>{{ p.name }}</td>
          <td>{{ p.version }}</td>
          <td><span class="mono">{{ p.ports.length ? p.ports.join(' / ') : (p.port || '—') }}</span></td>
          <td>
            <a v-if="p.consolePort" class="svc-port-link" @click="ctx.openConsole(p.consolePort)">:{{ p.consolePort }} ↗</a>
            <span v-else class="muted">—</span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</div>


</template>

<style scoped>
@keyframes env-spin { to { transform: rotate(360deg); } }

@keyframes svc-thumb-pulse { 0%,100% { opacity: 1 } 50% { opacity: .4 } }

.lib-picker .env-input { width: 100%; }

.ollama-pull-row .env-input { flex: 1; }

@keyframes ollama-slide {
  0% { margin-left: 0; }
  50% { margin-left: 60%; }
  100% { margin-left: 0; }
}

.svc-btn.console { background: var(--color-text-muted); }

.svc-port-link { text-decoration: none; transition: color var(--transition-fast); }

.svc-port-link:hover { color: var(--color-accent); text-decoration: underline; }

.svc-port-link.disabled { cursor: default; opacity: 0.5; }

.svc-port-link.disabled:hover { color: var(--color-text-disabled); text-decoration: none; }

.modal-actions-top {
  display: flex; align-items: flex-start; justify-content: space-between; gap: 16px;
  margin-bottom: 14px;
}

.modal-actions-top .modal-text { margin: 4px 0 0; }

.panorama-table {
  width: 100%; border-collapse: collapse; font-size: 13px;
}

.panorama-table th {
  text-align: left; font-weight: 600; color: var(--color-text-muted);
  padding: 8px 10px; border-bottom: 1px solid var(--color-border);
}

.panorama-table td {
  padding: 9px 10px; border-bottom: 1px solid var(--color-border);
  color: var(--color-text-primary);
}

.panorama-table tr:last-child td { border-bottom: none; }

.pano-dot {
  display: inline-block; width: 8px; height: 8px; border-radius: 50%;
  margin-right: 8px; vertical-align: middle; flex: none;
}

.mono { font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace); }

.muted { color: var(--color-text-muted); }

.op-btn {display: inline-flex; align-items: center; justify-content: center; gap: 4px; 
  font-size: 11px; height: 24px; padding: 0 8px; border-radius: var(--radius-sm); cursor: pointer;
  border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary);
  font-family: inherit; transition: background var(--transition-fast), color var(--transition-fast);
  white-space: nowrap;
}

.op-btn:hover { background: var(--color-bg-primary); color: var(--color-text-primary); }

.op-btn.on { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-border-focus); }

.op-btn.danger:hover { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }

.op-btn:disabled { opacity: 0.4; cursor: default; }

.op-btn.small { border-radius: var(--radius-sm); padding: 2px 9px; font-size: 11px; }

.op-btn.primary { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }

.op-btn.primary:hover:not(:disabled) { background: var(--color-accent-hover); }

.cert-root-line .op-btn { margin-left: auto; }

.cert-dir-row .env-input { flex: 1; }

.op-item.danger:hover { background: var(--color-danger-bg); color: var(--color-danger); }

.env-install-btn.danger { background: var(--color-danger); }

.env-install-btn.danger:hover { opacity: 0.9; }

.modal-text { font-size: 12px; color: var(--color-text-muted); margin: 6px 0 0; line-height: 1.5; }

.modal-check { display: flex; align-items: center; gap: 8px; margin: 10px 0 2px; font-size: 12px; color: var(--color-text-primary); cursor: pointer; user-select: none; }

.path-modal--760 { max-width: 760px; }

.path-modal--640 { max-width: 640px; }

.empty-state { display: flex; flex-direction: column; align-items: center; gap: 4px; padding: 26px 0; }

.empty-icon { font-size: 28px; color: var(--color-text-disabled); }

.empty-text { font-size: 13px; color: var(--color-text-muted); }

.env-input, .env-select {
  width: 100%; min-width: 0;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-primary); border-radius: var(--radius-sm); padding: 7px 9px; font-size: 12px;
  font-family: inherit; outline: none;
}

.env-input.custom { margin-top: 10px; }

.env-textarea {
  width: 100%; min-width: 0; resize: vertical;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-primary); border-radius: var(--radius-sm); padding: 7px 9px; font-size: 12px;
  font-family: inherit; outline: none; margin-top: 6px;
}

.env-select:focus, .env-input:focus, .env-textarea:focus { border-color: var(--color-border-focus-soft); }

.link-btn.danger { color: var(--color-danger); border-color: var(--color-danger); }

.link-btn.danger:hover { background: var(--color-danger-bg); }

.env-install-btn {
  flex-shrink: 0; padding: 8px 16px; border-radius: var(--radius-sm); border: none;
  background: var(--color-accent); color: var(--color-accent-text); font-size: 13px; font-weight: 500;
  cursor: pointer; font-family: inherit; transition: opacity var(--transition-fast);
}

.env-install-btn:hover { opacity: 0.9; }

.env-install-btn.busy { opacity: 0.7; cursor: default; }

.env-install-btn:disabled { opacity: 0.4; cursor: default; }

@keyframes env-indet { 0% { margin-left: -40%; } 100% { margin-left: 100%; } }

.env-msg { font-size: 11px; color: var(--color-text-disabled); margin: 10px 0 0; word-break: break-all; user-select: text; -webkit-user-select: text; }

.env-msg.error { color: var(--color-danger); }

.ftp-form .env-input { margin-bottom: 6px; }

.modal-overlay {
  position: fixed; inset: 0; background: var(--color-bg-overlay);
  display: flex; align-items: center; justify-content: center; z-index: 50;
}

.modal {
  position: relative;
  width: 560px;
  max-width: 94vw;
  max-height: 88vh;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-lg);
  box-shadow: 0 18px 56px var(--color-bg-overlay);
  display: flex;
  flex-direction: column;
  padding: 20px 22px;
  overflow: hidden;
  color: var(--color-text-primary);
}

.modal-title {
  font-size: 15px; font-weight: 600; color: var(--color-text-primary);
  margin: 0 0 4px; line-height: 1.4;
}

.modal-actions {
  display: flex; align-items: center; justify-content: flex-end; gap: 8px;
  margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--color-border); flex-shrink: 0;
}

.cfg-close {
  width: 30px; height: 30px; flex: none; border-radius: var(--radius-sm);
  border: 1px solid transparent; background: transparent; color: var(--color-text-muted);
  font-size: var(--text-xl); line-height: 1; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  transition: background var(--transition-fast), color var(--transition-fast);
}

.cfg-close:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.php-modal { width: 540px; max-width: 94vw; }

.php-tab-body { min-height: 200px; flex: 1 1 auto; max-height: 60vh; overflow: auto; }

.task-modal { width: 680px; }

.task-ver {
  margin-left: 8px; font-size: 12px; font-weight: 400; color: var(--color-text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.task-confirm {
  display: flex; gap: 10px; margin-top: 12px; padding: 10px 12px;
  border-radius: var(--radius-sm); border: 1px solid var(--color-danger);
  background: var(--color-danger-bg);
}

.task-confirm-icon {
  width: 20px; height: 20px; flex: none; border-radius: 50%; background: var(--color-danger);
  color: var(--color-accent-text); font-size: 12px; font-weight: 700;
  display: flex; align-items: center; justify-content: center;
}

.task-confirm-title { font-size: 12px; font-weight: 600; color: var(--color-text-primary); }

.task-confirm-text { margin: 2px 0 0; font-size: 12px; line-height: 1.5; color: var(--color-text-secondary); }

.task-output {
  margin: 12px 0 0; padding: 10px 12px; flex: 1 1 auto; max-height: 52vh; overflow: auto;
  background: var(--color-bg-primary); border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px; line-height: 1.6; color: var(--color-text-primary);
  white-space: pre-wrap; word-break: break-word;
}

.task-hint { margin: 6px 0 0; font-size: 11px; color: var(--color-text-muted); }

.log-btn {display: inline-flex; align-items: center; justify-content: center; gap: 4px; 
  height: 28px; padding: 0 11px; font-size: 12px; cursor: pointer;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm); white-space: nowrap;
}

.log-btn:hover { border-color: var(--color-accent); color: var(--color-accent); }

.log-btn.active { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }

.ev-test-row .env-input { flex: 1; }

.pc-row { display: flex; gap: 8px; margin-bottom: 10px; }

.pc-row .env-input { flex: 1; }

.pc-ok { color: var(--color-success); font-family: monospace; font-size: 11px; }

.pc-bad { color: var(--color-danger); font-size: 11px; }

.pc-select {
  margin-left: 6px; padding: 2px 5px; font-size: 11px;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
}

.pc-section-head {
  display: flex; align-items: baseline; gap: 8px;
  margin: 14px 0 8px; font-size: 12px; font-weight: 600;
  color: var(--color-text-secondary);
}

.pc-section-hint { font-size: 10px; font-weight: 400; color: var(--color-text-disabled); }

.pc-note { margin: 12px 0 4px; font-size: 11px; color: var(--color-text-muted); }

.pd-list { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 8px; }

.pd-card {
  min-width: 0; padding: 9px 10px;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
}

.pd-head { display: flex; align-items: center; gap: 8px; min-width: 0; }

.pd-kind { font-size: 12px; font-weight: 600; color: var(--color-text-primary); white-space: nowrap; }

.pd-manager { font-size: 11px; color: var(--color-accent); white-space: nowrap; }

.pd-manager-ver { color: var(--color-text-muted); }

.pd-manifest {
  margin-left: auto; min-width: 0; font-size: 10px; color: var(--color-text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.pd-grid { display: flex; flex-wrap: wrap; gap: 4px 14px; margin-top: 8px; }

.pd-item { display: flex; align-items: center; gap: 5px; min-width: 0; font-size: 11px; }

.pd-label { flex-shrink: 0; color: var(--color-text-muted); }

.pd-stale {
  display: inline-flex; align-items: center; justify-content: center;
  width: 14px; height: 14px; border-radius: 50%;
  font-size: 10px; font-weight: 700; cursor: help;
  color: var(--color-warning); background: var(--color-warning-bg);
}

.pd-path {
  margin-top: 7px; font-size: 10px; color: var(--color-text-disabled);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.ev-field .env-input { max-width: 420px; }
</style>
