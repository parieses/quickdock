// calc.ts — 轻量算术表达式求值器
// 替换 mathjs（~200KB），纯 TypeScript 实现，约 2KB gzip
// 支持的运算符：+ - * / ^ % ( )

// ---- Tokenizer & Parser (Recursive Descent) ----

class ParseError extends Error {
  constructor(msg: string) {
    super(msg)
    this.name = 'ParseError'
  }
}

function skipWS(expr: string, pos: number): number {
  while (pos < expr.length && expr[pos] === ' ') pos++
  return pos
}

function parseNumber(expr: string, pos: number): { value: number; pos: number } {
  const start = pos
  let dotSeen = false
  while (pos < expr.length) {
    const ch = expr[pos]
    if (ch >= '0' && ch <= '9') { pos++; continue }
    if (ch === '.' && !dotSeen) { dotSeen = true; pos++; continue }
    break
  }
  if (pos === start || (pos === start + 1 && expr[start] === '.')) {
    throw new ParseError(`Unexpected character "${expr[start]}" at position ${start}`)
  }
  return { value: parseFloat(expr.slice(start, pos)), pos }
}

function parseAtom(expr: string, pos: number): { value: number; pos: number } {
  pos = skipWS(expr, pos)
  if (pos >= expr.length) throw new ParseError('Unexpected end of expression')

  const ch = expr[pos]

  // 一元负号
  if (ch === '-') {
    const next = skipWS(expr, pos + 1)
    if (next < expr.length && (expr[next] >= '0' && expr[next] <= '9' || expr[next] === '.')) {
      // 从数字/小数点位置解析（支持 -.5 这类「负号+小数点」写法），再取负
      const num = parseNumber(expr, next)
      return { value: -num.value, pos: num.pos }
    }
    // 负号后跟括号: -(expr)
    if (next < expr.length && expr[next] === '(') {
      // 必须跳过左括号再交给 parseExpr：传 next（指向 "(" 本身）会让内层再把
      // "(" 当一次分组解析，外层随后找不到配对的 ")" 而误报 Expected ")"。
      const inner = parseExpr(expr, next + 1)
      const end = skipWS(expr, inner.pos)
      if (end >= expr.length || expr[end] !== ')') throw new ParseError(`Expected ")" at position ${end}`)
      return { value: -inner.value, pos: end + 1 }
    }
    throw new ParseError(`Unexpected character "${expr[next]}" at position ${next}`)
  }

  // 括号
  if (ch === '(') {
    const inner = parseExpr(expr, pos + 1)
    const end = skipWS(expr, inner.pos)
    if (end >= expr.length || expr[end] !== ')') throw new ParseError(`Expected ")" at position ${end}`)
    return { value: inner.value, pos: end + 1 }
  }

  // 正数
  if (ch >= '0' && ch <= '9' || ch === '.') {
    return parseNumber(expr, pos)
  }

  throw new ParseError(`Unexpected character "${ch}" at position ${pos}`)
}

function parsePower(expr: string, pos: number): { value: number; pos: number } {
  let left = parseAtom(expr, pos)
  pos = skipWS(expr, left.pos)
  while (pos < expr.length && expr[pos] === '^') {
    pos++
    // 右结合，递归解析
    const right = parsePower(expr, pos)
    left = { value: Math.pow(left.value, right.value), pos: right.pos }
    pos = skipWS(expr, left.pos)
  }
  return left
}

function parseMulDiv(expr: string, pos: number): { value: number; pos: number } {
  let left = parsePower(expr, pos)
  pos = skipWS(expr, left.pos)
  while (pos < expr.length) {
    const ch = expr[pos]
    if (ch !== '*' && ch !== '/' && ch !== '%') break
    pos++
    const right = parsePower(expr, pos)
    if (ch === '*') left = { value: left.value * right.value, pos: right.pos }
    else if (ch === '/') {
      if (right.value === 0) throw new ParseError('Division by zero')
      left = { value: left.value / right.value, pos: right.pos }
    } else {
      left = { value: left.value % right.value, pos: right.pos }
    }
    pos = skipWS(expr, left.pos)
  }
  return left
}

function parseExpr(expr: string, pos: number): { value: number; pos: number } {
  let left = parseMulDiv(expr, pos)
  pos = skipWS(expr, left.pos)
  while (pos < expr.length) {
    const ch = expr[pos]
    if (ch !== '+' && ch !== '-') break
    pos++
    const right = parseMulDiv(expr, pos)
    if (ch === '+') left = { value: left.value + right.value, pos: right.pos }
    else left = { value: left.value - right.value, pos: right.pos }
    pos = skipWS(expr, left.pos)
  }
  return { ...left, pos }
}

// ---- 输入归一化 ----
//
// 中文输入法下敲出来的运算符/括号/标点全是全角（`＝１２３` 之外还有 `＋－＊／`、`（）`、
// `×÷`），parser 只认 ASCII。这里统一折回半角，顺带去掉数字里的千分位逗号——
// 用户从报表里复制 `1,000` 是很常见的动作。

const FULLWIDTH_OPS: Record<string, string> = {
  '＋': '+', '－': '-', '＊': '*', '／': '/', '％': '%', '＾': '^',
  '（': '(', '）': ')', '＝': '=', '．': '.',
  '×': '*', '✕': '*', '÷': '/', '⁄': '/',
}

/** 全角运算符 / 千分位逗号 归一化为 ASCII；不认识的原样保留（交给 parser 报错）。 */
function normalizeOperators(s: string): string {
  return s.replace(/[＋－＊／％＾（）＝．×✕÷⁄,，]/g, (c) => FULLWIDTH_OPS[c] ?? '')
}

// ---- Public API ----

/**
 * evaluate 求值算术表达式
 * @param expression 如 "2 + 3 * 4", "(1+2)^3"
 * @returns 计算结果
 */
export function evaluate(expression: string): number {
  const trimmed = normalizeOperators(expression).trim()
  if (!trimmed) throw new ParseError('Empty expression')
  const result = parseExpr(trimmed, 0)
  const end = skipWS(trimmed, result.pos)
  if (end < trimmed.length) {
    const ch = trimmed[end]
    throw new ParseError(`Unexpected character "${ch}" at position ${end}`)
  }
  return result.value
}

/**
 * format 格式化数值（可选精度控制）
 * @param value 数值
 * @param options precision: 有效数字位数（默认 14）
 */
export function format(value: number, options?: { precision?: number }): string {
  const prec = options?.precision ?? 14
  // toPrecision 可能返回 "100.000" 等带多余零的字符串，去除尾部空格和多余0
  let raw = value.toPrecision(prec)
  // 去掉尾部多余的零（但保留至少一位小数以展示精度）
  if (raw.includes('.') && !raw.includes('e') && !raw.includes('E')) {
    raw = raw.replace(/\.?0+$/, '')
  }
  return raw
}

// ---- 单位换算 ----
// 与四则运算共用 `=` 触发前缀；当算术求值失败时回退到单位换算。
// 解析形如 `100kg = lb`、`100kg to lb`、`1024MB in GB`、`1h -> min`，
// 以及中文写法 `1公斤等于多少斤` / `100平米换成亩` / `30摄氏度 = 华氏度`。
// 不带目标单位（如 `100kg`）不触发，避免污染普通搜索（表达式必须以数字开头）。

interface UnitCategory {
  base: string
  units: Record<string, number> // 各单位的换算系数（相对 base）
}

// 单位表只放 ASCII / 拉丁缩写（键即用户可输入的别名），中文别名在 CN_UNIT_ALIASES
// 里折到这些键上——两处分开是为了让「一个单位多种中文叫法」不至于把系数表撑成一张
// 既难读又难查的大杂烩。
const UNIT_CATEGORIES: Record<string, UnitCategory> = {
  mass: { base: 'kg', units: { kg: 1, g: 0.001, mg: 1e-6, t: 1000, lb: 0.45359237, oz: 0.028349523125, jin: 0.5, liang: 0.05 } },
  // 数据量沿用 1024 进制（KiB 语义）：用户敲 1024MB 时想看到的就是 1GB
  data: { base: 'b', units: { b: 1, byte: 1, kb: 1024, mb: 1024 ** 2, gb: 1024 ** 3, tb: 1024 ** 4, kib: 1024, mib: 1024 ** 2, gib: 1024 ** 3, tib: 1024 ** 4 } },
  length: { base: 'm', units: { m: 1, km: 1000, cm: 0.01, mm: 0.001, mile: 1609.344, mi: 1609.344, ft: 0.3048, foot: 0.3048, inch: 0.0254, in: 0.0254, yd: 0.9144, yard: 0.9144, li: 500, nmi: 1852, chi: 1 / 3 } },
  time: { base: 's', units: { s: 1, sec: 1, ms: 0.001, min: 60, h: 3600, hr: 3600, hour: 3600, day: 86400, d: 86400, week: 604800 } },
  area: { base: 'm2', units: { m2: 1, cm2: 1e-4, km2: 1e6, ha: 10000, mu: 2000 / 3, acre: 4046.8564224, ft2: 0.09290304 } },
  volume: { base: 'l', units: { l: 1, ml: 0.001, cm3: 0.001, m3: 1000, gal: 3.785411784, qt: 0.946352946, pt: 0.473176473 } },
}

/**
 * 中文单位名 → 单位表里的键。
 *
 * 只收「日常真的会说」的叫法；「分」这类一词多义（分钟 / 角分 / 分贝）按最常用的
 * 口径收（分钟），落不到同一类别时换算会自然失败，不会给出错误答案。
 */
const CN_UNIT_ALIASES: Record<string, string> = {
  // 质量
  公斤: 'kg', 千克: 'kg', 克: 'g', 毫克: 'mg', 吨: 't', 磅: 'lb', 盎司: 'oz', 斤: 'jin', 市斤: 'jin', 两: 'liang',
  // 长度
  米: 'm', 千米: 'km', 公里: 'km', 厘米: 'cm', 公分: 'cm', 毫米: 'mm', 英里: 'mile', 海里: 'nmi',
  英尺: 'ft', 英寸: 'inch', 码: 'yd', 里: 'li', 华里: 'li', 尺: 'chi',
  // 数据量
  位: 'b', 比特: 'b', 字节: 'byte', 千字节: 'kb', 兆字节: 'mb', 兆: 'mb', 吉字节: 'gb', 吉: 'gb', 太字节: 'tb', 太: 'tb',
  // 时间
  秒: 's', 毫秒: 'ms', 分: 'min', 分钟: 'min', 小时: 'h', 时: 'h', 天: 'day', 日: 'day', 周: 'week', 星期: 'week', 礼拜: 'week',
  // 面积
  平方米: 'm2', 平米: 'm2', 平方厘米: 'cm2', 平方公里: 'km2', 平方千米: 'km2', 公顷: 'ha', 亩: 'mu', 英亩: 'acre', 平方英尺: 'ft2',
  // 体积
  升: 'l', 毫升: 'ml', 立方厘米: 'cm3', 立方米: 'm3', 加仑: 'gal', 夸脱: 'qt', 品脱: 'pt',
  // 温度（「度」按最常用的摄氏度收；碰到「1度等于多少分」这类角度用法，
  // 会因为类别对不上返回 null，而不是给一个错的数）
  摄氏度: 'c', 摄氏: 'c', 度: 'c', 华氏度: 'f', 华氏: 'f', 开尔文: 'k', 开氏度: 'k',
}

const TEMP_UNITS = new Set(['c', '°c', 'celsius', 'f', '°f', 'fahrenheit', 'k', 'kelvin'])

function cleanUnit(token: string): string {
  const t = token.toLowerCase().replace(/[?？°\s]/g, '')
  // ℃ / ℉ 是单字符符号，去掉「°」那条规则处理不到
  if (t === '℃') return 'c'
  if (t === '℉') return 'f'
  return CN_UNIT_ALIASES[t] ?? t
}

function findUnit(token: string): { category: string; factor: number } | null {
  const t = cleanUnit(token)
  for (const [cat, def] of Object.entries(UNIT_CATEGORIES)) {
    if (def.units[t] !== undefined) return { category: cat, factor: def.units[t] }
  }
  return null
}

function convertTemp(value: number, from: string, to: string): number | null {
  const f = cleanUnit(from)
  const t = cleanUnit(to)
  if (!TEMP_UNITS.has(f) || !TEMP_UNITS.has(t)) return null
  let c: number
  if (f === 'c' || f === 'celsius') c = value
  else if (f === 'f' || f === 'fahrenheit') c = (value - 32) * 5 / 9
  else if (f === 'k' || f === 'kelvin') c = value - 273.15
  else return null
  if (t === 'c' || t === 'celsius') return c
  if (t === 'f' || t === 'fahrenheit') return c * 9 / 5 + 32
  if (t === 'k' || t === 'kelvin') return c + 273.15
  return null
}

// 单位词允许的字符：拉丁缩写、希腊字母（μ）、度数符号、CJK（中文单位名）
const UNIT_CHARS = 'a-zA-Zμ°℃℉\\u4e00-\\u9fa5'
// 分隔符：英文 to/in/=/->/→ 与中文「等于 / 是 / 换成 / 多少」。长的排前面，
// 否则「等于多少」会被「等于」先吃掉、「换算成」会被「换」拆走。
const SEP = '等于多少|等於多少|换算成|换算为|转换成|转换为|转成|换成|转为|等于|等於|是多少|为多少|多少|几|是|为|to|in|->|→|='

// 必须以数字开头 + 源单位 + 分隔符 + 目标单位（可带 ? 占位）。编译一次即可——
// matchConvert 每敲一个字符都会调一次，没必要每轮重建正则。
const CONVERT_RE = new RegExp(
  '^([\\d.]+)\\s*([' + UNIT_CHARS + ']+?)\\s*(?:' + SEP + ')\\s*(?:多少|几)?\\s*(['
    + UNIT_CHARS + '?？]+)$',
)

/**
 * convertExpression 解析单位换算表达式。
 * @returns 成功时返回展示文本与数值；无法解析（非单位表达式）返回 null。
 */
export function convertExpression(expr: string): { text: string; value: number } | null {
  const src = normalizeOperators(expr).trim()
  const m = src.match(CONVERT_RE)
  if (!m) return null
  const value = parseFloat(m[1])
  if (!isFinite(value)) return null
  const fromTok = m[2]
  const toTok = m[3]
  const toDisplay = toTok.replace(/[?？]/g, '')

  if (TEMP_UNITS.has(cleanUnit(fromTok)) || TEMP_UNITS.has(cleanUnit(toTok))) {
    const tv = convertTemp(value, fromTok, toTok)
    if (tv === null) return null
    return { text: `${m[1]}${fromTok} = ${format(tv)} ${toDisplay}`, value: tv }
  }

  const from = findUnit(fromTok)
  const to = findUnit(toTok)
  if (!from || !to || from.category !== to.category) return null

  const base = value * from.factor
  const result = base / to.factor
  return { text: `${m[1]}${fromTok} = ${format(result)} ${toDisplay}`, value: result }
}
