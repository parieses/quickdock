# QuickDock 官网

纯静态站点，无构建步骤，由 `.github/workflows/pages.yml` 部署到 GitHub Pages。

```
website/
├── index.html          # 中文首页：定位、界面、工具、运行时、AI、更新日志、下载
├── guide.html          # 中文使用说明，16 节，左侧目录跟随滚动
├── en/
│   ├── index.html      # 英文首页，结构与中文页一一对应
│   └── guide.html      # 英文使用说明
├── assets/
│   ├── css/style.css   # 设计系统（深墨蓝 + 金）+ 亮色主题 + 说明页 + 英文覆盖
│   ├── js/site.js      # 版本号唯一来源，填充页面上的 [data-ver] / [data-ver-date]
│   ├── js/main.js      # 主题、顶栏分隔线、滚动入场、目录高亮
│   └── img/            # 截图与应用图标
├── tools/
│   ├── bump.py         # 发版：改版本号 + 滚动更新日志
│   └── check-parity.py # 校验中英两页结构没走偏
└── README.md
```

## 设计系统

令牌在 `style.css` 顶部的 `:root`，亮色在 `[data-theme='light']`，**两处成对修改**。

**颜色**：底色取应用图标里的深墨蓝（`--ink-950` … `--ink-700`，层级越高越亮），强调色取图标里金色 Q 的 `--gold`。全站只有这一个彩色，红绿只用于状态，不做装饰。

**字体**：`IBM Plex Sans` 做正文与标题，`IBM Plex Mono` 做编号、热键、路径、数字；中文回退 `PingFang SC` / `Microsoft YaHei`。选 Plex 是为了工程文档的气质，别换回 Inter。

**间距**：`--s1` … `--s9`，8px 基准。章节上下留白 `--s9`（112px），移动端降到 76px。

**圆角**：按层级递进，标签 3px、按钮 6–8px、卡片 12px、截图与下载区 16px。

**线**：一律半透明（`--line` = 8% 白），不用实色边框。暗色下靠亮度分层，阴影只用来把截图从背景里拔出来。

**版心**：1160px，移动端 `padding-inline` 24px。

## 页面结构

首页六段，编号 `01`–`05` 由 `.sec-num` 呈现，是页面里唯一带装饰意味的排版元素：

| 区块 | 内容 |
|------|------|
| Hero | 标题、副标题、两个按钮、四个硬指标、主界面截图 |
| `01` 界面 | 三组「说明在上、截图满宽在下」 |
| `02` 工具 | 14 条清单，两列，带一条略亮的背景带（全站只此一处） |
| `03` 运行时 | 按分组的 chip 表 + 三条补充说明 |
| `04` AI | 四张卡片，2×2 |
| `05` 更新日志 | `<details>` 折叠，只留最近四个版本，更早的指向 Releases |
| 下载 + FAQ | 两个下载按钮、系统要求侧栏、四条折叠问答 |

说明页是 `170px 目录 + 正文` 两栏。正文行宽用 `em` 控制——中文按 `ch` 算会窄一半，这个坑踩过一次。

## 中英双语

英文版是**独立的两份 HTML**（`en/`），不是运行时替换文本。

上一版是用「中文原文当 key」的词典（`i18n.js`）加 `main.js` 逐节点替换，代价是每次改中文文案，词典的 key 就失联一条，改着改着整份词典全对不上，最后只能弃用。现在两份 HTML 各写各的，改文案就改一处，不用维护映射表。

代价是两份会漂移：给中文页加一段忘了英文页，页面看着正常，只是少一截。所以有 `tools/check-parity.py`：

```bash
python tools/check-parity.py   # 结构一致返回 0，分叉会指出第几个标签
```

它只比标签序列和 `id` 集合，**不比对文字**——改文案不会报错，加删元素、改嵌套才会。

几条约定：

- 两页的元素顺序必须完全一致，包括语言切换那两个链接（都是一对 `<a>`，当前语言带 `class="on"` 与 `aria-current`）。
- 路径：英文页在子目录里，资源引用写 `../assets/…`，同目录页面互链写相对名。
- 每个页面都有三条 `hreflang`（`zh-CN` / `en` / `x-default`），指到对方语言的同一页。
- 语言切换**不做自动跳转**：URL 已经表明语言，再按 `localStorage` 自动跳会让想切回去的人摸不着头脑。
- 英文排版在 `style.css` 末尾的 `:lang(en)` 一节单独覆盖：中文用 `em` 控行宽，英文用 `ch`；工具清单的名称列加宽到 176px，否则 `Command palette` 会折行。
- `main.js` 里主题按钮的 `title` 跟着 `<html lang>` 走，不用为英文单独写一份脚本。

## 版本号

**只有一个来源**：`assets/js/site.js` 的 `version` / `released`。页面上的 `<span data-ver></span>` 与 `<span data-ver-date></span>` 由它填充，包括首屏版本行、下载区和更新日志第一条。

不要往 HTML 里手写版本号。日志里 `v0.0.29`、`v0.0.28` 这些是历史记录的文本，不参与同步。

## 发版时要改的地方

```bash
cd website
python tools/bump.py 0.0.31 2026-11-02
```

它做三件事：

1. 改 `site.js` 的 `version` / `released`；
2. 把现有日志条目的 `data-ver` / `data-ver-date` 固化成字面量（上一条「当前版」从此变成历史）；
3. 在 `#log` 顶部插一条新条目并默认展开，标题与正文留 `TODO`，中英各一份。

跑完填两个 `index.html` 里的 `TODO`，再 `git diff` 看一眼插入位置。旧条目超过四条时脚本会提醒删最旧的。

脚本靠 `<div class="logs rise">` 与 `<details class="log" open>` 定位，改更新日志的 HTML 结构时记得回来对一眼。

**更新日志的日期和内容必须去 `git log <tag>` 里核对**，不要凭印象填——有一次 v0.0.27 / v0.0.28 的条目整个写错了版本内容。发布日期用 `git log -1 --format=%ad --date=short <tag>`。

**功能描述**要和主仓库 `README.md` 保持一致。`设置 → X` 这类路径最容易过期，发版前在两个 `guide.html` 里 grep 一遍 `设置 → ` 与 `Settings → `，对着应用的设置菜单核。设置页合并过一次，`导航设置`、`快捷键设置`、`打开工具`、`通用` 这些名字都改过。

**运行时数量**出现在首屏 facts 与 `#runtime` 标题附近，增减运行时两页一起改，主仓库 README 也是。

下载链接用的是 GitHub latest release 的稳定地址（`releases/latest/download/...`），不用带版本号。

## 主题

右上角一个按钮，选择存 `localStorage` 的 `qd-theme`。中英两页共用同一个键，切了语言主题不丢。

- 首次访问跟随系统 `prefers-color-scheme`，手动切换后以手动选择为准。
- `<html>` 上的初始值写死 `dark`（产品本身的默认），脚本接手后再按存储或系统偏好覆盖，避免首屏闪白。
- 切主题会同步 `<meta name="theme-color">`。
- 写新样式时用 `--t1` / `--surf-*` / `--line*` 这些令牌，**不要硬编码 `rgba(255,255,255,…)`**，亮色下会看不见。

## 动画

`.rise` 元素由 `IntersectionObserver` 在进入视口时加 `.in`，位移 14px、600ms。`prefers-reduced-motion: reduce` 下直接可见。

不用 `scrollIntoView`——在 iframe 预览里会带动外层一起滚。

## 截图

`assets/img/` 里现有的都是 1063×740 的窗口截图，CSS 按满宽 1:1 显示，**别压到更小尺寸**，会糊。

截图自带窗口标题栏（左边金色图标 + 快速坞 QuickDock），本身就是品牌标识，**不要再外套一层假窗口边框**。

截图里的界面是中文的，英文页也用同一批图——产品本身有英文界面，只是还没补英文截图。

| 文件 | 位置 |
|------|------|
| `ui-main.png` | Hero、`01` 第一组 |
| `ui-env.png` | `01` 第二组 |
| `ui-plugins.png` | `01` 第三组 |
| `ui-onboarding.png` / `ui-workspace.png` | 暂未使用，可作为新增内容的素材 |

新增截图保持同样的窗口尺寸和暗色主题。

## 图标

图标来自 `build/appicon.png`（深蓝底 + 金色 Q 火箭），缩放三份：

| 文件 | 尺寸 | 用途 |
|------|------|------|
| `favicon-64.png` | 64×64 | 浏览器标签页 |
| `apple-touch-icon.png` | 180×180 | 添加到主屏 |
| `appicon-512.png` | 512×512 | 顶栏与页脚品牌标、og:image |

图标换了以后重新生成：

```bash
python -c "
from PIL import Image
src = Image.open('build/appicon.png').convert('RGBA')
w, h = src.size; s = min(w, h)
src = src.crop(((w-s)//2, (h-s)//2, (w-s)//2+s, (h-s)//2+s))
src.resize((64,64), Image.LANCZOS).save('website/assets/img/favicon-64.png', optimize=True)
src.resize((180,180), Image.LANCZOS).save('website/assets/img/apple-touch-icon.png', optimize=True)
src.resize((512,512), Image.LANCZOS).save('website/assets/img/appicon-512.png', optimize=True)
"
```

## 本地预览

```bash
cd website
python -m http.server 8080
# 打开 http://localhost:8080
```

> 编辑器 / 预览面板会给 HTML 注入 `data-page-node-id="…"`，每个标签一个，会让 diff 变脏。提交前清一下：
>
> ```bash
> python -c "
> import io, re
> p = 'website/index.html'; s = io.open(p, encoding='utf-8').read()
> n = len(re.findall(r'[ \t]+data-page-node-id=\"[^\"]*\"', s))
> s = re.sub(r'[ \t]+data-page-node-id=\"[^\"]*\"', '', s)
> io.open(p, 'w', encoding='utf-8', newline='').write(s)
> print('removed', n)
> "
> ```
>
> 别用「压掉连续空格」的写法整行处理，会把缩进（含 `<pre>` 内的）一起压掉。

## 部署

- 触发：push 到 `main` 且改动落在 `website/**`，或手动 Run workflow。
- 首次需在仓库 **Settings → Pages → Source** 选 **GitHub Actions**。
- 自定义域名：在 `website/` 下放 `CNAME`（内容只写域名），再到域名服务商加 CNAME 记录指向 `parieses.github.io`。加域名后记得把 `hreflang` 从相对路径换成绝对地址。

## 增删说明页章节时

两页的编号都是写死的，插入或删除一节要同时改四处，漏一处就会出现「目录写 9、正文写 8」：

1. `<aside class="doc-nav">` 里的 `<a href="#sN">NN 标题</a>`
2. 正文 `<section class="doc-sec" id="sN">` 的 `id` 与 `<h2><span>NN</span>标题</h2>`
3. 说明页开头那句「从装好到用顺手。前四节…」里的节数描述
4. 英文页的对应位置（内容自己译，编号相同）

批量重编号从大到小改（先 16→17，再 15→16），否则会把刚改好的数字再改一遍。目录高亮是 `main.js` 里动态取 `#docNav a` 的，不用跟着改。改完跑 `python tools/check-parity.py`。
