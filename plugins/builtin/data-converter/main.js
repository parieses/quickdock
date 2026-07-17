function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

// ---------- JSON ↔ YAML 转换（轻量 YAML 子集解析/生成）----------

function isYamlLine(line) {
  var t = line.trim()
  if (!t || t.startsWith('#')) return false
  return t.indexOf(':') > 0 || t.startsWith('- ') || t.startsWith('[') || t.startsWith('{')
}

// YAML → JSON
function yamlToJson(yaml) {
  var lines = yaml.split('\n')
  var result = {}
  var stack = [{obj: result, indent: -1}]

  for (var i = 0; i < lines.length; i++) {
    var line = lines[i]
    if (!line.trim() || line.trim().startsWith('#')) continue

    var indent = line.search(/\S/)
    if (indent < 0) continue
    var content = line.trim()
    var isList = content.startsWith('- ')
    if (isList) content = content.substring(2).trim()

    // Pop stack until we're at the right level
    while (stack.length > 1 && indent <= stack[stack.length-1].indent) {
      stack.pop()
    }

    var current = stack[stack.length-1].obj

    if (isList) {
      if (!Array.isArray(current)) {
        // This shouldn't happen if YAML is well-formed
        continue
      }
      if (content.indexOf(':') > 0 && !content.startsWith('"') && !content.startsWith("'")) {
        var colonIdx = content.indexOf(':')
        var key = content.substring(0, colonIdx).trim()
        var val = content.substring(colonIdx + 1).trim()
        var item = {}
        item[key] = parseValue(val)
        current.push(item)
        stack.push({obj: item, indent: indent})
      } else {
        current.push(parseValue(content))
      }
    } else if (content.indexOf(':') > 0) {
      var colonIdx = content.indexOf(':')
      var key = content.substring(0, colonIdx).trim()
      var val = content.substring(colonIdx + 1).trim()

      if (val === '' || val === '|' || val === '>') {
        // Object value
        if (!current[key]) {
          current[key] = {}
        }
        stack.push({obj: current[key], indent: indent})
      } else {
        current[key] = parseValue(val)
      }
    }
  }
  return result
}

function parseValue(val) {
  if (!val || val === 'null' || val === '~') return null
  if (val === 'true') return true
  if (val === 'false') return false
  if (val === '[]') return []
  if (val === '{}') return {}
  // Number
  if (/^-?\d+(\.\d+)?$/.test(val)) {
    return val.indexOf('.') >= 0 ? parseFloat(val) : parseInt(val, 10)
  }
  // String (possibly quoted)
  if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
    return val.substring(1, val.length - 1)
  }
  return val
}

// JSON → YAML
function jsonToYaml(obj, indent) {
  if (indent === undefined) indent = 0
  var prefix = '  '.repeat(indent)
  var result = ''

  if (obj === null || obj === undefined) { return 'null\n' }
  if (typeof obj === 'string') {
    if (obj.indexOf(':') >= 0 || obj.startsWith('- ') || obj.indexOf('#') >= 0 || obj === '') {
      return "'" + obj.replace(/'/g, "''") + "'\n"
    }
    return obj + '\n'
  }
  if (typeof obj === 'number' || typeof obj === 'boolean') { return String(obj) + '\n' }
  if (Array.isArray(obj)) {
    if (obj.length === 0) return '[]\n'
    for (var i = 0; i < obj.length; i++) {
      var item = obj[i]
      if (typeof item === 'object' && item !== null && !Array.isArray(item)) {
        result += prefix + '- '
        var first = true
        for (var k in item) {
          if (first) {
            result += k + ': ' + jsonToYaml(item[k], indent + 2).trim() + '\n'
            first = false
          } else {
            result += prefix + '  ' + k + ': ' + jsonToYaml(item[k], indent + 2).trim() + '\n'
          }
        }
      } else {
        result += prefix + '- ' + (typeof item === 'string' ? item : JSON.stringify(item)) + '\n'
      }
    }
    return result
  }
  // Object
  for (var key in obj) {
    var val = obj[key]
    if (typeof val === 'object' && val !== null && !Array.isArray(val)) {
      result += prefix + key + ':\n'
      result += jsonToYaml(val, indent + 1)
    } else if (Array.isArray(val)) {
      result += prefix + key + ':\n'
      result += jsonToYaml(val, indent + 1)
    } else {
      result += prefix + key + ': ' + jsonToYaml(val, 0).trim() + '\n'
    }
  }
  return result
}

// ---------- JSON ↔ TOML 转换（轻量 TOML 子集）----------
function jsonToToml(obj, prefix) {
  if (prefix === undefined) prefix = ''
  var result = ''
  for (var key in obj) {
    var val = obj[key]
    var fullKey = prefix ? prefix + '.' + key : key

    if (typeof val === 'object' && val !== null && !Array.isArray(val)) {
      result += '\n[' + fullKey + ']\n'
      result += jsonToToml(val, fullKey)
    } else if (Array.isArray(val)) {
      if (val.length > 0 && typeof val[0] === 'object') {
        for (var i = 0; i < val.length; i++) {
          result += '\n[[' + fullKey + ']]\n'
          result += jsonToToml(val[i], fullKey)
        }
      } else {
        result += key + ' = ' + JSON.stringify(val) + '\n'
      }
    } else if (typeof val === 'string') {
      result += key + ' = ' + JSON.stringify(val) + '\n'
    } else {
      result += key + ' = ' + String(val) + '\n'
    }
  }
  return result
}

function tomlToJson(toml) {
  var result = {}
  var lines = toml.split('\n')
  var currentSection = result

  for (var i = 0; i < lines.length; i++) {
    var line = lines[i].trim()
    if (!line || line.startsWith('#')) continue

    // Table: [section] or [[array]]
    var tableMatch = line.match(/^\[{1,2}(.+)]{1,2}$/)
    if (tableMatch) {
      currentSection = result
      var parts = tableMatch[1].split('.')
      var isArray = line.startsWith('[[')
      for (var j = 0; j < parts.length; j++) {
        var p = parts[j].trim()
        if (!currentSection[p]) {
          if (isArray && j === parts.length - 1) {
            currentSection[p] = []
          } else {
            currentSection[p] = {}
          }
        }
        if (isArray && j === parts.length - 1) {
          var newObj = {}
          currentSection[p].push(newObj)
          currentSection = newObj
        } else {
          currentSection = currentSection[p]
        }
      }
      continue
    }

    // key = value
    var kvMatch = line.match(/^([^=]+)=\s*(.+)$/)
    if (kvMatch) {
      var key = kvMatch[1].trim()
      var val = kvMatch[2].trim()
      currentSection[key] = parseTomlValue(val)
    }
  }
  return result
}

function parseTomlValue(val) {
  if (val === 'true') return true
  if (val === 'false') return false
  if (/^-?\d+\.\d+$/.test(val)) return parseFloat(val)
  if (/^-?\d+$/.test(val)) return parseInt(val, 10)
  if (val.startsWith('"') && val.endsWith('"')) return val.substring(1, val.length - 1)
  if (val.startsWith("'") && val.endsWith("'")) return val.substring(1, val.length - 1)
  if (val.startsWith('[') && val.endsWith(']')) {
    try { return JSON.parse(val.replace(/'/g, '"')) } catch(e) { return val }
  }
  return val.replace(/"/g, '')
}

// ---------- JSON ↔ XML 转换（轻量）----------
function jsonToXml(obj, key) {
  if (key === undefined) key = 'root'
  var result = ''

  if (obj === null || obj === undefined) { return '<' + key + '/>\n' }

  if (typeof obj === 'string' || typeof obj === 'number' || typeof obj === 'boolean') {
    return '<' + key + '>' + String(obj) + '</' + key + '>\n'
  }

  if (Array.isArray(obj)) {
    for (var i = 0; i < obj.length; i++) {
      result += '<' + key + '>'
      if (typeof obj[i] === 'object') {
        result += '\n' + jsonToXml(obj[i], '').replace(/^/gm, '  ').trim() + '\n'
      } else {
        result += String(obj[i])
      }
      result += '</' + key + '>\n'
    }
    return result
  }

  // Object
  for (var k in obj) {
    var v = obj[k]
    var tagName = k.replace(/\s+/g, '_')
    if (typeof v === 'object' && v !== null) {
      result += '<' + tagName + '>\n'
      result += jsonToXml(v, '').replace(/^/gm, '  ') + '\n'
      result += '</' + tagName + '>\n'
    } else {
      result += '<' + tagName + '>' + String(v) + '</' + tagName + '>\n'
    }
  }
  if (key) {
    result = '<' + key + '>\n' + result.replace(/^/gm, '  ').trim() + '\n</' + key + '>\n'
  }
  return result
}

function xmlToJson(xml) {
  var result = {}
  var tagRegex = /<(\w+)[^>]*>([\s\S]*?)<\/\1>/g
  var selfCloseRegex = /<(\w+)[^>]*\/>/g
  var match

  // Self-closing tags
  while ((match = selfCloseRegex.exec(xml)) !== null) {
    result[match[1]] = null
  }

  // Regular tags
  tagRegex.lastIndex = 0
  while ((match = tagRegex.exec(xml)) !== null) {
    var tag = match[1]
    var inner = match[2].trim()

    // Check if inner contains tags
    if (/<(\w+)[^>]*>/.test(inner)) {
      var child = xmlToJson(inner)
      if (result[tag]) {
        if (!Array.isArray(result[tag])) result[tag] = [result[tag]]
        result[tag].push(child)
      } else {
        result[tag] = child
      }
    } else {
      // Text content
      if (result[tag]) {
        if (!Array.isArray(result[tag])) result[tag] = [result[tag]]
        result[tag].push(inner)
      } else {
        result[tag] = inner
      }
    }
  }
  return Object.keys(result).length > 0 ? result : { _text: xml.trim() }
}

function detectFormat(text) {
  var t = text.trim()
  if (t.startsWith('{') || t.startsWith('[')) return 'json'
  if (t.startsWith('<')) return 'xml'
  if (t.indexOf(':') > 0 && t.indexOf('\n') > 0) return 'yaml'
  if (t.indexOf('=') > 0 && t.indexOf('\n') > 0 && t.indexOf('[') >= 0) return 'toml'
  return 'json'
}

function parseInput(text, format) {
  switch (format) {
    case 'yaml': return yamlToJson(text)
    case 'toml': return tomlToJson(text)
    case 'xml': return xmlToJson(text)
    case 'json':
    default: return JSON.parse(text)
  }
}

function convertTo(parsed, format) {
  switch (format) {
    case 'yaml': return jsonToYaml(parsed, 0)
    case 'toml': return jsonToToml(parsed, '')
    case 'xml': return jsonToXml(parsed, 'root').trim()
    case 'json':
    default: return JSON.stringify(parsed, null, 2)
  }
}

function handleExecute(params) {
  var command = params.command
  var inputObj = params.input || {}
  var text = inputObj.text || ''
  if (!text) return { error: '请输入待转换的数据' }

  // 优先使用前端指定的输入/输出格式，未指定时自动检测
  var fromFormat = inputObj.fromFormat || detectFormat(text)
  var toFormat = inputObj.toFormat || null

  try {
    var parsed = parseInput(text, fromFormat)

    // 指定了目标格式：精确转换为单一格式
    if (toFormat) {
      var single = convertTo(parsed, toFormat)
      return { text: single, display: single }
    }

    // 未指定目标格式：按命令或默认输出全部
    if (command === 'to-yaml') { var y = jsonToYaml(parsed, 0); return { text: y, display: y } }
    if (command === 'to-toml') { var tl = jsonToToml(parsed, ''); return { text: tl, display: tl } }
    if (command === 'to-xml') { var x = jsonToXml(parsed, 'root').trim(); return { text: x, display: x } }

    var yaml = jsonToYaml(parsed, 0)
    var toml = jsonToToml(parsed, '')
    var xml = jsonToXml(parsed, 'root').trim()
    return {
      text: yaml,
      display: 'YAML:\n' + yaml + '\n\nTOML:\n' + toml + '\n\nXML:\n' + xml
    }
  } catch (e) {
    return { error: '转换失败: ' + e.message }
  }
}
