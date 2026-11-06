// i18n 消息语法检查（**生产口径**）。
//
// 为什么需要这一步：
//   '{' '}' 是插值定界符，'@' 是 linked-message 前缀（@:key），'|' 是复数分隔符。
//   文案里出现这些**字面量**时，vue-i18n 的开发分支只打一条 warn —— 本地 dev 跑
//   起来完全正常，所以看不出来；生产分支直接抛 `SyntaxError: 10`，组件渲染中断，
//   整页白屏（QuickDock 就踩过：`AiPlaceholder` 里的「@ 引用文件」让生产包的
//   AI 页面全白、输入框消失）。
//
// 因此这里用 NODE_ENV=production 下的真实编译器把所有消息跑一遍，在 vite build
// 之前失败。要么把字面量写成 vue-i18n 的转义语法 `{'@'}`，要么（技术示例类，
// 如 JSON 片段）干脆移出字典、放组件常量。
process.env.NODE_ENV = 'production'

const { createServer } = await import('vite')
const { createI18n } = await import('vue-i18n/dist/vue-i18n.esm-bundler.js')
const { fileURLToPath } = await import('node:url')

const LOCALES = ['zh-CN', 'en-US']

const server = await createServer({
  // 不读 vite.config.ts —— 这一步只需要把 .ts 字典转译成模块，而加载完整配置会
  // 连带拉进 vue / wails 插件，并触发 client 侧依赖预构建（config 的
  // optimizeDeps.include 里有 mermaid、katex、xterm 这些重包）。
  // 本地 .vite 缓存是热的，看不出代价；CI 每次全新 checkout，等于在检查语法之前
  // 先完整预构建十几个重依赖 —— 白吃一两分钟和一大截内存，也是「本地能过、发版
  // 却挂在 exit status 116」时唯一与本地不同的那件重活。
  // 字典是纯 `export default {}`（无 import），configFile:false 下用内置 esbuild
  // 转译即可，与完整配置的结果完全一致。
  configFile: false,
  root: fileURLToPath(new URL('../../', import.meta.url)),
  // NODE_ENV 已是 production（要的是 vue-i18n 的抛错分支），但 vite 仍按开发模式
  // 起服务：这里只需要它把 .ts 字典转译出来，不要生产构建那套压缩与 tree-shaking。
  mode: 'development',
  server: { middlewareMode: true },
  appType: 'custom',
  logLevel: 'error',
})

function collect(node, prefix, out) {
  for (const [k, v] of Object.entries(node || {})) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object') collect(v, key, out)
    else if (typeof v === 'string') out.push([key, v])
  }
}

let total = 0
const failures = []

for (const locale of LOCALES) {
  const mod = await server.ssrLoadModule(`/src/i18n/${locale}.ts`)
  const pairs = []
  collect(mod.default || mod, '', pairs)
  total += pairs.length

  // 一个实例、逐条编译：key 用序号，避免文案 key 里带点号时被当成路径解析
  // （要校验的是消息内容，不是 key）。每条消息都真正过一遍编译器。
  const bucket = {}
  const i18n = createI18n({
    legacy: false,
    locale,
    fallbackLocale: locale,
    messages: { [locale]: bucket },
    missingWarn: false,
    fallbackWarn: false,
  })
  pairs.forEach(([, msg], i) => {
    const key = `__m${i}`
    bucket[key] = msg
    try {
      i18n.global.t(key)
    } catch (e) {
      failures.push({ locale, key: pairs[i][0], msg, err: String((e && e.message) || e) })
    }
  })
  console.log(`${locale}: ${pairs.length} 条`)
}

await server.close()

if (failures.length) {
  console.error(`\n✗ ${failures.length} 条文案无法被 vue-i18n 编译（生产构建会白屏）：\n`)
  for (const f of failures) {
    console.error(`  [${f.locale}] ${f.key}`)
    console.error(`    ${f.err}`)
    console.error(`    ${JSON.stringify(f.msg).slice(0, 240)}`)
  }
  console.error('\n修法：字面量 @ 写成 {\'@\'}；技术示例（JSON / 命令行片段）不要进 i18n 字典。')
  process.exit(1)
}

console.log(`✓ ${total} 条文案全部可编译`)
