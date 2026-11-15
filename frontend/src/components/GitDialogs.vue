<script setup lang="ts">
/**
 * GitPage 的弹窗层。
 *
 * 状态仍全部归 GitPage（与详情区、工具栏共用同一份 ref / reactive），
 * 这里只渲染并把交互写回 ctx。ctx 是父组件传来的 reactive({...})：
 * reactive 会自动 unwrap 其中的 ref，所以 `ctx.x = v` 原样落到父级
 * `x.value`，v-model 双向依然有效；函数则直接调父级实现。
 * 模板 ref 改函数式，保证父组件里的 focus() / markSaved() 仍能拿到实例。
 *
 * 样式随模板一起搬：scoped 选择器穿不进子组件，留在父级会整块失效。
 */
import { useI18n } from 'vue-i18n'
import { X, Loader2, Trash2, KeyRound, History } from '@lucide/vue'
defineProps<{ ctx: any }>()
const { t } = useI18n()
</script>

<template>
<!-- 远端管理：地址写错 / 有 origin + upstream 两条，都要能在这里改 -->
<div v-if="ctx.remoteOpen" class="modal-mask" @click.self="ctx.remoteOpen = false">
  <div class="modal modal-lg">
    <header class="modal-head">
      <span>{{ t('git_remotes') }}</span>
      <button class="modal-x" @click="ctx.remoteOpen = false"><X :size="13" /></button>
    </header>
    <div class="modal-body">
      <p class="modal-hint">{{ t('git_remotes_hint') }}</p>
      <ul v-if="ctx.remotes.length" class="rm-list">
        <li v-for="r in ctx.remotes" :key="r.name" class="rm-row">
          <span class="rm-name" :title="r.pushUrl || r.url">{{ r.name }}</span>
          <span :class="['rm-tag', r.isSsh ? 'ssh' : 'https']">{{ r.isSsh ? 'SSH' : 'HTTPS' }}</span>
          <input v-model="r.url" class="modal-input rm-input" spellcheck="false"
                 :disabled="ctx.remoteBusy === r.name" @change="ctx.saveRemote(r)"
                 @keydown.enter.prevent="ctx.saveRemote(r)" />
          <button class="rm-x" :disabled="!!ctx.remoteBusy" :title="t('git_remote_remove')"
                  @click="ctx.removeRemote(r)">
            <Loader2 v-if="ctx.remoteBusy === r.name" :size="11" class="spin" />
            <Trash2 v-else :size="11" />
          </button>
        </li>
      </ul>
      <p v-else class="modal-hint">{{ t('git_remotes_none') }}</p>

      <div class="rm-add">
        <input v-model="ctx.newRemote.name" class="modal-input rm-name-in" spellcheck="false"
               :placeholder="t('git_remote_name_ph')" />
        <input v-model="ctx.newRemote.url" class="modal-input" spellcheck="false"
               :placeholder="t('git_remote_url_ph')" @keydown.enter.prevent="ctx.addRemote" />
        <button class="qd-btn qd-btn-primary" :disabled="!!ctx.remoteBusy" @click="ctx.addRemote">
          {{ t('git_remote_add') }}
        </button>
      </div>
    </div>
    <footer class="modal-foot">
      <button class="qd-btn" @click="ctx.remoteOpen = false">{{ t('close') }}</button>
    </footer>
  </div>
</div>

<!-- 子模块：clone 后目录为空、改了 .gitmodules 地址却没生效，这两件事都在这修 -->
<div v-if="ctx.subOpen" class="modal-mask" @click.self="ctx.subOpen = false">
  <div class="modal modal-lg">
    <header class="modal-head">
      <span>{{ t('git_submodules') }}</span>
      <button class="modal-x" @click="ctx.subOpen = false"><X :size="13" /></button>
    </header>
    <div class="modal-body">
      <p class="modal-hint">{{ t('git_sub_hint') }}</p>
      <ul v-if="ctx.subs.length" class="sub-list">
        <li v-for="s in ctx.subs" :key="s.path" class="sub-row">
          <span :class="['sub-state', ctx.subStateCls(s.state)]">{{ ctx.subStateText(s.state) }}</span>
          <span class="sub-path" :title="s.path">{{ s.path }}</span>
          <span class="sub-sha mono">{{ s.sha.slice(0, 7) }}</span>
          <span v-if="s.describe" class="sub-desc" :title="s.describe">{{ s.describe }}</span>
          <button class="pb-btn" :disabled="!!ctx.subBusy" @click="ctx.subUpdate(s, false)">
            <Loader2 v-if="ctx.subBusy === s.path" :size="11" class="is-spinning" />
            <template v-else>{{ t('git_sub_update') }}</template>
          </button>
          <button class="pb-btn" :disabled="!!ctx.subBusy" :title="t('git_sub_remote_hint')"
                  @click="ctx.subUpdate(s, true)">
            <Loader2 v-if="ctx.subBusy === 'remote:' + s.path" :size="11" class="is-spinning" />
            <template v-else>{{ t('git_sub_remote') }}</template>
          </button>
        </li>
      </ul>
      <p v-else class="modal-hint">{{ t('git_sub_none') }}</p>

      <div class="rm-add">
        <input v-model="ctx.newSub.url" class="modal-input" spellcheck="false"
               :placeholder="t('git_sub_url_ph')" />
        <input v-model="ctx.newSub.path" class="modal-input rm-name-in" spellcheck="false"
               :placeholder="t('git_sub_path_ph')" @keydown.enter.prevent="ctx.subAdd" />
        <button class="qd-btn" :disabled="!!ctx.subBusy" @click="ctx.subAdd">{{ t('git_sub_add') }}</button>
      </div>
    </div>
    <footer class="modal-foot">
      <button class="qd-btn" :disabled="!!ctx.subBusy" @click="ctx.subSync">{{ t('git_sub_sync') }}</button>
      <button class="qd-btn qd-btn-primary" @click="ctx.subOpen = false">{{ t('close') }}</button>
    </footer>
  </div>
</div>

<!-- 分支对比：以别的分支为基准，当前分支多了什么、少了什么 -->
<div v-if="ctx.compareState" class="modal-mask" @click.self="ctx.compareState = null">
  <div class="modal modal-xl">
    <header class="modal-head">
      <span>{{ t('git_compare_title', { base: ctx.compareState.base, head: ctx.compareState.head }) }}</span>
      <button class="modal-x" @click="ctx.compareState = null"><X :size="13" /></button>
    </header>
    <div class="modal-body cmp-body">
      <p v-if="ctx.compareState.loading" class="modal-hint">
        <Loader2 :size="12" class="is-spinning" />{{ t('loading') }}
      </p>
      <p v-else-if="ctx.compareState.err" class="modal-err">{{ ctx.compareState.err }}</p>
      <template v-else-if="ctx.compareState.data">
        <div class="cmp-sum">
          <span class="cmp-pill add">↑ {{ t('git_compare_ahead', { n: ctx.compareState.data.ahead }) }}</span>
          <span class="cmp-pill del">↓ {{ t('git_compare_behind', { n: ctx.compareState.data.behind }) }}</span>
          <span class="cmp-files-n">{{ t('git_compare_files', { n: ctx.compareState.data.files?.length || 0 }) }}</span>
        </div>
        <div class="cmp-cols">
          <div class="cmp-col">
            <div class="cmp-head">{{ t('git_compare_only_head', { b: ctx.compareState.head }) }}</div>
            <ul class="cmp-list">
              <li v-for="c in ctx.compareState.data.headOnly || []" :key="c.hash" :title="c.subject">
                <span class="cmp-h mono">{{ c.short }}</span>
                <span class="cmp-s">{{ c.subject }}</span>
              </li>
              <li v-if="!ctx.compareState.data.headOnly?.length" class="cmp-empty">{{ t('git_compare_none') }}</li>
            </ul>
          </div>
          <div class="cmp-col">
            <div class="cmp-head">{{ t('git_compare_only_base', { b: ctx.compareState.base }) }}</div>
            <ul class="cmp-list">
              <li v-for="c in ctx.compareState.data.baseOnly || []" :key="c.hash" :title="c.subject">
                <span class="cmp-h mono">{{ c.short }}</span>
                <span class="cmp-s">{{ c.subject }}</span>
              </li>
              <li v-if="!ctx.compareState.data.baseOnly?.length" class="cmp-empty">{{ t('git_compare_none') }}</li>
            </ul>
          </div>
        </div>
        <ul v-if="ctx.compareState.data.files?.length" class="cmp-file-list">
          <li v-for="f in ctx.compareState.data.files || []" :key="f.path">
            <span :class="['file-badge', ctx.statusColor(f.status)]">{{ f.status || 'M' }}</span>
            <span class="file-path">{{ f.path }}</span>
            <span class="file-num">
              <b v-if="f.add" class="add">+{{ f.add }}</b>
              <b v-if="f.del" class="del">−{{ f.del }}</b>
            </span>
          </li>
        </ul>
        <pre v-if="ctx.compareState.data.diff" class="cmp-diff">{{ ctx.compareState.data.diff }}</pre>
      </template>
    </div>
    <footer class="modal-foot">
      <button class="qd-btn" @click="ctx.compareState = null">{{ t('close') }}</button>
    </footer>
  </div>
</div>

<!-- 克隆弹窗 -->
<div v-if="ctx.cloneOpen" class="modal-mask" @click.self="ctx.closeClone">
  <div class="modal">
    <header class="modal-head">
      <span>{{ t('git_clone') }}</span>
      <button class="modal-x" :disabled="ctx.cloning" @click="ctx.closeClone"><X :size="13" /></button>
    </header>
    <div class="modal-body">
      <label class="modal-label">{{ t('git_clone_url') }}</label>
      <input v-model="ctx.cloneUrl" class="modal-input" spellcheck="false"
             :placeholder="t('git_clone_url_ph')" :disabled="ctx.cloning" />
      <label class="modal-label">{{ t('git_clone_parent') }}</label>
      <div class="modal-row">
        <span class="modal-path" :title="ctx.cloneParent">{{ ctx.cloneParent || t('git_clone_none') }}</span>
        <button class="qd-btn qd-btn-sm" :disabled="ctx.cloning" @click="ctx.pickParent">
          {{ t('git_clone_pick') }}
        </button>
      </div>
      <p v-if="ctx.cloneTarget" class="modal-hint">{{ t('git_clone_target') }} <span class="mono">{{ ctx.cloneTarget }}</span></p>
      <p v-if="ctx.cloning" class="modal-hint cloning">
        <Loader2 :size="12" class="spin" />{{ t('git_cloning') }}
      </p>
      <p v-if="ctx.cloneErr" class="modal-err">{{ ctx.cloneErr }}</p>
    </div>
    <footer class="modal-foot">
      <button class="qd-btn" :disabled="ctx.cloning" @click="ctx.closeClone">{{ t('cancel') }}</button>
      <button class="qd-btn qd-btn-primary" :disabled="ctx.cloning" @click="ctx.doClone">
        {{ t('git_clone_go') }}
      </button>
    </footer>
  </div>
</div>

<!-- 命名输入：新建分支 / 打标签 / 重命名分支 -->
<div v-if="ctx.promptState" class="modal-mask" @click.self="ctx.promptState = null">
  <div class="modal modal-sm">
    <header class="modal-head">
      <span>{{ ctx.promptState.title }}</span>
      <button class="modal-x" @click="ctx.promptState = null"><X :size="13" /></button>
    </header>
    <div class="modal-body">
      <label class="modal-label">{{ ctx.promptState.label }}</label>
      <input :ref="(el: any) => (ctx.promptInput = el)" v-model="ctx.promptState.text" class="modal-input" spellcheck="false"
             @keydown.enter.prevent="ctx.onPromptOk" />
    </div>
    <footer class="modal-foot">
      <button class="qd-btn" @click="ctx.promptState = null">{{ t('cancel') }}</button>
      <button class="qd-btn qd-btn-primary" @click="ctx.onPromptOk">{{ t('confirm') }}</button>
    </footer>
  </div>
</div>

<!-- SSH 密钥：已有密钥 / 生成并写 config / 测试 / 仓库切 SSH -->
<div v-if="ctx.sshOpen" class="modal-mask" @click.self="ctx.closeSSH">
  <div class="modal modal-lg">
    <header class="modal-head">
      <span>{{ t('git_ssh') }}</span>
      <button class="modal-x" :disabled="ctx.sshBusy === 'gen'" @click="ctx.closeSSH"><X :size="13" /></button>
    </header>
    <div class="modal-body ssh-body">
      <section class="ssh-sec">
        <div class="ssh-sec-head">
          <span>{{ t('git_ssh_keys') }}</span>
          <span class="ssh-dir" :title="ctx.sshStatus?.configPath">{{ ctx.sshStatus?.dir }}</span>
        </div>
        <ul v-if="ctx.sshStatus?.keys?.length" class="ssh-keys">
          <li v-for="k in ctx.sshStatus.keys" :key="k.path" class="ssh-key">
            <span class="sk-name">{{ k.name }}</span>
            <span class="sk-type">{{ k.type }}</span>
            <span class="sk-fp" :title="k.fingerprint">{{ k.fingerprint || '—' }}</span>
            <button class="qd-btn qd-btn-sm" @click="ctx.copyText(k.publicKey)">{{ t('git_ssh_copy_pub') }}</button>
          </li>
        </ul>
        <p v-else class="modal-hint">{{ t('git_ssh_none') }}</p>
      </section>

      <section class="ssh-sec">
        <div class="ssh-sec-head"><span>{{ t('git_ssh_gen') }}</span></div>
        <div class="ssh-form">
          <label class="modal-label">{{ t('git_ssh_host') }}</label>
          <select v-model="ctx.sshHost" class="modal-input">
            <option value="github.com">github.com</option>
            <option value="gitlab.com">gitlab.com</option>
            <option value="gitee.com">gitee.com</option>
          </select>
          <label class="modal-label">{{ t('git_ssh_comment') }}</label>
          <input v-model="ctx.sshComment" class="modal-input" spellcheck="false"
                 :placeholder="`quickdock@${ctx.sshHost}`" />
          <label class="modal-label">{{ t('git_ssh_pass') }}</label>
          <input v-model="ctx.sshPass" type="password" class="modal-input" spellcheck="false"
                 :placeholder="t('git_ssh_pass_ph')" />
        </div>
        <p class="modal-hint">{{ t('git_ssh_hint', { host: ctx.sshHost }) }}</p>
        <div class="ssh-actions">
          <button class="qd-btn qd-btn-primary" :class="{ 'is-spinning': ctx.sshBusy === 'gen' }"
                  :disabled="!!ctx.sshBusy" @click="ctx.doSSHGenerate">
            <Loader2 v-if="ctx.sshBusy === 'gen'" :size="12" />
            <KeyRound v-else :size="12" />
            {{ ctx.sshBusy === 'gen' ? t('git_ssh_working') : t('git_ssh_gen') }}
          </button>
          <button class="qd-btn" :class="{ 'is-spinning': ctx.sshBusy === 'test' }"
                  :disabled="!!ctx.sshBusy" @click="ctx.doSSHTest">
            <Loader2 v-if="ctx.sshBusy === 'test'" :size="12" />
            <History v-else :size="12" />
            {{ t('git_ssh_test') }}
          </button>
        </div>
        <pre v-if="ctx.sshOut" :class="['ssh-out', { ok: ctx.sshTestOk }]">{{ ctx.sshOut }}</pre>
        <div v-if="ctx.sshGenerated" class="ssh-pub">
          <label class="modal-label">{{ t('git_ssh_pub') }}</label>
          <pre class="ssh-pub-text">{{ ctx.sshGenerated.publicKey }}</pre>
        </div>
      </section>

      <section v-if="ctx.activePath" class="ssh-sec">
        <div class="ssh-sec-head"><span>{{ t('git_ssh_remote') }}</span></div>
        <p class="modal-hint mono ssh-remote">{{ ctx.sshRemote?.url || t('git_ssh_no_remote') }}</p>
        <div class="ssh-actions">
          <button class="qd-btn" :disabled="!!ctx.sshBusy || !ctx.sshRemote?.url || ctx.sshRemote?.isSsh"
                  @click="ctx.remoteToSSH">
            {{ ctx.sshRemote?.isSsh ? t('git_ssh_already') : t('git_ssh_to_ssh') }}
          </button>
        </div>
      </section>
    </div>
    <footer class="modal-foot">
      <button class="qd-btn" :disabled="ctx.sshBusy === 'gen'" @click="ctx.closeSSH">{{ t('close') }}</button>
    </footer>
  </div>
</div>

<!-- 逐行归属：一行一句话「谁、什么时候、哪次提交写的」 -->
<div v-if="ctx.blameState" class="modal-mask" @click.self="ctx.blameState = null">
  <div class="modal modal-lg">
    <header class="modal-head">
      <span>{{ t('git_ctx_blame') }}</span>
      <span class="bm-file" :title="ctx.blameState.file">{{ ctx.blameState.file }}</span>
      <button class="modal-x" @click="ctx.blameState = null"><X :size="13" /></button>
    </header>
    <div class="modal-body blame-body">
      <p v-if="ctx.blameState.loading" class="modal-hint row-hint">
        <Loader2 :size="12" class="spin" />{{ t('loading') }}
      </p>
      <p v-else-if="ctx.blameState.err" class="modal-err">{{ ctx.blameState.err }}</p>
      <template v-else-if="ctx.blameState.data">
        <div class="blame-head">
          <span class="bl-c">{{ t('git_col_branch') }}</span>
          <span class="bl-a">{{ t('git_col_author') }}</span>
          <span class="bl-t">{{ t('git_col_date') }}</span>
          <span class="bl-x">{{ t('git_blame_code') }}</span>
        </div>
        <div class="blame-list">
          <div v-for="l in (ctx.blameState.data.lines || [])" :key="l.line" class="blame-row">
            <span class="bl-c mono bl-link" :title="t('git_blame_goto')" @click="ctx.jumpToCommit(l.hash)">{{ l.short }}</span>
            <span class="bl-a">{{ l.author }}</span>
            <span class="bl-t mono">{{ ctx.fmtTime(l.ts) }}</span>
            <span class="bl-x mono">{{ l.text }}</span>
          </div>
        </div>
        <p v-if="ctx.blameState.data.truncated" class="modal-hint">{{ t('git_blame_truncated') }}</p>
      </template>
    </div>
  </div>
</div>

<!-- 历史版本：某次提交里这个文件长什么样；确认后可以整体恢复到这个版本 -->
<div v-if="ctx.fileView" class="modal-mask" @click.self="ctx.fileView = null">
  <div class="modal modal-lg">
    <header class="modal-head">
      <span>{{ t('git_ctx_view_version') }}</span>
      <span class="bm-file mono">{{ ctx.fileView.file }} @ {{ (ctx.fileView.rev || '').slice(0, 8) }}</span>
      <button class="modal-x" @click="ctx.fileView = null"><X :size="13" /></button>
    </header>
    <div class="modal-body">
      <p v-if="ctx.fileView.loading" class="modal-hint row-hint">
        <Loader2 :size="12" class="spin" />{{ t('loading') }}
      </p>
      <p v-else-if="ctx.fileView.err" class="modal-err">{{ ctx.fileView.err }}</p>
      <template v-else-if="ctx.fileView.data">
        <p v-if="ctx.fileView.data.binary" class="modal-hint">{{ t('git_binary_file') }}</p>
        <pre v-else class="file-view">{{ ctx.fileView.data.content }}</pre>
        <p v-if="ctx.fileView.data.truncated" class="modal-hint">{{ t('git_truncated') }}</p>
      </template>
    </div>
    <footer class="modal-foot">
      <button v-if="ctx.fileView.data && !ctx.fileView.loading && !ctx.fileView.err"
              class="qd-btn qd-btn-primary" @click="ctx.confirmRestoreVersion">
        {{ t('git_restore_version') }}
      </button>
      <button class="qd-btn" @click="ctx.fileView = null">{{ t('close') }}</button>
    </footer>
  </div>
</div>

</template>

<style scoped>
.bar-actions .qd-btn { height: 26px; }

.qd-btn-primary .btn-badge { background: var(--color-accent-text); color: var(--color-accent); }

.git-warn .qd-btn { height: 24px; flex-shrink: 0; }

.cb-foot .qd-btn { margin-left: auto; height: 26px; }

.file-badge {
  width: 14px; flex-shrink: 0; text-align: center;
  font-family: var(--font-mono); font-size: 10px; color: var(--color-text-secondary);
}

.file-badge.add { color: var(--color-success); }

.file-badge.del { color: var(--color-danger); }

.file-badge.mod { color: var(--color-warning); }

.file-badge.ren { color: var(--color-accent); }

.file-badge.new { color: var(--color-success); }

.file-badge.conflict { color: var(--color-danger); }

.file-path {
  flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 11px;
  color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.file-num { display: flex; gap: 3px; flex-shrink: 0; font-family: var(--font-mono); font-size: 10px; }

.file-num b { font-weight: 500; }

.file-num .add { color: var(--color-success); }

.file-num .del { color: var(--color-danger); }

.diff span.add { color: var(--color-success); background: var(--color-success-bg); }

.diff span.del { color: var(--color-danger); background: var(--color-danger-bg); }

.side-table td.del { background: var(--color-danger-bg); color: var(--color-danger); }

.side-table td.add { background: var(--color-success-bg); color: var(--color-success); }

.pb-btn {
  padding: 0 7px; height: 20px; border-radius: var(--radius-xs); cursor: pointer;
  font: inherit; font-size: 11px;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border: 1px solid var(--color-border);
}

.pb-btn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent); }

.pb-btn.danger:hover:not(:disabled) { color: var(--color-danger); border-color: var(--color-danger); }

.pb-btn:disabled { opacity: .5; cursor: default; }

.spin { animation: git-spin 900ms linear infinite; }

.is-spinning :deep(svg) { animation: git-spin 900ms linear infinite; }

@keyframes git-spin { to { transform: rotate(360deg); } }

.modal-sm { width: 380px; }

.modal-mask {
  position: fixed; inset: 0; z-index: 60;
  display: grid; place-items: center;
  background: var(--color-bg-overlay);
}

.modal {
  width: 460px; max-width: calc(100vw - 48px);
  background: var(--color-surface-elevated);
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}

.modal-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 10px var(--space-5); font-size: 13px; font-weight: 600;
  color: var(--color-text-primary); border-bottom: 1px solid var(--color-border);
}

.modal-x {
  display: grid; place-items: center; width: 20px; height: 20px;
  background: none; border: 0; border-radius: var(--radius-xs); cursor: pointer;
  color: var(--color-text-muted);
}

.modal-x:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.modal-body { padding: var(--space-5); display: flex; flex-direction: column; gap: 6px; }

.modal-label { font-size: 11px; color: var(--color-text-muted); }

.modal-input {
  height: 30px; padding: 0 9px; font-size: 12px; font-family: var(--font-mono);
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); color: var(--color-text-primary); outline: none;
}

.modal-input:focus { border-color: var(--color-border-focus-soft); }

.modal-row { display: flex; align-items: center; gap: var(--space-2); }

.modal-path {
  flex: 1; min-width: 0; height: 28px; line-height: 28px; padding: 0 9px;
  font-size: 12px; font-family: var(--font-mono);
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.modal-hint { margin: 0; font-size: 11px; color: var(--color-text-muted); overflow-wrap: anywhere; }

.modal-hint.cloning { display: flex; align-items: center; gap: 5px; color: var(--color-accent); }

.modal-hint .mono { font-family: var(--font-mono); }

.modal-err { margin: 0; font-size: 11px; color: var(--color-danger); overflow-wrap: anywhere; }

.modal-foot {
  display: flex; justify-content: flex-end; gap: var(--space-2);
  padding: 10px var(--space-5); border-top: 1px solid var(--color-border);
}

.rm-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }

.rm-row { display: flex; align-items: center; gap: var(--space-3); }

.rm-name {
  width: 62px; flex-shrink: 0; font-family: var(--font-mono); font-size: 11px;
  color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.rm-tag {
  flex-shrink: 0; font-size: 10px; line-height: 15px; padding: 0 5px;
  border-radius: var(--radius-xs); font-family: var(--font-mono);
}

.rm-tag.https { background: var(--color-bg-tertiary); color: var(--color-text-muted); }

.rm-tag.ssh { background: var(--color-success-bg); color: var(--color-success); }

.rm-input { flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 11px; }

.rm-x {
  display: grid; place-items: center; width: 22px; height: 22px; flex-shrink: 0;
  padding: 0; border: 0; border-radius: var(--radius-xs);
  background: none; color: var(--color-text-muted); cursor: pointer;
}

.rm-x:hover:not(:disabled) { color: var(--color-danger); background: var(--color-danger-bg); }

.rm-x:disabled { opacity: .5; cursor: default; }

.rm-add {
  display: flex; align-items: center; gap: var(--space-3);
  margin-top: var(--space-3); padding-top: var(--space-4);
  border-top: 1px dashed var(--color-border);
}

.rm-name-in { width: 62px; flex: 0 0 62px; }

.rm-add .qd-btn { flex-shrink: 0; height: 26px; }

.modal-lg { width: 640px; max-height: 82vh; display: flex; flex-direction: column; }

.modal-lg .modal-body { overflow-y: auto; }

.modal-xl { width: 860px; max-height: 84vh; display: flex; flex-direction: column; }

.modal-xl .modal-body { overflow-y: auto; }

.sub-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }

.sub-row { display: flex; align-items: center; gap: var(--space-3); }

.sub-state {
  flex-shrink: 0; width: 62px; font-size: 10px; text-align: center; line-height: 16px;
  border-radius: var(--radius-xs); background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
}

.sub-state.ok { color: var(--color-success); background: var(--color-success-bg); }

.sub-state.init { color: var(--color-warning); background: var(--color-warning-bg); }

.sub-state.diff { color: var(--color-accent); background: var(--color-accent-bg); }

.sub-state.conflict { color: var(--color-danger); background: var(--color-danger-bg); }

.sub-path {
  flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 11px; color: var(--color-text-primary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.sub-sha { flex-shrink: 0; font-size: 10px; color: var(--color-text-disabled); }

.sub-desc {
  flex-shrink: 0; max-width: 110px; font-size: 10px; color: var(--color-text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.cmp-body { display: flex; flex-direction: column; gap: var(--space-4); }

.cmp-sum { display: flex; align-items: center; gap: var(--space-3); }

.cmp-pill {
  font-size: 11px; padding: 1px 7px; border-radius: var(--radius-full);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}

.cmp-pill.add { color: var(--color-success); background: var(--color-success-bg); }

.cmp-pill.del { color: var(--color-danger); background: var(--color-danger-bg); }

.cmp-files-n { font-size: 11px; color: var(--color-text-muted); }

.cmp-cols { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: var(--space-4); }

.cmp-col { min-width: 0; border: 1px solid var(--color-border); border-radius: var(--radius-sm); }

.cmp-head {
  padding: 4px var(--space-3); font-size: 11px; color: var(--color-text-muted);
  background: var(--color-bg-secondary); border-bottom: 1px solid var(--color-border);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.cmp-list { list-style: none; margin: 0; padding: 2px; max-height: 180px; overflow-y: auto; }

.cmp-list li {
  display: flex; align-items: baseline; gap: var(--space-3);
  padding: 2px var(--space-2); font-size: 11px; border-radius: var(--radius-xs);
}

.cmp-list li:hover { background: var(--color-bg-hover); }

.cmp-h { flex-shrink: 0; font-size: 10px; color: var(--color-text-disabled); }

.cmp-s { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.cmp-empty { color: var(--color-text-disabled); }

.cmp-file-list { list-style: none; margin: 0; padding: 0; max-height: 160px; overflow-y: auto; }

.cmp-file-list li { display: flex; align-items: center; gap: var(--space-3); padding: 2px var(--space-2); font-size: 11px; }

.cmp-diff {
  margin: 0; padding: var(--space-3); max-height: 260px; overflow: auto;
  font-family: var(--font-mono); font-size: 11px; line-height: 1.5;
  color: var(--color-text-secondary); background: var(--color-bg-secondary);
  border-radius: var(--radius-sm); white-space: pre;
}

.bm-file {
  flex: 1; min-width: 0; margin-left: var(--space-3); font-size: 11px; font-weight: 400;
  color: var(--color-text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.row-hint { display: flex; align-items: center; gap: 5px; }

.mono { font-family: var(--font-mono); }

.ssh-body { gap: var(--space-4); }

.ssh-sec {
  display: flex; flex-direction: column; gap: 6px;
  padding-bottom: var(--space-4); border-bottom: 1px solid var(--color-border);
}

.ssh-sec:last-child { border-bottom: 0; padding-bottom: 0; }

.ssh-sec-head {
  display: flex; align-items: baseline; gap: var(--space-3);
  font-size: 12px; font-weight: 600; color: var(--color-text-primary);
}

.ssh-dir {
  margin-left: auto; font-family: var(--font-mono); font-size: 10px; font-weight: 400;
  color: var(--color-text-disabled); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.ssh-keys { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 2px; }

.ssh-key {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 3px 6px; border-radius: var(--radius-xs); font-size: 11px;
}

.ssh-key:hover { background: var(--color-bg-hover); }

.sk-name { flex-shrink: 0; font-family: var(--font-mono); color: var(--color-text-primary); }

.sk-type { flex-shrink: 0; font-family: var(--font-mono); font-size: 10px; color: var(--color-text-muted); }

.sk-fp {
  flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 10px;
  color: var(--color-text-disabled); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.ssh-form { display: grid; grid-template-columns: 104px minmax(0, 1fr); align-items: center; gap: 6px var(--space-3); }

.ssh-form .modal-input { height: 28px; }

.ssh-actions { display: flex; gap: var(--space-2); }

.ssh-out {
  margin: 0; padding: 7px 9px; max-height: 130px; overflow: auto;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); font-family: var(--font-mono); font-size: 11px;
  color: var(--color-text-muted); white-space: pre-wrap; overflow-wrap: anywhere;
}

.ssh-out.ok { color: var(--color-success); }

.ssh-pub-text {
  margin: 0; padding: 7px 9px; max-height: 92px; overflow: auto;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); font-family: var(--font-mono); font-size: 10px; line-height: 1.5;
  color: var(--color-text-secondary); white-space: pre-wrap; overflow-wrap: anywhere;
}

.ssh-remote { font-size: 11px; }

.blame-body { gap: 0; padding: 0; }

.blame-head, .blame-row {
  display: grid; grid-template-columns: 68px 100px 78px minmax(0, 1fr);
  gap: var(--space-3); align-items: center; padding: 0 var(--space-4);
}

.blame-head {
  position: sticky; top: 0; height: 24px; flex-shrink: 0;
  font-size: 10px; letter-spacing: .03em; color: var(--color-text-muted);
  background: var(--color-bg-secondary); border-bottom: 1px solid var(--color-border);
}

.blame-list { overflow: auto; max-height: 58vh; }

.blame-row { height: 20px; font-size: 11px; }

.blame-row:hover { background: var(--color-bg-hover); }

.bl-c { color: var(--color-accent); }

.bl-link { cursor: pointer; text-decoration: underline dotted; text-underline-offset: 2px; }

.bl-link:hover { text-decoration-style: solid; }

.bl-a { color: var(--color-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.bl-t { color: var(--color-text-disabled); }

.bl-x { color: var(--color-text-secondary); white-space: pre; overflow: hidden; text-overflow: ellipsis; }

.file-view {
  margin: 0; padding: var(--space-3) var(--space-4); max-height: 62vh; overflow: auto;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono); font-size: 11px; line-height: 1.6;
  color: var(--color-text-secondary); white-space: pre;
}

@media (max-width: 1080px) {
  .git-side { width: 186px; }
  .commit-head, .commit-item { grid-template-columns: var(--graph-ch, 3ch) minmax(0, 1fr) 0 68px; }
  .col-author { display: none; }
  .detail-files { width: 200px; }
  .st-path { display: none; }
}
</style>
