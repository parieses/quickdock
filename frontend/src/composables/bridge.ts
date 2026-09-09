// 跨页面桥接：笔记 ↔ AI 互通使用的轻量模块级共享状态。
// 主窗口内所有页面同属一个 WebView，直接用 Vue ref 共享即可，无需 postMessage。
import { ref } from 'vue'

// 笔记 → AI：待注入 AI 输入框的文本（问 AI 时写入，AIPage 监听后填充）
export const aiDraft = ref('')

// 应用层注册的导航函数（App.vue 在 setup 中注入）：
// 关闭笔记/剪贴板等独立视图并切到目标侧边栏页面。
export const navigateTo = ref<((page: string) => void) | null>(null)

// 笔记深链：命令面板选中某条笔记后写入该 id，NoteManagerPage 监听并打开对应文档。
export const pendingOpenNoteId = ref<string>('')
