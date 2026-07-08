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
      return parseNumber(expr, pos)
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
