// 跨页面桥接：轻量模块级共享状态（导航器、笔记深链等）。
// 主窗口内所有页面同属一个 WebView，直接用 Vue ref 共享即可，无需 postMessage。
import { ref } from 'vue'

// ---- 导航器 ----
// App.vue 在 setup 里通过 setNavigator 注入「切到某个侧边栏页」的实现
// （会先清 hash 关掉独立视图，再改 currentPage）。
//
// 为什么不是导出一个 ref 让调用方 `navigateTo.value?.()`：
// 那样「谁先谁后」就是一条隐式约定 —— 写这行的人必须知道 App 的 setup 已经跑完，
// 否则调用被静默丢弃（`?.` 把问题吃掉了）。改成显式契约：没注册时把目标页记下来，
// 注册时立刻补发一次。于是无论 App 的 onMounted 监听先还是后、事件何时到达，
// 结果都一样，不依赖执行顺序。
let navigatorFn: ((page: string) => void) | null = null
let pendingPage = ''

export function setNavigator(fn: (page: string) => void) {
  navigatorFn = fn
  if (!pendingPage) return
  const page = pendingPage
  pendingPage = '' // 先清再发：补发过程中若再次 navigate，不会被这次冲掉
  fn(page)
}

/** 切到某个侧边栏页面；导航器尚未注册时排队，注册后补发 */
export function navigate(page: string) {
  if (navigatorFn) navigatorFn(page)
  else pendingPage = page
}

// 笔记深链：命令面板选中某条笔记后写入该 id，NoteManagerPage 监听并打开对应文档。
export const pendingOpenNoteId = ref<string>('')

// 待办深链：命令面板选中某条待办后写入该 id，TodoPage 监听并滚动定位 + 短暂高亮。
export const pendingOpenTodoId = ref<string>('')

// AI 生成状态（给侧栏用）：AI 页被 KeepAlive 保活，离开后回答仍在后台生成，
// 但用户在别的页看不到任何迹象 —— 尤其是完成时，回来之前需要一个提示。
export const aiGenerating = ref(false)
export const aiFinishedUnread = ref(false)
