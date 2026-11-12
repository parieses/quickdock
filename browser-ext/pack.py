#!/usr/bin/env python3
"""把 browser-ext 打成可上传 Chrome Web Store / 可直接拖进浏览器的 zip。

用法：
    python browser-ext/pack.py            # 源码有改动才出包
    python browser-ext/pack.py --force    # 无条件重打

产物：
    browser-ext/dist/quickdock-vault-extension-v<tag>.zip

版本号不写在源码里：manifest.json 的 version 恒为 0.0.0，打包时按
`git describe --tags` 注入当前 tag（v0.3.0 → 0.3.0）。扩展跟桌面同一个
版本号，不需要额外记一套 ext-v*。

三个刻意的选择：

1. 不用 Compress-Archive / 系统 zip —— Windows 上它们可能写出反斜杠分隔符，
   商店上传和部分解压器会认错路径，而报错信息不会告诉你问题出在分隔符。
2. 用白名单而不是排除清单 —— keys/ 里是签名私钥，万一漏进分发包，
   等于把扩展 ID 的所有权送人。默认不收，比事后检查可靠。
3. 源码摘要没变就跳过 —— 插件没改就不该产出一个只换了版本号的空包，
   免得用户在商店后台分不清该传哪个。
"""

import hashlib
import json
import os
import re
import subprocess
import sys
import zipfile

BASE = os.path.dirname(os.path.abspath(__file__))
INCLUDE_FILES = ('manifest.json', 'README.md')
INCLUDE_DIRS = ('src', 'icons')
DIST = os.path.join(BASE, 'dist')
STATE_FILE = os.path.join(DIST, '.pack-state.json')


def run_git(*args):
    try:
        return subprocess.run(
            ('git',) + args,
            cwd=BASE, capture_output=True, text=True, check=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError) as e:
        detail = getattr(e, 'stderr', '') or str(e)
        sys.exit(f'git {args[0]} 失败：{detail.strip()}')


def resolve_version():
    """当前 tag → Chrome 版本号。同时提示「HEAD 已经领先 tag」。"""
    # --long 形式是 v0.0.48-3-gabc1234，末段是「距该 tag 多少个提交」。
    desc = run_git('describe', '--tags', '--long')
    m = re.fullmatch(r'v?(\d+(?:\.\d+){0,3})-(\d+)-g[0-9a-f]+', desc)
    if not m:
        sys.exit(f'看不懂 git describe 的输出：{desc}（tag 形如 v0.0.48）')
    version, ahead = m.group(1), int(m.group(2))
    if not re.fullmatch(r'\d{1,5}(\.\d{1,5}){0,3}', version):
        sys.exit(f'tag 版本号 {version} 不合 Chrome 规范（1~4 段数字）')
    if any(int(x) > 65535 for x in version.split('.')):
        sys.exit(f'tag 版本号 {version} 有一段超过 65535')
    if ahead:
        print(f'提示：当前 HEAD 比 v{version} 多 {ahead} 个提交，'
              f'这些改动不会体现在版本号里（发版前记得打 tag）')
    return version


def collect():
    """按白名单收集文件，返回 zip 内的相对路径（一律正斜杠）。"""
    out = []
    for name in INCLUDE_FILES:
        if not os.path.isfile(os.path.join(BASE, name)):
            sys.exit(f'缺少文件：{name}')
        out.append(name)
    for d in INCLUDE_DIRS:
        path = os.path.join(BASE, d)
        if not os.path.isdir(path):
            sys.exit(f'缺少目录：{d}/')
        for name in sorted(os.listdir(path)):
            if os.path.isfile(os.path.join(path, name)):
                out.append(f'{d}/{name}')
    return out


def digest_of(members):
    """源码内容摘要。manifest 里的 version 恒为 0.0.0，所以与 tag 无关。"""
    h = hashlib.sha256()
    for rel in members:
        h.update(rel.encode('utf-8'))
        h.update(b'\x00')
        with open(os.path.join(BASE, rel), 'rb') as f:
            h.update(f.read())
    return h.hexdigest()


def read_state():
    try:
        with open(STATE_FILE, encoding='utf-8') as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def build(version, members):
    with open(os.path.join(BASE, 'manifest.json'), encoding='utf-8') as f:
        manifest = json.load(f)
    if 'key' not in manifest:
        print('警告：manifest 没有 key，换个目录安装会变成另一个扩展')

    # 版本在这里注入，源码里那个 0.0.0 只是占位。
    manifest['version'] = version
    manifest_bytes = (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode('utf-8')

    out = os.path.join(DIST, f'quickdock-vault-extension-v{version}.zip')
    with zipfile.ZipFile(out, 'w', zipfile.ZIP_DEFLATED) as z:
        for rel in members:
            if rel == 'manifest.json':
                z.writestr(rel, manifest_bytes)
            else:
                z.write(os.path.join(BASE, rel), rel)

    # 三条上传硬性要求，出包后立刻自查 —— 等商店后台报错再查就晚了。
    with zipfile.ZipFile(out) as z:
        names = z.namelist()

    problems = []
    if 'manifest.json' not in names:
        problems.append('manifest.json 不在 zip 根目录')
    if any('\\' in n for n in names):
        problems.append('存在反斜杠路径')
    leaked = [n for n in names if '/keys/' in n or n.endswith('.pem')]
    if leaked:
        problems.append(f'私钥被打进包：{leaked}')
    if problems:
        os.remove(out)
        sys.exit('打包失败：' + '；'.join(problems))

    # 旧包留着会让人分不清该传哪一个。
    for old in sorted(os.listdir(DIST)):
        if old != os.path.basename(out) and old.endswith('.zip'):
            os.remove(os.path.join(DIST, old))
            print('已删除旧包：', old)

    print(f'输出 {os.path.relpath(out)}  {os.path.getsize(out)} 字节  {len(names)} 个文件')
    return out


def main():
    force = '--force' in sys.argv[1:]
    version = resolve_version()
    members = collect()
    digest = digest_of(members)
    os.makedirs(DIST, exist_ok=True)

    state = read_state()
    if not force and state.get('digest') == digest:
        prev = state.get('zip') or ''
        if prev and os.path.isfile(os.path.join(DIST, prev)):
            if state.get('version') != version:
                print(f'扩展源码没有改动，跳过打包。'
                      f'（现有 {prev} 是 v{state.get("version")}，当前 tag 是 v{version}）')
            else:
                print(f'扩展源码没有改动，跳过打包：{prev}')
            print('确需重打：python browser-ext/pack.py --force')
            return

    out = build(version, members)
    with open(STATE_FILE, 'w', encoding='utf-8') as f:
        json.dump({'version': version, 'digest': digest, 'zip': os.path.basename(out)}, f, ensure_ascii=False)


if __name__ == '__main__':
    main()
