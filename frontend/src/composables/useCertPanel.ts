import { inject, reactive, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  EnvCertStatus,
  EnvCertInstallRoot,
  EnvCertIssue,
} from '../../bindings/quickdock/services/env/environmentservice'
import { PickFolderPath } from '../../bindings/quickdock/services/plugin/pluginservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

// useCertPanel mkcert 证书一键签发：状态 + 信任根 + 签发。
// 证书是 mkcert 运行时的专属能力，区块随选中 mkcert 时展示（见模板 selected.id==='mkcert'）。
export function useCertPanel(selectedId: Ref<string>) {
  const { t } = useI18n()
  const toast = inject<{ error: (m: string) => void; success: (m: string) => void }>('toast')!

  const certModal = reactive<{
    status: { exe: boolean; rootTrusted: boolean; message: string } | null
    hosts: string
    name: string
    outDir: string
    busy: boolean
    result: string
    error: string
  }>({
    status: null,
    hosts: 'localhost',
    name: 'localhost',
    outDir: '',
    busy: false,
    result: '',
    error: '',
  })

  async function loadCertStatus() {
    try {
      certModal.status = unwrap(await EnvCertStatus())
    } catch (e) {
      certModal.error = getErrorMessage(e)
    }
  }

  // 切到 mkcert 分类时刷新证书区块状态（含根 CA 信任情况）
  function ensureCertLoaded() {
    if (selectedId.value === 'mkcert') loadCertStatus()
  }

  async function certInstallRoot() {
    certModal.busy = true
    certModal.error = ''
    try {
      unwrap(await EnvCertInstallRoot())
      toast.success(t('certRootInstalled'))
      await loadCertStatus()
    } catch (e) {
      certModal.error = getErrorMessage(e)
    } finally {
      certModal.busy = false
    }
  }

  async function certPickDir() {
    const dir = unwrap<string | null>(await PickFolderPath(t('certPickDir')))
    if (dir) certModal.outDir = dir
  }

  async function certIssue() {
    const hosts = certModal.hosts.split(/[\s,，]+/).filter(Boolean)
    if (!certModal.outDir) {
      toast.error(t('certNeedDir'))
      return
    }
    certModal.busy = true
    certModal.result = ''
    certModal.error = ''
    try {
      const res = unwrap<{ cert: string; key: string }>(await EnvCertIssue(certModal.outDir, certModal.name || 'localhost', hosts))
      if (res) certModal.result = res.cert + '\n' + res.key
      toast.success(t('certIssued'))
    } catch (e) {
      certModal.error = getErrorMessage(e)
    } finally {
      certModal.busy = false
    }
  }

  return { certModal, loadCertStatus, ensureCertLoaded, certInstallRoot, certPickDir, certIssue }
}
