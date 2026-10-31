import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CreateField } from '../components/CreateDialog.vue'

/**
 * 场景表单字段（新建 / 编辑共用）。
 *
 * 抽成 composable 是因为「新建场景」有两个入口：
 * 1. SceneRail 头部的 + 与空状态按钮；
 * 2. 工作空间空状态引导页（EmptyGuide variant="scene"）的主按钮 —— 此时 SceneRail 不渲染。
 * 两处各写一份字段定义必然漂移（类型选项、i18n 键、顺序），所以只留这一份。
 */
export function useSceneFields() {
  const { t, tm } = useI18n()

  return computed<CreateField[]>(() => {
    const types = tm('sceneTypes') as Record<string, string>
    return [
      {
        key: 'name',
        label: t('sceneName'),
        type: 'text',
        placeholder: t('sceneNamePlaceholder'),
      },
      {
        key: 'type',
        label: t('sceneType'),
        type: 'select',
        options: ['项目', '办公', '工程', '设计', '通用', '自定义'].map(v => ({
          label: types[v] || v,
          value: v,
        })),
      },
      {
        key: 'groupName',
        label: t('mon_group'),
        type: 'text',
        placeholder: t('mon_group_ph'),
      },
    ]
  })
}
