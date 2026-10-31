// pages/registry.ts — 页面注册表（导航项 + 内容页的单一数据源）
//
// 这是「引入页面注册机制」债务项的落地：把主窗口可导航内容页 + 侧边栏导航项收敛为
// 单一数据源。新增一个页面只需在此加一条 NavEntry，App 内容区（<component :is>）与
// Sidebar 导航（v-for）都从这里驱动，彻底消灭「改四处漏一处」的脆弱性。
//
// 为什么不直接引 vue-router：本应用是 Wails 单窗口桌面程序，页面只是原地切换的面板，
// 没有真实 URL / 前进后退需求，vue-router 属杀鸡用牛刀。故用轻量自研注册表 + 动态组件。
//
// 每条 NavEntry 只声明「它是什么」，行为差异由**正交字段**表达，不再有 kind 分派：
//   component     有就进内容区渲染（workspace 也是内容页，复合布局在组件自己内部）
//   activate      非内容页入口（ssh / dsh）：点击时执行它，不切 currentPage、不置 active
//   hintKey       悬停说明（告诉用户点下去会发生什么）
//   busyLabelKey  启动中态的文案（App 用 busyPage 报告哪个 id 正在启动）
//   dot           「后台仍在跑」指示点来源（provider 表在 Sidebar —— 那里才有响应式数据）
//
// 为什么不用 kind：「kind 决定行为」意味着每加一个 kind 都要同时改 registry 类型、
// Sidebar 模板、App 的分发分支，而三处各自只看到 kind 的一小部分语义。现在改成
// 「条目自己声明行为」，App 的分发只有一句 `if (entry.activate)`，不需要认识 dsh/ssh。

import { defineAsyncComponent, type Component, type Ref } from 'vue'
import {
  FolderKanban, FileText, ListTodo, AlarmClock, Activity,
  Clipboard, Server, SquareTerminal, Bot, Terminal, Puzzle,
} from '@lucide/vue'
// 保活白名单从零依赖模块引（名字常量与组件里 defineOptions 共用同一份）
import { KEEP_ALIVE_PAGES } from './keepAlive'
// 工作空间复合页是首屏默认页，静态引（异步会让启动时内容区先空一拍）
import WorkspacePage from '../components/WorkspacePage.vue'

// 主窗口内容页的异步组件集中在此定义（替代此前散落在 App.vue 的 defineAsyncComponent）。
const NoteManagerPage = defineAsyncComponent(() => import('../components/NoteManagerPage.vue'))
const PluginManagerPage = defineAsyncComponent(() => import('../components/PluginManagerPage.vue'))
const TodoPage = defineAsyncComponent(() => import('../components/TodoPage.vue'))
const SchedulePage = defineAsyncComponent(() => import('../components/SchedulePage.vue'))
const MonitorPage = defineAsyncComponent(() => import('../components/MonitorPage.vue'))
const AIPage = defineAsyncComponent(() => import('../components/AIPage.vue'))
// 注意：不能在这里给 AIPage 赋 name 来对齐白名单。KeepAlive 判断异步组件时读的是
// `vnode.type.__asyncResolved`（内层 SFC 定义）的名字，外层包装器上的 name 无人问津。
// 所以名字写在 AIPage.vue 的 defineOptions 里（值来自 pages/keepAlive.ts 的常量）。
const EnvironmentPage = defineAsyncComponent(() => import('../components/EnvironmentPage.vue'))
// 剪贴板历史页需要 .clipboard-page 容器撑满，故单独引用组件 + wrapperClass
const ClipboardPanel = defineAsyncComponent(() => import('../components/ClipboardPanel.vue'))

/** 「行尾活动指示点」的来源标识。闭集（union）：新增一种必须同时在 Sidebar 的
 *  DOT_PROVIDERS 里加 provider，否则 TS 报缺项 —— 这是把 stringly-typed 的
 *  `{ source, condition }` 换成编译期可查的写法，运行时不需要解释器。 */
export type NavDotKind = 'ai'

// App 注入的运行期上下文：内容页拿它算动态 props/事件，非内容页入口拿它执行 activate。
// 注册表本身不依赖 App 状态，只依赖这个接口 —— 保证「加一条数据」不会牵扯 App 内部。
export interface PageContext {
  envInitialSection: Ref<string | undefined>
  openSettings: (page?: string) => void
  /** 打开 DSH 原生窗口（含 node/dsh 环境探测与自动安装） */
  openDsh: () => void | Promise<void>
  /** 唤起 SSH 独立终端窗口（已存在则前置复用） */
  openSsh: () => void | Promise<void>
}

export interface NavEntry {
  id: string
  labelKey: string
  icon: Component
  /** 内容区渲染的组件：有 component 即被渲染（与「是不是导航页」无关）。
   *  无 component 的是独立窗口入口（ssh/dsh），它们靠 activate 响应点击。 */
  component?: Component
  /** 包裹容器 class（如 clipboard 需要 .clipboard-page 撑满） */
  wrapperClass?: string
  /** 动态 props（如 environments 的 initial-section） */
  props?: (ctx: PageContext) => Record<string, unknown>
  /** 动态事件监听（如 ai 的 open-settings） */
  listeners?: (ctx: PageContext) => Record<string, (...args: any[]) => void>
  /** 点击时唤起独立窗口而不是切页：有它就不置 active，也不参与内容区渲染 */
  activate?: (ctx: PageContext) => void | Promise<void>
  /** 悬停提示（说明点击后果，如 ssh 会跳到独立窗口） */
  hintKey?: string
  /** 启动中态文案：配合 App 的 busyPage（Sidebar 据此换 spinner / 禁用 / 换文案） */
  busyLabelKey?: string
  /** 行尾「后台活动」指示点来源 */
  dot?: NavDotKind
}

// 顺序即侧边栏导航顺序（workspace → notes → todo → schedule → monitor → clipboard →
// environments → ssh → ai → dsh → plugins），新增页请保持语义位置。
export const navEntries: NavEntry[] = [
  // workspace 是复合页（引导 / 空态 / 三栏），布局细节在 WorkspacePage 内部，
  // 注册表这边与其它内容页没有区别。
  { id: 'workspace', labelKey: 'navWorkspace', icon: FolderKanban, component: WorkspacePage,
    listeners: (ctx) => ({ 'open-settings': ctx.openSettings }) },
  { id: 'notes', labelKey: 'navNotes', icon: FileText, component: NoteManagerPage },
  { id: 'todo', labelKey: 'navTodo', icon: ListTodo, component: TodoPage },
  { id: 'schedule', labelKey: 'navSchedule', icon: AlarmClock, component: SchedulePage },
  { id: 'monitor', labelKey: 'navMonitor', icon: Activity, component: MonitorPage },
  { id: 'clipboard', labelKey: 'navClipboard', icon: Clipboard, component: ClipboardPanel, wrapperClass: 'clipboard-page' },
  { id: 'environments', labelKey: 'navEnvironments', icon: Server, component: EnvironmentPage,
    props: (ctx) => ({ initialSection: ctx.envInitialSection.value }) },
  // ssh：唤起独立终端窗口，主窗口内容不动
  { id: 'ssh', labelKey: 'navSsh', icon: SquareTerminal,
    activate: (ctx) => ctx.openSsh(), hintKey: 'navSshHint' },
  // ai：被 KeepAlive 保活，回答会在别的页面继续生成 → 行尾挂活动指示点
  { id: 'ai', labelKey: 'navAi', icon: Bot, component: AIPage, dot: 'ai',
    listeners: (ctx) => ({ openSettings: ctx.openSettings }) },
  // dsh：唤起 DeepSeek Harness 原生窗口，启动中显示 loading（App 以 busyPage='dsh' 报告）
  { id: 'dsh', labelKey: 'navDsh', icon: Terminal,
    activate: (ctx) => ctx.openDsh(), busyLabelKey: 'dshLaunching' },
  { id: 'plugins', labelKey: 'navPlugins', icon: Puzzle, component: PluginManagerPage },
]

export function findNavEntry(id: string): NavEntry | undefined {
  return navEntries.find((e) => e.id === id)
}

/**
 * 内容区按 currentPage 取注册页：只要带 component 就渲染。
 * 注意「是否渲染」只看 component，与导航项行为（activate / dot / busy）无关 ——
 * 曾经因为没有这个区分，漏渲染过 kind='plugins' 的插件页。
 */
export function findContentPage(id: string): NavEntry | undefined {
  return navEntries.find((e) => e.id === id && e.component)
}

// KeepAlive 白名单：这些页离开后继续存活，切回来不重 fetch / 不重建（如 SSH 的 xterm 会话、
// Monitor 的轮询状态、Note 的编辑态、AI 的回答流）。其余页（workspace/todo/schedule/
// clipboard/environments）仍按常规卸载重载 —— 它们是无状态列表，重建便宜且能拿到最新数据。
// 名字的唯一来源在 pages/keepAlive.ts（组件 defineOptions 与这里引同一个常量）。
export { KEEP_ALIVE_PAGES }
