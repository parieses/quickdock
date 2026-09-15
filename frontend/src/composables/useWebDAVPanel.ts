import { computed, inject, reactive, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { EnvConfigGet } from '../../bindings/quickdock/services/env/environmentservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

// useWebDAVPanel 内置 WebDAV 服务面板：连接信息与各平台挂载示例。
// 服务端没有专属 API：配置直接读通用 ConfigProvider（config.json 全文），
// 运行状态复用版本表已有的 EnvStatus 轮询结果，故本面板只负责配置展示。
export function useWebDAVPanel(selectedId: Ref<string>) {
  const { t } = useI18n()
  const toast = inject<{ error: (m: string) => void; success: (m: string) => void }>('toast')!

  const webdavInfo = reactive({ path: '', addr: '127.0.0.1', port: 9080, root: '', username: '', password: '', readOnly: false })

  async function loadWebDAV() {
    try {
      const cfg = unwrap<any>(await EnvConfigGet('webdav', 'builtin'))
      if (!cfg?.raw) return
      const c = JSON.parse(cfg.raw)
      Object.assign(webdavInfo, {
        path: cfg.path || '',
        addr: c.addr || '127.0.0.1',
        port: c.port || 9080,
        root: c.root || '',
        username: c.username || '',
        password: c.password || '',
        readOnly: !!c.readOnly,
      })
    } catch {
      /* 配置尚未生成时忽略 */
    }
  }

  // 0.0.0.0 表示对局域网开放，连接地址仍回落到回环地址，方便本机先自测。
  const webdavURL = computed(() => {
    const host = webdavInfo.addr === '0.0.0.0' || webdavInfo.addr === '::' ? '127.0.0.1' : webdavInfo.addr
    return `http://${host}:${webdavInfo.port}/`
  })
  const webdavExposed = computed(() => webdavInfo.addr === '0.0.0.0' || webdavInfo.addr === '::')
  const webdavCmds = computed(() => [
    { label: t('webdavWinLabel'), cmd: `net use Z: ${webdavURL.value} /user:${webdavInfo.username} ${webdavInfo.password}` },
    { label: t('webdavMacLabel'), cmd: webdavURL.value },
    { label: t('webdavLinuxLabel'), cmd: `sudo mount -t davfs ${webdavURL.value} /mnt/webdav` },
  ])

  async function copyWebDAV(text: string) {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      toast.success(t('copied'))
    } catch (e) {
      toast.error(getErrorMessage(e))
    }
  }

  watch(selectedId, (id) => { if (id === 'webdav') loadWebDAV() })

  return { webdavInfo, webdavURL, webdavExposed, webdavCmds, copyWebDAV, loadWebDAV }
}
