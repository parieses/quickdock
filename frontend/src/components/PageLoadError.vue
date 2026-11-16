<script setup lang="ts">
/**
 * 懒加载页面 chunk 拉取失败时的兜底面板（defineAsyncComponent 的 errorComponent）。
 *
 * 为什么必须有：defineAsyncComponent 一旦 fail()，该组件实例就永久钉在 error 状态；
 * 不配 errorComponent 时 Vue 只渲染一个注释节点 —— 视觉上就是整块空白。
 * 而 SSH 终端窗口是 Hide/Show 保活的（Vue 只在首次加载时 mount 一次），
 * 用户既看不到任何提示，也没有入口让它再 import 一次，只能重启整个应用，
 * 于是「偶发一次 504」被体验成「这个功能彻底坏了」。
 *
 * 触发场景主要是 dev 期：Vite 重跑 optimizeDeps 换掉依赖的 ?v= 哈希、
 * 或 SFC 正处于编译中，已经加载完的模块图去拉懒加载 chunk 会吃到 504
 * （控制台报 Failed to fetch dynamically imported module）。
 *
 * 这里只做一件事：把白屏换成「看得懂 + 点一下能自救」。
 * Vue 不会给 errorComponent 传 retry，所以重试只能是整页重载 ——
 * SSH 窗口重载会清掉 xterm 屏幕，但 Go 侧会话是 retained 的，比白屏划算。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ error?: unknown }>()
const { t } = useI18n()

/** 原始错误消息：dev 期它就是唯一线索（504 / 编译错误都在这句里），
 *  截断到 300 字，避免整段堆栈把卡片撑爆。 */
const detail = computed(() => {
  const e: any = props.error
  const msg = e?.message || String(e ?? '')
  return msg.length > 300 ? msg.slice(0, 300) + '…' : msg
})

/** dev 专属的恢复提示：只有「依赖预构建产物缺失」这一种失败是重试按钮治不好的
 *  —— 文件压根不在磁盘上（Vite 8 rolldown 的 re-optimize 会删掉 deps 目录、
 *  重建到一半崩掉，只留下一堆只有 package.json 的 deps_temp_*），刷多少次都一样。
 *  这时唯一出路是补跑一次预构建。把命令写在界面上，省得下次又对着 504 猜。
 *  生产构建没有依赖优化器，这段永远不渲染。 */
const devHint = computed(() => {
  if (!import.meta.env.DEV) return ''
  const msg = (props.error as any)?.message || String(props.error ?? '')
  return /504|Outdated Optimize Dep|Failed to fetch dynamically imported module/i.test(msg)
    ? t('pageLoadFailedDevHint')
    : ''
})

function reload() {
  location.reload()
}
</script>

<template>
  <div class="page-load-error" role="alert">
    <div class="ple-card">
      <h2 class="ple-title">{{ t('pageLoadFailedTitle') }}</h2>
      <p class="ple-desc">{{ t('pageLoadFailedDesc') }}</p>
      <p v-if="detail" class="ple-detail">{{ detail }}</p>
      <p v-if="devHint" class="ple-hint">{{ devHint }}</p>
      <div class="ple-actions">
        <button class="btn btn-primary" type="button" @click="reload">
          {{ t('retry') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 与 PageLoading 同策略：外面那层要自己撑满（.page-shell 是 display:contents，
   本组件就是 .app-content / .ssh-standalone 的直接 flex 子项）。 */
.page-load-error {
  flex: 1;
  min-height: 0;
  display: grid;
  place-items: center;
  padding: var(--space-8);
  overflow: auto;
  background: var(--color-bg-primary);
}

.ple-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  width: 100%;
  max-width: 520px;
  padding: var(--space-7);
  background: var(--color-card-bg);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-card);
}

.ple-title {
  margin: 0;
  font-size: var(--text-lg);
  font-weight: 600;
  color: var(--color-text-primary);
}

.ple-desc {
  margin: 0;
  font-size: var(--text-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

/* 错误原文用等宽 + 可换行：里面常是 URL 或 504 长句，按比例字体读起来断不开 */
.ple-detail {
  margin: 0;
  padding: var(--space-4);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  line-height: 1.5;
  color: var(--color-text-muted);
  word-break: break-word;
  background: var(--color-bg-secondary);
  border-radius: var(--radius-sm);
}

.ple-actions { display: flex; justify-content: flex-end; }

/* dev 恢复提示：故意和错误原文分开一档色，它是要用户「做一件事」而不是「看现场」 */
.ple-hint {
  margin: 0;
  padding: var(--space-4);
  font-size: var(--text-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
  background: var(--color-warning-bg);
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-sm);
}
</style>
