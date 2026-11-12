/**
 * Service worker：扩展与 QuickDock 本地 API 之间的唯一通道。
 *
 * 为什么所有网络请求都放在这里，而不是 content script：
 *   - content script 跑在页面上下文里，跨域请求会受页面的同源策略约束，
 *     还会把 token 暴露给页面脚本；
 *   - MV3 的 service worker 属于扩展自身来源（chrome-extension://），
 *     fetch 127.0.0.1 既不受页面 CORS 限制，也不需要页面参与。
 * 结果就是：页面永远接触不到 token，只拿到「这一次」要填的值。
 *
 * 通信协议（chrome.runtime 消息）：
 *   PAGE_READY      content → bg   页面有登录表单，问要不要显示建议条
 *   MATCH           content → bg   按 host 取候选
 *   REVEAL          content → bg   取单条明文
 *   CACHE_TYPED     content → bg   用户刚敲进密码框，先存一份待提交候选
 *   MAYBE_SAVE      content → bg   刚提交了一个密码，判断要不要提示保存
 *   GET_PENDING     content → bg   新页面加载，问「上一页有没有没确认的提交」
 *   SAVE            content → bg   用户点了「保存」
 *   DISMISS         content → bg   用户点了「不用了」，别再弹
 *   TOUCH           content → bg   密码一致，只记一次使用
 *   SET_CREDENTIAL  popup   → bg   填充指定条目（转给当前 tab 的 content）
 */

/** 默认端口；QuickDock 侧被占用时会顺延，options 页可探测。 */
const DEFAULT_PORT = 9240
const DEFAULT_TIMEOUT_MS = 8000

/**
 * 待确认提交的缓存键。
 *
 * 为什么必须存在 background 而不是页面里：登录成功后整页会跳走，
 * content script 连同它的内存一起销毁 —— 用户根本来不及点「保存」。
 * 存在这里（storage.session，仅内存、浏览器关闭即失效），新页面加载时
 * 再把它捞回来补一次提示。这是「登录完没存进库」最主要的那条漏路。
 */
/** 候选有效期：超过它就不再打扰（用户可能只是中途去看了别的页面）。 */
const PENDING_TTL_MS = 3 * 60 * 1000

// ---- 配置读写 ----

async function getConfig() {
  const { qdPort = DEFAULT_PORT, qdToken = '' } = await chrome.storage.local.get(['qdPort', 'qdToken'])
  return { port: qdPort, token: qdToken, baseUrl: `http://127.0.0.1:${qdPort}` }
}

async function setConfig(patch) {
  await chrome.storage.local.set(patch)
}

/** 本地 API 请求。失败一律抛出可读错误，调用方决定怎么显示。 */
async function api(path, { method = 'GET', body } = {}) {
  const { baseUrl, token } = await getConfig()
  const controller = new AbortController()
  // QuickDock 没启动时 fetch 会挂到系统超时（可能几十秒），
  // 而用户此刻正等着登录 —— 8 秒足够本地回环往返了。
  const timer = setTimeout(() => controller.abort(), DEFAULT_TIMEOUT_MS)
  try {
    const res = await fetch(baseUrl + path, {
      method,
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
      signal: controller.signal,
    })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) {
      throw new Error(data.msg || `QuickDock 返回 ${res.status}`)
    }
    if (data.code && data.code !== 0) {
      throw new Error(data.msg || 'QuickDock 返回错误')
    }
    return data
  } catch (e) {
    if (e.name === 'AbortError') {
      throw new Error('连接 QuickDock 超时，请确认应用正在运行')
    }
    throw e
  } finally {
    clearTimeout(timer)
  }
}

/** 探测可用端口：QuickDock 被占用时会从 9240 顺延，最多试 10 个。 */
async function probePort() {
  for (let p = DEFAULT_PORT; p < DEFAULT_PORT + 10; p++) {
    try {
      const res = await fetch(`http://127.0.0.1:${p}/vault/status`, { method: 'GET' })
      if (res.ok) {
        const data = await res.json()
        if (data && data.app === 'quickdock') return p
      }
    } catch {
      // 这个端口没人监听，继续下一个
    }
  }
  return 0
}

// ---- 待确认提交 ----

/**
 * 取站点主域（近似 eTLD+1）。
 *
 * 登录页和登录后的首页经常不同子域（login.foo.com → app.foo.com），
 * 只比 host 会让跳转后的提示被判定成「不是一个站」而丢掉。
 * 真 eTLD+1 需要公共后缀表（几十 KB），这里用最小规则覆盖常见多段后缀。
 */
function siteOf(host) {
  const raw = String(host || '').toLowerCase().replace(/^www\./, '')
  if (!raw) return ''
  // IP 没有「主域」概念，只能整体当一个站点。
  // 不特判的话 192.168.1.10 会被切成 "1.10"，整个网段挤进同一个桶。
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(raw) || raw.includes(':')) return raw

  const parts = raw.split('.').filter(Boolean)
  if (parts.length <= 2) return parts.join('.')
  const second = parts[parts.length - 2]
  const multi = ['co', 'com', 'net', 'org', 'gov', 'edu', 'ac']
  const take = multi.includes(second) && parts[parts.length - 1].length === 2 ? 3 : 2
  return parts.slice(-take).join('.')
}

/**
 * 「刚提交、还没确认」的密码，按站点分桶。
 *
 * 早期是一个全局单键 qdPendingSave —— 同时登录两个网站时后写的覆盖前一个，
 * 表现为「第二个站保存后，第一个站的记录被顶掉」，甚至把 B 站的密码
 * 当成 A 站的新密码去比对。分桶后两个站互不干扰。
 */
const PENDING_PREFIX = 'qdPendingSave:'

function pendingKeyFor(host) {
  // 用 site 而不是完整 host：同站跳转（login.x.com → app.x.com）共用一个桶。
  return PENDING_PREFIX + siteOf(host || '')
}

async function readPending(host) {
  const key = pendingKeyFor(host)
  const stored = await chrome.storage.session.get(key)
  const p = stored?.[key]
  if (!p || !p.secret) return null
  if (Date.now() - (p.at || 0) > PENDING_TTL_MS) {
    await chrome.storage.session.remove(key)
    return null
  }
  return p
}

async function writePending(p) {
  if (!p?.host) return
  await chrome.storage.session.set({ [pendingKeyFor(p.host)]: p })
}

async function clearPending(host) {
  if (host) {
    await chrome.storage.session.remove(pendingKeyFor(host))
    return
  }
  // 没有 host 就清空所有桶（DISMISS 兜底的场景）。
  const all = await chrome.storage.session.get(null)
  const keys = Object.keys(all).filter((k) => k.startsWith(PENDING_PREFIX))
  if (keys.length) await chrome.storage.session.remove(keys)
}

/**
 * 最近 withinMs 内提交过、且还没确认的一条（跨站）。
 *
 * OAuth 登录会「A 站提交 → 回跳 B 站」，那条记录落在 A 站的桶里，
 * 只看当前站就漏了。这里只认时间窗口，窗口外一律不打扰。
 */
async function readRecentPending(withinMs) {
  const all = await chrome.storage.session.get(null)
  let best = null
  for (const [k, v] of Object.entries(all)) {
    if (!k.startsWith(PENDING_PREFIX) || !v?.secret) continue
    if (Date.now() - (v.at || 0) > PENDING_TTL_MS) {
      await chrome.storage.session.remove(k)
      continue
    }
    if (!best || (v.at || 0) > (best.at || 0)) best = v
  }
  if (!best) return null
  if (Date.now() - (best.at || 0) > withinMs) return null
  return best
}

// ---- 注入可行性判断 ----

/**
 * Chrome 不允许扩展注入的页面协议。
 *
 * 报错原文是 `Cannot access a chrome-extension:// URL of different extension`，
 * 触发场景其实很常见：打开一个 PDF（它跑在 Chrome 自带的 pdf 扩展里）、
 * 停在别的扩展的选项页、或者停在应用商店页。原样丢给用户看只会让人
 * 以为是密码库坏了。
 */
const NO_INJECT_SCHEME = /^(chrome|edge|brave|opera|vivaldi|about|devtools|view-source|chrome-extension|extension|moz-extension|safari-extension):/i

function isNoInjectUrl(url) {
  if (!url) return false // 拿不到地址时先放行，交给后面的错误兜底
  return NO_INJECT_SCHEME.test(url) || /^https:\/\/(chromewebstore\.google\.com|chrome\.google\.com\/webstore)/i.test(url)
}

function noInjectReason(url) {
  if (/^chrome-extension:/i.test(url)) {
    return '当前标签页是另一个扩展的页面（比如 PDF 阅读器），扩展不能往这里注入。换一个普通网页再用。'
  }
  if (/chromewebstore|chrome\.google\.com\/webstore/i.test(url)) {
    return 'Chrome 应用商店的页面不允许扩展注入。'
  }
  return '当前是浏览器内部页面（设置、扩展管理、新标签页等），扩展不能注入。换一个普通网页再用。'
}

/** 把 executeScript 的原生报错翻成一句人话。 */
function describeInjectError(e) {
  const msg = String(e?.message || e || '')
  if (/different extension/i.test(msg)) return '这个页面里嵌了其他扩展的内容，Chrome 不允许在这里执行脚本。'
  if (/extensions gallery/i.test(msg)) return 'Chrome 应用商店的页面不允许扩展注入。'
  if (/cannot be scripted|Cannot access contents/i.test(msg)) return '这个页面不允许扩展执行脚本（多半是浏览器内部页或商店页）。'
  return msg || '无法检查当前页面'
}

// ---- 业务 ----

/** 按 host 找候选。content 只知道域名，不知道用户存的是 example.com 还是 www.example.com，
 *  归一化交给后端（match.go 的 normalizeHost），这里原样传。 */
async function matchByHost(host) {
  const q = new URLSearchParams({ kind: 'login', host: host || '' })
  const data = await api(`/vault/match?${q.toString()}`)
  return data.items || []
}

/**
 * 当前标签页的 host。
 *
 * 拿不到就返回空串：`chrome://`、`about:blank`、扩展自己的页面都没有
 * 「关联站点」这个概念，调用方据此退回全库。
 */
async function activeTabHost() {
  try {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true })
    return new URL(tab?.url || '').hostname || ''
  } catch {
    return ''
  }
}

async function reveal(id) {
  const data = await api('/vault/reveal', { method: 'POST', body: { id } })
  return { username: data.username || '', secret: data.secret || '' }
}

/**
 * 判断刚提交的密码该不该提示保存。
 *
 * 三种结果：
 *   'same'    库里已有完全相同的一条 → 只 touch，不打扰
 *   'update'  同用户名但密码不同 → 提示「更新这条」
 *   'new'     没有 → 提示「保存」
 *
 * 为什么必须在这里比对（而不是让 content 自己决定）：比对要解密，
 * 而解密只能发生在拿到 token 的这一层。
 */
async function classifySubmission(payload) {
  const { host, username, secret } = payload
  if (!secret) return { action: 'none' }
  // 没有域名就无法判断这是哪个站的密码：既不能提示，更不能拿去覆盖某条。
  // （iframe / about:blank 里 location.hostname 可能是空串）
  if (!host) return { action: 'none' }

  const items = await matchByHost(host)
  if (!items.length) return { action: 'new' }

  // 先按用户名缩小范围；用户名也可能为空（分步登录时第一屏没填用户名）。
  const sameUser = username
    ? items.filter((it) => (it.username || '').toLowerCase() === username.toLowerCase())
    : items
  // 本站有记录、但没有一条是这个用户名 → 这是本站在存新账号，
  // 不能拿另一个账号的条目当「更新目标」，否则会把人家密码覆盖掉。
  if (username && !sameUser.length) return { action: 'new' }
  const pool = sameUser.length ? sameUser : items

  for (const it of pool) {
    if (!it.hasSecret) continue
    try {
      const full = await reveal(it.id)
      if (full.secret === secret) {
        await api('/vault/touch', { method: 'POST', body: { id: it.id } })
        return { action: 'same', id: it.id }
      }
    } catch {
      // 单条解密失败（密钥换了 / 数据坏了）不该打断整个判断，
      // 继续比下一条；全都失败时自然会走到 'new'。
    }
  }
  return { action: 'update', id: pool[0]?.id, name: pool[0]?.name }
}

async function saveEntry(payload) {
  const { host, url, username, secret, name } = payload
  // 没有 host 的条目存进去也没法再匹配出来，而且会污染按站点分桶的判定。
  if (!host) throw new Error('识别不到当前网站的域名，未保存')
  const data = await api('/vault/save', {
    method: 'POST',
    body: {
      id: '',
      name: name || host || username || '网页登录',
      kind: 'login',
      username: username || '',
      secret: secret || '',
      clearSecret: false,
      host: host || '',
      port: 0,
      url: url || '',
      notes: '',
      tags: '',
      source: 'browser',
      sourceId: host || '',
      favorite: false,
    },
  })
  return data
}

async function updateEntry(id, payload) {
  // payload.host 为空会把既有条目的域名清成空串 —— 那条从此再也匹配不到。
  if (!payload.host) throw new Error('识别不到当前网站的域名，未更新')
  await api('/vault/save', {
    method: 'POST',
    body: {
      id,
      name: payload.name || payload.host || '',
      kind: 'login',
      username: payload.username || '',
      secret: payload.secret || '',
      clearSecret: false,
      host: payload.host || '',
      port: 0,
      url: payload.url || '',
      notes: '',
      tags: '',
      source: 'browser',
      sourceId: payload.host || '',
      favorite: false,
    },
  })
}

// ---- 自动保存 ----

/**
 * 自动保存开关，默认开。
 *
 * 只对「新密码」生效：覆盖已有条目仍然要用户点头 ——
 * 登录失败时输错的那一次不覆盖掉正确密码，是这里唯一的硬约束。
 */
async function autoSaveEnabled() {
  const { qdAutoSave = true } = await chrome.storage.local.get('qdAutoSave')
  return !!qdAutoSave
}

/**
 * 按用户设置决定「要不要问」。
 *
 * 自动保存失败（应用没开、密钥失效）时退回手动确认 ——
 * 至少让用户看到那张卡，而不是静默以为存过了。
 */
async function applyVerdict(verdict, payload) {
  if (!verdict || verdict.action === 'none' || verdict.action === 'same') return verdict
  if (verdict.action !== 'new') return verdict
  if (!(await autoSaveEnabled())) return verdict
  try {
    await saveEntry(payload)
    return { action: 'saved' }
  } catch {
    return verdict
  }
}

// ---- 消息分发 ----

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  // 返回 true 表示异步 sendResponse，缺了它 Chrome 会立刻关掉通道、前端拿到 undefined。
  handleMessage(msg, sender).then(sendResponse).catch((e) => {
    sendResponse({ ok: false, error: e.message || String(e) })
  })
  return true
})

async function handleMessage(msg, sender) {
  switch (msg?.type) {
    case 'MATCH': {
      const items = await matchByHost(msg.host)
      return { ok: true, items }
    }
    case 'REVEAL': {
      const full = await reveal(msg.id)
      return { ok: true, ...full }
    }
    case 'MAYBE_SAVE': {
      const payload = msg.payload || {}
      const verdict = await applyVerdict(await classifySubmission(payload), payload)
      // 只有「不需要再问」才清缓存。需要保存时**故意不标记已提示**：
      // 这张卡 20 秒后自己消失，而用户可能同时已经跳走 —— 留着让新页面
      // 再问一次，比让密码悄悄丢掉强。
      if (!verdict || ['none', 'same', 'saved'].includes(verdict.action)) {
        await clearPending(payload.host)
      } else {
        // 用提交时的值刷新缓存（用户最后一秒改的那一位，防抖可能还没写进去）。
        await writePending({ ...payload, at: Date.now(), prompted: false })
      }
      return { ok: true, verdict }
    }
    // 用户每敲一下密码就缓存一份：提交瞬间页面可能就卸载了，
    // 那时的 sendMessage 不可靠，必须提前留底。
    case 'CACHE_TYPED': {
      const p = msg.payload || {}
      if (!p.secret || !p.host) return { ok: true }
      await writePending({ ...p, at: Date.now(), prompted: false })
      return { ok: true }
    }
    // 新页面加载：上一页有没有「提交了但没来得及确认」的密码。
    case 'GET_PENDING': {
      // 先看当前站自己的桶（登录页 → 首页这类同站跳转）。
      let p = await readPending(msg.host)
      // 跨站捞（OAuth：A 站提交 → 回跳 B 站）默认只给「页面上真有密码框」
      // 的页面开。否则用户登录完 A 站，接下来 30 秒里打开的每个网站都会
      // 去捞那条记录，命中了就弹卡 —— 明明没在登录的页面也会自己闪一下。
      if (!p && msg.allowCrossSite !== false) p = await readRecentPending(30 * 1000)
      if (!p || p.prompted) return { ok: true, verdict: null }
      const payload = { host: p.host, url: p.url, username: p.username, secret: p.secret }
      const verdict = await applyVerdict(await classifySubmission(p), payload)
      if (!verdict || ['none', 'same', 'saved'].includes(verdict.action)) {
        await clearPending(p.host)
        return { ok: true, verdict: null }
      }
      await writePending({ ...p, prompted: true })
      return { ok: true, verdict, payload }
    }
    case 'DISMISS': {
      await clearPending(msg.host)
      return { ok: true }
    }
    // popup 自检：这个标签页（含子框架）到底有没有注入 content script。
    // 扩展 reload 之后旧标签页不会重新注入，此时填充和保存都毫无反应，
    // 而用户从界面上完全看不出区别 —— 这条就是给那种情况一个明确答案。
    case 'PING_TAB': {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true })
      if (!tab?.id) return { ok: false, error: '找不到当前标签页' }
      const url = tab.url || tab.pendingUrl || ''
      if (isNoInjectUrl(url)) {
        return { ok: false, blocked: true, error: noInjectReason(url) }
      }
      // 读 window 上的字段而不是 <html> 的 data-* 属性：
      // content script 和 executeScript 共享同一个 isolated world。
      // 往 documentElement 写属性会惊动监听根节点变化的框架（整页重渲染，
      // 表现为页面闪一下），所以注入标记一律不落在 DOM 上。
      const probe = (allFrames) => chrome.scripting.executeScript({
        target: { tabId: tab.id, allFrames },
        func: () => window.__qdVaultVersion || '',
      })
      let frames
      try {
        frames = await probe(true)
      } catch {
        // 页面里如果嵌了「别的扩展」的 iframe（很多扩展会往页面塞自己的
        // frame），allFrames 会整批失败并抛 "different extension"。
        // 主框架通常是干净的，降级再试一次 —— 别因为一个 iframe
        // 就把结论说成「这个页面没注入」。
        try {
          frames = await probe(false)
        } catch (e) {
          return { ok: false, blocked: true, error: describeInjectError(e) }
        }
      }
      const versions = [...new Set(frames.map((f) => f.result).filter(Boolean))]
      const injectedFrames = frames.filter((f) => !!f.result).length
      return { ok: true, injected: versions.length > 0, versions, injectedFrames, frames: frames.length }
    }
    // popup 的列表。两种作用域：
    //   site（默认）只给当前网页关联的条目 —— 弹层里列一堆别的网站的密码，
    //       既没用又危险，手滑就填进错站。
    //   all  用户开始在搜索框里打字才用：搜索这个动作本身就是跨站找。
    case 'LIST': {
      const host = await activeTabHost()
      if (msg.scope === 'all' || !host) {
        // 没有 host 就没有「关联」可言（chrome:// 之类），只能给全库。
        const data = await api('/vault/list')
        return { ok: true, items: data.items || [], scope: 'all', host }
      }
      const items = await matchByHost(host)
      return { ok: true, items, scope: 'site', host }
    }
    case 'SAVE': {
      const saved = await saveEntry(msg.payload || {})
      // 只清自己这一桶：另一个网站还没确认的提交不能跟着一起没了。
      await clearPending(msg.payload?.host)
      return { ok: true, saved }
    }
    case 'UPDATE': {
      await updateEntry(msg.id, msg.payload || {})
      await clearPending(msg.payload?.host)
      return { ok: true }
    }
    case 'TOUCH': {
      await api('/vault/touch', { method: 'POST', body: { id: msg.id } })
      return { ok: true }
    }
    // popup 点「填充」：把值转给该 tab 的 content script。
    case 'SET_CREDENTIAL': {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true })
      if (!tab?.id) return { ok: false, error: '找不到当前标签页' }
      const res = await chrome.tabs.sendMessage(tab.id, { type: 'FILL', credential: msg.credential }).catch(() => null)
      return res || { ok: false, error: '页面未响应，尝试刷新后再试' }
    }
    default:
      return { ok: false, error: 'unknown message' }
  }
}

// ---- 首次安装：直接打开设置页，否则用户不知道要去哪配对 ----

chrome.runtime.onInstalled.addListener(async (details) => {
  if (details.reason === 'install') {
    chrome.runtime.openOptionsPage()
    return
  }
  // 升级：不打扰，只确保端口配置还在。
  const cfg = await getConfig()
  if (!cfg.port) await setConfig({ qdPort: DEFAULT_PORT })
})
