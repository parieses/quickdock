import { describe, expect, it } from 'vitest'
import {
  formatBytes,
  formatDuration,
  isFingerprintError,
  isSessionAlive,
  isSessionRetained,
  isMultiLinePaste,
  splitPasteLines,
  sshErrorKey,
  sshErrorText,
  SSH_ERR,
  type SshSessionInfo,
} from './ssh'

function session(over: Partial<SshSessionInfo> = {}): SshSessionInfo {
  return {
    id: 's1',
    hostId: 'h1',
    state: 'connected',
    cols: 80,
    rows: 24,
    received: 0,
    label: 'root@1.2.3.4:22',
    started: Date.now(),
    rttMs: 0,
    ...over,
  }
}

describe('错误码映射', () => {
  it('把已知错误码映射到可操作的文案键', () => {
    expect(sshErrorKey(SSH_ERR.UNREACHABLE)).toBe('sshErrUnreachable')
    expect(sshErrorKey(SSH_ERR.AUTH_FAILED)).toBe('sshErrAuthFailed')
    expect(sshErrorKey(SSH_ERR.NO_AUTH_METHOD)).toBe('sshErrNoAuthMethod')
  })

  it('未知错误码返回空串，由调用方兜底', () => {
    // 不能把未知码映射成 sshErrInternal 再显示——那样调用方分不清
    // 「确实是内部错误」和「后端加了个新码但前端还没跟上」。
    expect(sshErrorKey('ssh_brand_new_code')).toBe('')
    expect(sshErrorKey('')).toBe('')
  })

  it('指纹类错误要能单独识别', () => {
    expect(isFingerprintError(SSH_ERR.HOSTKEY_UNKNOWN)).toBe(true)
    expect(isFingerprintError(SSH_ERR.HOSTKEY_CHANGED)).toBe(true)
    expect(isFingerprintError(SSH_ERR.AUTH_FAILED)).toBe(false)
    expect(isFingerprintError('')).toBe(false)
  })
})

describe('会话状态判定', () => {
  it('只有 connected 才算活着（断开的敲键无效）', () => {
    expect(isSessionAlive(session())).toBe(true)
    expect(isSessionAlive(session({ state: 'disconnected' }))).toBe(false)
    expect(isSessionAlive(session({ state: 'closed' }))).toBe(false)
  })

  it('断开的会话仍算留存（Go 侧留着供回放）', () => {
    expect(isSessionRetained(session())).toBe(true)
    expect(isSessionRetained(session({ state: 'disconnected' }))).toBe(true)
    expect(isSessionRetained(session({ state: 'closed' }))).toBe(false)
  })
})

describe('sshErrorText', () => {
  // 假的 t：直接把键回显出来，便于断言「选了哪个键」。
  const t = (k: string) => `T:${k}`
  const err = (msg: string) => new Error(msg)

  it('已知错误码走映射表', () => {
    expect(sshErrorText(err('sshx: 无法连接到服务器'), SSH_ERR.UNREACHABLE, t)).toBe('T:sshErrUnreachable')
    expect(sshErrorText(err('任意文本'), SSH_ERR.AUTH_FAILED, t)).toBe('T:sshErrAuthFailed')
  })

  it('未知错误码退回原始 message，而不是笼统的「内部错误」', () => {
    // 后端加新码而前端还没跟上时，具体描述比一句「内部错误」有用得多。
    expect(sshErrorText(err('ssh: handshake failed: no common algorithm'), 'ssh_new', t)).toBe(
      'ssh: handshake failed: no common algorithm',
    )
  })

  it('既无映射又无 message 时兜底到内部错误', () => {
    expect(sshErrorText({}, '', t)).toBe('T:sshErrInternal')
    expect(sshErrorText(null, '', t)).toBe('T:sshErrInternal')
  })
})

describe('formatDuration', () => {
  it('按量级切换精度', () => {
    expect(formatDuration(0)).toBe('0s')
    expect(formatDuration(45_000)).toBe('45s')
    expect(formatDuration(90_000)).toBe('1m30s')
    expect(formatDuration(3_600_000)).toBe('1h0m')
    expect(formatDuration(90_000_000)).toBe('1d1h')
  })

  it('非法输入不产生 NaN', () => {
    expect(formatDuration(-1)).toBe('0s')
    expect(formatDuration(NaN)).toBe('0s')
    expect(formatDuration(Infinity)).toBe('0s')
  })
})

describe('formatBytes', () => {
  it('按 1024 进位', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2.0 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB')
  })

  it('非法输入不产生 NaN', () => {
    expect(formatBytes(-5)).toBe('0 B')
    expect(formatBytes(NaN)).toBe('0 B')
  })
})

describe('isMultiLinePaste', () => {
  it('回车键（\r）不算多行粘贴', () => {
    // 这是最容易被误伤的一条：终端里按回车发出来的就是 \r，
    // 按「含换行」判断会让用户每敲一次回车都弹确认框。
    expect(isMultiLinePaste('ls\r')).toBe(false)
    expect(isMultiLinePaste('\r')).toBe(false)
    expect(isMultiLinePaste('ls -la\r')).toBe(false)
  })

  it('末尾换行（\n / \r\n）同样只是提交当前行', () => {
    expect(isMultiLinePaste('ls\n')).toBe(false)
    expect(isMultiLinePaste('ls\r\n')).toBe(false)
  })

  it('连续两个换行夹出空行，算多行', () => {
    // 等价于连按两次回车：shell 会执行两条命令（第二条是空行）。
    expect(isMultiLinePaste('ls\r\n\r\n')).toBe(true)
  })

  it('换行夹在中间才是真的多行', () => {
    expect(isMultiLinePaste('ls\nrm -rf /')).toBe(true)
    expect(isMultiLinePaste('ls\r\nrm -rf /\r\n')).toBe(true)
    expect(isMultiLinePaste('ls\rpwd')).toBe(true)
  })

  it('单行粘贴（带不带换行）都不拦', () => {
    expect(isMultiLinePaste('rm -rf /')).toBe(false)
    expect(isMultiLinePaste('rm -rf /\r')).toBe(false)
  })

  it('能识别 bracketed paste 包裹', () => {
    const wrap = (s: string) => `\u001b[200~${s}\u001b[201~`
    expect(isMultiLinePaste(wrap('ls'))).toBe(false)
    expect(isMultiLinePaste(wrap('ls\r'))).toBe(false)
    expect(isMultiLinePaste(wrap('ls\npwd'))).toBe(true)
    // 包裹里的换行只出现在末尾 → 仍是单行
    expect(isMultiLinePaste(wrap('ls\r\n'))).toBe(false)
  })

  it('空输入不拦', () => {
    expect(isMultiLinePaste('')).toBe(false)
  })
})

describe('splitPasteLines', () => {
  it('单行不触发二次确认的素材（total=1）', () => {
    const r = splitPasteLines('ls -l')
    expect(r.total).toBe(1)
    expect(r.lines).toEqual(['ls -l'])
    expect(r.truncated).toBe(false)
  })

  it('多行按 \\n 切分', () => {
    const r = splitPasteLines('pwd\nls\nwhoami')
    expect(r.total).toBe(3)
    expect(r.lines).toEqual(['pwd', 'ls', 'whoami'])
  })

  it('Windows 的 \\r\\n 不能留下尾随 \\r', () => {
    // 只按 \n 切的话每行末尾会挂一个 \r，弹窗里显示成「ls^M」很难读，
    // 而且行数统计会偏。
    const r = splitPasteLines('pwd\r\nls -l\r\n')
    expect(r.lines).toEqual(['pwd', 'ls -l'])
    expect(r.total).toBe(2)
  })

  it('结尾换行产生的空串不算一行', () => {
    expect(splitPasteLines('a\nb\n').total).toBe(2)
    expect(splitPasteLines('a\n\nb').total).toBe(3)
  })

  it('超过预览上限时截断并标记', () => {
    const data = Array.from({ length: 20 }, (_, i) => `cmd${i}`).join('\n')
    const r = splitPasteLines(data, 6)
    expect(r.total).toBe(20)
    expect(r.lines).toHaveLength(6)
    expect(r.lines[0]).toBe('cmd0')
    expect(r.truncated).toBe(true)
  })

  it('空内容不产生行', () => {
    expect(splitPasteLines('').total).toBe(0)
    expect(splitPasteLines('\n').total).toBe(0)
  })
})
