#!/usr/bin/env python3
"""列出 index.html 里还没进 i18n 字典的中文文案。

用法（在 website/ 目录下）：
    python tools/extract-i18n-strings.py            # 只列缺的
    python tools/extract-i18n-strings.py --all      # 连已覆盖的一起列

原理：英文词典以「中文原文」为 key，所以新增文案时不用改 HTML、不用打标记，
把这里列出的中文原文翻好填进 assets/js/i18n.js 即可。查表时会折叠空白，
所以字典里的 key 写成单行、词间单空格就行。
"""
import io
import os
import re
import sys
from html.parser import HTMLParser

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
SKIP_TAGS = {'script', 'style', 'pre', 'code', 'kbd', 'samp', 'textarea', 'svg', 'noscript'}
CJK = re.compile(r'[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]')


class TextNodes(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.depth = 0
        self.texts = []

    def handle_starttag(self, tag, attrs):
        if tag in SKIP_TAGS:
            self.depth += 1

    def handle_endtag(self, tag):
        if tag in SKIP_TAGS and self.depth:
            self.depth -= 1

    def handle_data(self, data):
        if self.depth:
            return
        t = data.strip()
        if t and CJK.search(t):
            self.texts.append(t)


def dict_keys(path):
    """从 i18n.js 里抠出 en 段的键（粗略但够用）"""
    src = io.open(path, encoding='utf-8').read()
    try:
        en = src[src.index('en: {'):src.index('/* 属性类文案')]
    except ValueError:
        en = src
    return set(re.findall(r'^\s{4}"((?:[^"\\]|\\.)*)": ', en, re.M))


def main():
    show_all = '--all' in sys.argv
    p = TextNodes()
    p.feed(io.open(os.path.join(ROOT, 'index.html'), encoding='utf-8').read())
    keys = dict_keys(os.path.join(ROOT, 'assets/js/i18n.js'))

    seen, missing = set(), []
    for t in p.texts:
        norm = re.sub(r'\s+', ' ', t).strip()
        if norm in seen or norm in keys:
            continue
        seen.add(norm)
        missing.append(norm)

    mode = '全部中文文案' if show_all else '未翻译'
    print('%s：%d 条' % (mode, len(seen) if show_all else len(missing)))
    for s in (seen if show_all else missing):
        print('  ' + s)


if __name__ == '__main__':
    main()
