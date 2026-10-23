import { describe, it, expect } from 'vitest'
import { ref, watch, nextTick, type Ref } from 'vue'

/**
 * 在 Node 里复现 SshSessionView -> SshAiPanel 的 pendingAsk 注入时序，
 * 不依赖组件挂载（只验证 watch + props 同步语义）。
 *
 * 关键事实：在一个组件树里，父组件 setup 里 `await nextTick()` 之后改 prop，
 * 子组件的 props 会在同一个 tick 的后续微任务里更新，watch 随即触发。
 * 这里用两个 ref 模拟「父持有的 pendingAsk」与「子收到的 props.pendingAsk」，
 * 并通过把父 ref 的变化同步给子 prop（模拟 Vue 的 prop 传递）来重放。
 */

function makeChild(pendingAskProp: Ref<{ text: string; seq: number } | null>) {
  const captured = ref('')
  watch(
    () => pendingAskProp.value?.seq ?? 0,
    () => {
      const p = pendingAskProp.value
      if (!p) return
      captured.value = p.text
    },
    { immediate: true },
  )
  return captured
}

async function runParent() {
  // 父组件内部状态
  const aiOpen = ref(false)
  const pendingAsk = ref<{ text: string; seq: number } | null>(null)
  let askSeq = 0

  // 模拟子组件：它的 props.pendingAsk 由父 pendingAsk 驱动（Vue 自动同步）
  // 这里用「同一个 ref」代表 prop 链路（父 ref 改 -> 子 prop 改）
  const childCaptured = makeChild(pendingAsk)

  async function askAIAbout(selection: string) {
    aiOpen.value = true
    await nextTick()
    pendingAsk.value = { text: selection, seq: ++askSeq }
  }

  return { aiOpen, pendingAsk, askAIAbout, childCaptured }
}

describe('pendingAsk 注入时序', () => {
  it('首次右键：面板挂载后选区应被填进子组件', async () => {
    const { askAIAbout, childCaptured } = await runParent()
    await askAIAbout('ERROR: connection refused\n  at foo')
    await nextTick()
    expect(childCaptured.value).toBe('ERROR: connection refused\n  at foo')
  })

  it('面板已开时再次右键：选区应被最新值覆盖', async () => {
    const { askAIAbout, childCaptured } = await runParent()
    await askAIAbout('first selection')
    await nextTick()
    const afterFirst = childCaptured.value
    await askAIAbout('second selection')
    await nextTick()
    expect(afterFirst).toBe('first selection')
    expect(childCaptured.value).toBe('second selection')
  })

  it('手动打开面板（pendingAsk=null）不应误填选区', async () => {
    const { childCaptured } = await runParent()
    // 面板由 FAB 手动打开，pendingAsk 始终是 null
    await nextTick()
    expect(childCaptured.value).toBe('')
  })
})
