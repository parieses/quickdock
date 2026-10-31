// useEnvServiceControl.ts — Ctrl+K 直控「环境运行时服务」（启动 / 停止 / RabbitMQ 管理后台）
//
// 这套东西原本切成两半：状态（envRuntimes/envStatuses/envMgmt）、拉取、轮询、执行都在
// CommandPalette.vue，而「怎么把它匹配成一条命令」在 useCommandSearch.ts。同一领域跨两个
// 文件，加一个服务动作要同时改两边，还容易只改一半 —— 所以整块收进这里。
// 搜索侧现在只消费三个 ref（当纯数据读），职责边界跟着清晰。
//
// 轮询是「按需」而非常驻：面板唤起时刷一次；执行动作后再补一次（延迟 1.5s 给后端留出
// 端口就绪的时间）。不做定时器常驻 —— Ctrl+K 面板是开合频繁的浮窗，常驻轮询纯属浪费 IPC。

import { ref, onScopeDispose } from 'vue'
import { useI18n } from 'vue-i18n'
import { inject } from 'vue'
import { unwrap } from '../utils/api'
import { logDebug } from '../utils/logger'
import { getErrorMessage } from '../utils/error'
import {
  EnvList,
  EnvStatus,
  EnvStart,
  EnvStop,
  EnvRabbitMQEnableMgmt,
  EnvRabbitMQDisableMgmt,
  EnvRabbitMQIsMgmtEnabled,
} from '../../bindings/quickdock/services/env/environmentservice'
import type { EnvRuntimeInfo, EnvServiceStatus, ToastAPI } from '../types'
import type { SearchResult } from './useCommandSearch'

// 执行动作后再补一次轮询的延迟：启停是异步的，立刻查会拿到旧状态
const ENV_POLL_RETRY_MS = 1500

export function useEnvServiceControl() {
  const { t } = useI18n()
  const toast = inject<ToastAPI>('toast')

  const envRuntimes = ref<EnvRuntimeInfo[]>([])
  const envStatuses = ref<Record<string, EnvServiceStatus>>({})
  // RabbitMQ 各版本的管理后台启用状态（version → enabled）
  const envMgmt = ref<Record<string, boolean>>({})

  let pollGen = 0
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  const envKeyOf = (id: string, v: string) => id + ':' + v

  async function loadEnvList() {
    try {
      const list = unwrap<EnvRuntimeInfo[]>(await EnvList())
      envRuntimes.value = list || []
    } catch (e) {
      console.error('[CmdPalette] EnvList:', e)
    }
  }

  // 轮询各服务运行时的安装版本状态，写入 envStatuses（key = runtimeId:version）；
  // 对 RabbitMQ 各版本额外查询管理后台启用状态，写入 envMgmt。
  async function pollEnvStatus() {
    const gen = ++pollGen
    const next: Record<string, EnvServiceStatus> = {}
    const nextMgmt: Record<string, boolean> = {}
    for (const r of envRuntimes.value) {
      if (!r.hasService || r.installed.length === 0) continue
      for (const ins of r.installed) {
        if (ins.scope === 'linked' && r.id !== 'php') continue
        try {
          const st = unwrap<EnvServiceStatus>(await EnvStatus(r.id, ins.version))
          if (st) next[envKeyOf(r.id, ins.version)] = st
        } catch {
          /* 单次失败忽略 */
        }
        if (r.id === 'rabbitmq') {
          try {
            nextMgmt[ins.version] = !!unwrap<boolean>(await EnvRabbitMQIsMgmtEnabled(ins.version))
          } catch {
            /* 单次失败忽略 */
          }
        }
      }
    }
    // 代次守卫：期间又发起过新一轮就丢弃本次结果，避免旧快照覆盖新状态
    if (gen === pollGen) {
      envStatuses.value = next
      envMgmt.value = nextMgmt
    }
  }

  /** 拉取列表 + 轮询状态（面板唤起时调用） */
  async function refreshEnv() {
    await loadEnvList()
    await pollEnvStatus().catch(() => {})
  }

  async function executeEnvAction(r: SearchResult) {
    if (!r.envRuntimeId || !r.envAction) return
    try {
      if (r.envAction === 'start') {
        unwrap(await EnvStart(r.envRuntimeId, r.envVersion || ''))
        toast?.success?.(t('envStarted') + ' ' + r.label)
      } else if (r.envAction === 'stop') {
        unwrap(await EnvStop(r.envRuntimeId))
        toast?.success?.(t('envStopped') + ' ' + r.label)
      } else if (r.envAction === 'mgmt') {
        const ver = r.envVersion || ''
        // 依据当前启用状态切换：已启用→关闭，未启用→启用
        if (envMgmt.value[ver] === true) {
          const out = unwrap(await EnvRabbitMQDisableMgmt(ver))
          toast?.success?.(t('rabbitmqDisableMgmtDone'))
          if (out) logDebug('[RabbitMQ mgmt]', out)
        } else {
          const out = unwrap(await EnvRabbitMQEnableMgmt(ver))
          toast?.success?.(t('envMgmtEnabled'))
          if (out) logDebug('[RabbitMQ mgmt]', out)
        }
      }
      // 执行后重轮询状态，让结果即时翻转为相反动作（启动→停止），不关闭面板便于连续控制
      await pollEnvStatus().catch(() => {})
      retryTimer = setTimeout(() => pollEnvStatus().catch(() => {}), ENV_POLL_RETRY_MS)
    } catch (e: any) {
      const msg = r.envAction === 'mgmt' ? t('envMgmtFailed') + ': ' + getErrorMessage(e) : getErrorMessage(e)
      toast?.error?.(msg)
    }
  }

  // 面板卸载时的补偿轮询不能留成孤儿定时器
  onScopeDispose(() => {
    if (retryTimer) clearTimeout(retryTimer)
  })

  return { envRuntimes, envStatuses, envMgmt, refreshEnv, pollEnvStatus, executeEnvAction }
}
