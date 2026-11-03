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

// 中文输入法下身下用户敲的是一整句话（「1公斤等于多少斤」），单位名也是汉字。
// 这组锁死中文单位名 → 换算系数的折表，以及各类中文分隔符的切分。
describe('convertExpression：中文', () => {
  const val = (s: string) => {
    const r = convertExpression(s)
    expect(r, s).not.toBeNull()
    return r!.value
  }

  it('质量：斤 / 公斤 / 吨 / 两', () => {
    expect(val('1公斤等于多少斤')).toBeCloseTo(2, 6)
    expect(val('1吨等于多少斤')).toBeCloseTo(2000, 6)
    expect(val('5斤 = 公斤')).toBeCloseTo(2.5, 6)
    expect(val('1斤等于多少两')).toBeCloseTo(10, 6)
    expect(val('100克换成公斤')).toBeCloseTo(0.1, 6)
  })

  it('长度：公里 / 里 / 米 / 英里', () => {
    expect(val('1米等于多少厘米')).toBeCloseTo(100, 6)
    expect(val('1公里等于多少里')).toBeCloseTo(2, 6)
    expect(val('1英里是多少公里')).toBeCloseTo(1.609344, 6)
    expect(val('1海里等于多少米')).toBeCloseTo(1852, 6)
    expect(val('1米等于多少尺')).toBeCloseTo(3, 6)
    expect(val('30公分等于多少厘米')).toBeCloseTo(30, 6)
  })

  it('时间：小时 / 分钟 / 天 / 周', () => {
    expect(val('1小时等于多少分钟')).toBeCloseTo(60, 6)
    expect(val('2天是多少小时')).toBeCloseTo(48, 6)
    expect(val('1周等于多少天')).toBeCloseTo(7, 6)
    expect(val('1分钟等于多少秒')).toBeCloseTo(60, 6)
  })

  it('数据量：兆 / 吉 沿用 1024 进制', () => {
    expect(val('1024兆等于多少吉')).toBeCloseTo(1, 6)
    expect(val('1吉字节换算成兆')).toBeCloseTo(1024, 6)
    expect(val('2048字节等于多少千字节')).toBeCloseTo(2, 6)
  })

  it('面积 / 体积', () => {
    expect(val('100平米换成亩')).toBeCloseTo(0.15, 6)
    expect(val('1公顷等于多少亩')).toBeCloseTo(15, 6)
    expect(val('1升等于多少毫升')).toBeCloseTo(1000, 6)
    expect(val('1立方米等于多少升')).toBeCloseTo(1000, 6)
  })

  it('温度：中文单位走加减公式而非乘系数', () => {
    expect(val('30摄氏度 = 华氏度')).toBeCloseTo(86, 6)
    expect(val('100摄氏度等于多少华氏度')).toBeCloseTo(212, 6)
    expect(val('0摄氏度是多少开尔文')).toBeCloseTo(273.15, 6)
    // 口语里的「度」按摄氏度算
    expect(val('37度等于多少华氏度')).toBeCloseTo(98.6, 6)
  })

  it('多种中文分隔符都能切分', () => {
    for (const s of ['1公斤等于多少斤', '1公斤是多少斤', '1公斤多少斤', '1公斤换成斤', '1公斤换算成斤', '1公斤等于几斤']) {
      expect(val(s), s).toBeCloseTo(2, 6)
    }
  })

  it('全角等号与全角数字标点一并归一化', () => {
    expect(val('1公斤＝多少斤')).toBeCloseTo(2, 6)
  })

  it('回显保留用户写的中文单位', () => {
    expect(convertExpression('1公斤等于多少斤')!.text).toBe('1公斤 = 2 斤')
  })

  it('中文数字、货币、跨类别都不猜', () => {
    // 中文数字不在能力范围内：宁可不出结果，也不给出一个闭嘴算错的数
    expect(convertExpression('一公斤等于多少斤')).toBeNull()
    expect(convertExpression('100元等于多少美元')).toBeNull()
    expect(convertExpression('1米等于多少斤')).toBeNull()
    // 「是」后面跟数字时不能被当成目标单位
    expect(convertExpression('1米是100厘米')).toBeNull()
  })
})

describe('全角与千分位归一化', () => {
  it('全角运算符折回半角', () => {
    expect(evaluate('（1＋2）×3')).toBe(9)
    expect(evaluate('12×8＋5')).toBe(101)
    expect(evaluate('10÷4')).toBe(2.5)
    expect(evaluate('10％3')).toBe(1)
  })

  it('千分位逗号直接去掉', () => {
    expect(evaluate('1,000+2')).toBe(1002)
    expect(convertExpression('1,000克等于多少公斤')!.value).toBeCloseTo(1, 6)
  })
})
