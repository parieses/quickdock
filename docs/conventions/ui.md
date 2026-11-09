# UI 样式规范

适用范围：**只限 `frontend/src/**`（宿主 UI）**。
插件页面（`plugins/**/frontend/**`）的样式规范在**另一个仓库** `quickdock-plugins/plugin-ui-style.md`，
本文不涉及 —— 两套令牌体系互不相通，混着写必错（见 §7）。
要把插件样式批量对齐到宿主当前色阶，用 `quickdock-plugins/plugin-style-prompt.md`
（可粘贴提示词，带旧值 → 新值对照表、共享组件规则与门禁验收）。
代码组织约定见 `frontend.md`，本文只管**长什么样**。

一句话原则：**同一类元素在全应用只有一份实现**。新增页面只做"使用方"，不做"再写一套"。

---

## 1. 样式文件职责（改哪里决定了影响面）

| 文件 | 管什么 | 改动影响 |
|---|---|---|
| `frontend/src/style.css` | **token 定义** + 全局重置 + 滚动条 + 焦点环 | 全应用换肤，改这里等于改全部 |
| `frontend/src/styles/app.css` | 应用外壳：`.qd-page*` / `.qd-btn*` / `.qd-icon-btn` / `.qd-input*` / `.qd-card` / `.qd-toolbar` | 所有业务页页头与控件 |
| `frontend/src/styles/panels.css` | 三栏：`.qd-panel*` / `.qd-row*` / `.qd-empty*` | 工作空间三栏 |
| `frontend/src/styles/guide.css` | 整页引导：`.qd-guide*` | `EmptyGuide.vue` |
| `frontend/src/styles/settings.css` | 设置页：`.settings-*` | 设置页 |

> 插件页面的样式不属宿主层，见 §7。

**组件 `<style scoped>` 只允许写两类东西**：① 该组件独有的布局偏移；② 该组件独有的元素（如 app 图标、拖拽手柄）。
面板头、按钮、输入框、行、空状态**一律不写进 scoped** —— 历史上三栏各写一套，同屏出现三种面板头 padding，改一处必漏两处。

---

## 2. Design Token

**唯一来源 `style.css`。** 新增 token 必须在 `:root,[data-theme="dark"]` 与 `[data-theme="light"]` **两处同时定义**，否则浅色主题静默失效。
禁止 `var(--x, #333)` 兜底写法 —— 它掩盖了"变量不存在"这个错误。

### 2.1 核心色板（对照）

| 语义 | token | 深色 | 浅色 |
|---|---|---|---|
| 应用底 | `--color-bg-primary` | `#0c0e13` | `#f6f7f9` |
| 侧栏 / 页头（外壳） | `--color-bg-secondary` | `#14171f` | `#ffffff` |
| 控件底 | `--color-bg-tertiary` | `#1b202a` | `#eef0f4` |
| hover | `--color-bg-hover` | `#212734` | `#eceff4` |
| 选中/按下 | `--color-bg-active` | `#2a303e` | `#e0e5ed` |
| **卡片底** | `--color-card-bg` | `#171b22` | `#ffffff` |
| **卡片描边** | `--card-border-color` | `transparent` | `var(--color-border)` |
| **浮层底** | `--color-popover-bg` | `#1e232d` | `#ffffff` |
| **浮层描边** | `--popover-border-color` | `rgba(255,255,255,.08)` | `var(--color-border)` |
| 正文 | `--color-text-primary` | `#e8ecf4` | `#1a1f27` |
| 次要 | `--color-text-secondary` | `#a3acbb` | `#525b68` |
| 弱 | `--color-text-muted` | `#74808f` | `#6e7683` |
| 禁用 | `--color-text-disabled` | `#59616e` | `#9aa2ae` |
| 分隔线 | `--color-border` | `#232936` | `#dde1e8` |
| hover 描边 | `--color-border-light` | `#313948` | `#c9cfd9` |
| 强调 | `--color-accent` | `#5b8cff` | `#5b8cff` |
| 强调 hover | `--color-accent-hover` | `#4a7af0` | `#4a7af0` |
| 强调淡底 | `--color-accent-bg` | `rgba(91,140,255,.085)` | `rgba(91,140,255,.075)` |
| **选中行底** | `--color-select-bg` | `rgba(91,140,255,.14)` | `rgba(91,140,255,.12)` |
| **勾选框底** | `--color-check-bg` | `#101419` | `#ffffff` |
| **勾选框边** | `--color-check-border` | `#3a4353` | `#c2c9d4` |
| 危险 | `--color-danger` / `-bg` / `-border` | `#ef4444` / `.12` / `.40` | `#ef4444` / `.10` / `.45` |
| 成功 | `--color-success` / `-bg` | `#22c55e` / `.12` | `#22c55e` / `.12` |
| 警告 | `--color-warning` / `-bg` / `-border` | `#f59e0b` / `.12` / `.40` | `#f59e0b` / `.14` / `.45` |

**这三组新 token 是分层方式的核心，用错等于回到旧样式**：

- `--color-card-bg` 比 `--color-bg-secondary` 亮一档，**卡片靠它分层**，不靠描边和投影。
- `--card-border-color` 深色是 `transparent`（让 `border` 声明失效）、浅色是实色 ——
  因为深色有亮度差可读、浅色白压白必须描边。写死 `1px solid var(--color-border)` 会让
  深色卡片又长出一圈亮边。
- `--color-popover-bg` 是**面层最高的一级**：下拉、右键菜单、命令面板、模态。
  卡片提到 `#171b22` 后，浮层若还用 `bg-secondary`（`#14171f`）会比它盖住的卡片更暗，
  看起来像在卡片上挖了个洞。凡是 `position: absolute/fixed` 且会盖住内容的面层，用它。
- `--color-select-bg` 只给「列表行 / 导航项当前选中」，`--color-accent-bg` 留给
  chip、开关型按钮（`.qd-btn.is-on`、`.seg-btn.active` 这类）。两者不要混用。
- `--color-check-bg` / `--color-check-border` 专给勾选框和单选框。**不要复用
  `bg-tertiary`**：它需要的是「永远实色的凹槽」，与控件底色职责不同。旧版材质档
  曾把 `bg-*` 改成半透明，正是这个独立 token 保住了 14px 小方块不发虚；
  现在 `bg-*` 恒实色，语义仍独立，不要合并。
- `--color-terminal-bg` 在 `html.qd-backdrop` 下是**半透明**（深 `rgba(15,17,22,.55)` /
  浅 `rgba(255,255,255,.72)`），关掉材质时是实色。它喂给 xterm 6 的 `theme.background`，
  会被原样写成 `.xterm-scrollable-element` 的内联背景（DOM renderer，不需要
  `allowTransparency`）。凡是「必须盖住底下内容」的地方（光标反色文字 `cursorAccent`、
  `.sess-overlay` 连接中/失败遮罩）一律用 `--color-terminal-bg-solid`。

**材质档位（`html.qd-backdrop`）另有一组窗口底 token，只给大面积铺底层用**：

| token | 深色 | 浅色 | 用在哪 |
|---|---|---|---|
| `--color-window-bg` | `rgba(12,14,19,.50)` | `rgba(246,247,249,.72)` | `.app-content`、独立窗口内层根 |
| `--color-window-bg-2` | `rgba(20,23,31,.58)` | `rgba(255,255,255,.80)` | 主侧栏 `.sidebar` |
| `--color-acrylic-bg` | `rgba(12,14,19,.90)` | `rgba(246,247,249,.94)` | 命令面板 / 插件窗口 |
| `--color-settings-bg` | 实色（= `bg-primary`） | 实色 | 设置面板 |

关材质时这四个都是 `bg-primary/secondary` 的**别名**（值逐像素相同），只有材质档才换成 rgba。
**永远不要在 `html.qd-backdrop` 里重定义 `--color-bg-primary/-secondary/-tertiary`**：
90 多个组件把这组 token 当作输入框、textarea、代码块、表头、表格行、选中态的底色
（全项目 494 处声明），一旦改成半透明，承载文字的控件会一起透出壁纸。
正确做法是「窗口底单独一组半透明 + 组件层保持实色」，材质只从主侧栏、内容区留白、
卡片缝隙透出。多栏页左栏与 L3 及更深的全屏铺底必须在材质档让开（`style.css` 里
那组 `.app-content .notes-tree / .git-side / .env-cats / .ai-sidebar / .kb-side /
.onboarding` 规则），否则和 `.app-content` 连乘后左 25% / 右 50% 透过，左右割裂。
材质档还把 `--color-select-bg` 换成实色蓝灰，避免 14% 蓝底混进壁纸变脏蓝。

### 勾选框 / 单选框

原生 `<input type="checkbox">` 由 WebView2 按系统绘制，深色主题下是一块发白的方块，
所以 `style.css` 里统一 `appearance: none` 自绘：14px、`radius-xs`、未选用
`--color-check-bg` + `--color-check-border`，选中用 `--qd-check-color`（默认 accent）
铺实底 + 白勾（SVG background-image，跟着盒子缩放；`input` 是替换元素，
伪元素在 Chromium 下不可靠）。`:indeterminate` 画横杠，`:disabled` 降透明度。

- **组件里不要再写 checkbox 的尺寸和外观**。之前 12 / 13 / 14 / 15 / 16px 五种尺寸
  分散在七个文件里，`accent-color` 满天飞；现在只有全局那一条。
- 要换选中色（如指纹确认的红勾），**只改 `--qd-check-color`**，不要另画一套。
- 组件里保留的只有布局相关的（`margin-top`、`flex: none`）。

**面层只有三级，自下而上**：`bg-primary`（页面）→ `card-bg`（卡片 / 列表行）→ `popover-bg`（浮层）。
`bg-secondary` 不再是"卡片色"，它只代表**外壳**（侧栏 / 页头 / 面板头 / 工具栏），
以及卡片**内部**的下沉区（输入框、代码块、chip）—— 那些地方比卡片暗是对的。

其余（终端调色板 20 项、代码块、日志级别、搜索高亮、toast）见 `style.css`，同样双主题成对。

### 2.2 尺寸与阶梯

| 阶梯 | 值 | 用途 |
|---|---|---|
| 间距 | `--space-1..11` = 2/4/6/8/12/16/20/24/32/40/48 | 组件内边距与块间距 |
| 圆角 | `--radius-xs` 4 / `-sm` 6 / `-md` 8 / `-lg` 12 / `-full` | 控件 6、卡片 8、浮层 12；相邻两档只差 2px，别跨档用 |
| 阴影 | `--shadow-card`（静态，深色为 `none`）/ `-md`（中浮层）/ `-pop`=`-lg`（模态、命令面板）/ `-toast` | 只有这四层；`-sm` 是 `-md` 的别名、`-border-right` 已废弃为 `none`，新代码不许用 |
| 动效 | `--transition-fast` 110ms / `-base` 160ms | 只给 color/background/border，不给尺寸 |

**高度基准（全应用统一，不得自造）**

| 元素 | 高度 | 水平内边距 |
|---|---|---|
| 一级页头 `.qd-page-header` | 46（`min-height`） | 16 |
| 面板头 `.qd-panel-header` | 38 | 12 左 / 8 右 |
| 工具栏 `.qd-toolbar` | 44（8+28+8） | 16 |
| 按钮 `.qd-btn`（= **md**） | 28 | 11 |
| 小按钮 `.qd-btn-sm`（= **sm**） | 24 | 8 |
| 大按钮（= **lg**，对话框主行动、引导页 CTA） | 32 | 16 |
| chip（筛选 / 视图切换，`radius-full`） | 22 | 9 |
| 图标按钮 `.qd-icon-btn` | 26×26 | 0 |
| 行内按钮 `.qd-row-btn` | 22×22 | 0 |
| 输入框 `.qd-input` / 搜索行 `.qd-panel-search` | 28 | 9 / 8 |
| 列表行 `.qd-row` | `min-height` 34 | 8 左 / 6 右 |
| 侧栏导航 `.nav-item` | `min-height` 32 | 10 |
| 徽章 `.nav-badge` | 16 | 4 |

### 2.3 字号阶梯

八档，一档一个用途。**禁止再出现 `10.5` / `11.5` / `12.5` 这类半像素值**——
100% 缩放下 0.5px 字号不产生可辨差异，只会让阶梯失去参照（2026-11 已全量抹平）。

| 字号 | token | 用途 |
|---|---|---|
| 10 | `--text-2xs` | 角标、计数徽章、图表刻度 |
| 11 | `--text-xs` | 分区标题（`.qd-panel-title` / `.navigator-header`）、行副标题、时间与 meta |
| 12 | `--text-sm` | 控件文字（按钮、输入框）、页头副标题 `.qd-page-sub`、表格单元格 |
| 13 | `--text-md` | **界面正文基准**：列表行标题、表单值 |
| 14 | `--text-base` | **长文正文**：AI 回复 / Markdown、对话框正文与标题；只用于「读」，不进列表行 |
| 15 | `--text-lg` | 一级页头标题 `.qd-page-title`（600） |
| 18 | `--text-xl` | 引导页标题、区块标题、仪表盘问候语、图标占位字号 |
| 24 | `--text-2xl` | 数据展示（统计读数）、长文 h1，**不进列表** |

`--text-base` 名字里的 base **不是阶梯起点**：13px 的 `--text-md` 才是界面正文基准，
14px 是给「需要连读几行」的长文单独加的一级。
**不再有「允许直接写 14px」的例外**：2026-11 已把散落的 14px / 20px 全量收进
`--text-base` / `--text-xl` / `--text-2xl`。唯一还能写裸字号的地方是
`StatusPage` 导出的**独立 HTML 快照** —— 那份文档不加载本仓库 CSS，所有
`var(--…)` 都会静默失效，色值、圆角、字号必须写具体值（尤其别写 `var(--radius-*)`，
会退化成直角）。
28 / 34 / 48 只出现在图标字号与超大数字读数上，属特例，不要复用。

> 分区标题不再大写、不再加 `letter-spacing`：中文没有大小写，`uppercase` 对中文无效，
> 剩下的只有「字距被拉开」。标题的层级靠字号与字重表达，不靠字距。

**注意字号与正文档位的关系**：存量里 11px 仍有约 495 处（元信息、密集表格），
这是历史密度决定的，不是目标值。新增的「列表标题 / 卡片标题 / 表单标签」一律 13px 起，
不要在 11px 上继续堆正文。

---

## 3. 组件清单（只准用这些）

### 3.1 页头（只有两级）

```html
<!-- 一级页面：46px，标题 15px，右侧动作区自动靠右 -->
<div class="qd-page-header">
  <div class="qd-page-titles">
    <h2 class="qd-page-title">环境服务</h2>
    <span class="qd-page-sub">可选副标题</span>
  </div>
  <div class="qd-page-actions">
    <div class="qd-action-group">
      <button class="qd-btn qd-btn-ghost">刷新</button>
      <button class="qd-icon-btn">…</button>
    </div>
    <span class="qd-action-sep"></span>
    <button class="qd-btn qd-btn-primary">新建</button>
  </div>
</div>

<!-- 二级面板：38px，标题 11px 大写 -->
<div class="qd-panel-header">
  <span class="qd-panel-title">场景</span>
  <span class="qd-panel-chip">当前集合名</span>
  <div class="qd-panel-actions"><button class="qd-icon-btn">…</button></div>
</div>
```

- 页头需要中间控件（搜索、标签）时用 `.qd-page-fill` 占位，动作区靠 `.qd-page-actions` 的 `margin-left:auto` 自己贴右。
- **右侧动作规则**：最多 1 个主按钮（`.qd-btn-primary`，给这一页的主操作）+ 一组
  `.qd-action-group`（次要动作，一律 ghost / 图标按钮），两者之间用 `.qd-action-sep`
  一条 16px 短竖线分开；没有主操作的页面只放一组、不画线。46px 的页头里平铺
  5~7 个同级按钮时，「这页要做的事」和「顺手的工具」一样重，视线没有落点。
  类名刻意不带 `.qd-page-actions` 前缀：Git 顶部是 `.bar-actions` 工具栏，也用同一套。
  已落地：Git（Push 为主按钮）、环境页（环境体检为主按钮，另 3 个工具收成图标）。
- **任何页面不许再写 `xxx-header`。** 存量 5 种写法（10/14/16/20px 内边距、14/15/18px 标题、有无分隔线）已全部迁移；新增一律用上面两个类。

### 3.2 按钮（四种尺寸，四种语气）

**尺寸只有四档，一律 `border-radius: var(--radius-sm)`（chip 用 `--radius-full`）。
定高 + `0 Npx` 内边距 + `display: inline-flex; align-items: center; justify-content: center`
是同一个盒子配方 —— 三件缺一件就会出现"文字顶到上沿"或"图标和文字粘在一起"。**

| 档 | 高度 | 左右内边距 | 字号 | 用在哪 |
|---|---|---|---|---|
| **lg** | 32 | 16 | 13 | 对话框底部主/次行动、引导页 CTA、设置页 `.cfg-btn` |
| **md**（默认） | 28 | 11 | 12 | 页头动作、工具栏、列表行尾、卡片内主操作 |
| **sm** | 24 | 8 | 11 | 三栏面板头、密集工具栏、弹窗内次要操作 |
| **chip** | 22 | 9 | 11 | 筛选标签、视图切换（`--radius-full`） |

| 类 | 用途 |
|---|---|
| `.qd-btn` | 默认次级：28px 定高 + `bg-tertiary` 底 + 1px 边 + 内高光，hover 抬到 `bg-hover` |
| `.qd-btn-primary` | 主行动：实底 accent。**同屏只允许一个** |
| `.qd-btn-ghost` | 页头里成组的次要动作，无边框，hover 才浮出 |
| `.qd-btn-danger` | 破坏性动作：淡红底 + 红字，hover 转实红 |
| `.qd-btn.is-on` | 开关型已选中（视图切换、分组筛选） |
| `.qd-btn-sm` | 表格行尾、卡片内部 |

> **历史类名（`.op-btn` / `.log-btn` / `.svc-btn` / `.tb-btn` / `.sess-btn` …）**：
> 分散在 30 多个组件的 scoped 样式里，改名风险远大于收益，2026-11 已把它们的
> **盒子值**逐条对齐到上表四档（不再有 26/30/35 这类中间高度，圆角统一 `radius-sm`）。
> 新增代码不许再造这类类名；改的时候保留类名、只改值。

图标按钮统一 `.qd-icon-btn`（26×26，`padding: 0` 必须写死）。
**`.qd-icon-btn` 的定义在 `app.css`**，不要在 `panels.css` 里重复一份 —— 曾经它只在 `panels.css` 里，于是只引 `app.css` 的页面拿到浏览器默认按钮样式（白底方块）。

### 3.3 输入框

- 独立输入：`.qd-input`（28px，`bg-tertiary` 底，focus 转 accent 边）。
- 带图标：`.qd-input-wrap > svg + .qd-input`（图标绝对定位，输入框左内边距 28）。
- 三栏过滤行：`.qd-panel-search` + `.qd-search-input`（**`max-width: 380px`**，否则项目栏会把搜索框拉满上千像素）。

### 3.4 行 / 卡片 / 空状态

- 列表行：`.qd-row`（`min-height:34`、`gap:8`、`margin-bottom:2`、`radius-sm`）
  - 内部：`.qd-row-icon`(24) + `.qd-row-main` > `.qd-row-title` / `.qd-row-sub` + `.qd-row-actions`
  - 状态：`:hover` = `bg-hover`；`.active` = `select-bg` + 左缘 3px 竖条（**图标不变色**）；`.dragging` = `opacity:.4`
- 卡片：`.qd-card`（`--color-card-bg` + `radius-md`；深色无描边无投影，浅色由 `--card-border-color` 补 1px 边）
  - **卡片里不许再套 `.qd-card`**：没有更高一档的亮度可用，套了只能靠加描边，等于回到三层装饰
  - 卡片**内部**的输入框 / 代码块 / chip 用 `bg-secondary` 或 `bg-tertiary`（比卡片暗 = 下沉），这是对的
- 浮层（下拉 / 右键菜单 / 命令面板 / 模态 / 通知中心）：`--color-popover-bg` + `--popover-border-color` +
  `--shadow-pop`（或 `-lg`），`radius-md` 起步、`-lg` 给模态。
  **浮层是唯一允许带投影的一级**；它比任何静态层都亮，所以盖在卡片上不会像挖了个洞
- 空状态：`.qd-empty` > `.qd-empty-icon`(40，1px 虚线方框) + `.qd-empty-title` + `.qd-empty-hint` + `.qd-empty-cta`
- 整页引导：`.qd-guide` > `.qd-guide-inner` + 标题/描述 + `.qd-guide-cta` + `.qd-guide-steps`（3 列 grid，≤660px 折 1 列）

### 3.5 图标

用 `@lucide/vue`，尺寸只用 **14 / 15 / 16 / 20**（页头按钮 14、行内 16、空状态 20）。
图标继承 `currentColor`，**不要给 svg 单独写 fill 色**。

---

## 4. 页面骨架（新页面照抄）

```html
<template>
  <div class="qd-page">
    <div class="qd-page-header">…</div>
    <div class="qd-page-body"><!-- 可滚动内容，padding 16 --></div>
  </div>
</template>
```

- 页容器**不要加 padding**：页头必须通栏，内边距由 `.qd-page-body` / 内容块自己承担。
  历史上监控、定时任务、待办、插件把 padding 加在页容器上，页头缩在里面，与其它页错位 20px。
- 列表类页面：`.qd-toolbar`（筛选条）+ 滚动区，滚动区自己 `flex:1; min-height:0`。
- 滚动容器必须是 flex 子项且 `min-height: 0`，否则内容一多整页被裁、滚不动。

---

## 5. 交互状态

| 状态 | 表达方式 | 禁止 |
|---|---|---|
| hover | 底色 `bg-hover`，文字升一档 | 改字号/位置 |
| 选中（列表行/导航项） | 底色 `select-bg` + 左缘 3px accent 竖条，图标保持中性 | 再叠图标变色（三个弱信号不如两个强信号）；整行文字变蓝（字多像超链接） |
| 选中（chip / 开关型按钮） | 底色 `accent-bg` + `accent-border` 描边 + 文字 accent | 与列表行选中混用同一底色（两者语义不同：一个是"我在哪"，一个是"这个开着"） |
| 键盘焦点 | `:focus-visible` 2px accent 外框 | 去掉 outline 又不给替代 |
| 禁用 | 底色不动，文字转 `text-disabled`，`cursor: not-allowed` | 用 `opacity`（叠在 muted 上对比度只剩 ~1.6:1，等于消失） |
| 加载中 | 原位换成 `<Loader2 class="spin">`，宽度不变 | 按钮变宽/抖动 |

> 「选中（列表行/导航项）」覆盖**所有**列表当前项：三栏 `.qd-row`、知识库文档行、
> 剪贴板条目、Git 提交行 / 工作区条 / 文件行。grid 布局里不能加 `border-left`
> （会顶偏列宽），改用 `box-shadow: inset 3px 0 0 var(--color-accent)` 画同一根竖条
> —— 实现可以因布局而异，信号必须只有「`--color-select-bg` 实底 + 3px accent 竖条
> + 图标中性」这一组。`outline` 是焦点环的语言，不拿来画选中；`hover` 也不能和
> 选中长得一样，否则鼠标划过一整列时每条都像被选中。
> 2026-11 已把 Git 的 2px inset、知识库的文字/图标变蓝、剪贴板的 1px outline
> 三种画法全部收编到这一条。chip / 开关型按钮仍用 `accent-bg`，两者语义不同。

对比度底线：正文 ≥ 7:1，次要 ≥ 4.5:1，禁用态允许低但必须**仍可辨认**。

---

## 6. 禁止事项

| 做法 | 后果 | 正确做法 |
|---|---|---|
| 自造 `xxx-header` / `xxx-btn` | 同类元素多套实现，改一处漏两处 | 用 `qd-*` |
| 无前缀的公共类名（`.action-btn` / `.btn`） | 与 `settings.css` 全局类撞车：`border-box` 下 `padding:7px 14px` 把 24px 按钮内容盒算成负数，**SVG 被收缩到 0 宽**，按钮有框、hover 有底色、图标完全没有，且**无任何报错** | 公共类一律 `qd-` 前缀；自查时量 `button` 与内部 `svg` 的 `getBoundingClientRect()` |
| `var(--x, #333)` | 掩盖变量不存在 | 只写 `var(--x)` |
| 用 `--shadow-border` 做页头分隔 | 底色压深后 `0 1px 0 rgba(0,0,0,.08)` 几乎不可见，页头悬空 | `border-bottom: 1px solid var(--color-border)` |
| 阴影做层级 | 深浅主题差异被放大、边缘发糊 | 底色差 + 1px 线；阴影只留给浮层 |
| 容器叠三层装饰（底色 + 描边 + 内高光） | 一堆卡片同屏时每张都带一圈亮边，整页发毛 | 只留一层：深色靠 `--color-card-bg` 的亮度差，浅色靠 `--card-border-color` |
| 圆角随手写 10/14/16 | 与 token 阶梯对不上，将来改全局圆角必漏 | 只用 4/6/8/12 四档（`--radius-xs/sm/md/lg`） |
| 按钮只写 padding 不定高 | 26/30/35 三种"差不多高"的按钮同屏，看着像三个产品拼的 | 定高 + `0 Npx` + `inline-flex` 居中，见 §3.2 四档 |
| 浮层用 `bg-secondary` / `--color-surface` | 比卡片暗一档，下拉盖在卡片上像挖了个洞 | `--color-popover-bg` |
| 材质档位下给 `.sess` / `.ssh-term` 铺实色 | 连接后右侧终端整片死黑，和左侧透材质割裂；半透明叠三层后材质只剩 2% | `style.css` 里 `html.qd-backdrop .sess, .ssh-term { background: transparent }`，只留 `.ssh-page` 与 xterm 两层 |
| 在 `html.qd-backdrop` 里把 `bg-primary/secondary/tertiary` 改成 rgba | 494 处控件底色一起透壁纸；多栏页左栏再叠一层后左右透过率差一倍 | 窗口铺底改用 `--color-window-bg*`，组件层保持实色，见 §2.1 |
| 给设置面板（`inset:0` 全屏覆盖层）留半透明 | 除了壁纸，还会隐约透出被它盖住的主窗口卡片轮廓，像两层界面叠在一起 | 保持实色 `--color-settings-bg`（默认就是实色） |
| 卡片写 `1px solid var(--color-border)` | 深色主题下每张卡片一圈亮边 | `var(--card-border-color)` |
| 半像素字号（10.5 / 11.5 / 12.5） | 100% 缩放下不可辨，只让字号阶梯失去参照 | 落到最近的一档，见 §2.3 |
| 组件里给 `input[type=checkbox]` 写尺寸或 `accent-color` | 12/13/15/16px 五个尺寸并存，系统绘制在深色下是发白方块 | 全局只有 `style.css` 那一份；换色改 `--qd-check-color` |
| 中文标题写 `letter-spacing: -0.01em` 或 `uppercase` | 中文负字距把笔画挤在一起；大写对中文无效，只剩"字距被拉开" | 层级靠字号与字重表达，字距留空 |
| `:has()` / `@container` / `:is()` | 宿主是 WebView2，旧版整条规则失效且**无报错** | 显式加 class |
| `-webkit-app-region: drag` | 吞掉 mousedown，按钮点不动 | `--wails-draggable: drag` 只给标题栏，内部控件显式 `no-drag` |
| flex 容器同时 `justify-content:center` + `overflow:auto` | 内容溢出被顶出可视区且**滚不上去** | 容器只 `display:flex; overflow:auto`，居中交给内层 `margin:auto` |

---

## 7. 插件相关界面（只讲宿主侧）

**插件页面本身不归本文管。** 插件的样式规范、令牌与组件在插件仓库
`quickdock-plugins/plugin-ui-style.md`。两套体系互不相通，这条事实划出了本仓库的边界：

- **插件 iframe 拿不到宿主 CSS 变量** —— `utils/pluginBridge.ts` 只把 `data-theme` 写进 iframe 的
  `<html>` 并转发 postMessage，**不注入任何变量**。所以不要在本仓库里给插件写 `qd-*` 类或
  `var(--color-*)` 样式：写过去是空值，整条声明失效且**无任何报错**。
- **窗口材质靠一个布尔标记传达，不注入样式**：`plugin:theme` 消息带 `backdrop: boolean`
  （`usePluginHost.ts` 的 `themePayload()`），桥脚本据此写 `<html data-backdrop="1">`。
  插件侧只需 `html[data-backdrop] body { background: transparent }` 就能让材质透出来；
  标记由 MutationObserver 盯 `.qd-backdrop` 补发 —— 材质是异步确定的，常晚于 iframe onload。
- **插件窗口只允许一层铺底**：`.plugin-standalone`（App.vue 外壳）与 `.pf-iframe` 都必须透明，
  唯一那层在 `.plugin-window`（PluginPage.vue 根节点），材质档用更实的
  `--color-acrylic-bg`（0.90 / 0.94）—— iframe 内容是透明的，窗口底太透会让插件正文
  直接压在壁纸上。三层半透明叠乘后材质透过率只剩 12%，症状是「窗口有材质、页面看不出」。
- 本仓库 `plugins/shared/common.css` 是插件仓库模板的副本（主程序启动时同步给旧插件兜底注入），
  **不在它上面改样式**。
- 宿主配色变更需要搬进插件模板，属插件仓库的流程，见那边文档第 3 节。

宿主自己的三处插件界面，**完全遵守第 3~6 节**：

- **插件管理页 / 插件市场页**是普通业务页：`.qd-page-header` + `.qd-btn*`，不另起页头。
- **插件窗口外壳 `PluginPage.vue`** 的标题栏是 36px（窗口小，不套 44px 页头），颜色与描边仍走 token。
- **插件窗口底色**由 Go 侧 `pluginWindowBackground()` 决定，**必须与 `--color-bg-primary` 同值**
  （当前深 `#0c0e13` / 浅 `#f6f7f9`）。改配色时两处一起改，否则内容渲染前首帧会闪一块异色底。
  材质档 `.plugin-window` 是 0.90 / 0.94，与首帧实色只差 10%，比旧版 0.50 更不容易看出闪。

---

## 8. 改完必须验证（`vue-tsc` 与测试对 CSS 一无所知）

1. `npx vue-tsc --noEmit` —— 只保证类型，不保证样式。
2. **SFC 解析体检**：`@vue/compiler-sfc` 的 `parse` + `compileTemplate` 扫 `src/**/*.vue`。
   漏写 `<script setup>` 开标签时 `vue-tsc` 也会 EXIT=0，只有 `vite build` 才报。
3. `vite build`（沙箱里输出到 `E:/tmp/qd-dist`，避开对 `dist/assets` 的安全删除拦截）。
4. **截图看**：把真实 `style.css` + `app.css` + 组件 scoped 拼成预览页，
   Chrome `--headless=new --force-device-scale-factor=1|2 --screenshot`，**深/浅各一张**，
   逐条检查：三栏顶部基线是否同一水平线、hover 与选中是否可分、禁用态是否还能看见、
   1080px 宽下长路径是否只剩省略号。
5. 预览页**必须包含 App 层的横栏**（场景标签条），否则会得出"看起来是齐的"的错误结论。
