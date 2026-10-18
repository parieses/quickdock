# 前端样式与组件规范

适用范围：`frontend/src/**`。总纲见根目录 `AGENTS.md`。

## 1. 设计 Token（唯一源：`frontend/src/style.css`）

### 1.1 完整变量族

| 族 | 变量 | 说明 |
|---|---|---|
| 间距 | `--space-1`(2px) … `--space-11`(48px) | 8px 基准递进，禁止写死 `margin: 8px` |
| 圆角 | `--radius-xs`(4) `-sm`(6) `-md`(10) `-lg`(14) `-full` | 无裸 `--radius` |
| 阴影 | `--shadow-sm` `-md` `-lg` `-focus` `-border` `-border-right` `-toast` `-toast-hover` | 阴影即边框，不用 1px border 描边分层 |
| 背景 | `--color-bg-primary` `-secondary` `-tertiary` `-hover` `-active` `-overlay` | 层级由浅入深 |
| 面 | `--color-surface`、`--color-surface-elevated` | 卡片 / 浮层 |
| 文字 | `--color-text-primary` `-secondary` `-muted` `-disabled` | 无裸 `--color-text` |
| 边框 | `--color-border`、`--color-border-light`、`--color-border-focus` | |
| 强调 | `--color-accent` `-hover` `-bg` `-text` `-border` `-muted` `-light` | |
| 状态 | `--color-danger(-bg)`、`--color-success(-bg)`、`--color-warning(-bg)`、`--color-star` | `-bg` 为低透明底，用于 hover / 徽章 |
| 滚动条 | `--color-scrollbar-thumb` `-hover` `-track` | |
| Toast | `--color-toast-{error,success}-{bg,text,border}` | |
| 布局/字体 | `--sidebar-width` `--collection-width` `--search-height` `--font-family` `--font-mono` | |

### 1.2 硬性规则

- 主题由 `<html data-theme="dark|light">` 驱动，`composables/useThemeName.ts` 用 MutationObserver 订阅，**是唯一真相**。
  `App.vue` 与 `SettingsModal.vue` 各写了一份 apply/setTheme（债务 D4），新增代码**不要**再引入第三份。
- 新增 token：必须在 `:root,[data-theme="dark"]` 与 `[data-theme="light"]` **两处同时定义**，否则浅色主题会失效。
- 禁止 `var(--xxx, #333)` 这种"变量 + 硬编码兜底"的写法——它掩盖了变量未定义的问题。
- 语义色优先用状态 token；确实需要品牌/图表色时，先扩展 token，不要在脚本里存色值数组（反例：`EnvironmentPage.vue` 的 29 个运行时品牌色）。

## 2. 类名体系（公共类，新增代码一律复用）

公共类目前定义在 `style.css` 与 `SettingsModal.vue`（后者为非 scoped 的"事实全局"，债务 D5）。
**新增代码必须用下面这套，不得新建同义类。**

| 语义 | 类 | 备注 |
|---|---|---|
| 设置分组容器 | `.section` / `.section-title` / `.section-desc` | 每个设置页一个 `.section` 起步 |
| 表单行 | `.setting-row` > `.setting-label` + `.setting-control` | 标签与控件**同行** |
| 按钮 | `.btn .btn-primary` / `.btn .btn-secondary` | 两个类必须成对出现，缺 `.btn` 会丢内边距 |
| 操作行 / 结果提示 | `.action-row` / `.result-hint` | |
| 纵向字段 | `.field` / `.field-label` / `.field-input` / `.field-hint` / `.field-checkbox` / `.toggle-field` | 用于需要独占一行的长表单 |
| 开关 | `.toggle-btn` + `.toggle-knob`（激活态 `.active`） | 不用 `.switch/.knob`（债务 D3） |
| 空状态 | `.empty-icon` / `.empty-text` / `.empty-hint` | |

**命名约定**：BEM 风格的块前缀 + 短横线（`.snapshot-item-actions`），组件私有类加业务前缀避免与公共类重名。

## 3. 单文件组件规范

- 语言：全部 `<script lang="ts" setup>`（项目 43/43 如此），不用 Options API。
- 样式：`<style scoped>`。Teleport 到 body 的内容（右键菜单等）才允许非 scoped，并加组件前缀类名防污染。
- 块顺序：`script → template → style`（与现有绝大多数文件一致）。
- 禁止内联样式。允许的例外：
  1. Canvas / SVG 运行时绘制（`MonitorPage.vue` 图表，色值仍需取自 token 或主题扩展表）；
  2. 依赖运行时数据的动态尺寸（如树缩进 `paddingLeft: depth * 14 + 'px'`）；
  3. 例外必须在就近注释说明理由。
- props：`defineProps<{...}>()` + `withDefaults`；emits 统一用**元组式** `defineEmits<{ close: [] }>()`，不再使用回调式 `(e:'x', v:T) => void`。
- 文件命名：组件 PascalCase（`SettingsModal.vue`）；composables camelCase 且带 `use` 前缀；utils / types camelCase。
- 页面级组件（`*Page.vue`）目前与子组件同放 `components/`，**保持现状**，新建页面仍放 `components/`（不再引入 `views/`，避免双目录并存）。

## 4. 调用后端的统一模式

`frontend/bindings/**` 由 `wails3 generate bindings -ts -i -clean` 生成，头部标注 DO NOT EDIT，
后端改签名后需重新生成（`npm run bindings`）。

```ts
import { unwrap } from '../utils/api'          // code !== 0 时 throw
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import type { ToastAPI } from '../types'
const toast = inject<ToastAPI>('toast')!       // App.vue provide

try {
  const data = unwrap<Foo>(await SomeService.DoSomething(a, b))
  // ...
} catch (e) {
  logErr('Scope.action', e)
  toast.error(getErrorMessage(e))
}
```

- 跨组件共享状态用 `composables/bridge.ts` / `stores/workspace.ts`，不新增全局事件总线。
- Toast 与 theme 通过 `provide/inject` 获取（`App.vue` 注入 `toast`、`theme`）。

## 5. i18n

- `i18n/index.ts`：`legacy:false`，默认 `zh-CN`，fallback `zh-CN`。
- 键名 camelCase；已有 `sched_*`、`mon_*`、`toolType_*` 等下划线前缀属历史遗留，新增键**不用下划线**。
- `zh-CN.ts` 与 `en-US.ts` 键集合必须完全一致（当前 1504/1504，双向无缺失）。新增文案两处都要加。
- 硬编码中文现状（待清理，不得新增）：`SettingsModal.vue` 语言按钮、`SettingsAI.vue` 测试提示、`TypeIcon.vue` 类型名映射。

## 6. 工具链现状

- 无 ESLint / Prettier 配置，构建脚本 `npm run build` = `vue-tsc && vite build`。
  **类型检查是前端唯一的硬门禁**，改动后必须跑 `npx vue-tsc --noEmit -p tsconfig.json`。
- 双入口 `main.html` / `plugin.html`（插件窗口），插件相关改动需确认两个入口。
