#!/usr/bin/env python3
"""发版时同步官网的版本号与更新日志。

用法（在 website/ 目录下）：

    python tools/bump.py 0.0.31              # 日期取今天
    python tools/bump.py 0.0.31 2026-11-02   # 指定日期

它做三件事：

1. 改 assets/js/site.js 的 version / released —— 这是全站唯一的版本来源，
   首屏版本行、下载区、更新日志第一条都由它填。
2. 把现有日志条目里的 data-ver / data-ver-date 固化成字面量：上一条「当前版」
   从此变成历史记录，不会再跟着版本号跑。
3. 在 #log 顶部插一条新条目（默认展开），标题与正文留 TODO。中英两页各一份。

跑完去两个 index.html 里填 TODO，再 `git diff` 看一眼有没有插错位置。

注意：脚本靠 `<div class="logs rise">` 和 `<details class="log" open?>` 定位，
改动更新日志的 HTML 结构时记得回来对一下。
"""

import io
import os
import re
import sys
from datetime import date

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

SITE_JS = os.path.join(ROOT, 'assets', 'js', 'site.js')
PAGES = [
    (os.path.join(ROOT, 'index.html'), 'zh'),
    (os.path.join(ROOT, 'en', 'index.html'), 'en'),
]

# 新条目的骨架。缩进跟现有条目对齐（6 空格进 <details>）
ENTRY = {
    'zh': """      <details class="log" open>
        <summary>
          <span class="log-ver" data-ver></span>
          <span class="log-date" data-ver-date></span>
          <span class="log-title">TODO：一句话概括这一版</span>
          <svg class="log-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 6 6 6-6 6"/></svg>
        </summary>
        <ul class="log-body">
          <li>TODO：用户能感知到什么，一句话一条</li>
        </ul>
      </details>

""",
    'en': """      <details class="log" open>
        <summary>
          <span class="log-ver" data-ver></span>
          <span class="log-date" data-ver-date></span>
          <span class="log-title">TODO: one line about this release</span>
          <svg class="log-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 6 6 6-6 6"/></svg>
        </summary>
        <ul class="log-body">
          <li>TODO: one user-visible change per line</li>
        </ul>
      </details>

""",
}

LOGS_ANCHOR = '<div class="logs rise">\n'
FROZEN_VER = '<span class="log-ver" data-ver></span>'
FROZEN_DATE = '<span class="log-date" data-ver-date></span>'
OPEN_DETAILS = '<details class="log" open>'


def read(path):
    with io.open(path, encoding='utf-8') as f:
        return f.read()


def write(path, text):
    with io.open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(text)


def rel(path):
    """报错时用 index.html / en/index.html 区分，光看 basename 两个都叫 index.html。"""
    return os.path.relpath(path, ROOT).replace(os.sep, '/')


def die(msg):
    sys.stderr.write('bump: ' + msg + '\n')
    sys.exit(1)


def main():
    args = [a for a in sys.argv[1:] if not a.startswith('-')]
    if not args:
        die('用法: python tools/bump.py <版本> [YYYY-MM-DD]')

    ver = args[0] if args[0].startswith('v') else 'v' + args[0]
    if not re.fullmatch(r'v\d+\.\d+\.\d+', ver):
        die('版本号要写成 0.0.31 或 v0.0.31，收到 ' + args[0])

    day = args[1] if len(args) > 1 else date.today().isoformat()
    if not re.fullmatch(r'\d{4}-\d{2}-\d{2}', day):
        die('日期要写成 2026-11-02，收到 ' + day)

    site = read(SITE_JS)
    m = re.search(r"version:\s*'([^']+)'", site)
    if not m:
        die('assets/js/site.js 里找不到 version 字段')
    old_ver = m.group(1)
    m = re.search(r"released:\s*'([^']+)'", site)
    old_day = m.group(1) if m else day

    if old_ver == ver:
        die('site.js 已经是 ' + ver + '，没什么要改的')

    # 现有条目还没填内容就直接跑第二次，会把空条目固化成历史
    for path, _ in PAGES:
        html = read(path)
        head = html.split(LOGS_ANCHOR, 1)
        if len(head) != 2:
            die(rel(path) + ' 里找不到 <div class="logs rise">')
        if 'class="log-title">TODO' in head[1]:
            die(rel(path) + ' 的最新一条还是 TODO，先填完再打下一个版本')

    site = re.sub(r"version:\s*'[^']*'", "version: '" + ver + "'", site, count=1)
    site = re.sub(r"released:\s*'[^']*'", "released: '" + day + "'", site, count=1)
    write(SITE_JS, site)

    for path, lang in PAGES:
        html = read(path)

        # 1. 旧的第一条收起，不再默认展开
        if OPEN_DETAILS not in html:
            die(rel(path) + ' 里没有展开的日志条目（<details class="log" open>）')
        html = html.replace(OPEN_DETAILS, '<details class="log">', 1)

        # 2. 旧的「当前版」固化：data-ver 换成写死的文本
        if FROZEN_VER not in html or FROZEN_DATE not in html:
            die(rel(path) + ' 里找不到 data-ver / data-ver-date 的日志条目')
        html = html.replace(FROZEN_VER, '<span class="log-ver">' + old_ver + '</span>', 1)
        html = html.replace(FROZEN_DATE, '<span class="log-date">' + old_day + '</span>', 1)

        # 3. 插入新条目
        html = html.replace(LOGS_ANCHOR, LOGS_ANCHOR + ENTRY[lang], 1)

        count = len(re.findall(r'<details class="log"', html))
        if count > 4:
            sys.stdout.write(
                '  提醒：%s 现在有 %d 条日志，README 的约定是只留最近四条\n'
                % (rel(path), count)
            )

        write(path, html)

    print('版本 %s → %s，日期 %s → %s' % (old_ver, ver, old_day, day))
    print('已插入新日志条目，接下来：')
    print('  1. 填 index.html 与 en/index.html 里的 TODO（标题 + 正文）')
    print('  2. git diff website 看一眼插入位置')
    print('  3. 版本号本身不用再改别处，site.js 是唯一来源')


if __name__ == '__main__':
    main()
