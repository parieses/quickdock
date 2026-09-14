import { describe, it, expect } from 'vitest'
import { evaluate, format, convertExpression } from './calc'

// 命令面板用的轻量表达式求值器（替代 mathjs）。运算优先级、右结合幂、除零
// 都是手写 parser 最容易错的地方，这里逐条锁死。
describe('evaluate', () => {
  it('运算符优先级：先乘除后加减', () => {
    expect(evaluate('2 + 3 * 4')).toBe(14)
    expect(evaluate('10 - 6 / 2')).toBe(7)
  })

  it('括号改变优先级', () => {
    expect(evaluate('(1+2)*3')).toBe(9)
    expect(evaluate('(1+2)^3')).toBe(27)
  })

  it('幂运算右结合：2^3^2 = 512 而非 64', () => {
    expect(evaluate('2^3^2')).toBe(512)
  })

  it('取模', () => {
    expect(evaluate('7 % 3')).toBe(1)
  })

  it('一元负号与负号后跟括号', () => {
    expect(evaluate('-5 + 2')).toBe(-3)
    expect(evaluate('-(2+3)')).toBe(-5)
    // 实现上一元负号先与数字结合，故 -2^2 = (-2)^2 = 4（部分计算器给出 -4，语义不同）
    expect(evaluate('-2^2')).toBe(4)
  })

  it('忽略空白', () => {
    expect(evaluate('  1  +  2  ')).toBe(3)
  })

  it('小数', () => {
    expect(evaluate('1.5 * 2')).toBe(3)
    expect(evaluate('.5 + .5')).toBe(1)
  })

  it('除零抛错', () => {
    expect(() => evaluate('1/0')).toThrow(/Division by zero/)
  })

  it('空表达式抛错', () => {
    expect(() => evaluate('')).toThrow(/Empty expression/)
    expect(() => evaluate('   ')).toThrow(/Empty expression/)
  })

  it('残缺表达式抛错', () => {
    expect(() => evaluate('1 +')).toThrow()
    expect(() => evaluate('(1+2')).toThrow(/Expected "\)"/)
  })

  it('非法字符抛错', () => {
    expect(() => evaluate('1 $ 2')).toThrow(/Unexpected character/)
    expect(() => evaluate('abc')).toThrow()
  })
})

describe('format', () => {
  it('整数不显示多余的小数位', () => {
    expect(format(100)).toBe('100')
    expect(format(7)).toBe('7')
  })

  it('保留有效小数', () => {
    expect(format(1.5)).toBe('1.5')
  })

  it('默认 14 位有效数字吸收浮点误差', () => {
    expect(format(0.1 + 0.2)).toBe('0.3')
    expect(format(1 / 3)).toBe('0.33333333333333')
  })

  it('可指定精度', () => {
    expect(format(1.23456, { precision: 3 })).toBe('1.23')
  })
})

describe('convertExpression', () => {
  it('质量换算', () => {
    const r = convertExpression('1kg to g')
    expect(r).not.toBeNull()
    expect(r!.value).toBeCloseTo(1000, 6)
  })

  it('数据量换算（1024 进制）', () => {
    const r = convertExpression('1024MB in GB')
    expect(r).not.toBeNull()
    expect(r!.value).toBeCloseTo(1, 6)
  })

  it('时间换算', () => {
    const r = convertExpression('1h -> min')
    expect(r).not.toBeNull()
    expect(r!.value).toBeCloseTo(60, 6)
  })

  it('温度换算不走乘系数（摄氏→华氏）', () => {
    expect(convertExpression('0c to f')!.value).toBeCloseTo(32, 6)
    expect(convertExpression('100c to f')!.value).toBeCloseTo(212, 6)
  })

  it('带回显文本', () => {
    expect(convertExpression('1h to min')!.text).toBe('1h = 60 min')
  })

  it('跨类别（长度→质量）无法换算', () => {
    expect(convertExpression('1m to kg')).toBeNull()
  })

  it('非单位表达式返回 null，不污染普通搜索', () => {
    expect(convertExpression('hello')).toBeNull()
    expect(convertExpression('100')).toBeNull()
    expect(convertExpression('abc to def')).toBeNull()
  })
})
