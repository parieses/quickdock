// 跨页面桥接：轻量模块级共享状态（导航函数、笔记深链等）。
// 主窗口内所有页面同属一个 WebView，直接用 Vue ref 共享即可，无需 postMessage。
import { ref } from 'vue'

// 应用层注册的导航函数（App.vue 在 setup 中注入）：
// 关闭笔记/剪贴板等独立视图并切到目标侧边栏页面。
export const navigateTo = ref<((page: string) => void) | null>(null)

// 笔记深链：命令面板选中某条笔记后写入该 id，NoteManagerPage 监听并打开对应文档。
export const pendingOpenNoteId = ref<string>('')
