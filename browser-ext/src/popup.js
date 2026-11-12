/** popup：搜索条目并填充到当前页面。 */
const $ = (id) => document.getElementById(id)

/**
 * 列表有两个作用域：
 *   site  当前网页关联的条目（默认）。不列别的网站的密码 —— 列了也没用，
 *         手滑点一下还会把密码填进完全不相干的站点。
 *   all   用户开始在搜索框打字时才取：搜索这个动作本身就是跨站找。
 */
let siteItems = []
let siteHost = ''
let allItems = []
let allLoaded = false
/** 请求序号：连续打字时只让最后一次的结果上屏。 */
let reqSeq = 0
let inputTimer = 0

function setStatus(text, ok) {
  $('statusText').textContent = text
  $('statusDot').classList.toggle('ok', !!ok)
}

function showEmpty(lines) {
  const box = $('list')
  box.replaceChildren()
  const p = document.createElement('div')
  p.className = 'empty'
  lines.forEach((line, i) => {
    if (i) p.appendChild(document.createElement('br'))
    p.appendChild(document.createTextNode(line))
  })
  box.appendChild(p)
}

function renderList(items) {
  const box = $('list')
  box.replaceChildren()
  for (const it of items) {
    const btn = document.createElement('button')
    btn.className = 'item'
    btn.type = 'button'
    const name = document.createElement('span')
    name.className = 'name'
    name.textContent = it.name || it.host || '未命名'
    const sub = document.createElement('span')
    sub.className = 'sub'
    sub.textContent = it.username ? `${it.username} · ${it.host}` : it.host
    btn.append(name, sub)
    btn.addEventListener('click', () => void fill(it))
    box.appendChild(btn)
  }
}

function filterItems(items, q) {
  const s = q.trim().toLowerCase()
  if (!s) return items
  return items.filter((it) =>
    [it.name, it.username, it.host].some((v) => (v || '').toLowerCase().includes(s)))
}

async function fill(item) {
  setStatus('正在填充…', true)
  try {
    const res = await chrome.runtime.sendMessage({ type: 'SET_CREDENTIAL', credential: item })
    if (res?.ok) {
      setStatus('已填充', true)
      setTimeout(() => window.close(), 300)
    } else {
      setStatus(res?.error || '填充失败', false)
    }
  } catch (e) {
    setStatus(e.message || '填充失败', false)
  }
}

/**
 * 自检当前页有没有注入 content script。
 *
 * 扩展 reload 之后，已经打开的标签页不会重新注入 —— 这是「改了没生效」
 * 最常见的原因，而界面上完全看不出来。这里直接给结论，省掉一轮猜。
 */
async function checkInjection() {
  const el = $('diag')
  el.className = 'diag'
  try {
    const res = await chrome.runtime.sendMessage({ type: 'PING_TAB' })
    if (!res?.ok) {
      el.classList.add('warn')
      // blocked 的 error 本身就是一句完整的人话（"当前是浏览器内部页面…"），
      // 再套一层「无法检查当前页面：」读着像绕口令。
      el.textContent = res?.blocked ? res.error : `无法检查当前页面：${res?.error || '未知原因'}`
      return
    }
    if (!res.injected) {
      el.classList.add('warn')
      el.textContent = '当前页面还没启用（扩展重载后的旧标签页常见）：刷新一次这个页面即可。'
      return
    }
    const v = res.versions.join(' / ')
    el.textContent = res.injectedFrames > 1
      ? `页面已启用 QuickDock 填充 v${v}（${res.injectedFrames} 个框架）`
      : `页面已启用 QuickDock 填充 v${v}`
  } catch (e) {
    el.classList.add('warn')
    el.textContent = `无法检查当前页面：${e.message || e}`
  }
}

/** 无关键字状态：只显示关联本页的条目。 */
function paintSite() {
  if (siteItems.length) {
    renderList(siteItems)
    $('count').textContent = `${siteItems.length} 条 · 关联本页`
    return
  }
  if (siteHost) {
    showEmpty([`${siteHost} 还没有保存的密码。`, '输入关键词可跨站搜索整个库。'])
  } else {
    showEmpty(['当前不是普通网页（浏览器内部页）。', '输入关键词可搜索整个库。'])
  }
  $('count').textContent = '0 条'
}

/** 有关键字：在全库里过滤。 */
function paintAll(q) {
  const hit = filterItems(allItems, q)
  if (!hit.length) {
    showEmpty([`全库里没有匹配「${q}」的条目`])
    $('count').textContent = '0 条'
    return
  }
  renderList(hit)
  $('count').textContent = `${hit.length} 条 · 全库`
}

/** 第一次搜索时才拉全库，之后都走本地过滤。 */
async function loadAllThenPaint(q) {
  const seq = ++reqSeq
  try {
    const res = await chrome.runtime.sendMessage({ type: 'LIST', scope: 'all' })
    if (seq !== reqSeq) return
    if (!res?.ok) throw new Error(res?.error || '连接失败')
    allItems = res.items || []
    allLoaded = true
    paintAll($('search').value.trim() || q)
  } catch (e) {
    if (seq !== reqSeq) return
    setStatus(e.message || '连接失败', false)
  }
}

async function init() {
  void checkInjection()
  try {
    const res = await chrome.runtime.sendMessage({ type: 'LIST' })
    if (!res?.ok) throw new Error(res?.error || '连接失败')
    siteItems = res.items || []
    siteHost = res.host || ''
    // 浏览器内部页没有关联站点，background 直接给了全库，别再多请求一次。
    if (res.scope === 'all' && !siteHost) {
      allItems = siteItems
      allLoaded = true
    }
    setStatus('已连接 QuickDock', true)
    paintSite()
  } catch (e) {
    setStatus(e.message || '连接失败', false)
    showEmpty([
      '连不上 QuickDock。',
      '请先打开应用并开启「浏览器集成」，再到「设置 / 密钥」里保存连接密钥。',
    ])
  }
}

$('search').addEventListener('input', () => {
  const q = $('search').value.trim()
  if (!q) {
    // 清空搜索：回到「关联本页」，用已缓存的列表，不再打接口。
    reqSeq++
    clearTimeout(inputTimer)
    paintSite()
    return
  }
  if (allLoaded) {
    paintAll(q)
    return
  }
  // 全库还没拉过：防抖 120ms，避免一个字一次请求。
  clearTimeout(inputTimer)
  inputTimer = setTimeout(() => void loadAllThenPaint(q), 120)
})

$('openOptions').addEventListener('click', () => chrome.runtime.openOptionsPage())

void init()
