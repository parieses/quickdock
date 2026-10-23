import { describe, expect, it } from 'vitest'
import {
  baseName,
  entryKind,
  filterEntries,
  formatEntrySize,
  formatMtime,
  isPreviewable,
  joinPath,
  looksBinary,
  parentOf,
  permOctal,
  splitPath,
  type SftpEntry,
} from './sshSftp'

function entry(over: Partial<SftpEntry> = {}): SftpEntry {
  return {
    name: 'a.txt',
    path: '/tmp/a.txt',
    size: 1024,
    mode: '-rw-r--r--',
    modTime: 0,
    isDir: false,
    isLink: false,
    linkTo: '',
    ext: 'txt',
    ...over,
  }
}

describe('splitPath', () => {
  it('根目录只有一段', () => {
    expect(splitPath('/')).toEqual([{ name: '/', path: '/' }])
  })

  it('空值不炸', () => {
    expect(splitPath('')).toEqual([])
    expect(splitPath(undefined as unknown as string)).toEqual([])
  })

  it('逐层累积绝对路径', () => {
    expect(splitPath('/var/log/nginx')).toEqual([
      { name: '/', path: '/' },
      { name: 'var', path: '/var' },
      { name: 'log', path: '/var/log' },
      { name: 'nginx', path: '/var/log/nginx' },
    ])
  })

  it('结尾多余斜杠不产生空段', () => {
    expect(splitPath('/etc/')).toEqual([
      { name: '/', path: '/' },
      { name: 'etc', path: '/etc' },
    ])
  })

  it('中间连续斜杠被合并', () => {
    expect(splitPath('//var///log')).toEqual([
      { name: '/', path: '/' },
      { name: 'var', path: '/var' },
      { name: 'log', path: '/var/log' },
    ])
  })
})

describe('parentOf', () => {
  it('普通路径上一层的 Klin上一层', () => {
    expect(parentOf('/var/log/nginx')).toBe('/var/log')
  })

  it('一层目录回到根', () => {
    expect(parentOf('/var')).toBe('/')
  })

  it('根一直是根', () => {
    expect(parentOf('/')).toBe('/')
  })

  it('结尾斜杠先剥掉', () => {
    expect(parentOf('/var/log/')).toBe('/var')
  })
})

describe('joinPath', () => {
  it('目录带尾斜杠不出现双斜杠', () => {
    expect(joinPath('/tmp/', 'a.txt')).toBe('/tmp/a.txt')
  })

  it('name 里夹路径只取末段', () => {
    expect(joinPath('/tmp', 'sub/dir/a.txt')).toBe('/tmp/a.txt')
  })

  it('拼接后的结果不带尾斜杠', () => {
    expect(joinPath('/var/log', 'nginx')).toBe('/var/log/nginx')
  })

  it('目录为空时原样返回', () => {
    expect(joinPath('', 'a.txt')).toBe('')
  })

  it('name 为斜杠时不拼出怪路径', () => {
    expect(joinPath('/tmp', '/')).toBe('/tmp')
  })
})

describe('baseName', () => {
  it('默认保留扩展名', () => {
    expect(baseName('/var/log/a.log')).toBe('a.log')
  })

  it('可去掉扩展名', () => {
    expect(baseName('/var/log/a.log', false)).toBe('a')
  })

  it('没有扩展名时原样返回', () => {
    expect(baseName('/etc/hosts', false)).toBe('hosts')
  })

  it('隐藏文件不是扩展名', () => {
    // .gitignore 以点开头但没有「名字 + 扩展名」结构，去掉会变成空串。
    expect(baseName('/repo/.gitignore', false)).toBe('.gitignore')
  })

  it('目录尾斜杠先剥掉', () => {
    expect(baseName('/var/log/')).toBe('log')
  })
})

describe('filterEntries', () => {
  const list = [
    entry({ name: 'nginx.conf', ext: 'conf' }),
    entry({ name: 'access.log', ext: 'log' }),
    entry({ name: 'certs', isDir: true, ext: '' }),
  ]

  it('空关键词不过滤', () => {
    expect(filterEntries(list, '')).toBe(list)
    expect(filterEntries(list, '   ')).toBe(list)
  })

  it('按名称模糊匹配且忽略大小写', () => {
    expect(filterEntries(list, 'LOG').map(e => e.name)).toEqual(['access.log'])
  })

  it('目录也参与过滤', () => {
    expect(filterEntries(list, 'cert').map(e => e.name)).toEqual(['certs'])
  })

  it('无匹配返回空数组', () => {
    expect(filterEntries(list, 'zzz')).toEqual([])
  })
})

describe('isPreviewable', () => {
  it('目录不可预览', () => {
    expect(isPreviewable(entry({ isDir: true, ext: '' }))).toBe(false)
  })

  it('常见二进制不可预览', () => {
    for (const ext of ['png', 'zip', 'mp4', 'pdf', 'exe']) {
      expect(isPreviewable(entry({ ext }))).toBe(false)
    }
  })

  it('无扩展名按可预览处理（conf / Dockerfile 一类）', () => {
    expect(isPreviewable(entry({ ext: '' }))).toBe(true)
  })

  it('配置文件可预览', () => {
    expect(isPreviewable(entry({ ext: 'conf' }))).toBe(true)
  })
})

describe('entryKind', () => {
  it('目录类型由调用方判，这里走 text 兜底', () => {
    expect(entryKind(entry({ isDir: true, ext: '' }))).toBe('text')
  })

  it('各类扩展名归桶', () => {
    expect(entryKind(entry({ ext: 'tar' }))).toBe('archive')
    expect(entryKind(entry({ ext: 'png' }))).toBe('image')
    expect(entryKind(entry({ ext: 'mp4' }))).toBe('video')
    expect(entryKind(entry({ ext: 'log' }))).toBe('log')
    expect(entryKind(entry({ ext: 'yaml' }))).toBe('config')
    expect(entryKind(entry({ ext: 'go' }))).toBe('code')
    expect(entryKind(entry({ ext: 'whatever' }))).toBe('text')
  })
})

describe('formatMtime', () => {
  // 固定「现在」：2026-10-19 12:00（本地时区）
  const now = new Date(2026, 9, 19, 12, 0, 0).getTime()

  it('零值是占位符', () => {
    expect(formatMtime(0, now)).toBe('-')
  })

  it('同一天只显示时间', () => {
    const t = Math.floor(new Date(2026, 9, 19, 9, 5).getTime() / 1000)
    expect(formatMtime(t, now)).toBe('今天 09:05')
  })

  it('昨天显示昨天', () => {
    const t = Math.floor(new Date(2026, 9, 18, 22, 30).getTime() / 1000)
    expect(formatMtime(t, now)).toBe('昨天 22:30')
  })

  it('更早按日期 + 时间', () => {
    const t = Math.floor(new Date(2026, 2, 11, 8, 7).getTime() / 1000)
    expect(formatMtime(t, now)).toBe('2026-03-11 08:07')
  })
})

describe('formatEntrySize', () => {
  it('目录显示占位符', () => {
    expect(formatEntrySize(entry({ isDir: true, size: 4096 }))).toBe('-')
  })

  it('文件走通用字节格式化', () => {
    expect(formatEntrySize(entry({ size: 1024 }))).toBe('1.0 KB')
  })
})

describe('permOctal', () => {
  it('标准权限转八进制', () => {
    expect(permOctal('-rw-r--r--')).toBe('644')
    expect(permOctal('drwxr-xr-x')).toBe('755')
    expect(permOctal('-rwx------')).toBe('700')
  })

  it('setuid / sticky 位计入执行位', () => {
    expect(permOctal('-rwsr-xr-x')).toBe('755')
    expect(permOctal('drwxrwxrwt')).toBe('777')
  })

  it('无权限位', () => {
    expect(permOctal('-r--------')).toBe('400')
  })

  it('mode 太短返回空串', () => {
    expect(permOctal('??')).toBe('')
    expect(permOctal('')).toBe('')
  })
})

describe('looksBinary', () => {
  it('普通文本 / 空内容不算二进制', () => {
    expect(looksBinary('')).toBe(false)
    expect(looksBinary('server {\n  listen 80;\n}\n')).toBe(false)
  })

  it('中文与 tab、CRLF 都是合法文本', () => {
    expect(looksBinary('# 中文注释\r\n\tkey = 值\r\n')).toBe(false)
  })

  it('出现 NUL 直接判定二进制（最强信号）', () => {
    expect(looksBinary('abc\u0000def')).toBe(true)
  })

  it('零散控制字符不到 2% 仍按文本处理', () => {
    // 100 个字符里 1 个 \u0007，不足以判定——终端转义残留很常见。
    expect(looksBinary('a'.repeat(99) + '\u0007')).toBe(false)
  })

  it('控制字符成片出现判定二进制', () => {
    expect(looksBinary('\u0001\u0002\u0003\u0004abcdefgh')).toBe(true)
  })

  it('U+FFFD 成片出现判定二进制（原文不是合法 UTF-8）', () => {
    expect(looksBinary('\ufffd'.repeat(10) + 'abc')).toBe(true)
  })

  it('只采样前 8KB，尾部是二进制不影响开头是文本的判定', () => {
    // 这是刻意行为：只影响要不要拦，不影响正确性——真保存二进制时
    // 前面的 NUL 判定已经拦住了绝大多数情况，此处锁定「采样」这个契约。
    expect(looksBinary('a'.repeat(9000) + '\u0000')).toBe(false)
  })
})
