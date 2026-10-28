import { describe, it, expect } from 'vitest'
import {
  filterByTag,
  queryPushForTag,
  tagPushSignature,
  isSmartTextTag,
  classifyTextType,
  type FilterableEntry,
} from './clipboardFilter'

function entry(over: Partial<FilterableEntry> & { id: string }): FilterableEntry {
  return {
    contentType: 'text',
    textContent: '',
    isPinned: 0,
    createdAt: 1_700_000_000_000,
    ...over,
  }
}

const sample: FilterableEntry[] = [
  entry({ id: 't1', contentType: 'text', textContent: 'hello world' }),
  entry({ id: 'i1', contentType: 'image', textContent: '' }),
  entry({ id: 'i2', contentType: 'image', textContent: '' }),
  entry({ id: 'f1', contentType: 'file', textContent: 'C:\\a.txt' }),
  entry({ id: 't2', contentType: 'text', textContent: 'https://example.com', isPinned: 1 }),
  entry({ id: 'i3', contentType: 'image', textContent: '', isPinned: 1 }),
  entry({ id: 't3', contentType: 'text', textContent: 'user@example.com' }),
  entry({ id: 't4', contentType: 'text', textContent: '{"a":1}' }),
]

describe('classifyTextType', () => {
  it('与后端 classifyTextType 同构：email/url/phone/json/code 各归其类', () => {
    expect(classifyTextType('user@example.com')).toBe('email')
    expect(classifyTextType('https://example.com')).toBe('url')
    expect(classifyTextType('13800138000')).toBe('phone')
    expect(classifyTextType('{"a":1}')).toBe('json')
    expect(classifyTextType('[1,2,3]')).toBe('json')
    expect(classifyTextType('function foo(){}')).toBe('code')
    expect(classifyTextType('hello world')).toBe('')
    expect(classifyTextType('')).toBe('')
  })
})

describe('queryPushForTag', () => {
  it('image/file 下推成 content_type 整类', () => {
    expect(queryPushForTag('image')).toEqual({ push: 'contentType', value: 'image' })
    expect(queryPushForTag('file')).toEqual({ push: 'contentType', value: 'file' })
  })

  it('智能分类标签下推成 smartType（携带具体类型，不等同于 text）', () => {
    for (const t of ['url', 'email', 'json', 'code', 'phone']) {
      expect(queryPushForTag(t)).toEqual({ push: 'smartType', value: t })
    }
  })

  it('all/favorite 不下推（none）', () => {
    expect(queryPushForTag('all')).toEqual({ push: 'none' })
    expect(queryPushForTag('favorite')).toEqual({ push: 'none' })
  })
})

describe('tagPushSignature', () => {
  it('none 分支也能出签名（回归：曾直接读 p.value 导致 TS2339）', () => {
    expect(tagPushSignature(queryPushForTag('all'))).toBe('none:')
    expect(tagPushSignature(queryPushForTag('image'))).toBe('contentType:image')
  })

  it('同一种下推方式、不同值 → 签名不同（url↔email 必须重查）', () => {
    expect(tagPushSignature(queryPushForTag('url'))).not.toBe(
      tagPushSignature(queryPushForTag('email')),
    )
  })

  it('all 与 favorite 都是 none → 签名相同（不必重查）', () => {
    expect(tagPushSignature(queryPushForTag('all'))).toBe(
      tagPushSignature(queryPushForTag('favorite')),
    )
  })
})

describe('filterByTag', () => {
  it('image 标签筛出全部图片条目（回归：曾因套 text 前置条件而永远为空）', () => {
    const r = filterByTag(sample, 'image')
    expect(r.map(e => e.id)).toEqual(['i1', 'i2', 'i3'])
  })

  it('file 标签筛出全部文件条目（回归：同上）', () => {
    const r = filterByTag(sample, 'file')
    expect(r.map(e => e.id)).toEqual(['f1'])
  })

  it('image 标签不会误伤文本条目', () => {
    expect(filterByTag(sample, 'image').some(e => e.contentType === 'text')).toBe(false)
  })

  it('favorite 筛出收藏项，包含图片类型', () => {
    const r = filterByTag(sample, 'favorite')
    expect(r.map(e => e.id)).toEqual(['t2', 'i3'])
  })

  it('all 原样返回', () => {
    expect(filterByTag(sample, 'all')).toBe(sample)
  })

  it('URL 智能分类只匹配 url 条目（用存储/正则类型，不靠 matchText 回调）', () => {
    const r = filterByTag(sample, 'url')
    expect(r.map(e => e.id)).toEqual(['t2'])
  })

  it('email 智能分类只匹配 email 条目', () => {
    const r = filterByTag(sample, 'email')
    expect(r.map(e => e.id)).toEqual(['t3'])
  })

  it('json 智能分类只匹配 json 条目', () => {
    const r = filterByTag(sample, 'json')
    expect(r.map(e => e.id)).toEqual(['t4'])
  })

  it('智能分类标签不会匹配到图片/文件条目', () => {
    const r = filterByTag(sample, 'url')
    expect(r.every(e => e.contentType === 'text')).toBe(true)
  })

  it('无 textType 的条目回退到 classifyTextType 复算', () => {
    const r = filterByTag([entry({ id: 'x', textContent: 'user@example.com' })], 'email')
    expect(r.map(e => e.id)).toEqual(['x'])
  })

  it('已存储的 textType 直接生效（即使内容被改，也以存储类型为准）', () => {
    const r = filterByTag([entry({ id: 'x', textContent: 'plain', textType: 'url' })], 'url')
    expect(r.map(e => e.id)).toEqual(['x'])
  })
})

describe('isSmartTextTag', () => {
  it('区分智能分类标签与整类标签', () => {
    expect(isSmartTextTag('url')).toBe(true)
    expect(isSmartTextTag('code')).toBe(true)
    expect(isSmartTextTag('image')).toBe(false)
    expect(isSmartTextTag('file')).toBe(false)
    expect(isSmartTextTag('favorite')).toBe(false)
  })
})
