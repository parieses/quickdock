/**
 * 代码压缩/美化 — Goja 后端
 * 支持 JS / CSS / HTML 的 minify（压缩）和 prettify（美化）
 * 自动检测语言类型
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.1.0' }
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

function minifyJS(code) {
  // 1. 移除单行注释
  code = code.replace(/\/\/[^\n]*/g, '')
  // 2. 移除多行注释
  code = code.replace(/\/\*[\s\S]*?\*\//g, '')
  // 3. 移除多余空白
  code = code.replace(/\s+/g, ' ')
  // 4. 操作符周围去空格
  code = code.replace(/\s*([=+\-*\/%&|^~!?:;{}()\[\],<>])\s*/g, '$1')
  // 5. 恢复一些必要的空格（关键字后面）
  code = code.replace(/\b(function|if|else|for|while|do|switch|case|return|throw|try|catch|finally|typeof|instanceof|new|delete|void|in)\b(?=[a-zA-Z0-9_])/g, '$1 ')
  // 6. 去掉最后的分号前面的多余分号
  code = code.replace(/;+/g, ';')
  code = code.replace(/;}/g, '}')
  return code.trim()
}

function minifyCSS(code) {
  // 1. 移除注释
  code = code.replace(/\/\*[\s\S]*?\*\//g, '')
  // 2. 移除多余空白
  code = code.replace(/\s+/g, ' ')
  // 3. 移除空格围绕的 { } : ; ,
  code = code.replace(/\s*\{\s*/g, '{')
  code = code.replace(/\s*\}\s*/g, '}')
  code = code.replace(/\s*:\s*/g, ':')
  code = code.replace(/\s*;\s*/g, ';')
  code = code.replace(/\s*,\s*/g, ',')
  // 4. 去掉最后一个分号前的空格
  code = code.replace(/;}/g, '}')
  return code.trim()
}

function minifyHTML(code) {
  // 1. 移除 HTML 注释
  code = code.replace(/<!--[\s\S]*?-->/g, '')
  // 2. 移除多余空白（保留标签间的必要空格）
  code = code.replace(/>\s+</g, '><')
  // 3. 压缩行内空白
  code = code.replace(/\s{2,}/g, ' ')
  // 4. 移除属性值引号（简单情况）
  code = code.replace(/\s*=\s*"/g, '=')
  code = code.replace(/"\s*/g, '')
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
  var inRegex = false

  for (var i = 0; i < code.length; i++) {
    var ch = code[i]
    var next = code[i + 1] || ''

    // 处理字符串
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

    // 分号
    if (ch === ';') {
      out += ';\n' + indentStr(indent)
      continue
    }

    // 逗号（不在括号内时）
    if (ch === ',' && i > 0) {
      out += ',\n' + indentStr(indent)
      continue
    }

    // 空白字符
    if (ch === '\n' || ch === '\r') {
      // 忽略，由我们控制换行
      continue
    }
    if (ch === ' ' || ch === '\t') {
      // 只在非连续空白时保留一个空格
      if (out.length > 0 && out[out.length - 1] !== ' ' && out[out.length - 1] !== '\n') {
        out += ' '
      }
      continue
    }

    out += ch
  }

  // 清理多余空白行
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
