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
import ConfigEditor from './ConfigEditor.vue'
import RuntimeLogView from './RuntimeLogView.vue'
import { useToast } from '../composables/useToast'
const props = defineProps<{ ctx: any }>()
const { t } = useI18n()
const { confirm: toastConfirm } = useToast()

// 删除系统环境变量不可撤销：走一次 toast.confirm，不再用行内的「点两次」态。
async function confirmDeleteVar(name: string) {
  if (!(await toastConfirm(t('maintEnvConfirmDelete')))) return
  await props.ctx.deleteUserVar(name)
}
</script>

<template>
<!-- PHP 配置弹窗：php.ini / 禁用函数 / 错误日志 / 扩展 -->
<div v-if="ctx.phpModal.open" class="modal-overlay" @click.self="ctx.phpModal.open = false">
  <div class="modal php-modal">
    <div class="modal-title">{{ t('phpConfigTitle') }} · {{ ctx.phpModal.version }}</div>
    <div class="php-tabs">
      <button class="php-tab" :class="{ active: ctx.phpModal.tab === 'ini' }" @click="ctx.phpModal.tab = 'ini'">{{ t('phpIni') }}</button>
      <button class="php-tab" :class="{ active: ctx.phpModal.tab === 'disable' }" @click="ctx.phpModal.tab = 'disable'">{{ t('phpDisableFunctions') }}</button>
      <button class="php-tab" :class="{ active: ctx.phpModal.tab === 'errorlog' }" @click="ctx.phpModal.tab = 'errorlog'">{{ t('phpErrorLog') }}</button>
      <button class="php-tab" :class="{ active: ctx.phpModal.tab === 'ext' }" @click="ctx.phpModal.tab = 'ext'">{{ t('phpExtensions') }}</button>
    </div>
    <div class="php-tab-body">
      <template v-if="ctx.phpModal.tab === 'ini'">
        <label class="modal-label">{{ t('phpIni') }}（{{ t('phpIniHint') }}）</label>
        <!-- 原来是个 12 行的裸 textarea：php.ini 是上千行的键值配置，
             至少要行号、搜索和撤销，直接用配置文件编辑器 -->
        <div class="env-cfg-embed">
          <ConfigEditor v-model="ctx.phpModal.raw" filename="php.ini" @save="ctx.savePHPConfig" />
        </div>
      </template>
      <template v-else-if="ctx.phpModal.tab === 'disable'">
        <label class="modal-label">{{ t('phpDisableFunctions') }}</label>
        <textarea v-model="ctx.phpModal.disableFunctions" class="env-textarea" rows="4" :placeholder="t('phpDisablePlaceholder')"></textarea>
      </template>
      <template v-else-if="ctx.phpModal.tab === 'errorlog'">
        <label class="modal-label">{{ t('phpErrorLog') }}</label>
        <input v-model="ctx.phpModal.errorLog" class="env-input" :placeholder="t('phpErrorLogPlaceholder')" />
        <label class="modal-label">{{ t('phpErrorLogContent') }}</label>
        <!-- 只读快照：不提供刷新（内容随 php.ini 保存才更新），但搜索/高亮要有 -->
        <RuntimeLogView :text="ctx.phpModal.errorLogContent" :empty-text="t('phpNoLog')" hide-refresh />
      </template>
      <template v-else>
        <label class="modal-label">{{ t('phpExtensions') }}</label>
        <div class="ext-list">
          <label v-for="(e, i) in ctx.phpModal.extensions" :key="e.name" class="ext-row">
            <input type="checkbox" :checked="e.enabled" @change="ctx.toggleExt(i)" />
            <span class="ext-name">{{ e.name }}</span>
            <span class="ext-file">{{ e.file }}</span>
          </label>
          <div v-if="!ctx.phpModal.extensions.length" class="empty-text">{{ t('phpNoExt') }}</div>
        </div>
      </template>
    </div>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.phpModal.open = false">{{ t('cancel') }}</button>
      <button class="env-install-btn" @click="ctx.savePHPConfig">{{ t('save') }}</button>
    </div>
  </div>
</div>

<!-- Go 环境配置弹窗：上半是 PATH（命令能否被找到），下半是 go.env（go 的行为） -->
<div v-if="ctx.goEnvModal.open" class="modal-overlay" @click.self="ctx.goEnvModal.open = false">
  <div class="modal go-env-modal">
    <div class="modal-title">{{ t('goEnvConfig') }} · {{ ctx.goEnvModal.version }}</div>
    <div class="go-path-box">
      <div class="go-path-row">
        <span class="go-path-label">{{ t('goEnvBinLabel') }}</span>
        <code class="go-path-value" :title="ctx.goEnvModal.binDir">{{ ctx.goEnvModal.binDir || '—' }}</code>
        <span class="path-flag" :class="ctx.goEnvModal.inPath ? 'ok' : 'no'">
          <span class="path-flag-dot"></span>{{ ctx.goEnvModal.inPath ? t('inPath') : t('notInPath') }}
        </span>
        <button class="op-btn" :disabled="ctx.goEnvModal.pathBusy" @click="ctx.toggleGoPath">
          {{ ctx.goEnvModal.inPath ? t('unsetEnvVar') : t('setEnvVar') }}
        </button>
      </div>
      <p class="go-path-hint">{{ t('goEnvPathHint') }}</p>
    </div>
    <div class="php-tabs go-env-tabs">
      <button class="php-tab" :class="{ active: ctx.goEnvModal.scope === 'version' }" @click="ctx.switchGoEnvScope('version')">
        {{ t('goEnvScopeVersion', { v: ctx.goEnvModal.version }) }}
      </button>
      <button class="php-tab" :class="{ active: ctx.goEnvModal.scope === 'global' }" @click="ctx.switchGoEnvScope('global')">
        {{ t('goEnvScopeGlobal') }}
      </button>
    </div>
    <p class="modal-text go-env-desc">
      {{ ctx.goEnvModal.scope === 'global' ? t('goEnvGlobalHint') : t('goEnvDesc') }}
    </p>
    <div v-if="ctx.goEnvModal.loading" class="env-msg">{{ t('loading') }}</div>
    <div v-else-if="ctx.goEnvModal.error" class="env-msg error">{{ ctx.goEnvModal.error }}</div>
    <div v-else class="go-env-form">
      <div class="go-env-row go-env-presets">
        <span class="go-env-name">{{ t('goEnvProxyPreset') }}</span>
        <div class="go-env-preset-row">
          <button
            v-for="p in ctx.goProxyPresets"
            :key="p.value"
            class="go-env-preset"
            :class="{ active: ctx.goEnvModal.values['GOPROXY'] === p.value }"
            @click="ctx.applyGoProxy(p.value)"
          >{{ p.label }}</button>
          <button class="go-env-preset" @click="ctx.applyGoProxy('')">{{ t('goEnvDefault') }}</button>
        </div>
      </div>
      <div v-for="k in ctx.goEnvModal.keys" :key="k.key" class="go-env-row">
        <label class="go-env-label">
          <span class="go-env-key">{{ k.key }}</span>
          <span class="go-env-name">{{ k.label }}</span>
        </label>
        <select v-if="k.choices && k.choices.length" v-model="ctx.goEnvModal.values[k.key]" class="env-input go-env-input">
          <option value="">{{ t('goEnvDefault') }}</option>
          <option v-for="c in k.choices" :key="c" :value="c">{{ c || t('goEnvDefault') }}</option>
        </select>
        <input
          v-else
          v-model="ctx.goEnvModal.values[k.key]"
          :type="k.secret ? 'password' : 'text'"
          class="env-input go-env-input"
          :placeholder="k.placeholder"
        />
        <p v-if="k.desc" class="go-env-hint">{{ k.desc }}</p>
      </div>
    </div>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.goEnvModal.open = false">{{ t('cancel') }}</button>
      <button class="env-install-btn" :class="{ busy: ctx.goEnvModal.saving }" :disabled="ctx.goEnvModal.saving" @click="ctx.saveGoEnv">{{ t('save') }}</button>
    </div>
  </div>
</div>

<!-- Redis 配置 / 日志弹窗：redis.conf 编辑 + 运行日志查询 -->
<!-- PATH 可视化面板 -->
<div v-if="ctx.pathModal.open" class="modal-overlay" @click.self="ctx.pathModal.open = false">
  <div class="modal path-modal path-modal--600">
    <div class="path-modal-head">
      <span class="path-modal-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M8.5 6H14a4 4 0 0 1 4 4v0M15.5 18H10a4 4 0 0 1-4-4v0"/></svg>
      </span>
      <div class="path-modal-titles">
        <div class="modal-title">{{ t('pathPanel') }}</div>
        <p class="modal-text">{{ t('pathPanelDesc') }}</p>
      </div>
    </div>
    <div class="path-summary" v-if="ctx.pathEntries.length">
      <span class="path-summary-dot" :class="{ ok: ctx.pathEntries.some((e: any) => e.inPath) }"></span>
      {{ ctx.pathEntries.filter((e: any) => e.inPath).length }} / {{ ctx.pathEntries.length }} {{ t('pathSummary') }}
    </div>
    <div class="path-list">
      <div v-for="e in ctx.pathEntries" :key="e.id + e.version" class="path-row">
        <span class="path-badge" :style="{ background: ctx.avatarColor(e.id) }" v-html="ctx.avatarIcon(e.id)"></span>
        <span class="path-name">{{ e.name }} <em>{{ e.version }}</em></span>
        <span class="path-dir">{{ e.binDir }}</span>
        <span class="path-flag" :class="e.inPath ? 'ok' : 'no'">
          <span class="path-flag-dot"></span>{{ e.inPath ? t('inPath') : t('notInPath') }}
        </span>
      </div>
      <div v-if="!ctx.pathEntries.length" class="path-empty">{{ t('pathEmpty') }}</div>
    </div>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.pathModal.open = false">{{ t('close') }}</button>
    </div>
  </div>
</div>

<div v-if="ctx.redisModal.open" class="modal-overlay" @click.self="ctx.closeRedisModal()">
  <div class="modal php-modal">
    <div class="modal-title">{{ t('redisConfigTitle') }} · {{ ctx.redisModal.version }}</div>
    <div class="php-tabs">
      <button class="php-tab" :class="{ active: ctx.redisModal.tab === 'config' }" @click="ctx.redisModal.tab = 'config'">{{ t('redisConfigFile') }}</button>
      <button class="php-tab" :class="{ active: ctx.redisModal.tab === 'log' }" @click="ctx.openLogTab()">{{ t('redisLog') }}</button>
    </div>
    <div class="php-tab-body">
      <template v-if="ctx.redisModal.tab === 'config'">
        <label class="modal-label">{{ t('redisConfigFile') }}（{{ ctx.redisModal.path }}）</label>
        <!-- redis.conf 同样是键值配置：换成带行号/高亮/查找的编辑器 -->
        <div class="env-cfg-embed">
          <ConfigEditor v-model="ctx.redisModal.raw" filename="redis.conf" @save="ctx.saveRedisConfig" />
        </div>
        <p class="env-msg">{{ t('redisConfigHint') }}</p>
      </template>
      <template v-else>
        <RuntimeLogView
          :text="ctx.redisModal.log"
          :empty-text="t('redisNoLog')"
          @refresh="ctx.refreshRedisLog()"
        >
          <template #actions>
            <button class="log-btn" :class="{ active: ctx.redisModal.logAuto }" @click="ctx.toggleLogAuto()">
              {{ ctx.redisModal.logAuto ? t('logAutoOn') : t('logAutoOff') }}
            </button>
          </template>
        </RuntimeLogView>
      </template>
    </div>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.closeRedisModal()">{{ t('cancel') }}</button>
      <button v-if="ctx.redisModal.tab === 'config'" class="env-install-btn" @click="ctx.saveRedisConfig">{{ t('save') }}</button>
    </div>
  </div>
</div>

<!-- FTP 启动参数弹窗：结构化表单（端口 / 根目录 / 只读），底层写 ftpdmin.args -->
<div v-if="ctx.ftpModal.open" class="modal-overlay" @click.self="ctx.ftpModal.open = false">
  <div class="modal php-modal">
    <div class="modal-title">{{ t('ftpConfigTitle') }} · {{ ctx.ftpModal.version }}</div>
    <div class="ftp-form">
      <label class="modal-label">{{ t('ftpConfigPort') }}</label>
      <input v-model.number="ctx.ftpModal.port" class="env-input" type="number" min="1" max="65535" />
      <label class="modal-label">{{ t('ftpConfigRootDir') }}</label>
      <input v-model="ctx.ftpModal.rootDir" class="env-input" :placeholder="t('pickDirTitle')" />
      <label class="ftp-check">
        <input type="checkbox" v-model="ctx.ftpModal.readOnly" />
        <span>{{ t('ftpConfigReadOnly') }}</span>
      </label>
      <p class="env-msg">{{ t('ftpConfigReadOnlyHint') }}</p>
      <p class="env-msg">{{ t('ftpConfigHint') }}</p>
    </div>
    <div class="modal-actions">
      <button class="op-btn" @click="ctx.ftpModal.open = false">{{ t('cancel') }}</button>
      <button class="env-install-btn" @click="ctx.saveFTPConfig">{{ t('save') }}</button>
    </div>
  </div>
</div>

<!-- 通用配置编辑弹窗（Nginx / Apache / Caddy 等实现了 ConfigProvider 的运行时） -->
<div v-if="ctx.configModal.open" class="modal-overlay cfg-overlay" @click.self="ctx.configModal.open = false">
  <div class="cfg-modal">
    <header class="cfg-head">
      <div class="cfg-titles">
        <div class="cfg-title">{{ t('editConfig') }}</div>
        <span class="cfg-version">{{ ctx.configModal.version }}</span>
      </div>
      <button class="cfg-close" type="button" :title="t('close')" @click="ctx.configModal.open = false">×</button>
    </header>
    <div class="cfg-path">
      <svg class="cfg-path-ico" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/></svg>
      <code class="cfg-path-text">{{ ctx.configModal.path }}</code>
    </div>
    <ConfigEditor :ref="(el: any) => (ctx.editorRef = el)" v-model="ctx.configModal.raw" :filename="ctx.configModal.path" @save="ctx.saveConfig" />
    <p class="cfg-hint">{{ t('configRestartHint') }}</p>
    <footer class="cfg-foot">
      <button class="cfg-btn ghost" @click="ctx.configModal.open = false">{{ t('cancel') }}</button>
      <button class="cfg-btn primary" @click="ctx.saveConfig">{{ t('save') }}</button>
    </footer>
  </div>
</div>

<!-- 操作下拉点击外部关闭 -->
<!-- 维护面板：源体检 / 磁盘占用 / 镜像加速 / 环境清单 -->
<div v-if="ctx.maint.open" class="modal-overlay" @click.self="ctx.maint.open = false">
  <div class="modal maint-modal">
    <div class="modal-title">{{ t('maintTitle') }}</div>
    <div class="php-tabs">
      <button class="php-tab" :class="{ active: ctx.maint.tab === 'check' }" @click="ctx.openMaintenance('check')">{{ t('maintTabCheck') }}</button>
      <button class="php-tab" :class="{ active: ctx.maint.tab === 'disk' }" @click="ctx.openMaintenance('disk')">{{ t('maintTabDisk') }}</button>
      <button class="php-tab" :class="{ active: ctx.maint.tab === 'manifest' }" @click="ctx.openMaintenance('manifest')">{{ t('maintTabManifest') }}</button>
      <button class="php-tab" :class="{ active: ctx.maint.tab === 'envvar' }" @click="ctx.openMaintenance('envvar')">{{ t('maintTabEnvVar') }}</button>
    </div>

    <div class="php-tab-body maint-body">
      <!-- ① 源体检 -->
      <template v-if="ctx.maint.tab === 'check'">
        <p class="modal-text">{{ t('maintCheckDesc') }}</p>
        <div class="maint-actions">
          <button class="env-install-btn" :disabled="ctx.sourceCheck.running" @click="ctx.runSourceCheck">
            {{ ctx.sourceCheck.running ? t('maintChecking') : t('maintCheckRun') }}
          </button>
          <span v-if="ctx.sourceCheck.running" class="maint-summary">
            {{ t('maintCheckReceived', { n: ctx.sourceCheck.sources.length + ctx.sourceCheck.versions.length }) }}
          </span>
          <span v-else-if="ctx.sourceCheck.report" class="maint-summary">
            {{ t('maintCheckSummary', { ok: ctx.sourceCheck.report.ok, mirror: ctx.sourceCheck.report.mirrorOnly, failed: ctx.sourceCheck.report.failed, sec: (ctx.sourceCheck.report.durationMs / 1000).toFixed(1) }) }}
          </span>
        </div>
        <div v-if="ctx.sourceCheck.error" class="env-msg error">{{ ctx.sourceCheck.error }}</div>
        <!-- GitHub 下载加速：体检里「需加速」的项要靠这个开关才真正走 gh-proxy -->
        <label class="gh-accel">
          <input
            type="checkbox"
            :checked="ctx.githubAccel.enabled"
            :disabled="ctx.githubAccel.busy || ctx.githubAccel.loading"
            @change="ctx.toggleGithubAccel"
          />
          <span class="gh-accel-label">{{ t('ghAccelLabel') }}</span>
          <em class="gh-accel-hint">{{ t('ghAccelHint') }}</em>
        </label>

        <div v-if="ctx.sourceCheck.sources.length" class="maint-table">
          <div class="maint-row maint-head">
            <span>{{ t('maintColRuntime') }}</span>
            <span>{{ t('maintColSource') }}</span>
            <span>{{ t('maintColStatus') }}</span>
            <span>{{ t('maintColLatency') }}</span>
          </div>
          <div v-for="it in ctx.sourceCheck.sources" :key="it.runtime + '/' + it.sourceId" class="maint-row">
            <span class="maint-rt">{{ it.name }}<em v-if="it.version">{{ it.version }}</em></span>
            <span class="maint-src" :title="it.url || it.error">{{ it.source }}</span>
            <span
              class="maint-status"
              :class="it.ok ? 'ok' : (it.mirrorOk ? 'warn' : 'bad')"
              :title="!it.ok && it.mirrorOk ? t('maintMirrorOnlyHint', { ms: it.mirrorMs }) : it.error"
            >
              {{ it.ok ? t('maintOK') : (it.mirrorOk ? t('maintMirrorOnly') : (it.code ? 'HTTP ' + it.code : t('maintUnreachable'))) }}
            </span>
            <span class="maint-lat">{{ it.latencyMs }}ms</span>
          </div>
        </div>

        <div v-if="ctx.sourceCheck.versions.length" class="maint-sub">
          <div class="maint-sub-title">{{ t('maintVersionSources') }}</div>
          <div class="maint-table">
            <div class="maint-row maint-head">
              <span>{{ t('maintColRuntime') }}</span>
              <span>{{ t('maintColLatest') }}</span>
              <span>{{ t('maintColStatus') }}</span>
              <span>{{ t('maintColLatency') }}</span>
            </div>
            <div v-for="v in ctx.sourceCheck.versions" :key="'v/' + v.runtime" class="maint-row">
              <span class="maint-rt">{{ v.name }}</span>
              <span class="maint-src" :title="v.url || v.error">
                <template v-if="v.url">{{ v.latest || '—' }}<em v-if="v.stale"> · {{ t('maintStaleHint', { rec: v.recommended }) }}</em></template>
                <template v-else>{{ t('maintNoUpstream') }}</template>
              </span>
              <span class="maint-status" :class="v.ok ? (v.stale ? 'warn' : 'ok') : 'bad'">
                {{ v.ok ? (v.stale ? t('maintStaleTag') : t('maintOK')) : (v.code ? 'HTTP ' + v.code : t('maintUnreachable')) }}
              </span>
              <span class="maint-lat">{{ v.latencyMs }}ms</span>
            </div>
          </div>
        </div>
        <div v-if="!ctx.sourceCheck.running && !ctx.sourceCheck.sources.length && !ctx.sourceCheck.error" class="empty-text">
          {{ t('maintCheckIdle') }}
        </div>
      </template>

      <!-- ⑦ 磁盘占用 -->
      <template v-else-if="ctx.maint.tab === 'disk'">
        <p class="modal-text">{{ t('maintDiskDesc') }}</p>
        <div class="maint-actions">
          <button class="op-btn" :disabled="ctx.diskUsage.loading" @click="ctx.loadDiskUsage">
            {{ ctx.diskUsage.loading ? t('loading') : t('maintDiskRefresh') }}
          </button>
          <span v-if="ctx.diskUsage.totalBytes" class="maint-summary">{{ t('maintDiskTotal', { size: ctx.formatSize(ctx.diskUsage.totalBytes) }) }}</span>
        </div>
        <div v-if="ctx.diskUsage.items.length" class="disk-list">
          <div v-for="d in ctx.diskUsage.items" :key="d.runtime" class="disk-row">
            <div class="disk-head">
              <span class="disk-name">{{ d.name }}</span>
              <span class="disk-size">{{ ctx.formatSize(d.bytes) }}</span>
            </div>
            <div class="disk-chips">
              <span v-for="v in d.versions" :key="v.version" class="disk-chip">{{ v.version }} · {{ ctx.formatSize(v.bytes) }}</span>
            </div>
          </div>
        </div>
        <div v-else class="empty-text">{{ ctx.diskUsage.loading ? t('loading') : t('maintDiskEmpty') }}</div>
      </template>

      <!-- ⑧ 环境清单 -->
      <template v-else-if="ctx.maint.tab === 'manifest'">
        <p class="modal-text">{{ t('maintManifestDesc') }}</p>
        <div class="maint-actions">
          <button class="env-install-btn" :disabled="ctx.manifestPanel.busy" @click="ctx.exportManifest">{{ t('maintManifestExport') }}</button>
          <button class="op-btn" :disabled="ctx.manifestPanel.busy" @click="ctx.pickManifestFile">{{ t('maintManifestImport') }}</button>
          <button
            v-if="ctx.manifestPanel.report && ctx.manifestPanel.report.runtimes.some((d: any) => d.activeChanged)"
            class="op-btn"
            :disabled="ctx.manifestPanel.busy"
            @click="ctx.applyManifestActive"
          >{{ t('maintManifestApplyActive') }}</button>
          <input :ref="(el: any) => (ctx.manifestInput = el)" type="file" accept=".json,application/json" class="hidden-file" @change="ctx.onManifestFile" />
        </div>
        <div v-if="ctx.manifestPanel.error" class="env-msg error">{{ ctx.manifestPanel.error }}</div>
        <div v-if="ctx.manifestPanel.report" class="manifest-report">
          <div class="maint-summary">
            {{ t('maintManifestSummary', { matched: ctx.manifestPanel.report.matchedTotal, missing: ctx.manifestPanel.report.missingTotal }) }}
          </div>
          <div v-for="d in ctx.manifestPanel.report.runtimes" :key="d.runtime" class="manifest-row">
            <span class="manifest-name">{{ d.name }}</span>
            <span v-if="d.missing.length" class="manifest-tag bad">{{ t('maintManifestMissing') }}: {{ d.missing.join(', ') }}</span>
            <span v-if="d.extra.length" class="manifest-tag extra">{{ t('maintManifestExtra') }}: {{ d.extra.join(', ') }}</span>
            <span v-if="d.activeChanged" class="manifest-tag warn">{{ t('maintManifestActiveHint', { v: d.activeWanted }) }}</span>
            <span v-if="!d.missing.length && !d.extra.length && !d.activeChanged" class="manifest-tag ok">{{ t('maintManifestSame') }}</span>
          </div>
          <div v-if="ctx.manifestPanel.applied.length" class="maint-summary">{{ t('maintManifestApplied', { n: ctx.manifestPanel.applied.length }) }}</div>
        </div>
      </template>

      <!-- ⑤ 用户环境变量（HKCU\Environment）—— 链尾，兼作未知 tab 的兜底 -->
      <template v-else>
        <p class="modal-text">{{ t('maintEnvDesc') }}</p>
        <div v-if="ctx.userEnv.error" class="env-msg error">{{ ctx.userEnv.error }}</div>

        <div class="maint-sub">
          <div class="maint-sub-title">{{ t('maintEnvPathTitle') }}</div>
          <div class="maint-table">
            <div v-for="p in ctx.userEnv.pathEntries" :key="p" class="maint-row envvar-path-row">
              <span class="envvar-path">
                {{ p }}
                <em v-if="ctx.isQuickdockEntry(p)" class="envvar-badge">{{ t('maintEnvManaged') }}</em>
              </span>
              <button class="op-btn danger" :disabled="ctx.userEnv.saving" @click="ctx.removePathEntry(p)">{{ t('maintEnvRemove') }}</button>
            </div>
            <div v-if="!ctx.userEnv.pathEntries.length" class="empty-text">{{ t('maintEnvPathEmpty') }}</div>
          </div>
          <div class="maint-actions">
            <input v-model="ctx.userEnv.newPathEntry" class="env-input" :placeholder="t('maintEnvPathPlaceholder')" @keyup.enter="ctx.addPathEntry" />
            <button class="op-btn" :disabled="ctx.userEnv.saving" @click="ctx.addPathEntry">{{ t('maintEnvAdd') }}</button>
          </div>
        </div>

        <div class="maint-sub">
          <div class="maint-sub-title">{{ t('maintEnvVarTitle') }}</div>
          <div class="maint-table">
            <div v-for="v in ctx.userEnv.vars" :key="v.name" class="maint-row envvar-row">
              <span class="envvar-name">
                {{ v.name }}
                <em v-if="v.managed" class="envvar-badge">{{ t('maintEnvManaged') }}</em>
              </span>
              <!-- 编辑态：value 列变输入框，按钮区换成 保存 / 取消 -->
              <template v-if="ctx.userEnv.editingName === v.name">
                <input
                  v-model="ctx.userEnv.editingValue"
                  class="env-input"
                  @keyup.enter="ctx.commitEditVar"
                  @keyup.esc="ctx.cancelEditVar"
                />
                <span class="envvar-actions">
                  <button class="op-btn" :disabled="ctx.userEnv.saving" @click="ctx.commitEditVar">{{ t('save') }}</button>
                  <button class="op-btn" @click="ctx.cancelEditVar">{{ t('cancel') }}</button>
                </span>
              </template>
              <template v-else>
                <span class="envvar-value" :title="v.value">{{ v.value }}</span>
                <span class="envvar-actions">
                  <button class="op-btn" :disabled="ctx.userEnv.saving" @click="ctx.startEditVar(v)">{{ t('edit') }}</button>
                  <button
                    class="op-btn danger"
                    :disabled="ctx.userEnv.saving"
                    @click="confirmDeleteVar(v.name)"
                  >{{ t('maintEnvRemove') }}</button>
                </span>
              </template>
            </div>
            <div v-if="!ctx.userEnv.vars.length" class="empty-text">{{ t('maintEnvVarEmpty') }}</div>
          </div>
          <div class="maint-actions">
            <input v-model="ctx.userEnv.newName" class="env-input envvar-name-input" :placeholder="t('maintEnvNamePlaceholder')" />
            <input v-model="ctx.userEnv.newValue" class="env-input" :placeholder="t('maintEnvValuePlaceholder')" @keyup.enter="ctx.saveUserVar" />
            <button class="op-btn" :disabled="ctx.userEnv.saving" @click="ctx.saveUserVar">{{ t('maintEnvAdd') }}</button>
          </div>
        </div>

        <p class="env-msg">{{ t('maintEnvScopeHint') }}</p>
      </template>
    </div>

    <div class="modal-actions">
      <button class="op-btn" @click="ctx.maint.open = false">{{ t('close') }}</button>
    </div>
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

.g-status.ok { color: var(--color-success); }

.g-status.warn { color: var(--color-warning); }

.status-dot.ok { background: var(--color-success); box-shadow: 0 0 5px var(--color-success-border); }

.status-dot.warn { background: var(--color-warning); box-shadow: 0 0 5px var(--color-warning-border); }

.modal-actions-top .modal-text { margin: var(--space-2) 0 0; }

.op-btn {display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2); 
  font-size: var(--text-xs); height: 24px; padding: 0 var(--space-4); border-radius: var(--radius-sm); cursor: pointer;
  border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary);
  font-family: inherit; transition: background var(--transition-fast), color var(--transition-fast);
  white-space: nowrap;
}

.op-btn:hover { background: var(--color-bg-primary); color: var(--color-text-primary); }

.op-btn.on { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-border-focus); }

.op-btn.danger:hover { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }

.op-btn:disabled { opacity: 0.4; cursor: default; }

.op-btn.small { border-radius: var(--radius-sm); padding: var(--space-1) 9px; font-size: var(--text-xs); }

.op-btn.primary { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }

.op-btn.primary:hover:not(:disabled) { background: var(--color-accent-hover); }

.badge.ok { background: var(--color-success-bg); color: var(--color-success); }

.badge.warn { background: var(--color-warning-bg); color: var(--color-warning); }

.cert-root-line .op-btn { margin-left: auto; }

.cert-dir-row .env-input { flex: 1; }

.mcp-check.ok { color: var(--color-success); }

.op-item.danger:hover { background: var(--color-danger-bg); color: var(--color-danger); }

.env-install-btn.danger { background: var(--color-danger); }

.env-install-btn.danger:hover { opacity: 0.9; }

.modal-text { font-size: var(--text-sm); color: var(--color-text-muted); margin: var(--space-3) 0 0; line-height: 1.5; }

.path-list { display: flex; flex-direction: column; gap: 7px; margin: var(--space-2) 0 var(--space-5); max-height: 52vh; overflow: auto; }

.path-row { display: grid; grid-template-columns: 26px 130px 1fr auto; align-items: center; gap: 10px; padding: 9px 11px; background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: var(--radius-md); font-size: var(--text-sm); transition: border-color .12s ease, background .12s ease; }

.path-row:hover { border-color: var(--color-border-focus); background: var(--color-bg-secondary); }

.path-badge { width: 26px; height: 26px; border-radius: var(--radius-sm); display: inline-flex; align-items: center; justify-content: center; overflow: hidden; flex: none; }

.path-badge :deep(svg) { width: 16px; height: 16px; }

.path-name { font-weight: 600; color: var(--color-text-primary); white-space: nowrap; }

.path-name em { font-style: normal; color: var(--color-text-muted); font-weight: 400; margin-left: 5px; }

.path-dir { color: var(--color-text-muted); font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: var(--text-xs); word-break: break-all; min-width: 0; }

.path-flag { display: inline-flex; align-items: center; gap: 5px; font-size: var(--text-xs); padding: 3px 9px; border-radius: var(--radius-full); white-space: nowrap; font-weight: 600; }

.path-flag-dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }

.path-flag.ok { color: var(--color-success); background: var(--color-success-bg); }

.path-flag.no { color: var(--color-danger); background: var(--color-danger-bg); }

.path-empty { color: var(--color-text-muted); font-size: var(--text-sm); padding: 10px var(--space-1); text-align: center; }

.path-modal-head { display: flex; align-items: center; gap: var(--space-5); margin: var(--space-1) 0 var(--space-2); }

.path-modal-icon { width: 34px; height: 34px; border-radius: var(--radius-md); flex: none; display: inline-flex; align-items: center; justify-content: center; background: var(--color-accent-bg); color: var(--color-accent); }

.path-modal-icon svg { width: 18px; height: 18px; }

.path-modal-titles { min-width: 0; }

.path-modal--600 { max-width: 600px; }

.path-summary { display: flex; align-items: center; gap: 7px; font-size: var(--text-sm); color: var(--color-text-secondary); margin: var(--space-4) 0 var(--space-1); }

.path-summary-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--color-text-disabled); flex: none; }

.path-summary-dot.ok { background: var(--color-success); }

.empty-text { font-size: var(--text-md); color: var(--color-text-muted); }

.env-input, .env-select {
  width: 100%; min-width: 0;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-primary); border-radius: var(--radius-sm); padding: 7px 9px; font-size: var(--text-sm);
  font-family: inherit; outline: none;
}

.env-input.custom { margin-top: 10px; }

.env-textarea {
  width: 100%; min-width: 0; resize: vertical;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-primary); border-radius: var(--radius-sm); padding: 7px 9px; font-size: var(--text-sm);
  font-family: inherit; outline: none; margin-top: var(--space-3);
}

.env-select:focus, .env-input:focus, .env-textarea:focus { border-color: var(--color-border-focus-soft); }

.link-btn.danger { color: var(--color-danger); border-color: var(--color-danger); }

.link-btn.danger:hover { background: var(--color-danger-bg); }

.env-install-btn {
  flex-shrink: 0; padding: var(--space-4) var(--space-6); border-radius: var(--radius-sm); border: none;
  background: var(--color-accent); color: var(--color-accent-text); font-size: var(--text-md); font-weight: 500;
  cursor: pointer; font-family: inherit; transition: opacity var(--transition-fast);
}

.env-install-btn:hover { opacity: 0.9; }

.env-install-btn.busy { opacity: 0.7; cursor: default; }

.env-install-btn:disabled { opacity: 0.4; cursor: default; }

@keyframes env-indet { 0% { margin-left: -40%; } 100% { margin-left: 100%; } }

.env-msg { font-size: var(--text-xs); color: var(--color-text-disabled); margin: 10px 0 0; word-break: break-all; user-select: text; -webkit-user-select: text; }

.env-msg.error { color: var(--color-danger); }

.ftp-form { padding: var(--space-2) var(--space-1) 0; display: flex; flex-direction: column; gap: var(--space-2); }

.ftp-form .env-input { margin-bottom: var(--space-3); }

.ftp-check { display: flex; align-items: center; gap: var(--space-4); margin: var(--space-2) 0 0; font-size: var(--text-md); color: var(--color-text-primary); cursor: pointer; }

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
  padding: var(--space-7) 22px;
  overflow: hidden;
  color: var(--color-text-primary);
}

.modal-title {
  font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary);
  margin: 0 0 var(--space-2); line-height: 1.4;
}

.modal-actions {
  display: flex; align-items: center; justify-content: flex-end; gap: var(--space-4);
  margin-top: var(--space-6); padding-top: 14px; border-top: 1px solid var(--color-border); flex-shrink: 0;
}

.cfg-overlay { background: var(--color-bg-overlay); backdrop-filter: blur(2px); }

.cfg-modal {
  width: 860px; max-width: 94vw; height: 82vh; max-height: 88vh;
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-lg); box-shadow: 0 18px 56px var(--color-bg-overlay);
  display: flex; flex-direction: column; overflow: hidden;
}

.cfg-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 15px 18px 13px; border-bottom: 1px solid var(--color-border); flex-shrink: 0;
}

.cfg-titles { display: flex; align-items: baseline; gap: 10px; min-width: 0; }

.cfg-title { font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary); }

.cfg-version {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: var(--text-sm);
  color: var(--color-accent); background: var(--color-accent-bg); padding: var(--space-1) var(--space-4); border-radius: var(--radius-full);
  white-space: nowrap;
}

.cfg-close {
  width: 30px; height: 30px; flex: none; border-radius: var(--radius-sm);
  border: 1px solid transparent; background: transparent; color: var(--color-text-muted);
  font-size: var(--text-xl); line-height: 1; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  transition: background var(--transition-fast), color var(--transition-fast);
}

.cfg-close:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.cfg-path {
  display: flex; align-items: center; gap: var(--space-4); margin: 14px 18px 0;
  padding: 9px 11px; background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); min-width: 0;
}

.cfg-path-ico { color: var(--color-text-muted); flex: none; }

.cfg-path-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: var(--text-sm);
  color: var(--color-text-secondary); word-break: break-all;
}

.cfg-hint { margin: 9px 18px 0; font-size: var(--text-xs); color: var(--color-text-disabled); }

.cfg-foot {
  display: flex; align-items: center; justify-content: flex-end; gap: 9px;
  padding: 13px 18px var(--space-6); border-top: 1px solid var(--color-border); flex-shrink: 0;
}

.cfg-btn {display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2); 
  font-family: inherit; font-size: var(--text-md); height: 32px; padding: 0 var(--space-6); border-radius: var(--radius-sm);
  cursor: pointer; border: 1px solid transparent;
  transition: opacity var(--transition-fast), background var(--transition-fast), color var(--transition-fast);
}

.cfg-btn.ghost { background: transparent; border-color: var(--color-border); color: var(--color-text-secondary); }

.cfg-btn.ghost:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.cfg-btn.primary { background: var(--color-accent); color: var(--color-accent-text); font-weight: 500; }

.cfg-btn.primary:hover { opacity: 0.9; }

.php-modal { width: 540px; max-width: 94vw; }

.go-env-modal { width: 560px; max-width: 94vw; }

.go-path-box {
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  border-radius: var(--radius-md); padding: 10px var(--space-5); margin: var(--space-2) 0 14px;
}

.go-path-row { display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap; }

.go-path-label { font-size: var(--text-sm); color: var(--color-text-secondary); flex: none; }

.go-path-value {
  flex: 1 1 200px; min-width: 0; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: var(--text-sm); color: var(--color-text-primary); background: var(--color-bg-primary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm); padding: 3px var(--space-4);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.go-path-hint { margin: 7px 0 0; font-size: var(--text-sm); line-height: 1.5; color: var(--color-text-muted); }

.go-env-desc { font-size: var(--text-sm); color: var(--color-text-muted); margin: 0 0 var(--space-5); line-height: 1.5; }

.go-env-form { max-height: 64vh; overflow: auto; display: flex; flex-direction: column; gap: 14px; padding-right: var(--space-2); }

.go-env-row { display: flex; flex-direction: column; gap: 5px; }

.go-env-label { display: flex; align-items: baseline; gap: var(--space-4); flex-wrap: wrap; }

.go-env-key {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: var(--text-sm); font-weight: 600;
  color: var(--color-accent); background: var(--color-accent-bg); padding: 1px 7px; border-radius: var(--radius-full);
}

.go-env-name { font-size: var(--text-sm); color: var(--color-text-secondary); }

.go-env-input { width: 100%; }

.go-env-hint { font-size: var(--text-xs); color: var(--color-text-disabled); margin: 0; line-height: 1.45; }

.go-env-presets { gap: 7px; }

.go-env-preset-row { display: flex; flex-wrap: wrap; gap: var(--space-3); }

.go-env-preset {
  font-size: var(--text-sm); padding: var(--space-2) 10px; border-radius: var(--radius-sm); cursor: pointer;
  border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary);
  font-family: inherit;
}

.go-env-preset:hover { border-color: var(--color-border-focus); color: var(--color-text-primary); }

.go-env-preset.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-border-focus); }

.php-tabs { display: flex; gap: var(--space-2); margin: var(--space-2) 0 10px; flex-wrap: wrap; }

.php-tab {
  font-size: var(--text-sm); padding: 5px 11px; border-radius: var(--radius-sm); cursor: pointer;
  border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary);
  font-family: inherit;
}

.php-tab.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border-strong); }

.php-tab-body { min-height: 200px; flex: 1 1 auto; max-height: 60vh; overflow: auto; }

.env-cfg-embed { display: flex; flex-direction: column; height: 260px; margin-top: var(--space-3); }

.env-cfg-embed :deep(.ce) { flex: 1 1 auto; margin: 0; min-height: 0; }

.log-btn {display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2); 
  height: 28px; padding: 0 11px; font-size: var(--text-sm); cursor: pointer;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm); white-space: nowrap;
}

.log-btn:hover { border-color: var(--color-accent); color: var(--color-accent); }

.log-btn.active { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }

.ext-list { display: flex; flex-direction: column; gap: var(--space-1); max-height: 300px; overflow: auto; }

.ext-row {
  display: flex; align-items: center; gap: var(--space-4); font-size: var(--text-sm); padding: 5px var(--space-4);
  border-radius: var(--radius-sm); cursor: pointer; color: var(--color-text-secondary);
}

.ext-row:hover { background: var(--color-bg-tertiary); }

.ext-name { font-weight: 600; }

.ext-file { margin-left: auto; font-size: var(--text-2xs); color: var(--color-text-disabled); font-family: monospace; }

.maint-modal {
  width: min(940px, 94vw);
  max-height: 84vh;
  display: flex;
  flex-direction: column;
}

.maint-body {
  overflow-y: auto;
  min-height: 200px;
}

.maint-actions {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  flex-wrap: wrap;
  margin-bottom: var(--space-5);
}

.maint-summary {
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
}

.maint-sub { margin-top: var(--space-6); }

.maint-sub-title {
  font-size: var(--text-sm);
  font-weight: 600;
  color: var(--color-text-secondary);
  margin-bottom: var(--space-3);
}

.maint-table { display: flex; flex-direction: column; gap: var(--space-1); }

.maint-row {
  display: grid;
  grid-template-columns: 160px minmax(0, 1fr) 96px 68px;
  gap: var(--space-3);
  align-items: center;
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-xs);
  font-size: var(--text-sm);
}

.maint-row:not(.maint-head):hover { background: var(--color-bg-tertiary); }

.maint-head {
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  letter-spacing: 0.04em;
  border-bottom: 1px solid var(--color-border);
}

.maint-rt {
  display: flex;
  flex-direction: column;
  color: var(--color-text-primary);
  overflow: hidden;
}

.maint-rt em {
  font-style: normal;
  font-size: var(--text-2xs);
  color: var(--color-text-disabled);
  font-family: monospace;
}

.maint-src {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-secondary);
}

.maint-src em { font-style: normal; color: var(--color-warning); }

.maint-status {
  font-size: var(--text-xs);
  padding: var(--space-1) var(--space-3);
  border-radius: var(--radius-full);
  text-align: center;
}

.maint-status.ok { color: var(--color-success); background: var(--color-success-bg); }

.maint-status.bad { color: var(--color-danger); background: var(--color-danger-bg); }

.maint-status.warn { color: var(--color-warning); background: var(--color-warning-bg); }

.maint-status.off { color: var(--color-text-muted); background: var(--color-bg-tertiary); }

.maint-lat {
  text-align: right;
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.envvar-path-row { grid-template-columns: minmax(0, 1fr) 72px; }

.envvar-row { grid-template-columns: 170px minmax(0, 1fr) 148px; }

.envvar-actions { display: flex; align-items: center; gap: var(--space-2); justify-content: flex-end; }

.ev-test-row .env-input { flex: 1; }

.pc-row .env-input { flex: 1; }

.envvar-path,
.envvar-name {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: monospace;
  color: var(--color-text-primary);
}

.envvar-value {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: monospace;
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
}

.envvar-badge {
  flex-shrink: 0;
  font-style: normal;
  font-size: var(--text-2xs);
  padding: 1px var(--space-2);
  border-radius: var(--radius-full);
  color: var(--color-accent);
  background: var(--color-bg-tertiary);
}

.envvar-name-input { max-width: 170px; font-family: monospace; text-transform: uppercase; }

.disk-list { display: flex; flex-direction: column; gap: var(--space-3); }

.disk-row {
  padding: var(--space-3) var(--space-4);
  background: var(--color-card-bg);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-sm);
}

.disk-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
}

.disk-name { font-weight: 600; font-size: var(--text-md); }

.disk-size {
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}

.disk-chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-top: var(--space-2);
}

.disk-chip {
  font-size: var(--text-xs);
  padding: var(--space-1) var(--space-4);
  border-radius: var(--radius-full);
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
}

.gh-accel {
  display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap;
  margin: var(--space-2) 0 var(--space-3); padding: var(--space-4) var(--space-5);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); cursor: pointer;
}

.gh-accel input { flex: none; }

.gh-accel-label { font-size: var(--text-md); color: var(--color-text-primary); }

.gh-accel-hint { font-size: var(--text-sm); color: var(--color-text-muted); font-style: normal; }

.manifest-report {
  margin-top: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.manifest-row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: center;
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-xs);
  background: var(--color-card-bg);
}

.manifest-name { font-weight: 600; font-size: var(--text-sm); min-width: 110px; }

.manifest-tag {
  font-size: var(--text-xs);
  padding: var(--space-1) var(--space-4);
  border-radius: var(--radius-full);
}

.manifest-tag.bad { color: var(--color-danger); background: var(--color-danger-bg); }

.manifest-tag.warn { color: var(--color-warning); background: var(--color-warning-bg); }

.manifest-tag.extra { color: var(--color-text-secondary); background: var(--color-bg-tertiary); }

.manifest-tag.ok { color: var(--color-success); background: var(--color-success-bg); }

.hidden-file { display: none; }

.ev-field .env-input { max-width: 420px; }
</style>
