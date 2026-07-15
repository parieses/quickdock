/**
 * 变量命名转换 — Goja 后端
 * 自动检测输入命名风格，转换到所有主流命名约定
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.1.0' }
}

function handleExecute(params) {
  var input = params.input || {}
  var text = (input.text || '').trim()
  if (!text) return { error: '请输入要转换的变量名' }

  // 清理多余空格和引号
  text = text.replace(/^['"`]|['"`]$/g, '')
  if (!text) return { error: '请输入有效的变量名' }

  // ---- 分词器：将各种命名风格拆成单词数组 ----
  var words = tokenize(text)
  if (!words || words.length === 0) {
    return { error: '无法识别命名风格，请尝试：user_name, userName, UserName' }
  }

  // ---- 生成各种命名风格 ----
  var result = {
    'camelCase':    toCamel(words),
    'PascalCase':   toPascal(words),
    'snake_case':   toSnake(words, '_'),
    'kebab-case':   toKebab(words),
    'UPPER_CASE':   toUpper(words, '_'),
    'camel_Snake':  toCamelSnake(words),
    'Train-Case':   toTrain(words),
    'dot.case':     toDot(words)
  }

  // 高亮原始输入匹配的风格
  var detected = detectStyle(text)
  var lines = []
  lines.push('原始: ' + text + '  →  ' + detected)
  lines.push('')
  for (var name in result) {
    var marker = (detectStyle(result[name]) === getStyleLabel(name)) ? ' ←' : ''
    lines.push(name + ':  ' + result[name] + marker)
  }

  // 返回：text 复制到剪贴板（取 camelCase），display 显示在面板
  return {
    text: result['camelCase'],
    display: lines.join('\n')
  }
}

// ---- 分词 ----

function tokenize(str) {
  // snake_case / kebab-case / UPPER_CASE
  if (/[_-]/.test(str)) {
    return str.split(/[_-]+/).filter(function(w){ return w.length > 0 }).map(function(w){ return w.toLowerCase() })
  }
  // camelCase / PascalCase / Train-Case
  var parts = []
  var buf = ''
  for (var i = 0; i < str.length; i++) {
    var ch = str[i]
    if (ch >= 'A' && ch <= 'Z') {
      if (buf.length > 0) { parts.push(buf.toLowerCase()); buf = '' }
      // 连续大写处理（如 XMLParser → XML + Parser）
      var j = i
      while (j < str.length && str[j] >= 'A' && str[j] <= 'Z') j++
      if (j - i > 1 && j < str.length) {
        parts.push(str.substring(i, j - 1).toLowerCase())
        i = j - 2; continue
      }
      buf = ch
    } else if (ch === '-' || ch === '_') {
      if (buf.length > 0) { parts.push(buf.toLowerCase()); buf = '' }
    } else {
      buf += ch
    }
  }
  if (buf.length > 0) parts.push(buf.toLowerCase())
  return parts
}

function detectStyle(str) {
  if (/^[a-z]+_[a-z]+/.test(str)) return 'snake_case'
  if (/^[a-z]+-[a-z]+/.test(str)) return 'kebab-case'
  if (/^[A-Z]+_[A-Z]+/.test(str)) return 'UPPER_CASE'
  if (/^[a-z]+[A-Z]/.test(str)) return 'camelCase'
  if (/^[A-Z][a-z]+[A-Z]/.test(str)) return 'PascalCase'
  if (/^[A-Z][a-z]+-[A-Z]/.test(str)) return 'Train-Case'
  if (/[a-z]+_[A-Z]/.test(str)) return 'camel_Snake'
  if (/[a-z]+\.[a-z]+/.test(str)) return 'dot.case'
  return 'unknown'
}

function getStyleLabel(name) {
  var map = {
    'camelCase': 'camelCase',
    'PascalCase': 'PascalCase',
    'snake_case': 'snake_case',
    'kebab-case': 'kebab-case',
    'UPPER_CASE': 'UPPER_CASE',
    'camel_Snake': 'camel_Snake',
    'Train-Case': 'Train-Case',
    'dot.case': 'dot.case'
  }
  return map[name] || name
}

// ---- 生成器 ----

function toCamel(w) {
  var s = w[0].toLowerCase()
  for (var i = 1; i < w.length; i++) s += w[i].charAt(0).toUpperCase() + w[i].slice(1)
  return s
}

function toPascal(w) {
  var s = ''
  for (var i = 0; i < w.length; i++) s += w[i].charAt(0).toUpperCase() + w[i].slice(1)
  return s
}

function toSnake(w, sep) {
  return w.join(sep)
}

function toKebab(w) {
  return w.join('-')
}

function toUpper(w, sep) {
  var s = ''
  for (var i = 0; i < w.length; i++) {
    if (i > 0) s += sep
    s += w[i].toUpperCase()
  }
  return s
}

function toCamelSnake(w) {
  var s = w[0].toLowerCase()
  for (var i = 1; i < w.length; i++) s += '_' + w[i].charAt(0).toUpperCase() + w[i].slice(1)
  return s
}

function toTrain(w) {
  var s = ''
  for (var i = 0; i < w.length; i++) {
    if (i > 0) s += '-'
    s += w[i].charAt(0).toUpperCase() + w[i].slice(1)
  }
  return s
}

function toDot(w) {
  return w.join('.')
}
