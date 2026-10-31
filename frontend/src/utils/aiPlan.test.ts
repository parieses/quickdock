import { describe, expect, it } from 'vitest'
import { parsePlanSections, planOf, planStepIndex } from './aiPlan'

const SRC = `### 计划
1. 读一下配置
2. 改端口

### 涉及文件
- nginx.conf

### 风险
- 端口可能被占用
`

describe('parsePlanSections', () => {
  it('按标题拆成小节，列表项与正文分开', () => {
    const secs = parsePlanSections(SRC)
    expect(secs).not.toBeNull()
    expect(secs!.map((s) => s.title)).toEqual(['计划', '涉及文件', '风险'])
    expect(secs![0].items).toEqual(['读一下配置', '改端口'])
    expect(secs![1].items).toEqual(['nginx.conf'])
  })

  it('围栏里的 # 与 - 不当作标题/列表项', () => {
    const src = ['### 计划', '```bash', '# 这是注释', '- 这是参数', '```', '', '### 风险', '- 无'].join('\n')
    const secs = parsePlanSections(src)
    expect(secs).not.toBeNull()
    // 注释与参数进了正文，没有被拆成「章节」或「列表项」
    expect(secs!.map((s) => s.title)).toEqual(['计划', '风险'])
    expect(secs![0].items).toEqual([])
    expect(secs![0].text).toContain('# 这是注释')
  })

  it('少于两节视为普通回答（回落 markdown）', () => {
    expect(parsePlanSections('## 只有一个标题\n随便说两句')).toBeNull()
    expect(parsePlanSections('没有任何标题的回答')).toBeNull()
  })
})

describe('planStepIndex', () => {
  it('优先取标题命中「计划/步骤」且有列表项的那一节', () => {
    const secs = parsePlanSections(SRC)!
    expect(planStepIndex(secs)).toBe(0)
  })

  it('没有命中标题时取第一个有列表项的节', () => {
    const secs = parsePlanSections('### 甲\n- a\n\n### 乙\n- b\n')!
    expect(planStepIndex(secs)).toBe(0)
  })

  it('全都没有列表项时返回 -1', () => {
    const secs = parsePlanSections('### 甲\n文字\n\n### 乙\n文字\n')!
    expect(planStepIndex(secs)).toBe(-1)
  })
})

describe('planOf', () => {
  it('只对 assistant 消息生效', () => {
    const user = { id: 'u1', role: 'user', content: SRC }
    expect(planOf(user)).toBeNull()
  })

  it('同一内容重复调用命中缓存，返回同一引用', () => {
    const m = { id: 'a1', role: 'assistant', content: SRC }
    expect(planOf(m)).toBe(planOf(m))
  })
})
