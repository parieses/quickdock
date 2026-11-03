// 命令面板的「内容嗅探」+「matchPattern」通道：把一整段 JSON / SQL / JWT / URL 粘进
// Ctrl+K 时，本该命中声明了对应格式线索（或写了 matchPattern）的插件命令。这批用例锁：
// 1) 结构化内容能命中（而不是跟关键词等值全都对不上，一条都不出）；
// 2) 分档：matchPattern 内容命中 93 > keyword 等值 90 > 短命中 88；内容嗅探 72
//    （越过「最佳匹配」70 门槛），但都不压过用户真在敲命令名的场景；
// 3) 只有命令声明了 acceptsInput 才把原文带走，符合「不设置就不带」。

import { describe, it, expect } from 'vitest'
import { usePluginIndex, detectContentFormat } from './usePluginIndex'
import type { PluginCmdIndex } from './usePluginIndex'

const { calcPluginScore } = usePluginIndex()

// 构造 JWT 形状的样例串用于检测测试。在运行时拼接，避免源码里出现完整 JWT 字面量
// 被密钥扫描（GitGuardian）误报为泄露凭证。探测器本身只认形状（三段 base64url），
// 因此用 base64url(JSON) 拼出来即可，不含任何真实密钥。
function makeJwtSample(): string {
  const b64u = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return [b64u('{"alg":"HS256"}'), b64u('{"sub":"123"}'), b64u('signature')].join('.')
}

function makeIdx(over: { acceptsInput?: boolean; keywords?: string[]; aliases?: string[]; prefix?: string; matchPattern?: string } = {}): PluginCmdIndex {
  const cmd = {
    id: 'json-toolbox',
    title: 'JSON 工具箱',
    keywords: over.keywords ?? ['json', 'yaml', '格式化'],
    aliases: over.aliases ?? ['JSON 格式化'],
    acceptsInput: !!over.acceptsInput,
    ...(over.prefix ? { prefix: over.prefix } : {}),
  }
  // 宿主同款包裹语义：^…$ 由宿主统一拼接（buildPluginIndex）；编译失败置 regexValid=false
  let regex: RegExp | null = null
  let regexValid = true
  if (over.matchPattern) {
    try { regex = new RegExp('^(?:' + over.matchPattern + ')$') } catch { regexValid = false }
  }
  return {
    // 测试只关心打分，插件实体字段按类型断言补最小集
    plugin: { id: 'io.test.json-toolbox', name: 'JSON 工具箱', description: '' } as PluginCmdIndex['plugin'],
    cmd: cmd as PluginCmdIndex['cmd'],
    regex,
    regexValid,
    notRunning: false,
    titleLc: ['json 工具箱'],
    pinyinTitleFull: '',
    pinyinTitleInit: '',
    pinyinKwFull: [],
    pinyinKwInit: [],
    pinyinAliasFull: [],
    pinyinAliasInit: [],
    // 与 buildPluginIndex 同款预计算小写化（P-1）：calcPluginScore 现在只读这些字段
    keywordsLC: (cmd.keywords || []).map(k => k.toLowerCase()),
    aliasesLC: (cmd.aliases || []).map(a => a.toLowerCase()),
    pluginNameLC: 'json 工具箱',
    pluginDescLC: '',
    cmdIdLC: cmd.id.toLowerCase(),
  }
}

describe('detectContentFormat 多格式探测', () => {
  it('JSON 对象与数组', () => {
    expect(detectContentFormat('{"a":1}')).toBe('json')
    expect(detectContentFormat('  [1, 2, 3]  ')).toBe('json')
  })

  it('url / email / ip / timestamp / hash / color', () => {
    expect(detectContentFormat('https://example.com/a?b=1')).toBe('url')
    expect(detectContentFormat('a@b.com')).toBe('email')
    expect(detectContentFormat('192.168.1.10')).toBe('ip')
    expect(detectContentFormat('1730620800000')).toBe('timestamp')
    expect(detectContentFormat('d41d8cd98f00b204e9800998ecf8427e')).toBe('hash')
    expect(detectContentFormat('#4a9eff')).toBe('color')
    expect(detectContentFormat('rgba(74, 158, 255, 0.8)')).toBe('color')
  })

  it('sql / xml / base64 / cron / jwt', () => {
    expect(detectContentFormat('SELECT * FROM users')).toBe('sql')
    expect(detectContentFormat('<?xml version="1.0"?><root><a>1</a></root>')).toBe('xml')
    expect(detectContentFormat('aGVsbG8gd29ybGQgdGhpcw==')).toBe('base64')
    expect(detectContentFormat('*/5 * * * *')).toBe('cron')
    expect(detectContentFormat(makeJwtSample())).toBe('jwt')
  })

  it('普通文本与残缺内容不算内容', () => {
    expect(detectContentFormat('json')).toBeNull()
    expect(detectContentFormat('{"a":')).toBeNull()
    expect(detectContentFormat('12345')).toBeNull()
    expect(detectContentFormat('这是一段没有任何结构特征的普通中文句子')).toBeNull()
  })
})

describe('calcPluginScore 内容嗅探通道', () => {
  it('整段 JSON 命中声明了 json 线索的命令，分数 72（越过最佳匹配门槛）', () => {
    const raw = '{"name":"张三"}'
    const r = calcPluginScore(makeIdx(), raw, raw)
    expect(r.matchType).toBe('content')
    expect(r.score).toBe(72)
    // 命令没声明 acceptsInput：不把它当后端参数，但原文仍随 hitText 交给前端插件
    expect(r.inlineInput).toBeUndefined()
    expect(r.hitText).toBe(raw)
  })

  it('非 JSON 内容（SQL）也能通过格式线索命中', () => {
    const idx = makeIdx({ keywords: ['sql', '格式化'] })
    const q = 'SELECT * FROM users'
    const r = calcPluginScore(idx, q, q.toLowerCase())
    expect(r.matchType).toBe('content')
    expect(r.score).toBe(72)
  })

  it('命令声明 acceptsInput 时把原文作为参数带走', () => {
    const raw = '{"name":"张三"}'
    const r = calcPluginScore(makeIdx({ acceptsInput: true }), raw, raw)
    expect(r.matchType).toBe('content')
    expect(r.inlineInput).toBe(raw)
  })

  it('显式敲关键字仍是 keyword 等值，分数远高于嗅探', () => {
    const r = calcPluginScore(makeIdx(), 'json', 'json')
    expect(r.matchType).toBe('keyword')
    expect(r.score).toBe(90)
  })

  it('内容嗅探不覆盖命令行前缀匹配', () => {
    const idx = makeIdx({ acceptsInput: true, prefix: '/json' })
    const r = calcPluginScore(idx, '/json {"a":1}', '/json {"a":1}')
    expect(r.matchType).toBe('slash')
    expect(r.score).toBe(98)
    expect(r.inlineInput).toBe('{"a":1}')
  })
})

describe('calcPluginScore matchPattern 分档', () => {
  it('内容形态命中（≥6 字符）给 93，高于 keyword 等值', () => {
    // login-tester 侧新正则语义：前缀 + 包络
    const idx = makeIdx({ matchPattern: '^\\s*https?://[\\s\\S]*$', acceptsInput: true })
    const q = 'https://example.com/a?b=1'
    const r = calcPluginScore(idx, q, q.toLowerCase())
    expect(r.matchType).toBe('match pattern')
    expect(r.score).toBe(93)
    expect(r.inlineInput).toBe(q)
    expect(r.hitText).toBe(q)
  })

  it('短词命中给 88，不抢 keyword 等值（90）', () => {
    const idx = makeIdx({ matchPattern: '[\\s\\S]*j\\s*o\\s*son[\\s\\S]*' })
    const r = calcPluginScore(idx, 'json', 'json')
    // keyword 等值 90 压过短命中 88
    expect(r.matchType).toBe('keyword')
    expect(r.score).toBe(90)
  })

  it('短词命中且无其它通道时给 88 并进最佳匹配（85 分档 + 3 > 70）', () => {
    const idx = makeIdx({ matchPattern: '^\\d+$', acceptsInput: true, keywords: ['rmb', '大写'] })
    const r = calcPluginScore(idx, '123', '123')
    expect(r.matchType).toBe('pattern short')
    expect(r.score).toBe(88)
    expect(r.inlineInput).toBe('123')
  })

  it('完整 JWT：matchPattern 命中(93) 不被 content 通道(72) 覆盖', () => {
    const jwt = makeJwtSample()
    const idx = makeIdx({ matchPattern: '^[A-Za-z0-9_-]{8,}\\.[A-Za-z0-9_-]{8,}\\.[A-Za-z0-9_-]*$', acceptsInput: true })
    const r = calcPluginScore(idx, jwt, jwt.toLowerCase())
    expect(r.matchType).toBe('match pattern')
    expect(r.score).toBe(93)
    expect(r.inlineInput).toBe(jwt)
  })

  it('slash 前缀命中时正则不参与打分（prefixHit 显式跳过）', () => {
    const idx = makeIdx({ prefix: '/json', matchPattern: '[\\s\\S]*' })
    const r = calcPluginScore(idx, '/json', '/json')
    expect(r.matchType).toBe('slash')
    expect(r.score).toBe(95)
  })

  it('slash 带参 98 不被正则 93 覆盖', () => {
    const idx = makeIdx({ prefix: '/json', matchPattern: '[\\s\\S]*', acceptsInput: true })
    const r = calcPluginScore(idx, '/json {"a":1}', '/json {"a":1}')
    expect(r.matchType).toBe('slash')
    expect(r.score).toBe(98)
  })

  it('7 字符的邮箱也算「内容形态」（门槛 6）：粘邮箱和粘 URL 一样是「带了东西来」', () => {
    const idx = makeIdx({ matchPattern: '^[^@\\s]+@[^@\\s.]+\\.[^@\\s]+$', acceptsInput: true })
    const q = 'a@b.com'
    const r = calcPluginScore(idx, q, q.toLowerCase())
    expect(r.matchType).toBe('match pattern')
    expect(r.score).toBe(93)
    expect(r.inlineInput).toBe(q)
  })

  it('超长输入不跑正则（8KB 上限）：防 [\\s\\S]* 包络的正则回溯卡住主线程', () => {
    // 这条正则能匹配任意字符串；若不设上限，下面这 9000 字符会被它吃掉
    const idx = makeIdx({ matchPattern: '[\\s\\S]*', acceptsInput: true })
    const long = 'x'.repeat(9000)
    const r = calcPluginScore(idx, long, long)
    expect(r.score).toBe(0)
    expect(r.matchType).toBe('')
    // 同一策略下 8KB 以内仍然正常命中
    const short = 'x'.repeat(8000)
    expect(calcPluginScore(idx, short, short).score).toBe(93)
  })

  it('非法正则不炸打分（编译失败 regexValid=false 直接跳过）', () => {
    const idx = makeIdx({ matchPattern: '(' })
    expect(idx.regexValid).toBe(false)
    const r = calcPluginScore(idx, 'anything long enough', 'anything long enough')
    expect(r.score).toBe(0)
  })
})
