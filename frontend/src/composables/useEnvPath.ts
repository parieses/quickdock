// PATH 可视化面板：把「哪些 bin 目录已注册进用户 PATH」摊开给用户看。
//
// 只读聚合，真正的写操作在维护面板的「环境变量」标签页（useEnvMaintenance）。
// 之所以单独一个 composable：它依赖页面的 runtimes 列表做派生，而维护面板不依赖。
import { computed, reactive } from 'vue'
import type { Ref } from 'vue'
import { binDirOf } from '../utils/envPaths'

interface InstallLike {
  version: string
  scope: string
  path: string
  active: boolean
  inSystemPath: boolean
}
interface RuntimeLike {
  id: string
  name: string
  installed: InstallLike[]
}

export interface PathEntryRow {
  id: string
  name: string
  version: string
  binDir: string
  inPath: boolean
}

export function useEnvPath(deps: { runtimes: Ref<RuntimeLike[]> }) {
  const pathModal = reactive<{ open: boolean }>({ open: false })

  const pathEntries = computed<PathEntryRow[]>(() => {
    const out: PathEntryRow[] = []
    for (const r of deps.runtimes.value) {
      if (!r.installed) continue
      for (const ins of r.installed) {
        // 激活记录（active）或实际已在 PATH 中（inSystemPath）都要显示：
        // 后者覆盖「路径已注册但激活元数据缺失」的情况（如安装后手工改动过 PATH），
        // 否则面板会漏掉实际生效的目录，看着像没注册。
        if (ins.active || ins.inSystemPath) {
          out.push({
            id: r.id,
            name: r.name,
            version: ins.version,
            binDir: binDirOf(ins),
            inPath: !!ins.inSystemPath,
          })
        }
      }
    }
    return out
  })

  return { pathModal, pathEntries }
}
