/**
 * 代码压缩/美化 — Goja 后端
 * 支持 JS / CSS / HTML 的 minify（压缩）和 prettify（美化）
 * 自动检测语言类型
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var input = params.input || {}
  var text = (input.text || '').trim()
  if (!text) return { error: '请输入要处理的代码' }

  var command = params.command || ''
  var isMinify = command === 'code-minify'

  // 自动检测语言
  var lang = detectLanguage(text)
  var result

  if (isMinify) {
    result = minifyCode(text, lang)
  } else {
    result = prettifyCode(text, lang)
  }

  return {
    text: result,
    display: '// 语言: ' + lang + '  |  模式: ' + (isMinify ? '压缩' : '美化') + '\n' +
           '// 原大小: ' + text.length + ' 字符  |  处理后: ' + result.length + ' 字符' +
           (result.length < text.length ? '  (-' + (text.length - result.length) + ')' : '') +
           '\n────────────────────────────────────────\n' + result
  }
}

// ---- 语言检测 ----

function detectLanguage(code) {
  var t = code.trim()
  if (/^</.test(t) || /^<!DOCTYPE/i.test(t)) return 'HTML'
  if (/^[.#@]\w+\s*\{/.test(t) || /\{[\s\S]*:[^;]+;/.test(t)) return 'CSS'
  if (/^(function|var |let |const |import |export |class |async|await|=>|console\.|document\.)/.test(t)) return 'JavaScript'
  if (/^(def |class |import |from |print\()/.test(t)) return 'Python'
  return 'Unknown'
}

// ---- Minify ----

function minifyCode(code, lang) {
  if (lang === 'HTML') return minifyHTML(code)
  if (lang === 'CSS') return minifyCSS(code)
  return minifyJS(code)
}

// 字符串/模板字面量感知的注释剥离：仅在不处于字符串/模板内时才把 // 与 /* */ 当作注释。
function stripJSComments(code) {
  var out = ''
  var i = 0
  while (i < code.length) {
    var c = code[i]
    var n = code[i + 1]
    if (c === '`' ) {
      out += c; i++
      while (i < code.length) {
        out += code[i]
        if (code[i] === '\\') { out += code[i + 1] || ''; i += 2; continue }
        if (code[i] === '`') { i++; break }
        i++
      }
      continue
    }
    if (c === '"' || c === "'") {
      out += c; i++
      while (i < code.length) {
        out += code[i]
        if (code[i] === '\\') { out += code[i + 1] || ''; i += 2; continue }
        if (code[i] === c) { i++; break }
        i++
      }
      continue
    }
    if (c === '/' && n === '/') {
      while (i < code.length && code[i] !== '\n') i++
      continue
    }
    if (c === '/' && n === '*') {
      i += 2
      while (i < code.length && !(code[i] === '*' && code[i + 1] === '/')) i++
      i += 2
      continue
    }
    out += c
    i++
  }
  return out
}

function minifyJS(code) {
  // 1. 字符串感知地移除注释（不会误伤 "http://" 之类内容）
  code = stripJSComments(code)
  // 2. 折叠多余空白
  code = code.replace(/\s+/g, ' ')
  // 3. 去除空白围绕“安全”分隔符（不改变语义）
  code = code.replace(/\s*([{}();,:=])\s*/g, '$1')
  // 4. 去除空白围绕逻辑/位运算符
  code = code.replace(/\s*(&&|\|\||[&|])\s*/g, '$1')
  return code.trim()
}

function minifyCSS(code) {
  // 1. 移除注释（CSS 字符串内极少出现 /*，按普通处理）
  code = code.replace(/\/\*[\s\S]*?\*\//g, '')
  // 2. 折叠多余空白
  code = code.replace(/\s+/g, ' ')
  // 3. 去除空白围绕 { } : ; , =
  code = code.replace(/\s*([{}:;,])\s*/g, '$1')
  code = code.replace(/\s*=\s*/g, '=')
  // 4. 去掉最后一个分号前的多余
  code = code.replace(/;}/g, '}')
  return code.trim()
}

function minifyHTML(code) {
  // 1. 移除 HTML 注释
  code = code.replace(/<!--[\s\S]*?-->/g, '')
  // 2. 标签间换行压缩
  code = code.replace(/>\s+</g, '><')
  // 3. 压缩行内连续空白
  code = code.replace(/\s{2,}/g, ' ')
  // 4. 仅压缩 = 两侧空白，保留属性引号（避免 class="a b" 被破坏）
  code = code.replace(/\s*=\s*/g, '=')
  return code.trim()
}

// ---- Prettify ----

function prettifyCode(code, lang) {
  if (lang === 'HTML') return prettifyHTML(code)
  if (lang === 'CSS') return prettifyCSS(code)
  return prettifyJS(code)
}

function prettifyJS(code) {
  var indent = 0
  var out = ''
  var inStr = false
  var strChar = ''
  var parenDepth = 0

  for (var i = 0; i < code.length; i++) {
    var ch = code[i]
    var next = code[i + 1] || ''

    // 处理字符串（含转义）
    if (inStr) {
      out += ch
      if (ch === '\\' && next) { out += next; i++ }
      else if (ch === strChar) inStr = false
      continue
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      inStr = true
      strChar = ch
      out += ch
      continue
    }

    // 处理注释
    if (ch === '/' && next === '/') {
      while (i < code.length && code[i] !== '\n') { out += code[i]; i++ }
      out += '\n'
      continue
    }
    if (ch === '/' && next === '*') {
      out += '/*'
      i += 2
      while (i < code.length) {
        if (code[i] === '*' && code[i + 1] === '/') { out += '*/'; i += 2; break }
        out += code[i]
        i++
      }
      out += '\n'
      continue
    }

    // 圆括号 / 方括号深度（用于判断逗号、分号是否处于“行内”）
    if (ch === '(' || ch === '[') {
      parenDepth++
      out += ch
      continue
    }
    if (ch === ')' || ch === ']') {
      parenDepth = Math.max(0, parenDepth - 1)
      out += ch
      continue
    }

    // 大括号
    if (ch === '{') {
      out += ' {\n'
      indent++
      out += indentStr(indent)
      continue
    }
    if (ch === '}') {
      indent = Math.max(0, indent - 1)
      out += '\n' + indentStr(indent) + '}'
      continue
    }

    // 分号：仅在顶层（不在括号内）换行
    if (ch === ';') {
      if (parenDepth === 0) {
        out += ';\n' + indentStr(indent)
      } else {
        out += '; '
      }
      continue
    }

    // 逗号：仅在顶层换行，括号内保持同行
    if (ch === ',' && parenDepth === 0) {
      out += ',\n' + indentStr(indent)
      continue
    }

    // 换行符由我们控制，忽略原始换行
    if (ch === '\n' || ch === '\r') continue

    // 空白：仅在非连续时保留一个空格
    if (ch === ' ' || ch === '\t') {
      if (out.length > 0 && out[out.length - 1] !== ' ' && out[out.length - 1] !== '\n') {
        out += ' '
      }
      continue
    }

    out += ch
  }

  out = out.replace(/\n{3,}/g, '\n\n')
  return out.trim()
}

function prettifyCSS(code) {
  var out = ''
  var indent = 0
  var inBlock = false

  for (var i = 0; i < code.length; i++) {
    var ch = code[i]

    if (ch === '{') {
      out += ' {\n'
      indent = 1
      inBlock = true
      continue
    }
    if (ch === '}') {
      indent = 0
      out += '\n}\n'
      inBlock = false
      continue
    }
    if (ch === ';' && inBlock) {
      out += ';\n  '
      continue
    }
    if (ch === '\n' || ch === '\r') continue

    out += ch
  }

  return out.trim()
}

function prettifyHTML(code) {
  var indent = 0
  var out = ''
  var inTag = false
  var inClose = false

  for (var i = 0; i < code.length; i++) {
    var ch = code[i]

    if (ch === '<') {
      var next2 = code.substring(i, i + 4).toLowerCase()
      var isClosing = next2.indexOf('</') === 0
      var isSpecial = next2.indexOf('<!') === 0 || next2.indexOf('<?') === 0

      if (isClosing) {
        indent = Math.max(0, indent - 1)
        if (out.length > 0 && out[out.length - 1] !== '\n') out += '\n'
        out += indentStr(indent)
      } else if (!isSpecial) {
        if (out.length > 0 && out[out.length - 1] !== '\n') out += '\n'
        out += indentStr(indent)
      }

      // 读取整个标签
      var tag = ''
      while (i < code.length && code[i] !== '>') { tag += code[i]; i++ }
      tag += '>'
      out += tag

      if (!isClosing && !isSpecial && tag.indexOf('</') < 0 && tag.indexOf('/>') < 0) {
        indent++
      }
      continue
    }

    if (ch === '\n' || ch === '\r') continue

    if (ch === ' ' || ch === '\t') {
      if (out.length > 0 && out[out.length - 1] !== ' ' && out[out.length - 1] !== '\n') {
        out += ' '
      }
      continue
    }

    out += ch
  }

  return out.trim()
}

function indentStr(level) {
  var s = ''
  for (var i = 0; i < level; i++) s += '  '
  return s
}
