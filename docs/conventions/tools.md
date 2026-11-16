# 工具（Tool）注册与暴露规范

适用范围：新增/修改任何工具的地方——`services/mcp/tools_*.go`、`services/ai/tools/*.go`、
`services/mcp/catalog.go`。总纲见 `docs/conventions/backend.md`。

一句话：**能力只有一份，暴露策略有两份，闸门只有一条。** 违反这句话的改动，
最终都会变成「面板上明明开着，实际却调不到」或者「关一处，另一边也没了」。

## 1. 四个正交的维度

新增工具前先想清楚它在这四层上分别是什么，别把它们混成一个字段：

| 维度 | 回答的问题 | 落在哪 |
|---|---|---|
| **Domain（域）** | 它属于哪个能力域 | `Tool.Domain` = `services/mcp/catalog.go` 里的一个 ID |
| **Permission（等级）** | 它有多危险 | `Tool.Level`（`LevelRead` / `LevelWrite` / `LevelRisk`） |
| **Scene（能力档）** | 内置 AI 在哪种对话里能用它 | 由所属域的 `Domain.Scenes` 决定，如 `readScenes()` / `onlyRead()` |
| **Role（角色）** | 子 agent 能不能用 | `internal/mcp` 的 `ToolFilter`，与工具定义无关 |
| **Approval（审批）** | 执行前问不问、能不能记住 | `Tool.GrantKey` / `RiskNote` / `ArgBrief` |

历史包袱：`Tool.Category` 是 `Domain` 的旧名，`normalize()` 里互填，只为兼容没迁移的
读取方。**新代码一律写 `Domain`。**

## 2. 两份策略，互不读取

| | 外部 MCP 客户端 | 内置 AI |
|---|---|---|
| 策略值 | `mcpsrv.MCPPolicy()` | `AIService.aiToolPolicy()` |
| 存放 | MCP 的 `config.json` | `settings` 表的 `ai_tool_policy` |
| 界面 | 环境管理 → MCP | 设置 → AI → 权限（`AiToolSwitchList.vue`） |
| 读取方 | `MCPTools()` / `MCPDoctor()` | `AIToolPolicy()` |

**这两份不许互相读、不许「顺手一起改」。** 曾经 `maxLvl` / `disabled` 是全局各一份，
结果是「在 MCP 面板关掉 file_edit，内置 AI 也改不了文件」——用户以为自己在管一个开关，
实际动了两个。要合并回一份，先想清楚怎么回答「我只想让 AI 少一只手」这个诉求。

需要展示另一侧状态时，只做**只读徽标**（MCP 面板上的「AI 侧：关」），不做联动。

## 3. 一条闸门：Resolve → Invoke

```
Resolve(ToolScope{Caller, Policy, Scene, Keep})  → Surface{Tools, Domains, Dropped}
Invoke(ctx, Surface, name, args, Approver)       → 结果
```

- 装配与执行**共用同一个 `Surface`**。模型看到的工具面与「能调什么」必须是同一个对象；
  分成两套条件判断，迟早出现「面板显示开启、对话里却调不到」。
- 想加一层过滤 → 加进 `ToolScope`，不要在执行路径上再写一个 if。
- 被挡掉的原因要写进 `Surface.Dropped`，`MCPDoctor` 靠它回答「为什么它调不到」。

## 4. 提示词由装配结果生成

`services/ai/ai_tool_scope.go` 的 `buildToolHint(surface)` 按**这次真挂上的域**拼提示词。
不要新写「手写常量 + 人工同步」的说明段落——那正是之前「门放开了，文案还写着去教程模式」
的来源。

写 `Domain.Hint` / `Domain.WriteHint` 的纪律：

- `Hint` 只写该域**必然挂载**的只读工具；
- 写类工具写进 `WriteHint`（只读档不挂它们，却教模型去调，模型会对着报错反复重试）；
- `WriteHint` 可以引用读类工具（「改之前先 note_read 看清原文」），反方向不行。

`services/mcp/catalog_test.go` 会检查这条约定。

## 5. 新增一个工具：清单

1. 在 `services/mcp/catalog.go` 找到所属域；没有合适的域就加一条
   （`ID` / `LabelKey`（i18n key）/ `Order`（唯一且大于 0）/ `Summary` / `Hint` / `Scenes`）。
2. 在 `services/mcp/tools_*.go` 注册，`Domain` 填第 1 步的 ID，`Level` 想清楚。
3. 需要「记住本次会话都允许」的工具 → 加 `withGrantKey`；有特有风险文案 → `withRiskNote`。
4. 内置 AI 专有（改文件、跑命令、抓网页这类）→ 注册进 `mcpsrv.Agent`
   （`services/ai/tools`），**不要**注册进 `mcpsrv.Global`：外部客户端自带这些能力，
   再暴露一遍只会让模型在两套同名能力间反复挑。
5. 面板文案：域标题用 `LabelKey`，避免在 `useMCPPanel.ts` 里再写一份顺序表。
6. 跑 `go test ./services/mcp/ ./internal/mcp/ ./services/ai/`——域完备性、提示词一致性、
   核心工具是否还在 AI 面上，都有测试守着。

## 6. 别做的事

- 不要在 `Invoke` 之外再判一次「这个工具能不能调」。
- 不要把 `Permission`（问不问）和 `Scene`（有没有这只手）合成一个开关。
- 不要在工具注册处之外按工具名 `switch` 去决定审批粒度（旧 `ai_approval.go` 里的写法），
  那会让「加一个工具要改三个文件」重新成立。
- 不要让 `internal/**` 反向依赖 `services/**`。
