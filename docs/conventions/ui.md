# UI 样式规范

适用范围：**只限 `frontend/src/**`（宿主 UI）**。
插件页面（`plugins/**/frontend/**`）的样式规范在**另一个仓库** `quickdock-plugins/plugin-ui-style.md`，
本文不涉及 —— 两套令牌体系互不相通，混着写必错（见 §7）。
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
| 应用底 | `--color-bg-primary` | `#0f1116` | `#f5f6f8` |
| 侧栏/卡片 | `--color-bg-secondary` | `#151920` | `#ebedf1` |
| 控件底 | `--color-bg-tertiary` | `#1c2029` | `#e3e6eb` |
| hover | `--color-bg-hover` | `#1f242e` | `#e1e4e9` |
| 选中/按下 | `--color-bg-active` | `#262d39` | `#d7dbe2` |
| 正文 | `--color-text-primary` | `#e6e9ef` | `#1f242c` |
| 次要 | `--color-text-secondary` | `#a1a8b4` | `#565e6b` |
| 弱 | `--color-text-muted` | `#6f7784` | `#737b88` |
| 禁用 | `--color-text-disabled` | `#565d68` | `#a2a8b0` |
| 分隔线 | `--color-border` | `#22272f` | `#d8dce2` |
| hover 描边 | `--color-border-light` | `#2e3440` | `#c6cbd3` |
| 强调 | `--color-accent` | `#5b8cff` | `#5b8cff` |
| 强调 hover | `--color-accent-hover` | `#4a7af0` | `#4a7af0` |
| 强调淡底 | `--color-accent-bg` | `rgba(91,140,255,.10)` | `rgba(91,140,255,.08)` |
| 危险 | `--color-danger` / `-bg` / `-border` | `#ef4444` / `.12` / `.40` | `#ef4444` / `.10` / `.45` |
| 成功 | `--color-success` / `-bg` | `#22c55e` / `.12` | `#22c55e` / `.12` |
| 警告 | `--color-warning` / `-bg` / `-border` | `#f59e0b` / `.12` / `.40` | `#f59e0b` / `.14` / `.45` |

其余（终端调色板 20 项、代码块、日志级别、搜索高亮、toast）见 `style.css`，同样双主题成对。

### 2.2 尺寸与阶梯

| 阶梯 | 值 | 用途 |
|---|---|---|
| 间距 | `--space-1..11` = 2/4/6/8/12/16/20/24/32/40/48 | 组件内边距与块间距 |
| 圆角 | `--radius-xs` 4 / `-sm` 6 / `-md` 10 / `-lg` 14 / `-full` | 控件 6、卡片 10、浮层 10、全圆 999 |
| 动效 | `--transition-fast` 100ms / `-base` 150ms | 只给 color/background/border，不给尺寸 |

**高度基准（全应用统一，不得自造）**

| 元素 | 高度 | 水平内边距 |
|---|---|---|
| 一级页头 `.qd-page-header` | 44（`min-height`） | 16 |
| 面板头 `.qd-panel-header` | 38 | 12 左 / 8 右 |
| 工具栏 `.qd-toolbar` | 44（8+28+8） | 16 |
| 按钮 `.qd-btn` | 28 | 11 |
| 小按钮 `.qd-btn-sm` | 24 | 8 |
| 图标按钮 `.qd-icon-btn` | 24×24 | 0 |
| 行内按钮 `.qd-row-btn` | 22×22 | 0 |
| 输入框 `.qd-input` / 搜索行 `.qd-panel-search` | 28 | 9 / 8 |
| 列表行 `.qd-row` | `min-height` 34 | 8 左 / 6 右 |
| 侧栏导航 `.nav-item` | `min-height` 32 | 10 |
| 徽章 `.nav-badge` | 16 | 4 |

### 2.3 字号阶梯

| 字号 | 用途 |
|---|---|
| 10 | 角标、计数徽章 |
| 11 | 大写分区标题（`.qd-panel-title` 11/600/uppercase/`letter-spacing: .08em`）、次要说明、行副标题 |
| 12 | 控件文字（按钮、输入框）、页头副标题、`.qd-page-sub` |
| 13 | **正文基准**，列表行标题 |
| 15 | 一级页头标题 `.qd-page-title`（600） |
| 17 | 引导页标题 |
| 20+ | 仅数据展示（统计数值、图表标签），不进列表 |

存量里还有 14 / 16 / 18 散落在若干对话框，属**待迁移**，新增代码不得使用。

---

## 3. 组件清单（只准用这些）

### 3.1 页头（只有两级）

```html
<!-- 一级页面：44px，标题 15px，右侧动作区自动靠右 -->
<div class="qd-page-header">
  <div class="qd-page-titles">
    <h2 class="qd-page-title">环境服务</h2>
    <span class="qd-page-sub">可选副标题</span>
  </div>
  <div class="qd-page-actions">
    <button class="qd-btn">刷新</button>
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
- **任何页面不许再写 `xxx-header`。** 存量 5 种写法（10/14/16/20px 内边距、14/15/18px 标题、有无分隔线）已全部迁移；新增一律用上面两个类。

### 3.2 按钮（只有一种盒子，三种语气）

| 类 | 用途 |
|---|---|
| `.qd-btn` | 默认次级：1px 边 + 透明底，hover 出底色 |
| `.qd-btn-primary` | 主行动：实底 accent。**同屏只允许一个** |
| `.qd-btn-ghost` | 页头里成组的次要动作，无边框，hover 才浮出 |
| `.qd-btn-danger` | 破坏性动作：淡红底 + 红字，hover 转实红 |
| `.qd-btn.is-on` | 开关型已选中（视图切换、分组筛选） |
| `.qd-btn-sm` | 表格行尾、卡片内部 |

图标按钮统一 `.qd-icon-btn`（24×24，`padding: 0` 必须写死）。
**`.qd-icon-btn` 的定义在 `app.css`**，不要在 `panels.css` 里重复一份 —— 曾经它只在 `panels.css` 里，于是只引 `app.css` 的页面拿到浏览器默认按钮样式（白底方块）。

### 3.3 输入框

- 独立输入：`.qd-input`（28px，`bg-tertiary` 底，focus 转 accent 边）。
- 带图标：`.qd-input-wrap > svg + .qd-input`（图标绝对定位，输入框左内边距 28）。
- 三栏过滤行：`.qd-panel-search` + `.qd-search-input`（**`max-width: 380px`**，否则项目栏会把搜索框拉满上千像素）。

### 3.4 行 / 卡片 / 空状态

- 列表行：`.qd-row`（`min-height:34`、`gap:8`、`margin-bottom:2`、`radius-sm`）
  - 内部：`.qd-row-icon`(24) + `.qd-row-main` > `.qd-row-title` / `.qd-row-sub` + `.qd-row-actions`
  - 状态：`:hover` = `bg-hover`；`.active` = `accent-bg` + 图标转 accent；`.dragging` = `opacity:.4`
- 卡片：`.qd-card`（`bg-secondary` + 1px 边 + `radius-md`）
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
| 选中 | 底色 `accent-bg`，图标转 accent | 整行文字变蓝（字多像超链接） |
| 键盘焦点 | `:focus-visible` 1px accent 外框 | 去掉 outline 又不给替代 |
| 禁用 | 底色不动，文字转 `text-disabled`，`cursor: not-allowed` | 用 `opacity`（叠在 muted 上对比度只剩 ~1.6:1，等于消失） |
| 加载中 | 原位换成 `<Loader2 class="spin">`，宽度不变 | 按钮变宽/抖动 |

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
| 圆角随手写 8/12/16 | 13px 正文下像移动端卡片 | 4/6/10/14 |
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
- 本仓库 `plugins/shared/common.css` 是插件仓库模板的副本（主程序启动时同步给旧插件兜底注入），
  **不在它上面改样式**。
- 宿主配色变更需要搬进插件模板，属插件仓库的流程，见那边文档第 3 节。

宿主自己的三处插件界面，**完全遵守第 3~6 节**：

- **插件管理页 / 插件市场页**是普通业务页：`.qd-page-header` + `.qd-btn*`，不另起页头。
- **插件窗口外壳 `PluginPage.vue`** 的标题栏是 36px（窗口小，不套 44px 页头），颜色与描边仍走 token。
- **插件窗口底色**由 Go 侧 `pluginWindowBackground()` 决定，**必须与 `--color-bg-primary` 同值**
  （当前深 `#0f1116` / 浅 `#f5f6f8`）。改配色时两处一起改，否则内容渲染前首帧会闪一块异色底。

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
