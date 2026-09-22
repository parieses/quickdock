# QuickDock 官网

纯静态站点（无构建步骤），由 `.github/workflows/pages.yml` 自动部署到 GitHub Pages。

```
website/
├── index.html          # 单页，全部内容（中文为默认语言，直接写在 HTML 里）
├── assets/
│   ├── css/style.css   # 设计系统：Linear（暗色 / Inter / 金色强调）+ 亮色主题
│   ├── js/main.js      # 无依赖原生 JS（含主题与语言切换）
│   ├── js/i18n.js      # 英文词典（中文原文 → 英文）
│   └── img/            # 截图放这里
├── tools/
│   ├── extract-i18n-strings.py   # 列出还没翻译的中文文案
│   └── check-i18n-glue.py        # 检查行内标签两侧的英文值有没有漏空格（黏连）
└── README.md
```

## 主题与语言

右上角两个控件，选择存在 `localStorage`，刷新后保持。

**明暗主题**（`qd-theme` = `dark` | `light`）

- 首次访问跟随系统 `prefers-color-scheme`，手动切换后以手动选择为准，之后系统变化不再覆盖。
- `<head>` 里有一小段内联脚本在首屏渲染前定好 `data-theme`，避免刷新时闪一下。
- 主题变量在 `style.css` 的 `:root`（暗色）与 `[data-theme='light']`（亮色）两处，**成对修改**。
- 规则：暗色下用「叠加白」提亮层次（`--w002` … `--w014` 这组令牌），亮色下必须换成「叠加黑」，否则白底上完全看不见。新写样式请用这些令牌，**不要硬编码 `rgba(255,255,255,…)`**。
- 切主题时会同步更新 `<meta name="theme-color">`。

**中英文**（`qd-lang` = `zh` | `en`）

中文就是 `index.html` 里的原文（默认语言，搜索引擎收录的是中文），英文只存一份词典。**HTML 里没有任何 i18n 标记**，切换靠运行时按「中文原文」查表替换文本节点。

- 词典：`assets/js/i18n.js` 的 `QD_I18N.en`，键是中文原文、值是英文（`QD_I18N.head` 单独放 meta/og 这类属性文案，它们不是文本节点）。
- 查表时会折叠空白（`\s+` → 单空格），所以键写成单行、词间单空格即可，不用管 HTML 里的换行缩进。
- **键的首尾空格会被 `trim` 掉**，别在 key 两端写空格（写了就永远查不中，`extract` 会把这条一直报成「未翻译」）。行内标签断句留下的空格由 HTML 原文保留，替换时原样拼回 —— 所以英文值末尾**不用**再补空格。
- 查不到就保持中文 —— 漏译只会显示中文，不会出错。
- 跳过 `<pre>` `<code>` `<kbd>` 里的内容；只处理含中文的文本节点，纯 ASCII、数字、路径自动无视。
- 切语言会同步 `<html lang>`，`style.css` 里有 `html[lang='en']` 的少量适配。

**改文案 / 加新文案**

1. 直接在 `index.html` 里写中文。
2. 跑一下看还缺什么：

   ```bash
   python tools/extract-i18n-strings.py        # 只列未翻译的
   ```
3. 把列出来的中文原文翻好，填进 `assets/js/i18n.js` 的 `en` 段。
4. 中文里没有空格，英文有 —— **文案被 `<b>` `<code>` 等行内元素切断时，记得在英文那侧补空格**（例：`一个坞，装下<b>你的整个开发流</b>` → `"One dock for "` + `"your entire dev workflow"`，值末尾那个空格就是粘合点）。改完跑一下检查器，不用靠肉眼：

   ```bash
   python tools/check-i18n-glue.py     # ✅ 没有发现黏连 即可；有则按提示给前一条英文值补空格
   ```

   它会把 `Two routes: one-window snapping` + `with hotkeys` 这类拼成 `snappingwith` 的地方指出来。查不到就切到英文页面再扫一眼。

## 本地预览

```bash
cd website
python -m http.server 8080
# 打开 http://localhost:8080
```

> 编辑器 / 预览面板会给 `index.html` 注入 `data-page-node-id="…"`（每个标签一个，几千处）。不影响功能，但会让 diff 很脏。提交前清一下：
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
> 别用「压掉连续空格」的写法整行处理，会把行首缩进（含 `<pre>` 内缩进）一起压掉。

## 图标与配色

图标全部来自应用图标 `build/appicon.png`（深蓝底 + 金色 Q 火箭），缩放三份存于 `assets/img/`：

| 文件 | 尺寸 | 用途 |
|------|------|------|
| `favicon-64.png` | 64×64 | 浏览器标签页 |
| `apple-touch-icon.png` | 180×180 | iOS / macOS 添加到主屏 |
| `appicon-512.png` | 512×512 | 导航栏与页脚品牌标 + og:image |

`index.html` 里三处引用：`<link rel="icon">`、`.brand-mark > img`（导航栏与页脚各一处）。

**图标换了以后重新生成**：

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

配色跟着图标走：主题色在 `style.css` 的 `:root` 里，`--brand: #cfa93f` 取自图标金色，按钮文字用 `--on-brand: #16130a`（金底上白字对比度不够，必须是深色字）。亮色主题下金色要压深一档（`--brand: #9a7515`，配白字约 4.7:1）。想换回靛蓝紫只要改两处主题块里的这四个变量（`--brand` / `--brand-accent` / `--brand-hover` / `--on-brand`），但 CSS 里另有 11 处 `rgba(207, 169, 63, ...)` 半透明色（glow / 卡片底 / 标签）需要一并替换。

## 截图

5 张真实截图已就位，直接以 `<figure class="shot"><img …></figure>` 内联在 `index.html` 里：

| 文件 | 位置 | 内容 |
|------|------|------|
| `assets/img/ui-main.png` | Hero 下方 | 主界面：命令面板 + 工作空间列表 |
| `assets/img/ui-env.png` | 环境管理 | 侧栏运行时 + 版本下拉 + 运行状态 |
| `assets/img/ui-plugins.png` | 插件 | 插件市场卡片网格 |
| `assets/img/ui-onboarding.png` | 使用说明 01 | 首次启动引导页 |
| `assets/img/ui-workspace.png` | 使用说明 03 | 工作空间四层结构 + 搜索 |

**换图**：直接覆盖同名文件即可。建议 PNG、宽 1600px 左右（Hero 那张是 16:9）。

**加新图**（比如笔记、待办看板、监控）：

```html
<figure class="shot reveal"><img src="assets/img/ui-todo.png" alt="待办看板"></figure>
```

`reveal` 类保留滚动淡入；想加图注就在后面补 `<figcaption>`。

**还没截图时**可以用占位样式撑版式（`.shot-frame` 斜纹底 + `.shot-label` 说明文字）：

```html
<figure class="shot reveal">
  <div class="shot-frame" style="aspect-ratio:16/9">
    <span class="shot-label"><b>【占位图】应用主界面</b><span>说明文字</span></span>
  </div>
</figure>
```

> `img` 的 `alt` 文案不参与语言切换——词典只替换文本节点，属性不处理。要跟着中英走就得手工改，或者给 `i18n.js` 加一层属性表。


## 部署

- 触发：push 到 `main` 且改动落在 `website/**`（或手动 Run workflow）。
- 首次需在仓库 **Settings → Pages → Source** 选 **GitHub Actions**。
- 自定义域名：在 `website/` 下放一个 `CNAME` 文件（内容只写域名），再到域名服务商加 CNAME 记录指向 `parieses.github.io`。

## 改文案时注意

- **不写具体数量**：运行时 / 插件 / MCP 工具的个数会随版本漂移，Hero stats 与章节标题一律不写数字（改写「一键装切」「开箱即用」「以插件市场为准」这类表述），要数字以应用内的环境管理页 / 插件市场为准。枚举清单（运行时长表格、插件 chip、工具表格）保留。
- **版本号**散落在三处：Hero 的 `.hero-meta`、WHAT'S NEW 的 eyebrow + 副标题、`#changelog` 顶部那条的 `.log-ver`（配 `.log-date`）。改完记得同步 `i18n.js` 里对应的 key（key 本身含版本号，不是只改 value）。
- **发版要在 `#changelog` 顶部插一条**：复制最新那条 `<details class="log" open="">` 改成新版本，并把**上一条的 `open` 属性去掉**（只让最新版默认展开）。版本号、日期是纯 ASCII 不参与翻译；正文要点写中文，再补进 `i18n.js`。内容来源是 git 提交历史 —— GitHub Release 的 body 是 CI 自动生成的 changelog 链接，**没有实际内容**，别指望从那里抄。
- 下载链接用的是 GitHub latest release 稳定地址（`releases/latest/download/...`），不需要改版本号。

## 增删「使用说明」章节时

使用说明是**顺序编号**的，插入/删除一节要**五处联动**，漏一处就会出现「侧栏写着 9、正文写着 8」这类错位：

1. 正文的 `<span class="step-no">STEP NN</span>`（每节一处）
2. 正文上方的注释 `<!-- ============ N ============ -->`
3. 侧栏 `#guideNav` 里的 `<a href="#g-xxx">N. 标题</a>`
4. `i18n.js` 里的 `"N. 标题"` 条目 —— **key 和 value 都含编号，两处都要改**
5. 使用说明开头的「NN 节完整教程：…N—M 节覆盖…」那句总述

批量重编号建议从**大到小**替换（先 16→17，再 15→16 ……），否则会把刚改好的数字再改一遍。`main.js` 的 scrollspy 是动态取 `#guideNav a`，不用改。

