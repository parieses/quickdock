import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CreateField } from '../components/CreateDialog.vue'

/**
 * 集合表单字段（新建 / 编辑共用）。
 *
 * 抽成 composable 的理由与 useSceneFields 相同：「新建集合」有两个入口 ——
 * 集合栏头部的 + / 空状态按钮，以及「当前场景下没有集合」时的整页引导
 * （此时集合栏不渲染）。两处各写一份字段定义必然漂移（类型选项、i18n 键、顺序）。
 */
export function useCollectionFields() {
  const { t, tm } = useI18n()

  return computed<CreateField[]>(() => {
    const types = tm('collectionTypes') as Record<string, string>
    const strategies = tm('openStrategies') as Record<string, string>
    return [
      {
        key: 'name',
        label: t('collectionName'),
        type: 'text',
        placeholder: t('collectionNamePlaceholder'),
      },
      {
        key: 'type',
        label: t('collectionType'),
        type: 'select',
        options: ['目录集合', '网页集合', '命令集合', '文件集合', '应用集合'].map(v => ({
          label: types[v] || v,
          value: v,
        })),
      },
      {
        key: 'openStrategy',
        label: t('openStrategy'),
        type: 'select',
        options: [
          { label: strategies['single'] || '单次打开', value: 'single' },
          { label: strategies['batch'] || '批量打开', value: 'batch' },
          { label: strategies['all'] || '全部打开', value: 'all' },
        ],
      },
    ]
  })
}
