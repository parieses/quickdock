/**
 * options：端口探测 + 连接密钥校验 + 状态显示。
 *
 * 没有「配对码换 token」这一步了：QuickDock 里一个开关开着就长期有效，
 * 用户只需把「连接密钥」粘进来一次。校验方式是拿它请求一次 /vault/list ——
 * 401 说明 QuickDock 那边关了开关（或密钥过时），这正是用户需要知道的。
 */
const $ = (id) => document.getElementById(id)
const DEFAULT_PORT = 9240

function setStatus(text, ok) {
  $('statusText').textContent = text
  $('dot').classList.toggle('ok', !!ok)
}

function setMsg(text, kind) {
  $('msg').textContent = text || ''
  $('msg').className = `msg${kind ? ' ' + kind : ''}`
}

async function loadConfig() {
  const { qdPort = DEFAULT_PORT, qdToken = '', qdAutoSave = true } =
    await chrome.storage.local.get(['qdPort', 'qdToken', 'qdAutoSave'])
  $('port').value = qdPort
  $('key').value = qdToken
  $('autoSave').checked = !!qdAutoSave
  $('tokenState').textContent = qdToken ? '已保存连接密钥，网页里可以直接填充' : '还没有密钥'
  $('revoke').disabled = !qdToken
  await checkConnection(qdPort)
}

async function checkConnection(port) {
  setStatus('检查中…', false)
  try {
    const res = await fetch(`http://127.0.0.1:${port}/vault/status`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const data = await res.json()
    if (data.app !== 'quickdock') throw new Error('该端口不是 QuickDock')
    setStatus('已连上 QuickDock', true)
    $('portHint').textContent = data.enabled
      ? '本机已开启浏览器集成。把「连接密钥」粘到下面保存即可。'
      : '本机还没开启浏览器集成：到 QuickDock → 密码库 → 右上角打开「浏览器集成」。'
    return true
  } catch (e) {
    setStatus(`连不上（${e.message}）`, false)
    $('portHint').textContent = '请确认 QuickDock 正在运行、且已打开「浏览器集成」开关；端口可以点「自动探测」找一下。'
    return false
  }
}

async function probe() {
  $('probe').disabled = true
  $('probe').textContent = '探测中…'
  try {
    for (let p = DEFAULT_PORT; p < DEFAULT_PORT + 10; p++) {
      try {
        const res = await fetch(`http://127.0.0.1:${p}/vault/status`)
        if (!res.ok) continue
        const data = await res.json()
        if (data.app === 'quickdock') {
          $('port').value = p
          await chrome.storage.local.set({ qdPort: p })
          setMsg(`找到 QuickDock，端口 ${p}`, 'ok')
          await checkConnection(p)
          return
        }
      } catch {
        // 继续试下一个
      }
    }
    setMsg('9240~9249 都没有找到 QuickDock', 'err')
  } finally {
    $('probe').disabled = false
    $('probe').textContent = '自动探测'
  }
}

async function saveKey() {
  const port = Number($('port').value) || DEFAULT_PORT
  const key = $('key').value.trim()
  if (key.length < 16) {
    setMsg('密钥看起来不完整，到 QuickDock 里点「复制连接密钥」重新复制一次', 'err')
    return
  }
  $('save').disabled = true
  $('save').textContent = '校验中…'
  try {
    const res = await fetch(`http://127.0.0.1:${port}/vault/list`, {
      headers: { Authorization: `Bearer ${key}` },
    })
    const data = await res.json().catch(() => ({}))
    if (res.status === 401) throw new Error('密钥不对，或 QuickDock 里已经关掉了浏览器集成')
    if (!res.ok || data.code) throw new Error(data.msg || `HTTP ${res.status}`)
    await chrome.storage.local.set({ qdPort: port, qdToken: key })
    setMsg('已保存，现在可以在登录页直接填充了', 'ok')
    await loadConfig()
  } catch (e) {
    const msg = /failed to fetch/i.test(e.message || '')
      ? '连不上本地服务，请确认端口正确'
      : e.message || '保存失败'
    setMsg(msg, 'err')
  } finally {
    $('save').disabled = false
    $('save').textContent = '保存密钥'
  }
}

async function revoke() {
  await chrome.storage.local.remove('qdToken')
  setMsg('已在本机清除密钥（QuickDock 里的开关不受影响）', 'ok')
  await loadConfig()
}

$('probe').addEventListener('click', probe)
$('save').addEventListener('click', saveKey)
$('revoke').addEventListener('click', revoke)
$('autoSave').addEventListener('change', async (e) => {
  await chrome.storage.local.set({ qdAutoSave: !!e.target.checked })
  $('autoSaveMsg').textContent = e.target.checked
    ? '已开启：登录后直接写入密码库'
    : '已关闭：提交后会先问你一句'
  $('autoSaveMsg').className = 'msg ok'
})
$('port').addEventListener('change', async (e) => {
  const p = Number(e.target.value) || DEFAULT_PORT
  await chrome.storage.local.set({ qdPort: p })
  await checkConnection(p)
})

void loadConfig()
