import { describe, it, expect } from 'vitest'
import { pinyinParts, pinyinOf, pinyinHit, pinyinMatch, matchesTextOrPinyin, clearPinyinCache } from './pinyin'

// 拼音搜索的公共实现。此前有两份各自为政的副本（命令面板内的 pinyinMatch、
// usePluginIndex 里预计算的一批 pinyin()），其余搜索框够不着。抽出后需要锁住
// 「首字母串」与「全拼串」的推导规则，否则改动 pinyin-pro 调用参数会静默改变
// 所有搜索框的命中行为。
describe('pinyinParts', () => {
  it('推导首字母串与全拼串', () => {
    const p = pinyinParts('环境管理')
    expect(p.init).toBe('hjgl')
    expect(p.full).toBe('huanjingguanli')
  })

  it('空文本返回空串而非 undefined', () => {
    expect(pinyinParts('')).toEqual({ init: '', full: '' })
  })

  it('非中文字符原样保留（中英混排可用）', () => {
    const p = pinyinParts('Redis 缓存')
    expect(p.full).toContain('redis')
    expect(p.full).toContain('huancun')
  })

  it('纯标点不会拼出 "undefined"', () => {
    // 空音节的 p[0] 是 undefined，用 charAt 兜底后应为空串
    const p = pinyinParts('---')
    expect(p.init).not.toContain('undefined')
    expect(p.full).not.toContain('undefined')
  })
})

describe('pinyinHit / pinyinMatch', () => {
  it('首字母前缀命中', () => {
    expect(pinyinMatch('环境管理', 'hjg')).toBe(true)
    expect(pinyinMatch('环境管理', 'hjgl')).toBe(true)
  })

  it('全拼包含命中（非前缀位置也算）', () => {
    expect(pinyinMatch('环境管理', 'huanjing')).toBe(true)
    expect(pinyinMatch('环境管理', 'guanli')).toBe(true)
  })

  it('首字母非前缀不命中（只认前缀，避免弱命中）', () => {
    expect(pinyinMatch('环境管理', 'jgl')).toBe(false)
  })

  it('空查询与空文本一律不命中', () => {
    expect(pinyinMatch('环境管理', '')).toBe(false)
    expect(pinyinMatch('', 'hjgl')).toBe(false)
  })

  it('pinyinHit 对空部件返回 false', () => {
    expect(pinyinHit({ init: '', full: '' }, 'a')).toBe(false)
  })

  it('缓存命中与未命中结果一致', () => {
    clearPinyinCache()
    const uncached = pinyinMatch('工作空间', 'gzkj')
    const cached = pinyinMatch('工作空间', 'gzkj', 'ws:1')
    const cachedAgain = pinyinMatch('工作空间', 'gzkj', 'ws:1')
    expect(uncached).toBe(true)
    expect(cached).toBe(true)
    expect(cachedAgain).toBe(true)
    expect(pinyinOf('工作空间', 'ws:1').init).toBe('gzkj')
  })
})

describe('matchesTextOrPinyin', () => {
  it('原文包含命中（大小写不敏感）', () => {
    expect(matchesTextOrPinyin(['Redis 缓存'], 'redis')).toBe(true)
    expect(matchesTextOrPinyin(['Redis 缓存'], 'REDIS')).toBe(true)
  })

  it('拼音查询同样大小写不敏感', () => {
    expect(matchesTextOrPinyin(['开发数据库'], 'KFSJK')).toBe(true)
    expect(pinyinMatch('环境管理', 'HJGL')).toBe(true)
  })

  it('原文不中时回落到拼音，多字段任一命中即可', () => {
    expect(matchesTextOrPinyin(['MySQL', '开发数据库'], 'kfsjk')).toBe(true)
  })

  it('全部字段都不中则返回 false', () => {
    expect(matchesTextOrPinyin(['MySQL', '开发数据库'], 'zzzzz')).toBe(false)
  })

  it('空值与空字段被安全跳过', () => {
    expect(matchesTextOrPinyin([undefined, null, '', '缓存'], 'hc')).toBe(true)
    expect(matchesTextOrPinyin([undefined, null, ''], 'hc')).toBe(false)
  })
})
