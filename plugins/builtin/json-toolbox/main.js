/**
 * JSON 工具箱 — Goja 后端
 * 合并原 json-editor / json2ts / data-converter 的后端逻辑，按 command 分派：
 *   - convert-data : JSON ↔ YAML / TOML / XML 互转（data-converter 逻辑）
 *   - json-to-ts   : JSON → TypeScript interface
 *   - json-to-go   : JSON → Go struct
 * json-editor 的功能（解析/格式化/折叠/编辑/文件读写）全部由前端实现，无需后端。
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

// ---------- JSON ↔ YAML 转换（轻量 YAML 子集解析/生成）----------
function isYamlLine(line) {
  var t = line.trim()
  if (!t || t.startsWith('#')) return false
  return t.indexOf(':') > 0 || t.startsWith('- ') || t.startsWith('[') || t.startsWith('{')
}

function yamlToJson(yaml) {
  var lines = yaml.split('\n')
  var result = {}
  var stack = [{ obj: result, indent: -1 }]
  for (var i = 0; i < lines.length; i++) {
    var line = lines[i]
    if (!line.trim() || line.trim().startsWith('#')) continue
    var indent = line.search(/\S/)
    if (indent < 0) continue
    var content = line.trim()
    var isList = content.startsWith('- ')
    if (isList) content = content.substring(2).trim()
    while (stack.length > 1 && indent <= stack[stack.length - 1].indent) stack.pop()
    var current = stack[stack.length - 1].obj
    if (isList) {
      if (!Array.isArray(current)) continue
      if (content.indexOf(':') > 0 && !content.startsWith('"') && !content.startsWith("'")) {
        var colonIdx = content.indexOf(':')
        var key = content.substring(0, colonIdx).trim()
        var val = content.substring(colonIdx + 1).trim()
        var item = {}
        item[key] = parseValue(val)
        current.push(item)
        stack.push({ obj: item, indent: indent })
      } else {
        current.push(parseValue(content))
      }
    } else if (content.indexOf(':') > 0) {
      var colonIdx2 = content.indexOf(':')
      var key2 = content.substring(0, colonIdx2).trim()
      var val2 = content.substring(colonIdx2 + 1).trim()
      if (val2 === '' || val2 === '|' || val2 === '>') {
        if (!current[key2]) current[key2] = {}
        stack.push({ obj: current[key2], indent: indent })
      } else {
        current[key2] = parseValue(val2)
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
  if (/^-?\d+(\.\d+)?$/.test(val)) {
    return val.indexOf('.') >= 0 ? parseFloat(val) : parseInt(val, 10)
  }
  if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
    return val.substring(1, val.length - 1)
  }
  return val
}

function jsonToYaml(obj, indent) {
  if (indent === undefined) indent = 0
  var prefix = '  '.repeat(indent)
  var result = ''
  if (obj === null || obj === undefined) return 'null\n'
  if (typeof obj === 'string') {
    if (obj.indexOf(':') >= 0 || obj.startsWith('- ') || obj.indexOf('#') >= 0 || obj === '') {
      return "'" + obj.replace(/'/g, "''") + "'\n"
    }
    return obj + '\n'
  }
  if (typeof obj === 'number' || typeof obj === 'boolean') return String(obj) + '\n'
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
    var tableMatch = line.match(/^\[{1,2}(.+)]{1,2}$/)
    if (tableMatch) {
      currentSection = result
      var parts = tableMatch[1].split('.')
      var isArray = line.startsWith('[[')
      for (var j = 0; j < parts.length; j++) {
        var p = parts[j].trim()
        if (!currentSection[p]) {
          if (isArray && j === parts.length - 1) currentSection[p] = []
          else currentSection[p] = {}
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
    try { return JSON.parse(val.replace(/'/g, '"')) } catch (e) { return val }
  }
  return val.replace(/"/g, '')
}

// ---------- JSON ↔ XML 转换（轻量）----------
function jsonToXml(obj, key) {
  if (key === undefined) key = 'root'
  var result = ''
  if (obj === null || obj === undefined) return '<' + key + '/>\n'
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
  while ((match = selfCloseRegex.exec(xml)) !== null) result[match[1]] = null
  tagRegex.lastIndex = 0
  while ((match = tagRegex.exec(xml)) !== null) {
    var tag = match[1]
    var inner = match[2].trim()
    if (/<(\w+)[^>]*>/.test(inner)) {
      var child = xmlToJson(inner)
      if (result[tag]) {
        if (!Array.isArray(result[tag])) result[tag] = [result[tag]]
        result[tag].push(child)
      } else {
        result[tag] = child
      }
    } else {
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

// ---------- JSON → TypeScript ----------
function jsonToTypeScript(obj, name) {
  if (obj === null || obj === undefined) return 'type ' + name + ' = any\n'
  var type = typeof obj
  if (type === 'string') return 'type ' + name + ' = string\n'
  if (type === 'number') return 'type ' + name + ' = number\n'
  if (type === 'boolean') return 'type ' + name + ' = boolean\n'
  if (Array.isArray(obj)) return tsArrayToType(obj, name)
  if (type === 'object') return tsObjectToInterface(obj, name)
  return 'type ' + name + ' = any\n'
}
function tsArrayToType(arr, name) {
  if (arr.length === 0) return 'type ' + name + ' = any[]\n'
  var itemTypes = [], seen = {}
  for (var i = 0; i < arr.length; i++) {
    var item = arr[i], t = item === null ? 'null' : Array.isArray(item) ? 'array' : typeof item
    var key = t + '_' + JSON.stringify(item).substring(0, 50)
    if (!seen[key]) { seen[key] = true; itemTypes.push({ type: t, value: item }) }
  }
  if (itemTypes.length === 1) {
    var st = itemTypes[0]
    if (st.type === 'string') return 'type ' + name + ' = string[]\n'
    if (st.type === 'number') return 'type ' + name + ' = number[]\n'
    if (st.type === 'boolean') return 'type ' + name + ' = boolean[]\n'
    if (st.type === 'null') return 'type ' + name + ' = null[]\n'
    if (st.type === 'array') return 'type ' + name + ' = ' + tsArrayItemRef(st.value, name + 'Item') + '[][]\n'
    if (st.type === 'object') {
      var iname = name.charAt(0).toUpperCase() + name.slice(1)
      return tsObjectToInterface(st.value, iname) + '\nexport type ' + name + ' = ' + iname + '[]\n'
    }
  }
  var unionParts = []
  for (var j = 0; j < itemTypes.length; j++) {
    var it2 = itemTypes[j]
    if (it2.type === 'string') unionParts.push('string')
    else if (it2.type === 'number') unionParts.push('number')
    else if (it2.type === 'boolean') unionParts.push('boolean')
    else if (it2.type === 'null') unionParts.push('null')
    else if (it2.type === 'array') unionParts.push(tsArrayItemRef(it2.value, name + 'Item') + '[]')
    else if (it2.type === 'object') {
      var on2 = name.charAt(0).toUpperCase() + name.slice(1) + 'Item' + j
      tsObjectToInterface(it2.value, on2)
      unionParts.push(on2)
    }
  }
  return 'export type ' + name + ' = (' + unionParts.join(' | ') + ')[]\n'
}
function tsArrayItemRef(item, baseName) {
  if (item === null || item === undefined) return 'any'
  if (typeof item !== 'object') return typeof item
  if (Array.isArray(item)) return 'any[]'
  return baseName.charAt(0).toUpperCase() + baseName.slice(1)
}
function tsObjectToInterface(obj, name) {
  var keys = Object.keys(obj)
  if (keys.length === 0) return 'export interface ' + name + ' {}\n'
  var props = [], subInterfaces = []
  for (var i = 0; i < keys.length; i++) {
    var key = keys[i], val = obj[key]
    var tsType = tsValueToTypeRef(val, name + capitalize(key), subInterfaces)
    props.push('  ' + key + ((val === null || val === undefined) ? '?' : '') + ': ' + tsType + ';')
  }
  var result = 'export interface ' + name + ' {\n' + props.join('\n') + '\n}\n'
  if (subInterfaces.length > 0) result = subInterfaces.join('\n') + '\n' + result
  return result
}
function tsValueToTypeRef(val, contextName, collector) {
  if (val === null || val === undefined) return 'any'
  var t = typeof val
  if (t === 'string') return 'string'
  if (t === 'number') return 'number'
  if (t === 'boolean') return 'boolean'
  if (Array.isArray(val)) {
    if (val.length === 0) return 'any[]'
    var elemTypes = {}
    for (var i = 0; i < val.length; i++) {
      var et = tsValueToTypeRef(val[i], contextName + 'Item', collector)
      elemTypes[et] = true
    }
    var tl = Object.keys(elemTypes)
    return tl.length === 1 ? tl[0] + '[]' : '(' + tl.join(' | ') + ')[]'
  }
  if (t === 'object') { collector.push(tsObjectToInterface(val, contextName)); return contextName }
  return 'any'
}
function capitalize(str) { return str.charAt(0).toUpperCase() + str.slice(1) }

// ---------- JSON → Go Struct ----------
function jsonToGo(obj, name) {
  if (obj === null || obj === undefined) return 'type ' + name + ' interface{}\n'
  var type = typeof obj
  if (type === 'string') return 'type ' + name + ' string\n'
  if (type === 'number') return 'type ' + name + ' float64\n'
  if (type === 'boolean') return 'type ' + name + ' bool\n'
  if (Array.isArray(obj)) return goArrayToType(obj, name)
  if (type === 'object') return goObjectToStruct(obj, name)
  return 'type ' + name + ' interface{}\n'
}
function goArrayToType(arr, name) {
  if (arr.length === 0) return 'type ' + name + ' []interface{}\n'
  var elemType = goUnifyElements(arr, name + 'Item')
  return 'type ' + name + ' ' + elemType + '\n'
}
function goUnifyElements(arr, baseName) {
  var itemTypes = {}, seen = {}
  for (var i = 0; i < arr.length; i++) {
    var typeStr = goInferType(arr[i], baseName + 'Elem', seen)
    itemTypes[typeStr] = (itemTypes[typeStr] || 0) + 1
  }
  var tl = Object.keys(itemTypes)
  if (tl.length === 1) return tl[0].indexOf('struct') >= 0 ? '[]' + tl[0] : '[]' + tl[0]
  return '[]interface{}'
}
function goInferType(val, contextName, seen) {
  if (val === null || val === undefined) return 'interface{}'
  var t = typeof val
  if (t === 'string') return 'string'
  if (t === 'number') {
    if (val === Math.floor(val) && isFinite(val) && Math.abs(val) < 2147483648) return 'int'
    return 'float64'
  }
  if (t === 'boolean') return 'bool'
  if (Array.isArray(val)) { return val.length === 0 ? '[]interface{}' : goUnifyElements(val, contextName) }
  if (t === 'object') {
    if (seen[contextName]) return '*' + contextName
    seen[contextName] = true
    return goObjectToStruct(val, contextName)
  }
  return 'interface{}'
}
function goObjectToStruct(obj, name) {
  var keys = Object.keys(obj)
  if (keys.length === 0) return 'type ' + name + ' struct {}\n'
  var fields = [], seen = {}
  for (var i = 0; i < keys.length; i++) {
    var key = keys[i], val = obj[key]
    var fn = key.split('_').map(function (s) { return s.charAt(0).toUpperCase() + s.slice(1) }).join('')
    var gt = goInferType(val, name + fn, seen)
    if (val === null || val === undefined) gt = '*' + gt
    fields.push('  ' + fn + ' ' + gt + ' `json:"' + key + '"`')
  }
  return 'type ' + name + ' struct {\n' + fields.join('\n') + '\n}\n'
}

// ---------- 命令分派 ----------
function handleExecute(params) {
  var command = params.command || ''
  var inputObj = params.input || {}
  var text = (inputObj.text || '').trim()
  if (!text) return { error: '请输入待处理的数据' }

  try {
    if (command === 'json-to-ts' || command === 'json-to-go') {
      var parsed = JSON.parse(text)
      var code = command === 'json-to-go'
        ? jsonToGo(parsed, 'Root')
        : jsonToTypeScript(parsed, 'RootType')
      return { text: code, display: code }
    }

    if (command === 'convert-data') {
      var fromFormat = inputObj.fromFormat || detectFormat(text)
      var toFormat = inputObj.toFormat || null
      var p = parseInput(text, fromFormat)
      if (toFormat) {
        var single = convertTo(p, toFormat)
        return { text: single, display: single }
      }
      var yaml = jsonToYaml(p, 0)
      var toml = jsonToToml(p, '')
      var xml = jsonToXml(p, 'root').trim()
      return {
        text: yaml,
        display: 'YAML:\n' + yaml + '\n\nTOML:\n' + toml + '\n\nXML:\n' + xml
      }
    }

    return { error: '未知命令: ' + command }
  } catch (e) {
    return { error: '处理失败: ' + e.message }
  }
}
