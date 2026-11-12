/**
 * content script：页面里唯一干活的部分。
 *
 * 触发策略刻意用 `focusin` 而不是「页面加载就扫一遍」：
 *   - 现代登录页大量是动态渲染 + 分步登录（先用户名、下一页密码），
 *     一次性扫描必然漏；
 *   - 用户要填密码，必然先点进那个输入框 —— 这一个事件就是最准的时机，
 *     零轮询、零开销。
 *
 * 样式全部封在 shadow root 里：登录页的 CSS 千奇百怪，
 * 直接插 DOM 会被站点的 `input { ... }`、`div { ... }` 规则改得面目全非。
 *
 * 唯一必须做对的技术点（做错就是「看着填了但登录失败」）：
 * React / Vue 的受控输入框不认 `el.value = x`，必须用原型上的原生 setter
 * 再派发 input/change 事件。见 setNativeValue。
 */

(() => {
  /**
   * 只在顶层文档干活，iframe 里直接退出。
   *
   * 之前是 `all_frames: true`：每个页面的每个 iframe 都跑一份 ——
   * 各自注册 5 个 capture 监听、各自可能挂 DOM 宿主、各自读一次
   * storage。页面里 iframe 一多（广告位、客服、支付/表单组件），这些
   * 开销是叠加的，而且自适应高度的 iframe 里多一个节点就可能让父页面
   * 跟着重排。登录表单几乎都在主文档里，iframe 内的收益远小于代价。
   */
  if (window.top !== window.self) return

  // 同一页面可能被注入两次（扩展重载、babel 转译等），加个标记兜底。
  if (window.__qdVaultContentLoaded) return
  window.__qdVaultContentLoaded = true

  /**
   * 版本号只挂在 window 上，**绝不碰 DOM**。
   *
   * 早期版本写 `document.documentElement.dataset.qdVault`：那是一个
   * 每页都执行、且写在 <html> 上的属性变更。Next.js / Nuxt 这类框架
   * 会监听 documentElement 的属性来做水合与主题同步，一次属性写入就能
   * 触发整棵树重渲染 —— 用户看到的就是「页面闪一下，跟刷新一样」。
   *
   * content script 和 chrome.scripting.executeScript 共享同一个
   * isolated world，所以 window 上的字段照样能被 popup 的自检读到。
   *
   * 版本号不写死：manifest 的 version 由打包时按 git tag 注入，
   * 这里跟着读，省掉「改版本要动两处、漏一处就对不上」的麻烦。
   * 直接加载源码目录时是 0.0.0 —— 看到 0.0.0 就说明当前是未打包的目录。
   */
  const VERSION = (() => {
    try {
      return chrome.runtime.getManifest().version
    } catch {
      return '0.0.0'
    }
  })()
  window.__qdVaultVersion = VERSION

  const HOST_TAG = 'qd-vault-ui'
  const HOST_ID = '__qd-vault-root'
  const SAVE_HOST_ID = '__qd-vault-save'

  /**
   * 挂载层样式：**0×0 的 fixed 锚点**，不是铺满视口的整层。
   *
   * 这一处踩过两次坑，两次都表现为「页面闪」：
   *  1. 早期把容器当普通 div 塞进 <html>，它成了 html 的最后一个子元素，
   *     在 `html { display: flex }` / 依赖 `:last-child` 的站点上多占一格，
   *     页面高度抖动，表现为整页一直闪；
   *  2. 改成 `inset: 0` 的全屏 fixed 层之后，布局是没了，但铺满视口的
   *     超大合成层会让浏览器在它出现的一帧重绘整个视口 —— 依旧是闪。
   *
   * 现在宿主只占 (0,0) 一个点，内部元素各自 `position: fixed` 相对视口
   * 定位（宿主在原点，所以拿视口坐标直接算就行）。既没有布局影响，
   * 也没有整页合成层。
   */
  const LAYER_STYLE = [
    'all: initial',
    'display: block',
    'position: fixed',
    'top: 0',
    'left: 0',
    'width: 0',
    'height: 0',
    'overflow: visible',
    'z-index: 2147483647',
    // 容器本身不拦点击；真正可点的 .bubble / .card 自己声明 pointer-events: auto。
    'pointer-events: none',
  ].join('; ')

  function mountLayer(host) {
    host.style.cssText = LAYER_STYLE
    // 两处讲究：
    //  - 挂 documentElement 而不是 body。body 的子元素会被
    //    `body > div:last-child` / `:nth-child(n)` 这类选择器选中，
    //    多一个节点当场错位，布局瞬变又瞬回 —— 也是闪。
    //  - 用自定义标签 <qd-vault-ui> 而不是 div。未注册的自定义元素照样
    //    能挂 shadow root，但不会命中任何 `div:...` 规则，进一步压小误伤面。
    document.documentElement.appendChild(host)
  }
  /** 刚填过的密码框：避免每次 focus 都重新弹建议条。 */
  const filledFields = new WeakSet()
  /** 最近一次输入过的密码（按表单元素记），提交时用来判断要不要提示保存。 */
  const lastTyped = new WeakMap()

  let bubbleHost = null
  let bubbleRoot = null
  /** 气泡是否正亮着。宿主节点常驻，用它区分「建好了」和「看得见」。 */
  let bubbleVisible = false
  let anchorField = null
  let suggestionItems = []
  let hideTimer = null
  /** scroll 合帧用，0 表示当前没有待执行的帧。 */
  let posRaf = 0

  // ---------------- 工具 ----------------

  function isVisible(el) {
    if (!el || el.disabled || el.readOnly) return false
    const r = el.getBoundingClientRect()
    if (r.width < 8 || r.height < 8) return false
    const s = getComputedStyle(el)
    return s.display !== 'none' && s.visibility !== 'hidden' && s.opacity !== '0'
  }

  /**
   * 设置输入框的值并触发框架能感知的事件。
   *
   * 顺序有讲究：先清 React 的 valueTracker，再写值。
   * 否则 tracker 记着旧值，React 的 onChange 会因为「值没变」被跳过，
   * 页面上的输入框看着有新内容、组件 state 仍是空 —— 提交的是空密码。
   */
  function setNativeValue(el, value) {
    const proto = el instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype
    const desc = Object.getOwnPropertyDescriptor(proto, 'value')
    if (el._valueTracker) {
      try { el._valueTracker.setValue('') } catch { /* 非 React 页面没有这个字段 */ }
    }
    if (desc && desc.set) desc.set.call(el, value)
    else el.value = value
    el.dispatchEvent(new Event('input', { bubbles: true }))
    el.dispatchEvent(new Event('change', { bubbles: true }))
  }

  /**
   * 递归收集根节点（穿透 shadow DOM）。
   *
   * 只递归两层且每次用一次 querySelectorAll('*') 筛 shadowRoot：
   * 逐元素递归查询在长页面上会卡死主线程，而登录表单所在的 shadow 深度
   * 实际不超过两层（Salesforce / 部分银行站点）。
   */
  function collectRoots(root, depth = 0, out = []) {
    out.push(root)
    if (depth >= 2) return out
    let all
    try {
      all = root.querySelectorAll('*')
    } catch {
      return out
    }
    for (const el of all) {
      if (el.shadowRoot) collectRoots(el.shadowRoot, depth + 1, out)
    }
    return out
  }

  /**
   * 找页面里可见的密码框（跳过隐藏诱饵框）。
   *
   * 快慢两条路：主文档一次选择器就能命中绝大多数登录页，只有主文档里
   * 一个都没有时才去穿透 shadow DOM。原来无条件走 collectRoots，
   * 在长页面上（每次点击/回车都要调）就是一次 `querySelectorAll('*')`
   * 的同步遍历，主线程停一下 —— 用户看到的可能是「页面顿了一下」。
   */
  function collectFields(root, out) {
    let list
    try {
      list = root.querySelectorAll('input[type="password"]')
    } catch {
      return out
    }
    for (const el of list) {
      // 注册页 / 改密页的字段：不能自动填，否则会把用户的旧密码写进新密码框。
      if (el.getAttribute('autocomplete') === 'new-password') continue
      if (!isVisible(el)) continue
      out.push(el)
    }
    return out
  }

  function findPasswordFields() {
    const quick = collectFields(document, [])
    if (quick.length) return quick
    const out = []
    for (const root of collectRoots(document)) {
      if (root === document) continue
      collectFields(root, out)
    }
    return out
  }

  /**
   * 找密码框配套的用户名框。
   *
   * 三条规则依次兜底，覆盖绝大多数站点：
   *  1. autocomplete=username/email（最可靠，现代站点基本都有）
   *  2. 同一个 form 里、位于密码框之前的最后一个可见文本输入框
   *  3. 密码框前一个兄弟节点里的文本输入框（有些站点不用 form）
   */
  function findUsernameField(pw) {
    const scope = pw.form || pw.closest('form') || pw.parentElement
    if (scope) {
      const byAuto = scope.querySelector(
        'input[autocomplete="username"], input[autocomplete="email"], input[autocomplete="tel"]')
      if (isVisible(byAuto)) return byAuto

      const cands = Array.from(
        scope.querySelectorAll('input[type="text"], input[type="email"], input[type="tel"], input:not([type])'),
      ).filter(isVisible)
      const before = cands.filter((el) =>
        el !== pw && (el.compareDocumentPosition(pw) & Node.DOCUMENT_POSITION_FOLLOWING))
      if (before.length) return before[before.length - 1]
    }

    // 兜底：沿 DOM 往上三层找同级文本输入框。
    let node = pw
    for (let i = 0; i < 3 && node; i++) {
      node = node.parentElement
      if (!node) break
      const inputs = Array.from(node.querySelectorAll('input[type="text"], input[type="email"], input:not([type])'))
        .filter((el) => isVisible(el) && el !== pw)
      if (inputs.length) return inputs[inputs.length - 1]
    }
    return null
  }

  /** 当前页面的主机名（去掉 www.，与后端 normalizeHost 的规则保持一致）。 */
  function currentHost() {
    return location.hostname.replace(/^www\./, '')
  }

  // ---------------- 建议条 ----------------

  const BUBBLE_CSS = `
    :host { pointer-events: none; }
    .bubble {
      /* fixed 而不是 absolute：宿主是 0×0 的锚点（见 LAYER_STYLE），
         absolute 会相对宿主那个零尺寸盒子算坐标。fixed 相对视口，
         宿主在原点，所以下面填的 left/top 就是视口坐标。 */
      position: fixed;
      pointer-events: auto;
      z-index: 2147483647;
      max-width: 360px;
      padding: 6px;
      background: #1e232d;
      border: 1px solid #2f3745;
      border-radius: 8px;
      box-shadow: 0 12px 40px rgba(0, 0, 0, 0.45);
      font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
      font-size: 13px;
      color: #e6eaf2;
      user-select: none;
    }
    .head {
      display: flex;
      align-items: center;
      gap: 6px;
      padding: 2px 6px 6px;
      font-size: 11px;
      color: #8b95a8;
    }
    .dot { width: 6px; height: 6px; border-radius: 50%; background: #4a9eff; }
    .item {
      display: flex;
      flex-direction: column;
      width: 100%;
      padding: 6px 8px;
      background: none;
      border: none;
      border-radius: 5px;
      color: inherit;
      font: inherit;
      text-align: left;
      cursor: pointer;
    }
    .item:hover, .item:focus { background: #2a3140; outline: none; }
    .name { font-size: 13px; }
    .user { font-size: 11px; color: #8b95a8; }
    .empty { padding: 6px 8px; font-size: 12px; color: #6f7a8c; }
  `

  /**
   * 建（或复用）建议条。返回 shadow root ——
   * closed 模式下 `host.shadowRoot` 恒为 null，早期代码靠它取 `.bubble`
   * 会直接抛 TypeError，建议条一次都没显示成功过。
   *
   * 建好之后**常驻**，隐藏只切 display、不摘节点。理由：每个页面里
   * 一次 body/documentElement 的子节点增删，都会被站点的 MutationObserver
   * 当成内容变化重新水合一遍，弹一次、收一次就是两轮重绘。
   * 常驻之后整页生命周期内只动一次 DOM。
   */
  function ensureBubble() {
    if (bubbleHost && bubbleHost.isConnected && bubbleRoot) {
      bubbleVisible = true
      bubbleHost.style.display = 'block'
      attachPositionListeners()
      return bubbleRoot
    }
    bubbleHost = document.createElement(HOST_TAG)
    bubbleHost.id = HOST_ID
    bubbleRoot = bubbleHost.attachShadow({ mode: 'open' })
    const style = document.createElement('style')
    style.textContent = BUBBLE_CSS
    bubbleRoot.appendChild(style)
    const bubble = document.createElement('div')
    bubble.className = 'bubble'
    bubbleRoot.appendChild(bubble)
    mountLayer(bubbleHost)
    bubbleVisible = true
    attachPositionListeners()
    return bubbleRoot
  }

  function positionBubble() {
    if (!bubbleVisible || !bubbleHost || !anchorField || !anchorField.isConnected) {
      hideBubble()
      return
    }
    const bubble = bubbleRoot?.querySelector('.bubble')
    if (!bubble) return
    const r = anchorField.getBoundingClientRect()
    // 输入框滚出视口就收起，避免浮在无关内容上。
    if (r.bottom < 0 || r.top > window.innerHeight) {
      hideBubble()
      return
    }
    bubble.style.left = `${Math.max(8, Math.min(r.left, window.innerWidth - 360))}px`
    bubble.style.top = `${Math.min(r.bottom + 6, window.innerHeight - 40)}px`
    bubble.style.minWidth = `${Math.max(220, Math.min(r.width, 340))}px`
  }

  /**
   * scroll / resize 用 rAF 合帧。
   *
   * 页面滚动时 scroll 事件一秒能来几十上百次，每次都改样式会让浏览器
   * 反复重排，配合阴影很容易看出「闪」。合帧后一帧最多算一次。
   *
   * 监听器只在气泡可见期间挂着：气泡收起来之后还给页面一个干净的
   * 事件列表，滚动热路径上不该有别人的 capture 监听。
   */
  function queuePosition() {
    if (posRaf) return
    posRaf = requestAnimationFrame(() => {
      posRaf = 0
      positionBubble()
    })
  }

  let positionListening = false
  function attachPositionListeners() {
    if (positionListening) return
    positionListening = true
    window.addEventListener('scroll', queuePosition, true)
    window.addEventListener('resize', queuePosition)
  }
  function detachPositionListeners() {
    if (!positionListening) return
    positionListening = false
    window.removeEventListener('scroll', queuePosition, true)
    window.removeEventListener('resize', queuePosition)
    if (posRaf) {
      cancelAnimationFrame(posRaf)
      posRaf = 0
    }
  }

  function renderBubble(items, errorText) {
    const root = ensureBubble()
    const bubble = root.querySelector('.bubble')
    if (!bubble) return
    bubble.replaceChildren()

    const head = document.createElement('div')
    head.className = 'head'
    const dot = document.createElement('span')
    dot.className = 'dot'
    head.append(dot, document.createTextNode('QuickDock 密码库'))
    bubble.appendChild(head)

    if (errorText) {
      const p = document.createElement('div')
      p.className = 'empty'
      p.textContent = errorText
      bubble.appendChild(p)
    } else if (!items.length) {
      const p = document.createElement('div')
      p.className = 'empty'
      p.textContent = '没有这个网站的密码。登录提交后会问你一次要不要存。'
      bubble.appendChild(p)
    } else {
      for (const it of items) {
        const btn = document.createElement('button')
        btn.className = 'item'
        btn.type = 'button'
        const name = document.createElement('span')
        name.className = 'name'
        name.textContent = it.name || it.host || '未命名'
        const user = document.createElement('span')
        user.className = 'user'
        user.textContent = it.username || '（无用户名）'
        btn.append(name, user)
        btn.addEventListener('mousedown', (e) => {
          // mousedown 而不是 click：click 之前输入框会先 blur，
          // 部分站点在 blur 时收起表单，条就没了。
          e.preventDefault()
          e.stopPropagation()
          void fillFromItem(it)
        })
        bubble.appendChild(btn)
      }
    }
    bubble.style.display = 'block'
    positionBubble()
  }

  /**
   * 收起建议条：宿主常驻，只切 display 并清空内容。
   *
   * 早期是 remove() 摘节点。摘了再挂等于同一个位置反复增删子节点：
   * 站点的 MutationObserver 每次都当成内容变化重算一遍，弹一次收一次
   * 就是两轮重绘 —— 看起来就是页面闪。宿主留在这里但 width/height 都是 0、
   * 又不参与布局，对页面零影响。
   */
  function hideBubble() {
    clearTimeout(hideTimer)
    anchorField = null
    detachPositionListeners()
    if (!bubbleHost) return
    bubbleVisible = false
    bubbleHost.style.display = 'none'
    // 顺手清内容：下次显示直接是干净的一屏，不会先闪一下上一次的条目。
    bubbleRoot?.querySelector('.bubble')?.replaceChildren()
  }

  function scheduleHide() {
    clearTimeout(hideTimer)
    hideTimer = setTimeout(hideBubble, 180)
  }

  async function fillFromItem(item) {
    const pw = anchorField
    if (!pw || !pw.isConnected) return
    try {
      const res = await chrome.runtime.sendMessage({ type: 'REVEAL', id: item.id })
      if (!res?.ok) throw new Error(res?.error || '读取失败')
      const userField = findUsernameField(pw)
      if (userField && res.username) setNativeValue(userField, res.username)
      setNativeValue(pw, res.secret)
      filledFields.add(pw)
      lastTyped.set(pw, { username: res.username || '', secret: res.secret })
      hideBubble()
      pw.focus()
      // 有些站点的「登录」按钮在没有 input 事件时保持 disabled，
      // 这里再补一次 keyup 让它们解禁。
      pw.dispatchEvent(new KeyboardEvent('keyup', { bubbles: true, key: 'Unidentified' }))
    } catch (e) {
      try {
        renderBubble([], e.message || '读取密码失败')
      } catch {
        hideBubble()
      }
    }
  }

  /** 上一次展示的框与结果指纹：用来跳过重复渲染（重复替换 = 肉眼可见的闪）。 */
  let shownField = null
  let shownKey = ''
  let shownAt = 0

  function itemsKey(items) {
    return items.map((i) => `${i.id}:${i.username}`).join('|')
  }

  async function showSuggestions(pw) {
    anchorField = pw
    // 同一个框在极短时间内又 focus（页面自动 focus、脚本抢焦点）：
    // 只纠正位置，绝不重建 DOM。
    if (pw === shownField && bubbleVisible && Date.now() - shownAt < 2000) {
      queuePosition()
      return
    }
    // 已经亮着就直接换内容，别插一段「正在读取…」的中间态。
    const reused = bubbleVisible && !!bubbleHost
    // 「正在读取…」延迟 150ms 才出：本地回环通常几十毫秒就回来了，
    // 先占位再替换等于每个登录页都白闪一帧。真连不上时才由它兜底。
    let placeholder = 0
    if (!reused) {
      placeholder = setTimeout(() => {
        if (anchorField !== pw) return
        try {
          renderBubble([], '正在读取…')
        } catch {
          // 面板还没挂上就先不显示，不影响后面的 MATCH 结果渲染。
        }
      }, 150)
    }
    try {
      const res = await chrome.runtime.sendMessage({ type: 'MATCH', host: currentHost() })
      clearTimeout(placeholder)
      if (!res?.ok) throw new Error(res?.error || '连接 QuickDock 失败')
      suggestionItems = res.items || []
      if (anchorField !== pw) return // 期间用户切走了
      const key = itemsKey(suggestionItems)
      if (reused && key === shownKey) {
        shownField = pw
        shownAt = Date.now()
        queuePosition()
        return
      }
      renderBubble(suggestionItems)
      shownField = pw
      shownKey = key
      shownAt = Date.now()
    } catch (e) {
      clearTimeout(placeholder)
      if (anchorField !== pw) return
      // catch 里再渲染一次，但不能再让异常逃出去：
      // 展示层的错误绝不能升级成 unhandled rejection，脚本还要继续处理下一次输入。
      try {
        renderBubble([], e.message || '连接 QuickDock 失败')
      } catch {
        hideBubble()
      }
      shownField = pw
      shownKey = ''
      shownAt = Date.now()
    }
  }

  // ---------------- 保存提示 ----------------

  const SAVE_CSS = `
    :host { pointer-events: none; }
    .card {
      /* fixed 而不是 absolute：宿主是 0×0 的锚点（见 LAYER_STYLE），
         absolute 会相对宿主那个零尺寸盒子算坐标，right/bottom 会直接失效。
         fixed 相对视口，宿主有没有尺寸都不影响。 */
      position: fixed;
      pointer-events: auto;
      right: 20px;
      bottom: 20px;
      z-index: 2147483647;
      width: 300px;
      padding: 14px;
      background: #1e232d;
      border: 1px solid #2f3745;
      border-radius: 10px;
      box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
      font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
      color: #e6eaf2;
    }
    .title { font-size: 13px; font-weight: 600; margin-bottom: 4px; }
    .desc { font-size: 12px; line-height: 1.5; color: #8b95a8; margin-bottom: 12px; word-break: break-all; }
    .row { display: flex; gap: 8px; justify-content: flex-end; }
    button {
      height: 28px; padding: 0 12px; border-radius: 6px; border: none;
      font: inherit; font-size: 12px; cursor: pointer;
    }
    .ghost { background: #2a3140; color: #b7c0d0; }
    .ghost:hover { background: #333c4d; }
    .primary { background: #4a9eff; color: #08121f; font-weight: 600; }
    .primary:hover { background: #6bb0ff; }
    .check {
      display: flex; align-items: center; gap: 6px;
      margin-top: 10px; font-size: 11.5px; color: #8b95a8; cursor: pointer;
    }
    .check input { margin: 0; cursor: pointer; accent-color: #4a9eff; }
  `

  /**
   * 保存卡的宿主同样常驻。
   *
   * 早期每弹一次就 create + mount + remove 一套：登录成功后页面正在
   * 重新渲染，这时候往 documentElement 上增删节点，站点的
   * MutationObserver 会把它当成内容变化又水合一遍 —— 落在用户眼里
   * 就是「跳转后的新页面闪了一下，跟刷新一样」。
   */
  let saveHost = null
  let saveRoot = null
  /** 每换一张卡就递增：上一张的 20 秒定时器不能把新卡关掉。 */
  let saveToken = 0

  function ensureSaveLayer() {
    if (saveHost && saveHost.isConnected && saveRoot) return saveRoot
    saveHost = document.createElement(HOST_TAG)
    saveHost.id = SAVE_HOST_ID
    // open 而不是 closed：closed 下 host.shadowRoot 恒为 null，取节点只能
    // 靠额外变量记住根，早期正因为这点建议条一次都没渲染成功。
    saveRoot = saveHost.attachShadow({ mode: 'open' })
    const style = document.createElement('style')
    style.textContent = SAVE_CSS
    saveRoot.appendChild(style)
    mountLayer(saveHost)
    return saveRoot
  }

  /** 换上一张新卡（旧卡连内容一起清掉），返回递增后的令牌。 */
  function openSaveCard(build) {
    const root = ensureSaveLayer()
    for (const old of Array.from(root.querySelectorAll('.card'))) old.remove()
    root.appendChild(build())
    saveHost.style.display = 'block'
    return ++saveToken
  }

  function hideSavePrompt() {
    saveToken++
    if (!saveHost) return
    saveHost.style.display = 'none'
    saveRoot?.querySelectorAll('.card').forEach((el) => el.remove())
  }

  /** 自动保存成功后的轻提示：3 秒自己消失，不挡操作、不需要点。 */
  function showSavedToast(payload) {
    const my = openSaveCard(() => {
      const card = document.createElement('div')
      card.className = 'card'
      const title = document.createElement('div')
      title.className = 'title'
      title.textContent = '已存入 QuickDock 密码库'
      const desc = document.createElement('div')
      desc.className = 'desc'
      desc.style.marginBottom = '0'
      desc.textContent = `${payload.username || '（无用户名）'} · ${payload.host}`
      card.append(title, desc)
      return card
    })
    setTimeout(() => {
      if (saveToken === my) hideSavePrompt()
    }, 3000)
  }

  function showSavePrompt(verdict, payload) {
    if (!verdict || verdict.action === 'none' || verdict.action === 'same') return
    if (verdict.action === 'saved') {
      showSavedToast(payload)
      return
    }

    const isError = verdict.action === 'error'
    const isUpdate = verdict.action === 'update'

    const my = openSaveCard(() => {
      const card = document.createElement('div')
      card.className = 'card'

      const title = document.createElement('div')
      title.className = 'title'
      title.textContent = isError
        ? '暂时连不上密码库'
        : (isUpdate ? '更新 QuickDock 里的密码？' : '保存到 QuickDock 密码库？')

      const desc = document.createElement('div')
      desc.className = 'desc'
      desc.textContent = isError
        ? (verdict.message || '请确认 QuickDock 正在运行，且「浏览器集成」开关是开的')
        : `${payload.username || '（无用户名）'} · ${payload.host}`

      const row = document.createElement('div')
      row.className = 'row'

      if (isError) {
        const ok = document.createElement('button')
        ok.className = 'ghost'
        ok.textContent = '知道了'
        ok.addEventListener('click', hideSavePrompt)
        row.append(ok)
        card.append(title, desc, row)
        return card
      }

      const skip = document.createElement('button')
      skip.className = 'ghost'
      skip.textContent = '不用了'
      skip.addEventListener('click', () => {
        hideSavePrompt()
        // 告诉后台别再为这次提交弹第二次（按站点分桶，只清自己这一桶）。
        void chrome.runtime.sendMessage({ type: 'DISMISS', host: currentHost() }).catch(() => {})
      })

      // 顺手教一次：勾上以后就不再问，直接写库。
      const autoChk = document.createElement('input')
      autoChk.type = 'checkbox'
      const autoLabel = document.createElement('label')
      autoLabel.className = 'check'
      autoLabel.append(autoChk, document.createTextNode('以后新密码直接保存，不再询问'))

      const save = document.createElement('button')
      save.className = 'primary'
      save.textContent = isUpdate ? '更新' : '保存'
      save.addEventListener('click', async () => {
        save.disabled = true
        save.textContent = '保存中…'
        try {
          // 必须看返回值：background 出错时是 resolve 一个 {ok:false}，
          // 只 await 会当成成功、把卡关掉，用户以为存进去了。
          const res = isUpdate && verdict.id
            ? await chrome.runtime.sendMessage({
              type: 'UPDATE',
              id: verdict.id,
              payload: { ...payload, name: verdict.name },
            })
            : await chrome.runtime.sendMessage({ type: 'SAVE', payload })
          if (!res?.ok) throw new Error(res?.error || '保存失败')
          if (autoChk.checked) void chrome.storage.local.set({ qdAutoSave: true })
          hideSavePrompt()
        } catch (e) {
          desc.textContent = e.message || '保存失败'
          save.disabled = false
          save.textContent = isUpdate ? '更新' : '保存'
        }
      })

      row.append(skip, save)
      card.append(title, desc, row, autoLabel)
      return card
    })

    // 20 秒后自动消失：登录完用户常常先去点别的，太短就等于没提示。
    // 连接失败那种只给 8 秒，本来就是一次性提示。
    setTimeout(() => {
      if (saveToken === my) hideSavePrompt()
    }, isError ? 8000 : 20000)
  }

  // ---------------- 事件 ----------------

  let lastSubmitAt = 0

  /** 收集「这次提交了什么」，交给 background 判断要不要提示保存。 */
  async function collectAndMaybeSave(pw) {
    const typed = lastTyped.get(pw)
    const secret = typed?.secret || pw.value
    if (!secret) return
    // 300ms 内只处理一次：submit 与 Enter 常同时触发。
    const now = Date.now()
    if (now - lastSubmitAt < 300) return
    lastSubmitAt = now

    const host = currentHost()
    // 没有域名（about:blank iframe 等）就别提交：存进去之后再按站点匹配不回来，
    // 只会变成一条谁也找不到、还可能被误判成别人的记录。
    if (!host) return

    const userField = findUsernameField(pw)
    const payload = {
      host,
      url: location.href,
      username: typed?.username || userField?.value || '',
      secret,
    }
    try {
      const res = await chrome.runtime.sendMessage({ type: 'MAYBE_SAVE', payload })
      if (res?.ok) showSavePrompt(res.verdict, payload)
      else if (res?.error) showSavePrompt({ action: 'error', message: res.error }, payload)
    } catch {
      // 后台不可用时静默：保存提示是加分项，不该在登录流程里弹错误。
    }
  }

  /**
   * 把刚敲的密码提前交给后台存一份。
   *
   * 登录成功会让整页卸载，那一刻再发消息十有八九丢掉 ——
   * 用户的直观感受就是「登录完了，密码没进库」。所以每敲一次就留底
   * （600ms 防抖），提交失败/页面跳走都能由新页面接着问。
   */
  let cacheTimer = null
  function scheduleCache(pw) {
    clearTimeout(cacheTimer)
    cacheTimer = setTimeout(() => {
      const typed = lastTyped.get(pw)
      const secret = typed?.secret || pw.value
      if (!secret) return
      void chrome.runtime.sendMessage({
        type: 'CACHE_TYPED',
        payload: {
          host: currentHost(),
          url: location.href,
          username: typed?.username || findUsernameField(pw)?.value || '',
          secret,
        },
      }).catch(() => {})
    }, 600)
  }

  /**
   * 新页面加载后问一句：上一页有没有提交了但没确认的密码。
   *
   * 只在顶层帧问 —— content script 是 all_frames 注入的，
   * 每个 iframe 都问会在同一位置叠出好几张卡。
   *
   * `allowCrossSite` 是这里最关键的一道闸：没有它，用户登录 A 站之后
   * 30 秒内打开的**任何**网站都会去捞 A 站那条待保存记录，命中了就在
   * 右下角弹卡 —— 用户正在看新闻，页面却莫名多出一张卡再消失，观感
   * 就是「这页自己闪了一下」。现在只有「页面上真有密码框」才允许跨站捞，
   * 同站（登录页 → 首页那种）不受影响，补提示的初衷仍然成立。
   */
  async function checkPendingSave() {
    if (window.top !== window.self) return
    let hasPw = false
    try {
      // 这里只想要「大概是不是个登录页」，用一次廉价的选择器就够。
      // 别调 findPasswordFields()：它会穿透 shadow DOM 全量遍历元素，
      // 在长页面上是一次实打实的主线程停顿 —— 为了一个布尔值不值当，
      // 也正好避免用户看到「页面卡一下」。
      hasPw = !!document.querySelector('input[type="password"]')
    } catch {
      hasPw = false
    }
    try {
      const res = await chrome.runtime.sendMessage({
        type: 'GET_PENDING',
        host: currentHost(),
        allowCrossSite: hasPw,
      })
      if (res?.ok && res.verdict && res.payload) showSavePrompt(res.verdict, res.payload)
    } catch {
      // 后台还没醒或应用没开，静默。
    }
  }

  document.addEventListener('focusin', (e) => {
    const t = e.target
    if (!(t instanceof HTMLInputElement) || t.type !== 'password') return
    if (t.getAttribute('autocomplete') === 'new-password') return
    if (!isVisible(t)) return
    // 刚被我们填过的框不再弹：用户紧接着就要点登录。
    if (filledFields.has(t)) return
    clearTimeout(hideTimer)
    void showSuggestions(t)
  }, true)

  document.addEventListener('focusout', (e) => {
    const t = e.target
    if (t instanceof HTMLInputElement && t.type === 'password') scheduleHide()
  }, true)

  /** 记录用户手动打的密码：只有「用户自己输的」才值得提示保存。 */
  document.addEventListener('input', (e) => {
    const t = e.target
    if (!(t instanceof HTMLInputElement) || t.type !== 'password') return
    if (filledFields.has(t)) {
      // 填充后又改过，说明用户改的是新密码 → 当作手输，后面照样提示。
      filledFields.delete(t)
    }
    lastTyped.set(t, { username: '', secret: t.value })
    scheduleCache(t)
  }, true)

  document.addEventListener('submit', (e) => {
    const form = e.target
    if (!(form instanceof HTMLFormElement)) return
    const pw = form.querySelector('input[type="password"]')
    if (pw) void collectAndMaybeSave(pw)
  }, true)

  // 有些 SPA 不用 submit，直接拦 Enter 发 XHR。
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return
    const t = e.target
    if (t instanceof HTMLInputElement && t.type === 'password') {
      void collectAndMaybeSave(t)
      return
    }
    // 焦点常在用户名框或「登录」按钮上：这时 Enter 同样是提交，
    // 只认密码框本身会漏掉一半的登录页。
    if (t instanceof HTMLInputElement || t instanceof HTMLButtonElement) {
      const pw = findPasswordFields()[0]
      if (pw && pw.value) void collectAndMaybeSave(pw)
    }
  }, true)

  /**
   * 登录按钮的特征文本。
   *
   * 大量现代登录页（尤其管理后台）既没有 form 也不认 Enter：
   * 按钮就是个 `<button>` 绑了 click → fetch。只监听 submit 必然漏，
   * 所以再按「按钮长得像登录按钮」兜一层。
   */
  const SUBMIT_TEXT = /^(登\s*[录入陸]|登录|立即登录|登入|继续|下一步|submit|sign\s*in|log\s*in|logon|continue|next)$/i

  document.addEventListener('click', (e) => {
    const el = e.target instanceof Element ? e.target.closest('button, input, [role="button"]') : null
    if (!el) return
    // 最便宜的判断放最前面：页面上没有密码框就直接退出。
    // `innerText` 会强制同步布局，在长页面上每次点击都付这个代价，
    // 表现出来就是「点一下卡一下 / 闪一下」。
    const pw = document.querySelector('input[type="password"]')
    if (!pw || !pw.value) return
    if (pw.getAttribute('autocomplete') === 'new-password') return
    const isSubmitType = el.type === 'submit' || (el.tagName === 'INPUT' && el.type === 'submit')
    if (!isSubmitType) {
      const text = ((el.innerText || el.value || '') + '').trim()
      if (!SUBMIT_TEXT.test(text)) return
    }
    void collectAndMaybeSave(pw)
  }, true)

  // scroll / resize 的监听器由 attachPositionListeners 按需挂（见 ensureBubble），
  // 不在这里常驻：页面滚动是热路径，气泡没亮的时候不该有别人的 capture 监听。

  // 登录成功后页面整块换掉，保存提示只能由新页面补发。等它渲染完再弹，
  // 否则卡会被后续的 DOM 重绘盖住（或直接跟着老文档一起没了）。
  setTimeout(() => void checkPendingSave(), 1200)

  // popup 点「填充」时走这里：填当前页第一个可见密码框。
  chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
    // PING：popup 用来确认「这个页面注入成功了吗」。
    // 扩展 reload 后旧标签页没有 content script，此时填充/保存都不会有任何反应，
    // 而用户在界面上看不出区别 —— 这条就是给那种情况一个明确答案。
    if (msg?.type === 'PING') {
      sendResponse({
        ok: true,
        version: VERSION,
        isTop: window.top === window.self,
        hasPasswordField: findPasswordFields().length > 0,
      })
      return true
    }
    if (msg?.type !== 'FILL') return
    const fields = findPasswordFields()
    const pw = fields[0]
    if (!pw) {
      sendResponse({ ok: false, error: '当前页没有找到密码输入框' })
      return true
    }
    anchorField = pw
    void fillFromItem({ id: msg.credential.id, name: msg.credential.name }).then(() => {
      sendResponse({ ok: true })
    })
    return true
  })
})()
