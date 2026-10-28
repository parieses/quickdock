#!/usr/bin/env python3
"""检查中英两页的结构有没有走偏。

用法（在 website/ 目录下）：

    python tools/check-parity.py

英文版是独立的一份 HTML（不是运行时替换文本），好处是不用维护几百个
「中文原文当 key」的映射，代价是两份会漂移：给中文页加了一段，忘了英文页，
页面看着正常，只是少一截内容，不容易发现。

这个脚本只比结构，不比文字：把两页的标签序列拉平，逐个对齐，再比 id 集合。
对不上就打印第一个分歧的位置和前后几个标签，去那儿看就行。

改文案不会让脚本报错；加删元素、改嵌套才会。
"""

import io
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

PAIRS = [
    ('index.html', os.path.join('en', 'index.html')),
    ('guide.html', os.path.join('en', 'guide.html')),
]


def read(path):
    with io.open(os.path.join(ROOT, path), encoding='utf-8') as f:
        return f.read()


def tag_sequence(html):
    """拉平成 [(是闭合标签, 标签名)]，跳过注释、脚本与只有属性的差异。"""
    html = re.sub(r'<!--.*?-->', '', html, flags=re.S)
    html = re.sub(r'<script\b.*?</script>', '', html, flags=re.S)
    seq = []
    for m in re.finditer(r'<(/?)([a-zA-Z][\w-]*)', html):
        seq.append((m.group(1) == '/', m.group(2).lower()))
    return seq


def ids(html):
    html = re.sub(r'<!--.*?-->', '', html, flags=re.S)
    return set(re.findall(r'\sid="([^"]+)"', html))


def describe(seq, i):
    lo = max(0, i - 3)
    return ' → '.join(('/' if c else '') + n for c, n in seq[lo:i + 3])


def main():
    failed = False

    for zh_rel, en_rel in PAIRS:
        zh, en = read(zh_rel), read(en_rel)
        a, b = tag_sequence(zh), tag_sequence(en)

        if len(a) != len(b):
            print('%s: 标签数不同，中 %d / 英 %d' % (zh_rel, len(a), len(b)))

        bad = None
        for i in range(min(len(a), len(b))):
            if a[i] != b[i]:
                bad = i
                break

        if bad is None and len(a) == len(b):
            print('%s: 结构一致（%d 个标签）' % (zh_rel, len(a)))
        else:
            failed = True
            idx = bad if bad is not None else min(len(a), len(b))
            print('%s: 结构从第 %d 个标签起分叉' % (zh_rel, idx))
            print('  中文: ' + describe(a, idx))
            print('  英文: ' + describe(b, idx))

        only_zh = ids(zh) - ids(en)
        only_en = ids(en) - ids(zh)
        if only_zh or only_en:
            failed = True
            print('  id 只在中文页: %s' % sorted(only_zh))
            print('  id 只在英文页: %s' % sorted(only_en))

    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
