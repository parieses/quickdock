/**
 * JSON → TypeScript / Go Struct 类型定义生成器 — Goja 后端
 * 递归解析 JSON，支持 TS interface 和 Go struct + json tag 输出
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var input = params.input || {}
  var text = (input.text || '').trim()
  if (!text) return { error: '请输入 JSON 字符串' }

  var outputType = input.outputType || 'typescript'  // typescript | go

  // 尝试解析 JSON
  var parsed
  try {
    parsed = JSON.parse(text)
  } catch (e) {
    return { error: 'JSON 解析失败: ' + e.message }
  }

  // 检查是否为命令面板直调（无 outputType 参数）
  var fromFrontend = input.fromFrontend === true
  if (!fromFrontend && !input.outputType) {
    // 命令面板直调，默认显示两种
    outputType = 'typescript'
  }

  var code
  if (outputType === 'go') {
    code = jsonToGo(parsed, 'Root')
  } else {
    code = jsonToTypeScript(parsed, 'RootType')
  }

  return {
    text: code,
    display: code,
    outputType: outputType
  }
}

// ======== TypeScript 生成器（原有逻辑）========

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
  var itemTypes = []
  var seen = {}
  for (var i = 0; i < arr.length; i++) {
    var item = arr[i]
    var t = item === null ? 'null' : Array.isArray(item) ? 'array' : typeof item
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
      if (iname === name) iname = name + 'Item'
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
  var props = []
  var subInterfaces = []
  for (var i = 0; i < keys.length; i++) {
    var key = keys[i]
    var val = obj[key]
    var tsType = tsValueToTypeRef(val, name + capitalize(key), subInterfaces)
    var optional = (val === null || val === undefined) ? '?' : ''
    props.push('  ' + key + optional + ': ' + tsType + ';')
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
    var typeList = Object.keys(elemTypes)
    return typeList.length === 1 ? typeList[0] + '[]' : '(' + typeList.join(' | ') + ')[]'
  }
  if (t === 'object') {
    var iface = tsObjectToInterface(val, contextName)
    collector.push(iface)
    return contextName
  }
  return 'any'
}

// ======== Go Struct 生成器 ========

function jsonToGo(obj, name) {
  if (obj === null || obj === undefined) return 'type ' + name + ' interface{}'
  var type = typeof obj
  if (type === 'string') return 'type ' + name + ' string'
  if (type === 'number') return 'type ' + name + ' float64'
  if (type === 'boolean') return 'type ' + name + ' bool'
  if (Array.isArray(obj)) return goArrayToType(obj, name)
  if (type === 'object') return goObjectToStruct(obj, name)
  return 'type ' + name + ' interface{}'
}

function goArrayToType(arr, name) {
  if (arr.length === 0) return 'type ' + name + ' []interface{}'
  // 收集元素类型
  var elemType = goUnifyElementTypes(arr, name + 'Item')
  return 'type ' + name + ' ' + elemType
}

function goUnifyElementTypes(arr, baseName) {
  var itemTypes = {}
  var seen = {}
  for (var i = 0; i < arr.length; i++) {
    var item = arr[i]
    var typeStr = goInferGoType(item, baseName + 'Elem', seen)
    itemTypes[typeStr] = (itemTypes[typeStr] || 0) + 1
  }
  var typeList = Object.keys(itemTypes)
  if (typeList.length === 1) {
    var t = typeList[0]
    return t.indexOf('struct') >= 0 ? '[]' + t : '[]' + t
  }
  // 联合类型 → Go 用 interface{}
  return '[]interface{}'
}

function goInferGoType(val, contextName, seen) {
  if (val === null || val === undefined) return 'interface{}'
  var t = typeof val
  if (t === 'string') return 'string'
  if (t === 'number') {
    if (val === Math.floor(val) && isFinite(val) && Math.abs(val) < 2147483648) return 'int'
    return 'float64'
  }
  if (t === 'boolean') return 'bool'
  if (Array.isArray(val)) {
    if (val.length === 0) return '[]interface{}'
    var elemStr = goUnifyElementTypes(val, contextName)
    return elemStr
  }
  if (t === 'object') {
    // 防止循环引用/重名
    if (seen[contextName]) return '*' + contextName
    seen[contextName] = true
    return goObjectToStruct(val, contextName)
  }
  return 'interface{}'
}

function goObjectToStruct(obj, name) {
  var keys = Object.keys(obj)
  if (keys.length === 0) return 'type ' + name + ' struct {}'

  var fields = []
  var subStructs = []
  var seen = {}

  for (var i = 0; i < keys.length; i++) {
    var key = keys[i]
    var val = obj[key]
    var fieldName = goFieldName(key)
    var goType = goInferGoType(val, name + fieldName, seen)

    // Go struct 中嵌入 struct 定义为嵌套
    var jsonTag = '`json:"' + key + '"`'
    if (isOptional(val)) {
      goType = '*' + goType
    }

    fields.push('  ' + fieldName + ' ' + goType + ' ' + jsonTag)
  }

  var result = 'type ' + name + ' struct {\n' + fields.join('\n') + '\n}'
  return result
}

function goFieldName(key) {
  // snake_case → PascalCase
  return key.split('_').map(function(s) {
    return s.charAt(0).toUpperCase() + s.slice(1)
  }).join('')
}

function isOptional(val) {
  return val === null || val === undefined
}

function capitalize(str) {
  return str.charAt(0).toUpperCase() + str.slice(1)
}
