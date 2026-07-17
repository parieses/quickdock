/**
 * QuickDock 时间转换插件 — Goja 运行时
 * 功能：解析多种时间输入格式，输出 Unix 时间戳、ISO 8601、相对时间等
 *
 * 输入示例：
 *   "now"                    → 当前时间
 *   "1705300000"            → Unix 秒
 *   "1705300000000"         → Unix 毫秒
 *   "2024-01-15"            → ISO 日期
 *   "2024-01-15 14:30:00"  → ISO 日期时间
 *   "2024-01-15T14:30:00Z"  → ISO 8601
 *   "2024年1月15日"          → 中文日期
 */

// 星期映射
var WEEKDAY_CN = ['日', '一', '二', '三', '四', '五', '六']

function handleInitialize(params) {
    api.log('时间转换插件已加载')
    return { status: 'ready', version: '0.2.0' }
}

/**
 * 解析输入文本为 Date 对象
 * 支持多种格式，失败返回 null
 */
function parseTime(text) {
    if (!text || text.trim() === '') return null
    text = text.trim()

    // 1. "now" → 当前时间
    if (text.toLowerCase() === 'now') return new Date()

    // 2. Unix 时间戳（纯数字）
    if (/^\d+$/.test(text)) {
        var num = parseInt(text, 10)
        // 毫秒级时间戳（13 位）vs 秒级（10 位）
        if (num > 1e12) return new Date(num)
        return new Date(num * 1000)
    }

    // 3. ISO 日期: 2025-12-25（按本地时区解析，避免被当成 UTC 零点导致显示偏移）
    var dateMatch = text.match(/^(\d{4})-(\d{2})-(\d{2})$/)
    if (dateMatch) {
        return new Date(+dateMatch[1], +dateMatch[2] - 1, +dateMatch[3])
    }
    // 3b. ISO 日期时间（空格分隔，本地时区）: 2025-12-25 14:30:00
    var spMatch = text.match(/^(\d{4})-(\d{2})-(\d{2})\s+(\d{1,2}):(\d{2}):(\d{2})$/)
    if (spMatch) {
        return new Date(+spMatch[1], +spMatch[2] - 1, +spMatch[3], +spMatch[4], +spMatch[5], +spMatch[6])
    }
    // 3c. ISO 8601（T 分隔，无时区 → 视为本地）: 2025-12-25T14:30:00
    var tMatch = text.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{1,2}):(\d{2}):(\d{2})$/)
    if (tMatch) {
        return new Date(+tMatch[1], +tMatch[2] - 1, +tMatch[3], +tMatch[4], +tMatch[5], +tMatch[6])
    }

    // 4. ISO 8601 带 T 分隔符: 2025-12-25T14:30:00Z 或 2025-12-25T14:30:00
    var d = new Date(text)
    if (!isNaN(d.getTime())) return d

    // 4. 中文日期：2024年1月15日 或 2024年01月15日
    var cnMatch = text.match(/^(\d{4})年\s*(\d{1,2})月\s*(\d{1,2})日?\s*(\d{1,2})?[:：]?\s*(\d{1,2})?[:：]?\s*(\d{1,2})?$/)
    if (cnMatch) {
        var year = parseInt(cnMatch[1], 10)
        var month = parseInt(cnMatch[2], 10) - 1
        var day = parseInt(cnMatch[3], 10)
        var hour = parseInt(cnMatch[4] || '0', 10)
        var min = parseInt(cnMatch[5] || '0', 10)
        var sec = parseInt(cnMatch[6] || '0', 10)
        return new Date(year, month, day, hour, min, sec)
    }

    // 5. 无分隔符：20240115 或 202401151430
    var compactMatch = text.match(/^(\d{4})(\d{2})(\d{2})(?:(\d{2})(\d{2})(\d{2}))?$/)
    if (compactMatch) {
        return new Date(
            parseInt(compactMatch[1], 10),
            parseInt(compactMatch[2], 10) - 1,
            parseInt(compactMatch[3], 10),
            parseInt(compactMatch[4] || '0', 10),
            parseInt(compactMatch[5] || '0', 10),
            parseInt(compactMatch[6] || '0', 10)
        )
    }

    return null
}

/**
 * 补零
 */
function pad(n) {
    return n < 10 ? '0' + n : '' + n
}

/**
 * 从 Date 生成所有格式的输出
 */
function formatTime(d) {
    var year = d.getFullYear()
    var month = d.getMonth() + 1
    var day = d.getDate()
    var hour = d.getHours()
    var min = d.getMinutes()
    var sec = d.getSeconds()
    var ms = d.getMilliseconds()
    var weekday = d.getDay()
    var unixSec = Math.floor(d.getTime() / 1000)
    var unixMs = d.getTime()

    // 各时区 ISO
    var isoLocal = year + '-' + pad(month) + '-' + pad(day) + 'T' + pad(hour) + ':' + pad(min) + ':' + pad(sec)
    var isoUTC = d.toISOString().replace(/\.\d+Z$/, 'Z')
    // 中国时区 (UTC+8)
    var cn = new Date(d.getTime() + 8 * 3600000)
    var cnStr = cn.getFullYear() + '-' + pad(cn.getMonth()+1) + '-' + pad(cn.getDate()) +
                'T' + pad(cn.getHours()) + ':' + pad(cn.getMinutes()) + ':' + pad(cn.getSeconds()) + '+08:00'

    // 中文格式
    var cnDate = year + '年' + month + '月' + day + '日'
    var cnDateTime = cnDate + ' ' + pad(hour) + ':' + pad(min) + ':' + pad(sec)
    var cnWeekday = '星期' + WEEKDAY_CN[weekday]

    // 相对时间（距当前）
    var now = Date.now()
    var diffMs = d.getTime() - now
    var diffSec = Math.floor(Math.abs(diffMs) / 1000)
    var relative = ''
    if (Math.abs(diffMs) < 1000) {
        relative = '刚刚'
    } else if (diffSec < 60) {
        relative = (diffMs > 0 ? '' : '') + diffSec + ' 秒' + (diffMs > 0 ? '后' : '前')
    } else if (diffSec < 3600) {
        relative = Math.floor(diffSec / 60) + ' 分钟' + (diffMs > 0 ? '后' : '前')
    } else if (diffSec < 86400) {
        relative = Math.floor(diffSec / 3600) + ' 小时' + (diffMs > 0 ? '后' : '前')
    } else if (diffSec < 2592000) {
        relative = Math.floor(diffSec / 86400) + ' 天' + (diffMs > 0 ? '后' : '前')
    } else {
        relative = Math.floor(diffSec / 2592000) + ' 个月' + (diffMs > 0 ? '后' : '前')
    }

    return {
        'Unix 时间戳 (秒)': '' + unixSec,
        'Unix 时间戳 (毫秒)': '' + unixMs,
        'ISO 8601 (本地)': isoLocal,
        'ISO 8601 (UTC)': isoUTC,
        'ISO 8601 (中国时区)': cnStr,
        '日期 (中文)': cnDate,
        '日期时间 (中文)': cnDateTime,
        '星期': cnWeekday,
        '相对时间': relative,
        '年': '' + year,
        '月': '' + month,
        '日': '' + day,
        '时': '' + pad(hour),
        '分': '' + pad(min),
        '秒': '' + pad(sec),
    }
}

function handleExecute(params) {
    var command = params.command || ''
    var input = params.input || {}

    if (command !== 'time-convert') {
        throw new Error('未知命令: ' + command)
    }

    var text = input.text || ''

    // 只处理 matchPattern 命中的输入（由命令面板保证），非匹配的文本直接忽略
    if (!text.trim()) {
        return { error: '请输入时间', hint: '支持: now / Unix时间戳 / 2024-01-15 / 2024-01-15 14:30:00 / 2024年1月15日' }
    }

    // 后端额外校验：仅当输入符合预期格式时才处理（含 ISO 8601 的 T 分隔与可选时区）
    var expectedRE = /^(\d{4}[-/]\d{2}[-/]\d{2}(?:[T ]\d{1,2}:\d{2}(:\d{2})?(?:Z|[+-]\d{2}:?\d{2})?|\s+\d{1,2}:\d{2}(:\d{2})?)?|\d{10}|\d{13}|\d{8}(?:\d{6})?|\d{4}年\d{1,2}月\d{1,2}日|now)$/i
    if (!expectedRE.test(text.trim())) {
        return { error: '参数格式不匹配: ' + text, hint: '支持: now / Unix时间戳 / 2024-01-15 / 2024-01-15 14:30:00 / 2024年1月15日' }
    }

    var d = parseTime(text)
    if (!d) {
        return { error: '无法解析: ' + text, hint: '支持: now / Unix时间戳 / 2024-01-15 / 2024-01-15T14:30:00Z / 2024年1月15日' }
    }

    var result = formatTime(d)
    // 构建展示文本（第一行是输入的回显）
    var display = '输入: ' + text + '\n-----------------\n'
    var keys = Object.keys(result)
    for (var i = 0; i < keys.length; i++) {
        display += keys[i] + ': ' + result[keys[i]] + '\n'
    }

    return {
        text: display,
        result: result,
        translated: result['ISO 8601 (UTC)'], // 默认复制 UTC ISO
    }
}
