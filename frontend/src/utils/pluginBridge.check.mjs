// 插件 iframe 桥接自检（无依赖，node 直接跑）：`npm run check:bridge`
//
// 为什么需要它：BRIDGE_SCRIPT 是一段以模板字符串内联在 pluginBridge.ts 里的
// 压缩 JS，注入到每个插件 iframe 的 <head>。它没有类型检查、没有构建期校验，
// 改坏一个引号就是「所有 none 类插件的宿主能力静默失灵」，而这类问题在宿主里
// 只表现为插件行为异常，极难定位。
//
// 本脚本把 BRIDGE_SCRIPT 抽出来在 stub 环境里真跑一遍，断言：
//   1. 脚本可解析执行（语法坏 → 立刻失败）
//   2. 新老桥函数都在（新增时不会顺手删掉旧的）
//   3. qdHostCall / qdHttp 的请求构造、resolve/reject 语义正确
//   4. 被 __qdBridge 拦住、不重复注入
// 退出码非 0 即失败，可直接接进 CI。
//
// 放在 src/utils/ 而不是 frontend/scripts/：`.gitignore` 的 `scripts/` 规则会匹配
// 任意层级的 scripts 目录，放那儿会被静默忽略，npm script 就成了空头承诺。
import fs from 'node:fs'
import path from 'node:path'
import vm from 'node:vm'

const SRC = path.resolve(import.meta.dirname, 'pluginBridge.ts')
const src = fs.readFileSync(SRC, 'utf8')
const m = src.match(/const BRIDGE_SCRIPT = `([\s\S]*?)`\r?\n/)
if (!m) {
  console.error('FAIL 未在 ' + SRC + ' 中找到 BRIDGE_SCRIPT 定义')
  process.exit(1)
}

let inner = m[1]
if (!inner.startsWith('<script>')) {
  console.error('FAIL BRIDGE_SCRIPT 未以 <script> 开头')
  process.exit(1)
}
// 去掉外层 <script> 标签；源码里结尾写作 <\/script>（模板字符串转义），此处还原
inner = inner.slice('<script>'.length).replace(/<\\?\/script>\s*$/, '')

const listeners = {}
const posted = []
const docEl = { setAttribute() {}, getAttribute() { return 'dark' } }
const win = {
  addEventListener(t, fn) {
    ;(listeners[t] = listeners[t] || []).push(fn)
  },
  parent: { postMessage(msg) { posted.push(msg) } },
  document: { documentElement: docEl },
}
const ctx = { window: win, document: win.document, Promise, Object, Error, String, JSON }

let bad = 0
function ok(cond, label, extra) {
  if (cond) {
    console.log('  ok   ' + label)
  } else {
    bad++
    console.log('  FAIL ' + label + (extra === undefined ? '' : '  → ' + JSON.stringify(extra)))
  }
}

try {
  vm.runInNewContext(inner, ctx)
} catch (e) {
  console.error('FAIL 桥接脚本执行异常: ' + e.message)
  process.exit(1)
}
console.log('脚本解析执行 OK（' + inner.length + ' chars）')

for (const k of ['qdConfirm', 'qdAlert', 'qdPickFile', 'qdPickFolder', 'qdReadFile', 'qdHostCall', 'qdHttp']) {
  ok(typeof win[k] === 'function', 'window.' + k + ' 存在')
}
ok(typeof win.alert === 'function', 'window.alert 仍被覆盖')

function sendToIframe(data) {
  ;(listeners.message || []).forEach((fn) => fn({ data }))
}

// qdHostCall：发 plugin:host，收到 plugin:host-result 后 resolve
posted.length = 0
const p1 = win.qdHostCall('host.mcp.call', { tool: 'port_list', args: {} })
ok(posted.length === 1, 'qdHostCall 发出一条消息', posted.length)
const msg1 = posted[0]
ok(msg1.type === 'plugin:host', 'type=plugin:host', msg1.type)
ok(msg1.method === 'host.mcp.call', 'method 透传', msg1.method)
ok(msg1.params && msg1.params.tool === 'port_list', 'params 透传', msg1.params)
ok(typeof msg1.id === 'string' && msg1.id.length > 0, '带请求 id', msg1.id)
sendToIframe({ type: 'plugin:host-result', id: msg1.id, data: { tool: 'port_list', result: 'ok' } })

// 宿主返回 error → reject，不静默吞掉
posted.length = 0
let rejected = null
win.qdHostCall('db.set', { key: 'a' }).catch((e) => {
  rejected = e
})
sendToIframe({ type: 'plugin:host-result', id: posted[0].id, error: '权限不足: 插件没有 network 权限' })

// qdHttp：默认 GET → http.get
posted.length = 0
win.qdHttp({ url: 'https://example.com/a' })
ok(posted[0].method === 'http.get', '默认 GET → http.get', posted[0].method)
ok(posted[0].params.url === 'https://example.com/a', 'url 透传', posted[0].params.url)
ok(posted[0].params.body === '', 'GET body 为空', posted[0].params.body)

// qdHttp：POST + 对象 body 自动 JSON 化并补 Content-Type
posted.length = 0
win.qdHttp({ url: 'https://example.com/b', method: 'post', body: { a: 1, b: '中' }, headers: { 'X-K': 'v' } })
const m6 = posted[0]
ok(m6.method === 'http.post', 'POST → http.post', m6.method)
ok(m6.params.body === JSON.stringify({ a: 1, b: '中' }), '对象 body 自动 JSON.stringify', m6.params.body)
ok(m6.params.contentType === 'application/json', '自动补 Content-Type', m6.params.contentType)
ok(m6.params.headers['X-K'] === 'v', 'headers 透传', m6.params.headers)

// qdHttp：字符串 body 不覆盖调用方给的 contentType
posted.length = 0
win.qdHttp({ url: 'https://example.com/c', method: 'POST', body: 'x=1', contentType: 'application/x-www-form-urlencoded' })
ok(posted[0].params.body === 'x=1', '字符串 body 原样传', posted[0].params.body)
ok(posted[0].params.contentType === 'application/x-www-form-urlencoded', 'contentType 不被覆盖', posted[0].params.contentType)

// 旧桥未被破坏
posted.length = 0
const p3 = win.qdConfirm('确定?')
ok(posted[0].type === 'plugin:confirm', 'qdConfirm 仍发 plugin:confirm', posted[0].type)
sendToIframe({ type: 'plugin:confirm-result', id: posted[0].id, ok: true })

// 重复注入被拦
vm.runInNewContext(inner, ctx)
ok((listeners.message || []).length === 1, '重复注入被 __qdBridge 拦下', (listeners.message || []).length)

const r1 = await p1
ok(r1 && r1.tool === 'port_list', 'resolve 拿到宿主返回', r1)
ok(rejected instanceof Error, 'error 时 reject', rejected && rejected.message)
ok(rejected && /权限不足/.test(rejected.message), 'reject 保留宿主错误文案', rejected && rejected.message)
ok((await p3) === true, 'qdConfirm resolve(true)')

console.log('\nbad = ' + bad)
process.exit(bad === 0 ? 0 : 1)
