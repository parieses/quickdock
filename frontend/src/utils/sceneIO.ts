// 场景声明式导出。
//
// 只保留导出：产品决定场景不做导入（导入很难做到「合并语义」正确 ——
// 条目改名后按名字匹配必然对不上，静默合并不比重名更危险），
// 因此这里只剩「导出成 quickdock-scene.json」，供用户自己留档/进仓库。
//
// 约定：导出只含声明（不含 id 与本机统计），可放进项目仓库随代码走。

import { Dialogs } from '@wailsio/runtime'
import { SceneExport } from '../../bindings/quickdock/services/scene/sceneservice'
import { unwrap } from './api'

export interface SceneExportResult {
  collections: number
  items: number
}

/** 弹出保存对话框并导出；用户取消时返回 null。 */
export async function pickExportScene(
  sceneId: string,
  defaultName: string,
  title: string,
  filterName: string,
): Promise<SceneExportResult | null> {
  const picked = await Dialogs.SaveFile({
    Title: title,
    Filename: (defaultName || 'scene') + '.quickdock-scene.json',
    Filters: [{ DisplayName: filterName, Pattern: '*.json' }],
  })
  if (!picked) return null
  return unwrap<SceneExportResult>(await SceneExport(sceneId, picked)) ?? null
}
