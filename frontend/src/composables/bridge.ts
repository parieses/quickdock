// 跨页面桥接：轻量模块级共享状态（导航函数、笔记深链等）。
// 主窗口内所有页面同属一个 WebView，直接用 Vue ref 共享即可，无需 postMessage。
import { ref } from 'vue'

// 应用层注册的导航函数（App.vue 在 setup 中注入）：
// 关闭笔记/剪贴板等独立视图并切到目标侧边栏页面。
export const navigateTo = ref<((page: string) => void) | null>(null)

// 笔记深链：命令面板选中某条笔记后写入该 id，NoteManagerPage 监听并打开对应文档。
export const pendingOpenNoteId = ref<string>('')

// 待办深链：命令面板选中某条待办后写入该 id，TodoPage 监听并滚动定位 + 短暂高亮。
export const pendingOpenTodoId = ref<string>('')

// AI 生成状态（给侧栏用）：AI 页被 KeepAlive 保活，离开后回答仍在后台生成，
// 但用户在别的页看不到任何迹象 —— 尤其是完成时，回来之前需要一个提示。
export const aiGenerating = ref(false)
export const aiFinishedUnread = ref(false)
