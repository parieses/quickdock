// pages/registry.ts — 页面注册表（替代 App.vue 中 currentPage 手动 v-if 切换链）
//
// 这是「引入页面注册机制」债务项的落地：把主窗口可导航内容页 + 侧边栏导航项收敛为
// 单一数据源。新增一个页面只需在此加一条 NavEntry，App 内容区（<component :is>）与
// Sidebar 导航（v-for）都从这里驱动，彻底消灭「改四处漏一处」的脆弱性。
//
// 为什么不直接引 vue-router：本应用是 Wails 单窗口桌面程序，页面只是原地切换的面板，
// 没有真实 URL / 前进后退需求，vue-router 属杀鸡用牛刀。故用轻量自研注册表 + 动态组件。
//
// 哪些不是「内容页」但仍是导航项：
//   - workspace：复合页（Onboarding / 场景栏 / 集合列表拼装），不在内容区渲染单组件，保留 App 内显式分支。
//   - ssh：唤起独立终端窗口，主窗口内容不动，导航项不置 active。
//   - dsh：DeepSeek Harness 原生窗口入口，启动中显示 loading。
//   - plugins：点击前先清「可更新」角标，带角标展示。

import { defineAsyncComponent, type Component, type Ref } from 'vue'
import {
  FolderKanban, FileText, ListTodo, AlarmClock, Activity,
  Clipboard, Server, SquareTerminal, Bot, Terminal, Puzzle,
} from '@lucide/vue'

// 主窗口内容页的异步组件集中在此定义（替代此前散落在 App.vue 的 defineAsyncComponent）。
const NoteManagerPage = defineAsyncComponent(() => import('../components/NoteManagerPage.vue'))
const PluginManagerPage = defineAsyncComponent(() => import('../components/PluginManagerPage.vue'))
const TodoPage = defineAsyncComponent(() => import('../components/TodoPage.vue'))
const SchedulePage = defineAsyncComponent(() => import('../components/SchedulePage.vue'))
const MonitorPage = defineAsyncComponent(() => import('../components/MonitorPage.vue'))
const AI_KEEP_ALIVE_NAME = 'AIPageKeepAlive'
const AIPage = defineAsyncComponent(() => import('../components/AIPage.vue'))
// 注意：**不能**在这里给 AIPage 赋 name 来对齐白名单。KeepAlive 判断异步组件时读的是
// `vnode.type.__asyncResolved`（内层 SFC 定义）的名字，外层包装器上的 name 无人问津。
// 所以名字写在 AIPage.vue 的 defineOptions 里，这里只保留同一个常量做对照。
const EnvironmentPage = defineAsyncComponent(() => import('../components/EnvironmentPage.vue'))
// 剪贴板历史页需要 .clipboard-page 容器撑满，故单独引用组件 + wrapperClass
const ClipboardPanel = defineAsyncComponent(() => import('../components/ClipboardPanel.vue'))

// 导航项类型：
//   page      —— 普通内容页，点击切到该页（currentPage = id），按 currentPage 高亮
//   workspace —— 复合页导航，行为与 page 一致（仅 App 内容区渲染走显式分支）
//   window    —— 独立窗口入口（ssh），不切主窗口内容、不置 active
//   dsh       —— 原生窗口入口，启动中显示 loading 且禁用
//   plugins   —— 点击清角标后导航，带可更新角标
export type NavKind = 'page' | 'workspace' | 'window' | 'dsh' | 'plugins'

// App 注入渲染上下文：供注册页声明动态 props / 事件处理器，避免注册表直接依赖 App 状态
export interface PageContext {
  envInitialSection: Ref<string | undefined>
  openSettings: (page?: string) => void
}

export interface NavEntry {
  id: string
  labelKey: string
  icon: Component
  kind: NavKind
  /** 内容区渲染的组件：有 component 即会被渲染（与 kind 无关）。
   *  无 component 的条目（workspace/ssh/dsh）不进内容区——workspace 走 App 内显式复合分支，
   *  ssh/dsh 是独立窗口入口。 */
  component?: Component
  /** 包裹容器 class（如 clipboard 需要 .clipboard-page 撑满） */
  wrapperClass?: string
  /** 动态 props（如 environments 的 initial-section） */
  props?: (ctx: PageContext) => Record<string, unknown>
  /** 动态事件监听（如 ai 的 open-settings） */
  listeners?: (ctx: PageContext) => Record<string, (...args: any[]) => void>
}

// 顺序即侧边栏导航顺序（workspace → notes → todo → schedule → monitor → clipboard →
// environments → ssh → ai → dsh → plugins），新增页请保持语义位置。
export const navEntries: NavEntry[] = [
  { id: 'workspace', labelKey: 'navWorkspace', icon: FolderKanban, kind: 'workspace' },
  { id: 'notes', labelKey: 'navNotes', icon: FileText, kind: 'page', component: NoteManagerPage },
  { id: 'todo', labelKey: 'navTodo', icon: ListTodo, kind: 'page', component: TodoPage },
  { id: 'schedule', labelKey: 'navSchedule', icon: AlarmClock, kind: 'page', component: SchedulePage },
  { id: 'monitor', labelKey: 'navMonitor', icon: Activity, kind: 'page', component: MonitorPage },
  { id: 'clipboard', labelKey: 'navClipboard', icon: Clipboard, kind: 'page', component: ClipboardPanel, wrapperClass: 'clipboard-page' },
  { id: 'environments', labelKey: 'navEnvironments', icon: Server, kind: 'page', component: EnvironmentPage,
    props: (ctx) => ({ initialSection: ctx.envInitialSection.value }) },
  // ssh：独立终端窗口入口，主窗口内容不动，导航项不置 active
  { id: 'ssh', labelKey: 'navSsh', icon: SquareTerminal, kind: 'window' },
  { id: 'ai', labelKey: 'navAi', icon: Bot, kind: 'page', component: AIPage,
    listeners: (ctx) => ({ openSettings: ctx.openSettings }) },
  // dsh：DeepSeek Harness 原生窗口入口，启动中显示 loading
  { id: 'dsh', labelKey: 'navDsh', icon: Terminal, kind: 'dsh' },
  // plugins：kind 为 plugins 是为了侧栏角标/清角标，但它同样是内容页，必须带 component
  { id: 'plugins', labelKey: 'navPlugins', icon: Puzzle, kind: 'plugins', component: PluginManagerPage },
]

/**
 * 内容区按 currentPage 取注册页：只要带 component 就渲染。
 * 注意 kind 只决定侧边栏导航行为（角标 / loading / 不置 active），不决定是否渲染——
 * plugins 就是 kind='plugins' 但同样要渲染内容页的例子（曾因此漏渲染）。
 */
export function findContentPage(id: string): NavEntry | undefined {
  return navEntries.find((e) => e.id === id && e.component)
}

// KeepAlive 白名单：这些页离开后继续存活，切回来不重 fetch / 不重建（如 SSH 的 xterm 会话、
// Monitor 的轮询状态、Note 的编辑态、AI 的回答流）。其余页（workspace/notes/todo/schedule/
// clipboard/environments）仍按常规卸载重载——它们是无状态列表，重建便宜且能拿到最新数据。
// 注意：白名单里的页组件必须 `defineOptions({ name })` 与这里的值一致，KeepAlive 按 name 匹配。
export const KEEP_ALIVE_PAGES = [
  AI_KEEP_ALIVE_NAME,
  'MonitorPage',
  'SshPage',
  'NoteManagerPage',
  'PluginManagerPage',
]
