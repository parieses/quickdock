/**
 * 「克隆远端仓库」对话框：URL / 目标目录 / 目标路径预览 / 执行克隆。
 *
 * 从 GitPage 抽出来（那个文件 4000+ 行）。这里只放对话框自己的状态与请求；
 * 克隆成功后的收尾（刷新仓库列表、切到新仓库、重载视图）由 `onCloned` 回调注入
 * ——那些动作要动页面的 activePath 与视图状态，留在页面更清楚。
 */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from './useToast'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { GitClone, GitPickCloneParent } from '../../bindings/quickdock/services/git/gitservice'

export interface GitCloneDeps {
  /** 克隆成功后切到新仓库并刷新列表；target 为空表示后端没回目标路径 */
  onCloned: (target: string) => Promise<void>
}

export function useGitClone(deps: GitCloneDeps) {
  const { t } = useI18n()
  const toast = useToast()

  const cloneOpen = ref(false)
  const cloneUrl = ref('')
  const cloneParent = ref('')
  const cloneErr = ref('')
  const cloning = ref(false)

  /** 与后端 repoNameFromURL 同一规则：取路径末段、剥 .git，用于克隆前预览目标路径。 */
  const cloneName = computed(() => {
    let u = cloneUrl.value.trim().replace(/\/+$/, '')
    const i = Math.max(u.lastIndexOf('/'), u.lastIndexOf(':'))
    if (i >= 0) u = u.slice(i + 1)
    return u.replace(/\.git$/, '')
  })
  const cloneTarget = computed(() =>
    (cloneParent.value && cloneName.value)
      ? `${cloneParent.value.replace(/[\\/]+$/, '')}\\${cloneName.value}`
      : '')

  function openClone() {
    cloneUrl.value = ''
    cloneParent.value = ''
    cloneErr.value = ''
    cloneOpen.value = true
  }

  function closeClone() {
    if (cloning.value) return // 克隆进行中不允许误关：关了也不知道后台跑完没有
    cloneOpen.value = false
  }

  async function pickParent() {
    try {
      const picked = unwrap<string>(await GitPickCloneParent())
      if (picked) cloneParent.value = picked
    } catch (e) {
      logErr('git.pickCloneParent', e)
    }
  }

  async function doClone() {
    const url = cloneUrl.value.trim()
    if (!url) { cloneErr.value = t('git_clone_need_url'); return }
    if (!cloneParent.value) { cloneErr.value = t('git_clone_need_parent'); return }
    cloning.value = true
    cloneErr.value = ''
    try {
      // data 是后端归一化好的目标路径（Windows 分隔符由后端 filepath.Join 处理）
      const target = unwrap<string>(await GitClone(url, cloneParent.value)) || ''
      toast.success(t('git_cloned'))
      cloneOpen.value = false
      await deps.onCloned(target)
    } catch (e) {
      // clone 的失败原因（地址不对 / 没权限 / 目录已存在）在 stderr 里，后端已透出
      cloneErr.value = getErrorMessage(e)
      logErr('git.clone', e)
    } finally {
      cloning.value = false
    }
  }

  return {
    cloneOpen, cloneUrl, cloneParent, cloneErr, cloning,
    cloneName, cloneTarget,
    openClone, closeClone, pickParent, doClone,
  }
}
