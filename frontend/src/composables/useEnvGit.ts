// useEnvGit — Git 状态表（版本 / 路径 / SSH / Git LFS）
//
// 模块级缓存（cachedGitInfo）原本写在 EnvironmentPage 的 `<script lang="ts">` 里：
// 切走 git 区块再切回时用上一次结果立即预填，避免「空表 → 加载」闪一下。
// 缓存必须留在模块作用域（跨组件重挂载仍保留），所以跟着逻辑一起搬到这里。
import { computed, ref, watch } from 'vue'
import type { Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { EnvGitStatus } from '../../bindings/quickdock/services/env/environmentservice'

export interface GitStatusInfo {
  installed: boolean
  version: string
  path: string
  ssh: { available: boolean; keyExists: boolean; keyPath: string; result: string; command: string; output: string }
  lfs: { installed: boolean; version: string; command: string; output: string }
}

// 模块级变量只在模块首次加载时执行一次，因此跨组件重挂载仍保留
let cachedGitInfo: GitStatusInfo | null = null

export function useEnvGit(deps: { selectedId: Ref<string> }) {
  const { t } = useI18n()
  const gitInfo = ref<GitStatusInfo | null>(cachedGitInfo)

  async function loadGitInfo() {
    if (deps.selectedId.value !== 'git') {
      // 切走 git 时不清空，保留缓存供下次切回立即预填（消除闪现）
      return
    }
    // 先预填上次缓存，再后台刷新最新数据；首次无缓存（null）时显示空表属正常
    gitInfo.value = cachedGitInfo
    try {
      const res = unwrap<GitStatusInfo>(await EnvGitStatus())
      cachedGitInfo = res
      gitInfo.value = res
    } catch {
      // 刷新失败保留已预填的缓存，不清空
    }
  }

  function sshStatusClass(s: GitStatusInfo['ssh']): string {
    if (s.result === 'ok') return 'ok'
    if (s.result === 'no-key' || s.result === 'n/a') return 'warn'
    return 'fail'
  }
  function sshResultText(s: GitStatusInfo['ssh']): string {
    if (s.result === 'ok') return t('gitSshOk')
    if (s.result === 'no-key') return t('gitSshNoKey')
    if (s.result === 'n/a') return t('gitSshNA')
    return s.output || t('gitSshFail')
  }

  // 状态表行：每行一个 Git 维度，列固定为 Item / Status / Command / Path / Result
  const gitRows = computed(() => {
    const g = gitInfo.value
    if (!g) return []
    return [
      { item: t('gitVersion'), status: g.installed ? 'ok' : 'missing', command: 'git --version', path: '', result: g.version || t('notInstalled') },
      { item: t('gitPath'), status: g.installed ? 'ok' : 'missing', command: 'where git', path: g.path, result: g.path || t('notInstalled') },
      { item: 'SSH', status: sshStatusClass(g.ssh), command: g.ssh.command, path: g.ssh.keyPath, result: sshResultText(g.ssh) },
      { item: 'Git LFS', status: g.lfs.installed ? 'ok' : 'missing', command: g.lfs.command, path: '', result: g.lfs.version || t('notInstalled') },
    ]
  })

  watch(deps.selectedId, () => { void loadGitInfo() })

  return { gitInfo, gitRows, loadGitInfo, sshStatusClass, sshResultText }
}
