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
      const inner = parseExpr(expr, next)
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

// ---- Public API ----

/**
 * evaluate 求值算术表达式
 * @param expression 如 "2 + 3 * 4", "(1+2)^3"
 * @returns 计算结果
 */
export function evaluate(expression: string): number {
  const trimmed = expression.trim()
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
// 解析形如 `100kg = lb`、`100kg to lb`、`1024MB in GB`、`1h -> min`。
// 不带目标单位（如 `100kg`）不触发，避免污染普通搜索（表达式必须以数字开头）。

interface UnitCategory {
  base: string
  units: Record<string, number> // 各单位的换算系数（相对 base）
}

const UNIT_CATEGORIES: Record<string, UnitCategory> = {
  mass: { base: 'kg', units: { kg: 1, g: 0.001, mg: 1e-6, t: 1000, lb: 0.45359237, oz: 0.028349523125, jin: 0.5, liang: 0.05 } },
  data: { base: 'B', units: { b: 1, byte: 1, kb: 1024, mb: 1024 ** 2, gb: 1024 ** 3, tb: 1024 ** 4, kib: 1024, mib: 1024 ** 2, gib: 1024 ** 3 } },
  length: { base: 'm', units: { m: 1, km: 1000, cm: 0.01, mm: 0.001, mile: 1609.344, mi: 1609.344, ft: 0.3048, foot: 0.3048, inch: 0.0254, in: 0.0254, yd: 0.9144, yard: 0.9144 } },
  time: { base: 's', units: { s: 1, sec: 1, min: 60, h: 3600, hr: 3600, hour: 3600, day: 86400, d: 86400, week: 604800 } },
}

const TEMP_UNITS = new Set(['c', '°c', 'celsius', 'f', '°f', 'fahrenheit', 'k', 'kelvin'])

function cleanUnit(token: string): string {
  return token.toLowerCase().replace(/[?°]/g, '')
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

/**
 * convertExpression 解析单位换算表达式。
 * @returns 成功时返回展示文本与数值；无法解析（非单位表达式）返回 null。
 */
export function convertExpression(expr: string): { text: string; value: number } | null {
  // 必须以数字开头 + 源单位 + 分隔符(to/in/=/->/→) + 目标单位（可带 ? 占位）
  const m = expr.trim().match(/^([\d.]+)\s*([a-zA-Zμ°]+?)\s*(?:to|in|=|->|→)\s*([a-zA-Zμ°?]+)$/i)
  if (!m) return null
  const value = parseFloat(m[1])
  if (!isFinite(value)) return null
  const fromTok = m[2]
  const toTok = m[3]

  if (TEMP_UNITS.has(cleanUnit(fromTok)) || TEMP_UNITS.has(cleanUnit(toTok))) {
    const tv = convertTemp(value, fromTok, toTok)
    if (tv === null) return null
    return { text: `${m[1]}${fromTok} = ${format(tv)} ${toTok.replace(/\?/g, '')}`, value: tv }
  }

  const from = findUnit(fromTok)
  const to = findUnit(toTok)
  if (!from || !to || from.category !== to.category) return null

  const base = value * from.factor
  const result = base / to.factor
  return { text: `${m[1]}${fromTok} = ${format(result)} ${toTok.replace(/\?/g, '')}`, value: result }
}
