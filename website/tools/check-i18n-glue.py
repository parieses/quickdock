#!/usr/bin/env python3
"""检查英文词典有没有「黏连」：行内标签两侧的英文值首尾漏了空格。

用法（在 website/ 目录下）：
    python tools/check-i18n-glue.py

原理：`main.js` 按「中文原文」逐文本节点替换，节点之间**没有任何自动补空格**。
像 `<p>两条路：<b>单窗口贴屏</b>用热键，</p>` 这种，中文不需要空格，但英文
三个片段拼起来就成了 `Two routes: one-window snappingwith hotkeys, and`。

判断办法（本脚本做的事）：
1. 按 `main.js` 同样的规则展平 HTML，得到「文本节点 / 标签」序列；
2. 取相邻两个「词典命中的文本节点」A、B，若它们之间只隔着行内标签，
   且英文值 A 以字母数字结尾、B 以字母数字开头，就会黏在一起；
3. **排除夹着 `<span>` 的情况**——本站的 `<span class="k">` + `<span>` 是
   `.tbl-row` 的并排格子（各自独立成列，不会黏），套用会全是假阳性。
   `<b>` / `<kbd>` / `<code>` 才真正出现在正文流里。

修法：给 A 的英文值末尾补一个空格（中文字里没有空格，英文必须有）。
"""

import io
import os
import re
import sys
from html.parser import HTMLParser

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

SKIP_TAGS = {'script', 'style', 'pre', 'code', 'kbd', 'samp', 'textarea', 'svg', 'noscript'}
INLINE_TAGS = {'b', 'strong', 'em', 'i', 'span', 'a', 'small', 'sup', 'sub', 'u', 's',
               'mark', 'abbr', 'time', 'var', 'label', 'cite', 'q'}
# 用 <span> 隔开的是并排格子，不算正文流
CELL_TAGS = {'span'}

if hasattr(sys.stdout, 'reconfigure'):
    sys.stdout.reconfigure(encoding='utf-8', errors='replace')


class Flatten(HTMLParser):
    """展平成 [(kind, text, tag), ...]：kind 为 'text' 或 'tag'。"""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.depth = 0
        self.atoms = []

    def _skip(self, tag, delta):
        if tag in SKIP_TAGS:
            self.depth = max(0, self.depth + delta)

    def handle_starttag(self, tag, attrs):
        self.atoms.append(('tag', tag))
        self._skip(tag, 1)

    def handle_startendtag(self, tag, attrs):
        self.atoms.append(('tag', tag))

    def handle_endtag(self, tag):
        self.atoms.append(('tag', tag))
        self._skip(tag, -1)

    def handle_data(self, data):
        if not self.depth:
            self.atoms.append(('text', data))


def norm(s):
    return re.sub(r'\s+', ' ', s).strip()


def load_dict(path):
    src = io.open(path, encoding='utf-8').read()
    try:
        en = src[src.index('en: {'):src.index('/* 属性类文案')]
    except ValueError:
        en = src
    out = {}
    for m in re.finditer(r'^\s{4}"((?:[^"\\]|\\.)*)": "((?:[^"\\]|\\.)*)",\s*$', en, re.M):
        out[m.group(1).replace('\\"', '"')] = m.group(2).replace('\\"', '"')
    return out


def main():
    html = io.open(os.path.join(ROOT, 'index.html'), encoding='utf-8').read()
    dict_ = load_dict(os.path.join(ROOT, 'assets/js/i18n.js'))

    p = Flatten()
    p.feed(html)
    atoms = p.atoms

    cand = [(i, a[1]) for i, a in enumerate(atoms)
            if a[0] == 'text' and a[1] and norm(a[1]) in dict_]

    bad = []
    for (i, ka), (j, kb) in zip(cand, cand[1:]):
        if j == i + 1:          # 中间没有标签 → 原文本身就连着，中文里也连着，不怪词典
            continue
        mid = atoms[i + 1:j]
        midtext = ''.join(a[1] for a in mid if a[0] == 'text' and a[1])
        if midtext and midtext[0].isspace():
            continue            # 中间有空白
        tags = [a[1] for a in mid if a[0] == 'tag']
        if not tags or not all(t in INLINE_TAGS for t in tags):
            continue            # 夹着块级标签 → 各自成块
        if any(t in CELL_TAGS for t in tags):
            continue            # 并排格子（.tbl-row 的 span.k + span）
        if ka[-1].isspace():
            continue            # 原文已有空白
        a_en, b_en = dict_[norm(ka)], dict_[norm(kb)]
        if a_en and b_en and a_en[-1].isalnum() and b_en[0].isalnum():
            bad.append((ka, kb, a_en, b_en))

    if not bad:
        print('✅ 没有发现黏连')
        return 0
    print('⚠️  发现 %d 处黏连（给前一条英文值末尾补空格）：' % len(bad))
    for ka, kb, a_en, b_en in bad:
        print('  A: %s' % ka[:50])
        print('     英文尾：%r  → 建议改成 %r' % (a_en[-20:], a_en + ' '))
        print('  B: %s' % kb[:50])
    return 1


if __name__ == '__main__':
    sys.exit(main())
