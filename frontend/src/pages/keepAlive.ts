// pages/keepAlive.ts — KeepAlive 白名单的**唯一**名字来源
//
// KeepAlive 的 include 按组件 name 字符串匹配，而 name 写在各自 SFC 的
// `defineOptions({ name })` 里。两处字符串对不上时**不会报错**：症状只是
// 「切走再回来，页面状态没了」，最难查的一类问题（没有异常、没有日志）。
//
// 所以名字只在这里定义一次，两边都引同一个常量：
//   - 组件：defineOptions({ name: KEEP_ALIVE.monitor })
//   - 白名单：KEEP_ALIVE_PAGES（由同一对象派生，不可能漏项）
//
// 仍然有 TS 检查不到的一环：**组件忘了写 name**。SFC 的运行时 name 无法被静态
// 类型系统读到，所以新增保活页时依旧是两件事 —— 这里加一条 + 组件里引它。
//
// 为什么不写进 registry.ts：registry 静态 import 了 WorkspacePage（内容页首屏），
// 而 WorkspacePage 的子树里如果再有人 import registry 就成了循环。名字常量单独
// 成一个零依赖模块，谁都能引，不牵扯任何模块图。

export const KEEP_ALIVE = {
  ai: 'AIPageKeepAlive',
  monitor: 'MonitorPage',
  notes: 'NoteManagerPage',
  plugins: 'PluginManagerPage',
  ssh: 'SshPage',
} as const

// 类型写成 string[] 而不是 readonly：KeepAlive 的 include 声明是可变的
// (string | RegExp)[]，只读数组传进去 vue-tsc 会报不兼容。
export const KEEP_ALIVE_PAGES: string[] = Object.values(KEEP_ALIVE)
