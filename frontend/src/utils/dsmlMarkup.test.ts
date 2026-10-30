import { describe, expect, it } from 'vitest'
import { stripDsmlMarkup } from './dsmlMarkup'

// 线上真实抓到的形态：全角双竖线 + 标签名前的空格。
const LIVE = `骨架文件已经建好，我开始逐段填入内容。

<｜｜DSML｜｜ calls>
<｜｜DSML｜｜ invoke name="file_read">
<｜｜DSML｜｜ parameter name="path" string="true">quickdock-guide.md</｜｜DSML｜｜ parameter>
<｜｜DSML｜｜ parameter name="limit" string="false">40</｜｜DSML｜｜ parameter>
</｜｜DSML｜｜ invoke>
</｜｜DSML｜｜ calls>`

describe('stripDsmlMarkup', () => {
  it('剥掉整段工具调用标记，保留正文', () => {
    const out = stripDsmlMarkup(LIVE)
    expect(out).toContain('骨架文件已经建好')
    expect(out).not.toContain('DSML')
    expect(out).not.toContain('invoke')
    expect(out).not.toContain('quickdock-guide.md')
  })

  it('支持规范的单竖线形式', () => {
    const out = stripDsmlMarkup('前文\n<｜DSML｜tool_calls><｜DSML｜invoke name="app_info"></｜DSML｜invoke></｜DSML｜tool_calls>')
    expect(out.trim()).toBe('前文')
  })

  it('没有外层 calls 的孤立 invoke 也剥', () => {
    const out = stripDsmlMarkup('看看端口\n<｜｜DSML｜｜ invoke name="port_list"></｜｜DSML｜｜ invoke>')
    expect(out.trim()).toBe('看看端口')
  })

  it('未闭合的残片剥到结尾', () => {
    const out = stripDsmlMarkup('开头\n<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name="file_read">')
    expect(out.trim()).toBe('开头')
  })

  it('普通内容不受影响', () => {
    const plain = '一段普通回答，含 <b>粗体</b> 与比较符号 1 < 2。'
    expect(stripDsmlMarkup(plain)).toBe(plain)
  })

  it('空值与无标记文本原样返回', () => {
    expect(stripDsmlMarkup('')).toBe('')
    expect(stripDsmlMarkup('就是一句话')).toBe('就是一句话')
  })
})
