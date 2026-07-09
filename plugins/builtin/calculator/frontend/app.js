// 通讯桥：通过 postMessage 调用插件后端
let nextId = 1
const pending = {}

function pluginExec(command, input) {
  return new Promise((resolve, reject) => {
    const id = nextId++
    pending[id] = { resolve, reject }
    window.parent.postMessage({ type: 'plugin:execute', id, command, input }, '*')
  })
}

window.addEventListener('message', (e) => {
  if (e.data.type === 'plugin:result' && pending[e.data.id]) {
    if (e.data.error) pending[e.data.id].reject(new Error(e.data.error))
    else pending[e.data.id].resolve(e.data.data)
    delete pending[e.data.id]
  }
})

// ---- 计算器逻辑 ----
const exprEl = document.getElementById('expr')
const resultEl = document.getElementById('result')
const statusEl = document.getElementById('status')
let expression = ''
let lastResult = ''

function press(val) {
  if (val === 'C') { expression = ''; lastResult = ''; render(); return }
  if (val === '⌫') { expression = expression.slice(0, -1); render(); return }
  if (val === '=') { evaluate(); return }
  if (val === 'π') { expression += '3.141593'; render(); return }
  if (val === 'e') { expression += '2.718282'; render(); return }
  expression += val
  render()
}

function render() {
  exprEl.textContent = expression || '0'
  resultEl.textContent = lastResult ? '= ' + lastResult : '= 0'
}

async function evaluate() {
  if (!expression.trim()) return
  statusEl.textContent = '计算中…'
  try {
    const raw = await pluginExec('eval', { expression: expression })
    let text = ''
    if (typeof raw === 'object' && raw !== null) text = raw.result || raw.error || JSON.stringify(raw)
    else text = String(raw)
    lastResult = text
    statusEl.textContent = text || ''
  } catch (e) {
    statusEl.textContent = '错误: ' + e.message
  }
  render()
}

// 键盘事件
document.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { evaluate(); return }
  if (e.key === 'Escape') { expression = ''; lastResult = ''; render(); return }
  if (e.key === 'Backspace') { expression = expression.slice(0, -1); render(); return }
  const map = { '*': '×', '/': '÷' }
  const v = map[e.key] || e.key
  if ('0123456789.+-×÷()'.includes(v)) press(v)
})

// 按钮点击
document.querySelectorAll('.btn').forEach(btn => {
  btn.addEventListener('click', () => press(btn.dataset.v))
})
