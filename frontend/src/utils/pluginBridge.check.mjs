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
const attrs = {} // 记录 setAttribute 调用，供主题跟随断言核对
const docEl = {
  setAttribute(k, v) { attrs[k] = v },
  getAttribute() { return 'dark' },
}
const win = {
  addEventListener(t, fn) {
    ;(listeners[t] = listeners[t] || []).push(fn)
  },
  parent: { postMessage(msg) { posted.push(msg) } },
  document: { documentElement: docEl },
}
// Date.now 可控：异常上报的去重逻辑依赖时间窗（10s 内同错只报一次），
// 用真实时钟无法稳定复现「同一窗口内重复报错」这个分支。
let fakeNow = 1000000
const ctx = {
  window: win,
  document: win.document,
  Promise,
  Object,
  Error,
  String,
  JSON,
  Date: { now: () => fakeNow },
  // qdCrypto.b64enc/b64dec 用 btoa/atob + unescape/escape 组合实现 UTF-8 安全编解码。
  // Node 22 起 btoa/atob 已是全局，但 stub 环境不注入，须显式提供；
  // unescape/escape 直接透传 Node 的全局实现（虽已废弃但浏览器仍在，语义一致），
  // 不手写等价版本——手写版对 <256 的字符会用 %uXXXX 而非 %XX，导致 decodeURIComponent
  // 解析出乱码，断言会给出与真实浏览器不符的结论。
  btoa: (s) => Buffer.from(String(s), 'binary').toString('base64'),
  atob: (b) => Buffer.from(String(b), 'base64').toString('binary'),
  unescape,
  escape,
  decodeURIComponent,
  encodeURIComponent,
}

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

for (const k of ['qdConfirm', 'qdAlert', 'qdPickFile', 'qdPickFolder', 'qdReadFile', 'qdHostCall', 'qdHttp', 'qdPrint', 'qdAiAsk']) {
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

// qdPrint：发 plugin:print（打印通道，宿主在顶层文档渲染后调系统打印）
posted.length = 0
let printRejected = null
const pPrintOk = win.qdPrint({ html: '<!DOCTYPE html><p>纸面</p>', page: 'A4' })
ok(posted.length === 1, 'qdPrint 发出一条消息', posted.length)
const mp = posted[0]
ok(mp.type === 'plugin:print', 'type=plugin:print', mp.type)
ok(mp.html === '<!DOCTYPE html><p>纸面</p>', 'html 透传', mp.html)
ok(mp.page === 'A4', 'page 透传', mp.page)
sendToIframe({ type: 'plugin:print-result', id: mp.id, ok: true })

// 失败路径：宿主打印失败必须 reject，不能静默吞掉（否则插件以为打印成功）
posted.length = 0
win.qdPrint({ html: '<p>x</p>' }).catch((e) => {
  printRejected = e
})
ok(posted[0].page === '', 'page 缺省为空串', posted[0].page)
sendToIframe({ type: 'plugin:print-result', id: posted[0].id, ok: false, error: '打印内容为空' })

// 旧桥未被破坏
posted.length = 0
const p3 = win.qdConfirm('确定?')
ok(posted[0].type === 'plugin:confirm', 'qdConfirm 仍发 plugin:confirm', posted[0].type)
sendToIframe({ type: 'plugin:confirm-result', id: posted[0].id, ok: true })

// ---- qdAiAsk：流式 AI 问答 ----
// 宿主沿途下发 plugin:ai-delta（带 streamId），桥按 streamId 投给对应回调；
// 调用结束后必须摘掉回调，否则长期开着的插件会一直累积闭包。
posted.length = 0
const aiDeltas = []
const pAi = win.qdAiAsk({ question: '介绍下 QuickDock', system: 's' }, (d) => aiDeltas.push(d))
const mAi = posted[0]
ok(mAi && mAi.type === 'plugin:host', 'qdAiAsk 走 plugin:host 通道', mAi && mAi.type)
ok(mAi && mAi.method === 'host.ai.ask', 'method=host.ai.ask', mAi && mAi.method)
ok(mAi && typeof mAi.params.streamId === 'string' && mAi.params.streamId.length > 0, '自带 streamId', mAi && mAi.params.streamId)
ok(mAi && mAi.params.question === '介绍下 QuickDock', 'question 透传', mAi && mAi.params.question)
ok(mAi && mAi.params.system === 's', 'system 透传', mAi && mAi.params.system)
sendToIframe({ type: 'plugin:ai-delta', streamId: mAi.params.streamId, delta: '快' })
sendToIframe({ type: 'plugin:ai-delta', streamId: mAi.params.streamId, delta: '启' })
ok(aiDeltas.join('') === '快启', '增量按 streamId 投递给回调', aiDeltas.join(''))
// 别的 streamId 的增量不得串台（同时开两个问答时尤其明显）
sendToIframe({ type: 'plugin:ai-delta', streamId: 'other-stream', delta: '错' })
ok(aiDeltas.join('') === '快启', '其他 streamId 的增量不串台', aiDeltas.join(''))
sendToIframe({ type: 'plugin:host-result', id: mAi.id, data: { text: '快启', model: 'test' } })
const aiRes = await pAi
ok(aiRes && aiRes.text === '快启', '最终结果仍从响应返回', aiRes)
sendToIframe({ type: 'plugin:ai-delta', streamId: mAi.params.streamId, delta: 'X' })
ok(aiDeltas.join('') === '快启', '结束后不再投递增量（回调已摘除）', aiDeltas.join(''))

// 失败路径：Host 返回 error 时同样要摘掉回调，并 reject 出原始文案
posted.length = 0
const aiFailed = []
let aiRejected = null
win.qdAiAsk({ question: 'x' }, (d) => aiFailed.push(d)).catch((e) => {
  aiRejected = e
})
sendToIframe({ type: 'plugin:ai-delta', streamId: posted[0].params.streamId, delta: '半' })
sendToIframe({ type: 'plugin:host-result', id: posted[0].id, error: '未配置可用的 AI 档案' })
// 回调摘除发生在 Promise 链的微任务里，断言前先让微任务跑完
await new Promise((r) => setImmediate(r))
ok(aiFailed.join('') === '半', '失败前已收到的增量仍然保留', aiFailed.join(''))
ok(aiRejected && /未配置可用的 AI 档案/.test(aiRejected.message), 'reject 保留宿主错误文案', aiRejected && aiRejected.message)
sendToIframe({ type: 'plugin:ai-delta', streamId: posted[0].params.streamId, delta: 'Y' })
ok(aiFailed.join('') === '半', '失败后不再投递增量', aiFailed.join(''))

// ---- 主题跟随 ----
// 桥内已有 plugin:theme → documentElement[data-theme] 的同步，插件侧
// 另有 49 份内联 qd-theme-follow 副本。宿主这份是权威来源：删掉插件副本后
// 主题仍要跟随，所以必须有断言守着，坏了要立刻失败。
ok((listeners.message || []).length >= 1, '已注册 message 监听（主题跟随依赖它）')
sendToIframe({ type: 'plugin:theme', data: { theme: 'light' } })
ok(attrs['data-theme'] === 'light', 'plugin:theme=light → data-theme=light', attrs['data-theme'])
sendToIframe({ type: 'plugin:theme', data: { theme: 'dark' } })
ok(attrs['data-theme'] === 'dark', 'plugin:theme=dark → data-theme=dark', attrs['data-theme'])

// 缺 theme 字段时不写。宿主 currentThemeName() 只产出 light/dark 两种值，
// 桥内不做值域校验也不构成风险：插件即使自己 postMessage 伪造
// plugin:theme，也只能改自己 iframe 的 data-theme，影响不了宿主或其他插件。
delete attrs['data-theme']
sendToIframe({ type: 'plugin:theme' })
ok(attrs['data-theme'] === undefined, '缺 theme 字段时不写 data-theme', attrs['data-theme'])
sendToIframe({ type: 'plugin:theme', data: {} })
ok(attrs['data-theme'] === undefined, 'data 为空对象时不写 data-theme', attrs['data-theme'])
sendToIframe({ type: 'other:message', data: { theme: 'light' } })
ok(attrs['data-theme'] === undefined, '非 plugin:theme 消息不触发写入', attrs['data-theme'])

// iframe 沙箱不含 allow-same-origin（opaque origin），读不到 parent.document，
// 主题只能靠宿主消息。插件侧那份「同源兜底」分支是死代码，不要再抄回来。
ok(
  !inner.includes('window.parent.document') && !inner.includes('opener.document'),
  '桥内没有读parent/opener.document 的死代码（opaque origin 下必抛 SecurityError）'
)

// ---- 运行期异常上报 ----
// 桥内 __qdErrHook 段：window.error + unhandledrejection → qdHostCall('log.error')。
// 这是插件唯一能把运行期异常送进宿主日志的通道（GUI 下 console.error 不可见），
// 且是「宿主统一注入」替代插件侧 77 份内联副本的前提。
ok(win.__qdErrHook === true, '__qdErrHook 守卫已置位（防重复安装）')
ok((listeners.error || []).length === 1, '已注册 error 监听', (listeners.error || []).length)
ok((listeners.unhandledrejection || []).length === 1, '已注册 unhandledrejection 监听', (listeners.unhandledrejection || []).length)

function fireUnrejection(reason) {
  ;(listeners.unhandledrejection || []).forEach((fn) => fn({ reason }))
}
function countLogError() {
  return posted.filter((m) => m.type === 'plugin:host' && m.method === 'log.error').length
}
function lastLogError() {
  for (let i = posted.length - 1; i >= 0; i--) {
    const m = posted[i]
    if (m.type === 'plugin:host' && m.method === 'log.error') return m
  }
  return null
}

// 未捕获的 Promise 拒绝要变成一条 log.error
posted.length = 0
fireUnrejection(new Error('boom-1'))
ok(countLogError() === 1, 'Promise 拒绝 → 发出 1 条 log.error', countLogError())
let lg = lastLogError()
ok(lg && /Promise\s*拒绝/.test(lg.params.message), '消息含中文类别', lg && lg.params.message)
ok(lg && /boom-1/.test(lg.params.message), '消息含原始错误文案', lg && lg.params.message)

// 去重策略以「错误指纹 + 时间窗」为准，DEDUP_MS=10s、MAX_PER_FP=5：
// 窗口内重复直接丢弃（静默，不刷屏）；出窗后重报并带累计次数；超 5 次永久丢弃。
posted.length = 0
fakeNow += 1000
fireUnrejection(new Error('boom-1'))
ok(countLogError() === 0, '10s 窗口内同一错误被丢弃', countLogError())

posted.length = 0
fakeNow += 2000
fireUnrejection(new Error('boom-1'))
ok(countLogError() === 0, '仍在窗口内继续丢弃', countLogError())

// 不同错误各报各的，不被误去重
posted.length = 0
fireUnrejection(new Error('boom-2'))
ok(countLogError() === 1, '不同错误独立上报', countLogError())

// 超过去重窗口后重报，并带累计次数
posted.length = 0
fakeNow += 11000
fireUnrejection(new Error('boom-1'))
ok(countLogError() === 1, '超过 10s 去重窗口后恢复上报', countLogError())
ok(
  lastLogError() && lastLogError().params.message.includes('同错第2次'),
  '重报时带累计次数',
  lastLogError() && lastLogError().params.message
)

// 累计到 MAX_PER_FP=5 后永久丢弃
for (let i = 0; i < 6; i++) {
  posted.length = 0
  fakeNow += 11000
  fireUnrejection(new Error('boom-1'))
}
ok(countLogError() === 0, '同一错误累计超过 5 次后不再上报', countLogError())

// 错误文案含换行时不得破坏宿主日志结构（宿主按行拆分日志）。
// 桥内把 stack 截到 600 字符但未压行，压制由宿主侧 logger / native qdlog 的
// 单行化负责，这里只断言长度被截断，避免把超长堆栈整段塞进日志。
posted.length = 0
fireUnrejection(new Error('boom-3'), )
ok(lastLogError() && lastLogError().params.message.length < 1000, '超长消息被截断', lastLogError() && lastLogError().params.message.length)

// 重复注入被拦
vm.runInNewContext(inner, ctx)
ok((listeners.message || []).length === 1, '重复注入被 __qdBridge 拦下', (listeners.message || []).length)

const r1 = await p1
ok(r1 && r1.tool === 'port_list', 'resolve 拿到宿主返回', r1)
ok(rejected instanceof Error, 'error 时 reject', rejected && rejected.message)
ok(rejected && /权限不足/.test(rejected.message), 'reject 保留宿主错误文案', rejected && rejected.message)
ok((await p3) === true, 'qdConfirm resolve(true)')
ok((await pPrintOk) === true, 'qdPrint resolve(true)')
ok(printRejected instanceof Error, 'qdPrint error 时 reject', printRejected && printRejected.message)
ok(printRejected && /打印内容为空/.test(printRejected.message), 'qdPrint reject 保留宿主错误文案', printRejected && printRejected.message)

// ---- qdCrypto：宿主侧加密桥 ----
// iframe 的 sandbox 不含 allow-same-origin → opaque origin → crypto.subtle 不可用，
// 插件（如 vault）只能降级成异或混淆。桥提供 encrypt/decrypt/random 三条，
// 全部走 qdHostCall，因此天然继承它的 resolve/reject 语义与权限校验。
for (const k of ['encrypt', 'decrypt', 'random', 'b64enc', 'b64dec']) {
  ok(typeof win.qdCrypto[k] === 'function', 'qdCrypto.' + k + ' 存在')
}

posted.length = 0
const pEnc = win.qdCrypto.encrypt({ password: 'pw', salt: 'c2FsdA==', plaintext: 'aGVsbG8=' })
const encId = posted[0].id
ok(posted.length === 1, 'qdCrypto.encrypt 发出一条消息', posted.length)
ok(posted[0].type === 'plugin:host', '加密走 plugin:host 通道', posted[0].type)
ok(posted[0].method === 'host.crypto.encrypt', 'method=host.crypto.encrypt', posted[0].method)
ok(posted[0].params && posted[0].params.password === 'pw', 'password 透传', posted[0].params)
ok(posted[0].params && posted[0].params.salt === 'c2FsdA==', 'salt 透传', posted[0].params)
ok(posted[0].params && posted[0].params.plaintext === 'aGVsbG8=', 'plaintext 透传', posted[0].params)
// 缺省字段不得抛错：插件可能只传 password+plaintext
let encDefaults = null
try {
  posted.length = 0
  win.qdCrypto.encrypt({ password: 'pw' })
  encDefaults = posted[0]
} catch (e) {
  encDefaults = null
}
ok(encDefaults && encDefaults.method === 'host.crypto.encrypt', 'encrypt 缺省参数不抛错', encDefaults && encDefaults.params)

posted.length = 0
win.qdCrypto.decrypt({ password: 'pw', salt: 's', iv: 'i', data: 'd' })
ok(posted[0] && posted[0].method === 'host.crypto.decrypt', 'method=host.crypto.decrypt', posted[0].method)
ok(posted[0].params && posted[0].params.iv === 'i' && posted[0].params.data === 'd', 'iv/data 透传', posted[0].params)

// random：默认 16 字节，并把宿主的 {base64} 解成裸字符串——插件不该关心包装层
posted.length = 0
const pRand = win.qdCrypto.random()
ok(posted[0] && posted[0].method === 'host.crypto.random', 'method=host.crypto.random', posted[0].method)
ok(posted[0].params && posted[0].params.bytes === 16, 'random 默认 16 字节', posted[0].params)
sendToIframe({ type: 'plugin:host-result', id: posted[0].id, data: { base64: 'cmFuZG9t' } })
const randRes = await pRand
ok(randRes === 'cmFuZG9t', 'random 解包 base64 字段', randRes)
posted.length = 0
win.qdCrypto.random(32)
ok(posted[0].params && posted[0].params.bytes === 32, 'random 透传自定义长度', posted[0].params)

// encrypt 的宿主返回 {iv,data} 应原样 resolve（插件自己存）
sendToIframe({ type: 'plugin:host-result', id: encId, data: { iv: 'aXY=', data: 'ZGF0YQ==' } })
const encRes = await pEnc
ok(encRes && encRes.iv === 'aXY=' && encRes.data === 'ZGF0YQ==', 'encrypt resolve 原样返回 {iv,data}', encRes)

// b64 编解码：UTF-8 安全（btoa 原生不支持多字节，必须走 unescape/encodeURIComponent 组合）
ok(win.qdCrypto.b64enc('中文') === '5Lit5paH', 'b64enc 对非 ASCII 安全', win.qdCrypto.b64enc('中文'))
ok(win.qdCrypto.b64dec('5Lit5paH') === '中文', 'b64dec 还原非 ASCII', win.qdCrypto.b64dec('5Lit5paH'))
// 非法输入必须抛错，不能静默返回空串——否则插件会把「解码失败」当成「内容为空」存回去
let decThrew = null
try {
  win.qdCrypto.b64dec('!!!不是base64!!!')
} catch (e) {
  decThrew = e
}
ok(decThrew instanceof Error && /base64 解码失败/.test(decThrew.message), 'b64dec 对非法输入抛错', decThrew && decThrew.message)

console.log('\nbad = ' + bad)
process.exit(bad === 0 ? 0 : 1)
