/**
 * SSH AI 面板分段逻辑的回归测试。
 *
 * 这些断言直接对应用户实际遇到的现象：
 *   - 正文中间出现一大片空白（旧实现剥围栏后留下 3~4 行连续空行）
 *   - **加粗** 显示成星号（旧实现用纯文本渲染，没走 markdown）
 *   - 模型给了命令但界面上没有（旧实现要求配对围栏，未闭合时整条丢失）
 */
import { describe, test, expect } from 'vitest'
import { Marked } from 'marked'
import { splitSshAiSegments } from './sshAiSegments'

const md = new Marked({ breaks: true, gfm: true })

describe('splitSshAiSegments', () => {
  test('按原始顺序切分说明与命令', () => {
    const segs = splitSshAiSegments(
      '先探测端口：\n```bash\nss -tlnp | grep :80\n```\n再查服务：\n```bash\nsystemctl status nginx\n```\n拿到结果再判断。\n'
    )
    expect(segs.map((s) => s.kind)).toEqual(['text', 'cmd', 'text', 'cmd', 'text'])
    const cmds = segs.filter((s) => s.kind === 'cmd')
    expect(cmds[0]).toEqual({ kind: 'cmd', commands: ['ss -tlnp | grep :80'] })
    expect(cmds[1]).toEqual({ kind: 'cmd', commands: ['systemctl status nginx'] })
  })

  test('说明文字段不含空行残留（修掉"大片空白"）', () => {
    const segs = splitSshAiSegments('说明一\n\n```bash\ndf -h\n```\n\n说明二\n')
    const texts = segs.filter((s) => s.kind === 'text')
    for (const t of texts) {
      if (t.kind !== 'text') continue
      // 不应出现连续 3 个以上换行（旧实现的剥围栏残留）
      expect(t.text).not.toMatch(/\n{3,}/)
      expect(t.text.trim()).toBe(t.text)
    }
  })

  test('说明文字段保留 markdown 标记，交给渲染器处理', () => {
    const segs = splitSshAiSegments('说明**加粗**部分。\n\n```bash\ndf -h\n```\n')
    const t = segs[0]
    expect(t.kind).toBe('text')
    if (t.kind !== 'text') return
    expect(t.text).toContain('**加粗**')
    // 渲染后不应再出现字面量星号
    const html = md.parse(t.text, { async: false }) as string
    expect(html).toContain('<strong>加粗</strong>')
    expect(html).not.toContain('**')
  })

  test('未闭合围栏中的命令不被丢弃', () => {
    const segs = splitSshAiSegments('这条命令查看当前监听80端口的进程。\n\n```bash\nnetstat -tlnp | grep 80\n')
    const cmds = segs.flatMap((s) => (s.kind === 'cmd' ? s.commands : []))
    expect(cmds).toContain('netstat -tlnp | grep 80')
  })

  test('支持 ~~~ 围栏', () => {
    const segs = splitSshAiSegments('~~~bash\nfree -m\n~~~')
    expect(segs).toEqual([{ kind: 'cmd', commands: ['free -m'] }])
  })

  test('行内代码不被误判为围栏', () => {
    const segs = splitSshAiSegments('命令 `netstat -tlnp | grep 80` 没有输出。\n\n```bash\nss -tlnp\n```')
    const cmds = segs.flatMap((s) => (s.kind === 'cmd' ? s.commands : []))
    expect(cmds).toEqual(['ss -tlnp'])
  })

  test('注释行不作为命令', () => {
    const segs = splitSshAiSegments('```bash\n# 先看状态\nps aux | grep nginx\n```')
    expect(segs).toEqual([{ kind: 'cmd', commands: ['ps aux | grep nginx'] }])
  })

  // 以下用例与后端 services/sshx/ai_test.go 的 TestExtractCommands_ShellPromptPrefix /
  // TestExtractCommands_PromptRescueBoundaries **逐条对应**。
  // 前后端各有一份判定实现，用例不一一对应的话，改了一边另一边会静默漂移。

  test('剥掉 shell 提示符前缀', () => {
    const segs = splitSshAiSegments(
      '先看：\n```bash\n$ df -h\n```\n再装：\n```bash\n# apt-get install nginx\n```\n'
    )
    const cmds = segs.filter((s) => s.kind === 'cmd')
    expect(cmds[0]).toEqual({ kind: 'cmd', commands: ['df -h'] })
    expect(cmds[1]).toEqual({ kind: 'cmd', commands: ['apt-get install nginx'] })
  })

  test('纯中文注释块不被救成命令', () => {
    expect(splitSshAiSegments('```bash\n# 这是说明\n# 也是说明\n```')).toEqual([])
  })

  test('shebang 不是命令', () => {
    expect(splitSshAiSegments('```bash\n#!/bin/bash\nset -e\n```')).toEqual([
      { kind: 'cmd', commands: ['set -e'] },
    ])
  })

  test('$ 后无空白不是提示符', () => {
    expect(splitSshAiSegments('```bash\n$HOME/bin/deploy\n```')).toEqual([
      { kind: 'cmd', commands: ['$HOME/bin/deploy'] },
    ])
  })

  test('整块 root 提示符才回扫', () => {
    expect(
      splitSshAiSegments('```bash\n# apt-get install nginx\n# systemctl enable nginx\n```')
    ).toEqual([{ kind: 'cmd', commands: ['apt-get install nginx', 'systemctl enable nginx'] }])
  })

  test('块内有不带前缀的行就按注释处理', () => {
    expect(splitSshAiSegments('```bash\n# cp a b\nls -l\n```')).toEqual([
      { kind: 'cmd', commands: ['ls -l'] },
    ])
  })

  test('跨块去重', () => {
    // 第二个围栏里的 df -h 被去重后不产出 cmd 段，只剩中间的说明文字——
    // 而不是再渲染一张一模一样的命令卡片。
    expect(splitSshAiSegments('```bash\ndf -h\n```\n重复：\n```bash\ndf -h\n```')).toEqual([
      { kind: 'cmd', commands: ['df -h'] },
      { kind: 'text', text: '重复：' },
    ])
  })

  test('空输入返回空数组', () => {
    expect(splitSshAiSegments('')).toEqual([])
    expect(splitSshAiSegments('   \n\n  ')).toEqual([])
  })

  test('不产生空段（避免界面莫名空隙）', () => {
    const segs = splitSshAiSegments('\n\n```bash\n\n```\n\n说明\n\n')
    for (const s of segs) {
      if (s.kind === 'text') expect(s.text.trim()).not.toBe('')
      else expect(s.commands.length).toBeGreaterThan(0)
    }
  })
})
