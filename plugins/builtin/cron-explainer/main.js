/**
 * Crontab 解释器 — Goja 后端
 * 解析 5 段 cron 表达式（分 时 日 月 周），输出：
 *   - 人类可读描述
 *   - 下次 5 次执行时间
 *   - 可视化时间分布
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var input = params.input || {}
  var text = (input.text || '').trim()
  // 清理可能的 /cron 前缀
  text = text.replace(/^\/cron\s+/, '')
  if (!text) return { error: '请输入 cron 表达式，例如：*/5 * * * *' }

  var parts = text.split(/\s+/)
  if (parts.length !== 5) {
    return { error: 'cron 表达式需要 5 段（分 时 日 月 周），当前 ' + parts.length + ' 段' }
  }

  var fields = {
    minute: parts[0],
    hour:   parts[1],
    dom:    parts[2],
    month:  parts[3],
    dow:    parts[4]
  }

  // 验证每个字段
  var validation = validateFields(fields)
  if (!validation.valid) {
    return { error: validation.error }
  }

  // 生成人类可读描述
  var description = describeCron(fields)

  // 计算下次执行时间（从当前时间开始算 5 次）
  var nextRuns = computeNextRuns(fields, 5)

  // 生成可视化时间分布（小时的分布图）
  var visualization = visualizeCron(fields)

  var output = ''
  output += '┌─ 表达式: ' + text + '\n'
  output += '├─ 含义:   ' + description + '\n'
  output += '├─ 字段:\n'
  output += '│  分钟 (0-59):  ' + fields.minute + '\n'
  output += '│  小时 (0-23):  ' + fields.hour + '\n'
  output += '│  日期 (1-31):  ' + fields.dom + '\n'
  output += '│  月份 (1-12):  ' + fields.month + '\n'
  output += '│  星期 (0-7):   ' + fields.dow + '\n'
  output += '├─ 下次执行:\n'

  for (var i = 0; i < nextRuns.length; i++) {
    output += '│  [' + (i + 1) + '] ' + nextRuns[i] + '\n'
  }

  if (visualization) {
    output += '├─ 时间分布:\n'
    output += visualization
  }

  output += '└─ 提示: 输入新的 cron 表达式继续查询'

  return {
    text: nextRuns[0] || '无法计算',
    display: output
  }
}

// ---- 字段验证 ----

function validateFields(fields) {
  var fieldDefs = [
    { name: 'minute', min: 0, max: 59 },
    { name: 'hour',   min: 0, max: 23 },
    { name: 'dom',    min: 1, max: 31 },
    { name: 'month',  min: 1, max: 12 },
    { name: 'dow',    min: 0, max: 7 }
  ]

  for (var i = 0; i < fieldDefs.length; i++) {
    var def = fieldDefs[i]
    var val = fields[def.name]
    var result = validateField(val, def.min, def.max)
    if (!result.valid) {
      return { valid: false, error: def.name + ' 字段无效: ' + result.error }
    }
  }

  return { valid: true }
}

function validateField(val, min, max) {
  // *, */n, n, n-m, n,m, n-m/k
  var parts = val.split(',')
  for (var i = 0; i < parts.length; i++) {
    var p = parts[i].trim()
    if (p === '*') continue

    // */n or n-m/k
    var slashIdx = p.indexOf('/')
    var rangePart = slashIdx >= 0 ? p.substring(0, slashIdx) : p
    var stepPart = slashIdx >= 0 ? p.substring(slashIdx + 1) : ''

    if (stepPart) {
      var step = parseInt(stepPart, 10)
      if (isNaN(step) || step < 1) return { valid: false, error: '步长无效: ' + stepPart }
    }

    if (rangePart === '*') continue

    // n-m
    var dashIdx = rangePart.indexOf('-')
    if (dashIdx >= 0) {
      var rStart = parseInt(rangePart.substring(0, dashIdx), 10)
      var rEnd = parseInt(rangePart.substring(dashIdx + 1), 10)
      if (isNaN(rStart) || rStart < min || rStart > max) return { valid: false, error: rangePart + ' 超出范围' }
      if (isNaN(rEnd) || rEnd < min || rEnd > max) return { valid: false, error: rangePart + ' 超出范围' }
      if (rStart > rEnd) return { valid: false, error: rangePart + ' 起始大于结束' }
    } else {
      // 单个值
      var num = parseInt(rangePart, 10)
      if (isNaN(num) || num < min || num > max) return { valid: false, error: rangePart + ' 超出范围 [' + min + '-' + max + ']' }
    }
  }

  return { valid: true }
}

// ---- 人类可读描述 ----

function describeCron(fields) {
  var parts = []
  parts.push(describeField(fields.minute, '分钟', '每', '分钟'))
  parts.push(describeField(fields.hour, '小时', '每', '小时'))
  parts.push(describeField(fields.dom, '日', '每', '天'))
  parts.push(describeField(fields.month, '月', '每', '个月'))
  parts.push(describeField(fields.dow, '星期', '每', '天(周)'))

  // 组合成一句话
  var desc = ''

  if (fields.dow !== '*') {
    var dowDesc = describeSimpleField(fields.dow, ['日', '一', '二', '三', '四', '五', '六', '日'])
    desc += '每周' + dowDesc
  }

  if (fields.month !== '*') {
    var mDesc = describeSimpleField(fields.month, ['1月','2月','3月','4月','5月','6月','7月','8月','9月','10月','11月','12月'])
    desc += (desc ? '，' : '') + mDesc
  }

  if (fields.dom !== '*') {
    var domDesc = describeSimpleField(fields.dom, null)
    desc += (desc ? '，' : '') + '每月' + domDesc + '号'
  }

  if (fields.hour !== '*' && fields.minute !== '*') {
    var hDesc = describeSimpleField(fields.hour, null)
    var mDesc = describeSimpleField(fields.minute, null)
    desc += (desc ? '的' : '') + hDesc + '点' + mDesc + '分'
  } else if (fields.hour !== '*') {
    desc += (desc ? '的' : '') + describeSimpleField(fields.hour, null) + '点'
  } else if (fields.minute !== '*') {
    desc += (desc ? '每小时的' : '') + describeSimpleField(fields.minute, null) + '分'
  }

  if (!desc) desc = '每分钟执行一次'

  return desc
}

function describeField(val, unit, prefix, suffix) {
  if (val === '*') return ''
  return prefix + describeSimpleField(val, null) + suffix
}

function describeSimpleField(val, labels) {
  var parts = val.split(',')
  var result = []
  for (var i = 0; i < parts.length; i++) {
    var p = parts[i].trim()
    if (p.indexOf('/') >= 0) {
      var sp = p.split('/')
      var base = sp[0]
      var step = sp[1]
      if (base === '*') {
        result.push('每隔' + step)
      } else {
        result.push(base + '起每' + step)
      }
    } else if (p.indexOf('-') >= 0) {
      var dp = p.split('-')
      var a = labels ? labels[parseInt(dp[0],10)] : dp[0]
      var b = labels ? labels[parseInt(dp[1],10)] : dp[1]
      result.push(a + '-' + b)
    } else {
      result.push(labels ? labels[parseInt(p,10)] : p)
    }
  }
  return result.join('、')
}

// ---- 下次执行时间计算 ----

function computeNextRuns(fields, count) {
  var now = new Date()
  var results = []
  var current = new Date(now)

  // 对齐到下一分钟
  current.setSeconds(0)
  current.setMilliseconds(0)

  var minutes = expandField(fields.minute, 0, 59)
  var hours = expandField(fields.hour, 0, 23)
  var doms = expandField(fields.dom, 1, 31)
  var months = expandField(fields.month, 1, 12)
  var dows = expandField(fields.dow, 0, 7)

  // 把星期 7 映射到 0（某些 cron 格式周日可以是 0 或 7）
  var dowMap = {}
  for (var di = 0; di < dows.length; di++) {
    dowMap[dows[di] === 7 ? 0 : dows[di]] = true
  }

  var attempts = 0
  while (results.length < count && attempts < 10000) {
    attempts++

    var cm = current.getMonth() + 1  // 1-12
    var cd = current.getDate()
    var ch = current.getHours()
    var cmin = current.getMinutes()

    // 检查月份
    if (monthMatch(months, cm) && domMatch(doms, cd) && dowMatch(dowMap, current.getDay())) {
      // 月份/日期匹配，检查小时
      if (hourMatch(hours, ch)) {
        // 小时匹配，检查分钟
        if (minuteMatch(minutes, cmin)) {
          results.push(formatDate(current))
          current.setMinutes(cmin + 1)
          continue
        }
        // 尝试下一分钟
        current.setMinutes(cmin + 1)
        continue
      }
      // 尝试下一小时
      current.setHours(ch + 1, 0, 0, 0)
      continue
    }

    // 跳到下一天
    current.setDate(cd + 1)
    current.setHours(0, 0, 0, 0)
  }

  if (results.length === 0) {
    results.push('无法计算合理的执行时间（请检查表达式）')
  }

  return results
}

function expandField(val, min, max) {
  if (val === '*') return null // 通配

  var result = []
  var parts = val.split(',')
  for (var i = 0; i < parts.length; i++) {
    var p = parts[i].trim()
    var step = 1
    var rangeStart = min
    var rangeEnd = max

    var slashIdx = p.indexOf('/')
    if (slashIdx >= 0) {
      step = parseInt(p.substring(slashIdx + 1), 10)
      p = p.substring(0, slashIdx)
    }

    if (p === '*') {
      for (var v = min; v <= max; v += step) result.push(v)
      continue
    }

    var dashIdx = p.indexOf('-')
    if (dashIdx >= 0) {
      rangeStart = parseInt(p.substring(0, dashIdx), 10)
      rangeEnd = parseInt(p.substring(dashIdx + 1), 10)
    } else {
      rangeStart = parseInt(p, 10)
      rangeEnd = rangeStart
    }

    for (var vv = rangeStart; vv <= rangeEnd; vv += step) result.push(vv)
  }

  return result
}

function monthMatch(months, m) { return !months || months.indexOf(m) >= 0 }
function domMatch(doms, d) { return !doms || doms.indexOf(d) >= 0 }
function dowMatch(dowMap, d) { return Object.keys(dowMap).length === 0 || dowMap[d] }
function hourMatch(hours, h) { return !hours || hours.indexOf(h) >= 0 }
function minuteMatch(minutes, m) { return !minutes || minutes.indexOf(m) >= 0 }

function formatDate(d) {
  var pad = function(n){ return n < 10 ? '0' + n : '' + n }
  return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) +
    ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes())
}

// ---- 可视化 ----

function visualizeCron(fields) {
  var hours = expandField(fields.hour, 0, 23)
  if (!hours) return ''

  var vis = ''
  for (var i = 0; i < 24; i++) {
    var marker = hours.indexOf(i) >= 0 ? '█' : '·'
    vis += '  ' + (i < 10 ? '0' : '') + i + ': ' + marker + '\n'
  }
  return vis
}
