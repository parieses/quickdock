package ai

// 内置 AI 的工具面：策略、场景、装配、提示词。
//
// 与外部 MCP 的分工（这是本次改造的核心边界，改之前先读这段）：
//   - 能力定义只有一份（Tool / Level / Domain），两个入口复用同一批 Handler；
//   - 暴露策略有两份：外部客户端那份存在 MCP 配置里（mcpsrv.MCPPolicy），
//     内置 AI 这份存在 settings 的 ai_tool_policy 里，互不读取、互不影响；
//   - 装配与执行都走 mcpsrv.Resolve / mcpsrv.Invoke，两条路径的差别只有入参。
//
// 所以「在 MCP 面板关掉 file_edit」不会让 AI 改不了文件，反过来也一样。
// 想让 AI 少一只手，去设置 → AI 的工具开关里关；那是另一份名单。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/services"
)

// aiToolPolicyKey 内置 AI 的暴露策略（等级上限 + 单工具开关）落库键。
//
// 与 MCP 的 config.json 分开存是有意的：两个入口的开关必须互不影响，
// 共用一个键就又会回到「关一处、两边都没了」的老路上。
const aiToolPolicyKey = "ai_tool_policy"

// aiToolPolicyView 回给前端的形状。
type aiToolPolicyView struct {
	MaxLevel int      `json:"maxLevel"`
	Disabled []string `json:"disabled"`
}

// aiToolPolicy 读内置 AI 的策略。
//
// 默认「只读 + 低危写、一个都不关」：AI 侧从全开起步（用户明确要求），
// 高危仍被等级挡着；要收紧就去 AI 设置里关工具或调等级。
// 解析失败按默认值处理——一条写坏的 JSON 不该把 AI 变成全放行或全禁用。
func (a *AIService) aiToolPolicy() mcpsrv.Policy {
	def := mcpsrv.NewPolicy(mcpsrv.LevelWrite, nil)
	if a == nil || a.App == nil || a.App.DB == nil {
		return def
	}
	raw, err := a.App.DB.GetSetting(aiToolPolicyKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return def
	}
	var v aiToolPolicyView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		logger.W("[ai-tool-policy] 解析失败，按默认策略处理: %v", err)
		return def
	}
	return mcpsrv.NewPolicy(v.MaxLevel, v.Disabled)
}

// saveAIToolPolicy 写内置 AI 的策略。空策略也照写：用户可能就是要「全关 + 等级 0」。
func (a *AIService) saveAIToolPolicy(p mcpsrv.Policy) error {
	v := aiToolPolicyView{MaxLevel: p.MaxLevel, Disabled: p.DisabledNames()}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return a.App.DB.SetSetting(aiToolPolicyKey, string(b))
}

// aiSceneForPermission 把执行权限三档翻译成工具面档位。
//
// readonly → 只挂读类工具；其余两档都全开（差别在审批闸门要不要问）。
// legacyScene 是合并前的「能力档」列（ai_conversations.scene）：只在请求/会话
// 都没有明确权限时才看它，让升级前设成「只读档」的老任务不会突然长出写操作的手。
func aiSceneForPermission(mode, permission, legacyScene string) string {
	switch strings.ToLower(strings.TrimSpace(permission)) {
	case aiPermissionReadOnly:
		return mcpsrv.SceneReadOnly
	case aiPermissionFull:
		return mcpsrv.SceneAssistant
	}
	switch strings.ToLower(strings.TrimSpace(legacyScene)) {
	case mcpsrv.SceneReadOnly:
		return mcpsrv.SceneReadOnly
	case mcpsrv.SceneAssistant:
		return mcpsrv.SceneAssistant
	}
	// 遗留的 plan 会话当年是隐式只读：没有 scene、没有 permission 的老行靠这条收口。
	if strings.TrimSpace(mode) == aiPlanMode {
		return mcpsrv.SceneReadOnly
	}
	return mcpsrv.SceneAssistant
}

// aiSurface 装配内置 AI 的工具面。
//
// filter 是编排层的角色收窄（子 agent），nil 表示不收窄。场景、策略、角色三层
// 都在 mcpsrv.Resolve 里，这里只是把 AI 侧的输入翻译成它的 Scope。
func (a *AIService) aiSurface(mode, permission, legacyScene string, filter *ToolFilter) mcpsrv.Surface {
	return mcpsrv.Resolve(mcpsrv.ToolScope{
		Caller: mcpsrv.CallerAI,
		Policy: a.aiToolPolicy(),
		Scene:  aiSceneForPermission(mode, permission, legacyScene),
		Keep:   filter.keep,
	})
}

// aiToolsPayload 把装配结果转成 OpenAI tools[] 格式。
//
// 第二个返回值表示外部 MCP 工具因名额被截：调用方据此在 system 里补一句说明——
// 不说明的话，模型会拿着一轮里根本不存在的工具名去调，然后对着「未知工具」反复重试。
func aiToolsPayload(sf mcpsrv.Surface) ([]map[string]any, bool) {
	out := make([]map[string]any, 0, len(sf.Tools))
	// 外部工具按 server 分组收集：名额与体积都按**整组**判（见下面的说明）。
	var extGroups []*extToolGroup
	extByServer := map[string]*extToolGroup{}
	for _, t := range sf.Tools {
		desc := t.Description
		if t.Domain == mcpsrv.CategoryExternal {
			// 外部工具的描述截到 extToolDescMaxRune（理由见那个常量）。
			desc = clipRunes(desc, extToolDescMaxRune)
		}
		entry := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": desc,
				"parameters":  t.InputSchema,
			},
		}
		if t.Domain != mcpsrv.CategoryExternal {
			out = append(out, entry)
			continue
		}
		// 体积按**实际要发出去的那份**算：截过的描述 + 序列化后的 schema。
		// schema 序列化失败（理论上不会）就按 0 计——宁可多挂一个，也不要
		// 因为一次编码错误把整组工具藏掉。
		size := len(desc)
		if raw, err := json.Marshal(t.InputSchema); err == nil {
			size += len(raw)
		}
		key := extServerOf(t.Description)
		g, ok := extByServer[key]
		if !ok {
			g = &extToolGroup{server: key}
			extByServer[key] = g
			extGroups = append(extGroups, g)
		}
		g.items = append(g.items, entry)
		g.sizes = append(g.sizes, size)
		g.bytes += size
	}
	// 外部工具名额：tools[] 总数一大，一部分上游直接 400，token 账单也难看。
	// 内置工具优先，外部工具填剩余名额，超出不挂。
	//
	// 这里按**整组**取舍：把某个 server 挂一半比整套不挂更糟——模型手里有
	// 「这个 server 的一部分工具」，会认为缺的那些能力根本不存在。
	extTrimmed := false
	kept, bytes := 0, 0
	for _, g := range extGroups {
		if kept+len(g.items) <= extToolsMax && bytes+g.bytes <= extToolsMaxBytes {
			out = append(out, g.items...)
			kept += len(g.items)
			bytes += g.bytes
			continue
		}
		// 一个 server 自己就超了上限（只接了一两个 MCP 时很常见）：整套跳过等于
		// 用户刚接的 server 一个工具都调不到。这时按顺序装到装不下为止。
		if kept == 0 {
			for i, it := range g.items {
				if kept+1 > extToolsMax || bytes+g.sizes[i] > extToolsMaxBytes {
					break
				}
				out = append(out, it)
				kept++
				bytes += g.sizes[i]
			}
		}
		extTrimmed = true
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, extTrimmed
}

// ---- 提示词 ----

// 工具说明的固定段落：讲纪律，不讲清单。
//
// 各域「有什么、怎么用」由 Domain.Hint 生成（见 services/mcp/catalog.go），
// 这里只留三段与域无关的内容：能力总述、重复调用纪律、执行纪律。
const (
	// toolHintHeader 能力总述。后两条是踩出来的，不是预防性条款：
	//   - 重复调用：模型会在同一轮里给出两条一模一样的 robot_notify，两条都执行
	//     → 用户手机收到两遍。宿主只能拦「参数完全相同」的，换个参数重发还得靠模型收敛。
	//   - 能力介绍：schema 就在上下文里，问「你会什么」时模型会照抄工具名。
	toolHintHeader = "\n\n[可用工具]\n" +
		"你可以调用宿主提供的工具，直接查询甚至修改 QuickDock 里的东西——不必只讲操作步骤，" +
		"用户说「帮我看一下 / 帮我改一下 / 为什么不行」时，先调工具拿真实状态再回答。" +
		"工具返回错误时读错误信息自行调整参数，不要重复同一个失败调用；某个能力不可用时直接说明，不要假装已经完成。\n" +
		"[工具使用纪律]\n" +
		"1. 同一个工具 + 完全相同的参数，一次提问里只调一次。结果会立刻回给你，重调拿不到新信息；" +
		"发通知、写文件、启停服务这类操作重放一遍就是第二次真实副作用。宿主会拦下重复调用并直接返回上次结果。\n" +
		"2. 需要多个信息时并行调用**不同**工具，不要反复调同一个。已经调过并拿到结果的，直接用那个结果。\n" +
		"3. 工具是你干活的手段，不是你的能力清单。被问到「你会什么」时用 QuickDock 的功能说话" +
		"（工作空间、剪贴板、笔记、待办、定时任务、站点监控、环境管理、插件等），不要罗列工具名和接口。\n"

	// toolHintDiscipline 执行纪律：解决「每一步前后都夹一句旁白」。
	// 只在挂了 code/web 这类「连续动手」的域时追加——只查一条待办时讲这些是噪音。
	toolHintDiscipline = "[执行纪律]\n" +
		"- 要调工具就直接调：不写「我先看一下」「让我查查」这类预告，也不要每步都汇报一次进度。" +
		"工具返回后不复述结果——用户已经能在工具行里看到，你只给结论或做下一步。\n" +
		"- 要产出文件就用 file_write，**不要**在 run_command 里 echo / Set-Content / node -e 造文件——" +
		"引号、换行、编码在那条路上都容易出错，而且用户看不出你写了什么。\n" +
		"- 高危命令被拒绝时，把命令写给用户让他在终端里执行，不要换个写法反复试。\n"

	// toolHintReadOnly 只读权限档的说明。写类工具没挂，必须明说，
	// 否则模型会照着自己的习惯去调 file_write，然后收到一串「本次未开放」。
	toolHintReadOnly = "\n[执行权限：只读]\n" +
		"本次对话是**只读档**：写类工具（file_write / file_edit / run_command、启停服务、建任务等）没有挂载，你调不到。" +
		"需要动手时说明「把执行权限切到默认或完全访问」，不要尝试绕过。\n"

	// toolHintExhausted 工具轮次用尽后的 system 说明。
	//
	// 这段存在的唯一原因是一个实测出来的坏结果：轮次用尽时工具被撤下（请求里不再有
	// tools），但 system 里那段「可用工具」如果还留着，模型会认为工具仍然可用，
	// 于是把调用写成正文——DeepSeek 会吐出一串 `<｜DSML｜invoke>` 标记。
	toolHintExhausted = "\n\n[工具轮次已用尽]\n" +
		"本轮**没有任何可用工具**（工具已经全部撤下，再输出工具调用不会被执行）。" +
		"请直接根据上面已经拿到的结果，用文字把最终答案写完并收尾；" +
		"不要输出任何工具调用格式或标记（诸如 invoke / parameter 这类 DSML 标记）。" +
		"确实还没做完的，用一段话讲清楚「已经完成什么、还差什么、下一步该怎么做」，不要留悬空的半句话。\n"

	// toolHintNoTools 本次请求从始至终没有挂工具时的说明。
	//
	// 定时任务的「问答」档、以及权限过滤后一个工具都不剩的场景都走这条路。
	// 缺这段的实测后果很具体：模型按习惯发起工具调用（DeepSeek 写成 DSML 标记），
	// 本轮工具列表为空 → 调用被忽略 → 正文一个字都没有 → saveSegment 判定
	// 「未产出内容」跳过落库，用户看到的就是「任务执行成功，但没有任何输出」，
	// 库里只剩一条 user 消息。
	toolHintNoTools = "\n\n[本次不提供工具]\n" +
		"本轮**没有任何可用工具**（工具未挂载，输出工具调用不会被执行）。" +
		"请直接用文字回答：可以依据已有知识作答；需要实时信息（天气、股价、新闻等）时，" +
		"说明「这需要联网或工具才能查到」，不要编造数据。" +
		"不要输出任何工具调用格式或标记（诸如 invoke / parameter 这类 DSML 标记）。\n"
)

// buildToolHint 按装配结果生成工具说明。
//
// 只讲**这次真的挂上了**的域：没挂的域一个字都不提。这正是旧实现最大的毛病——
// 三份手写常量靠人跟代码同步，门放开了文案还写着「去教程模式」，或者反过来
// 教模型去调一个没挂的函数。
func buildToolHint(sf mcpsrv.Surface) string {
	if len(sf.Tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(toolHintHeader)
	if sf.Scene == mcpsrv.SceneReadOnly {
		b.WriteString(toolHintReadOnly)
	}
	// 写类工具在不在，决定要不要追加 WriteHint 与执行纪律。
	hasWrite := false
	for _, t := range sf.Tools {
		if t.Level > mcpsrv.LevelRead {
			hasWrite = true
			break
		}
	}
	var sections strings.Builder
	for _, d := range sf.Domains {
		if strings.TrimSpace(d.Hint) == "" {
			continue
		}
		sections.WriteString(d.Hint)
		if hasWrite {
			if wh := strings.TrimSpace(d.WriteHint); wh != "" {
				sections.WriteString(wh)
			}
		}
	}
	if sections.Len() > 0 {
		b.WriteString(sections.String())
	}
	if hasWrite {
		b.WriteString(toolHintDiscipline)
	}
	return b.String()
}

// approverFunc 把闭包适配成 mcpsrv.Approver（审批链路只认接口）。
type approverFunc func(ctx context.Context, req mcpsrv.ApprovalRequest) (bool, string)

func (f approverFunc) Approve(ctx context.Context, req mcpsrv.ApprovalRequest) (bool, string) {
	return f(ctx, req)
}

// ---- 绑定方法 ----

// AIToolPolicy 返回内置 AI 的暴露策略与全量工具清单（设置页用）。
//
// 与 MCP 面板的 MCPTools 是两份**互不读取**的数据：这里只报 AI 侧开关，
// 前端把两边并排显示，让用户看清「这两个开关不是一回事」。
func (a *AIService) AIToolPolicy() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	pol := a.aiToolPolicy()
	// 这里**不以 Surface 成员为准**：Surface 已经把等级门算进去了，拿它当开关状态
	// 会出现「用户什么都没关，但高危域整组显示已关」。开关只回答一件事——
	// 用户自己关没关它；等级挡住的那部分用 gated 单独标，两者在界面上是两种样子。
	tools := make([]map[string]any, 0, len(mcpsrv.Catalog()))
	for _, t := range mcpsrv.Catalog() {
		tools = append(tools, map[string]any{
			"name":      t.Name,
			"domain":    t.Domain,
			"level":     t.Level,
			"desc":      t.Description,
			"enabled":   !pol.Disabled[t.Name],
			"disabled":  pol.Disabled[t.Name],
			"gated":     t.Level > pol.MaxLevel,
			"agentOnly": t.AgentOnly,
			// notReady：运行环境没准备好（如 Everything 没跑）。开关开着也调不到，
			// 界面必须能解释——否则用户看到的是一条「已启用但从不出现」的工具。
			"notReady":     t.NotReady,
			"notReadyNote": t.NotReadyNote,
		})
	}
	domains := make([]map[string]any, 0, 8)
	for _, d := range mcpsrv.Domains() {
		domains = append(domains, map[string]any{
			"id":     d.ID,
			"label":  d.LabelKey,
			"order":  d.Order,
			"desc":   d.Summary,
			"public": d.External,
		})
	}
	return services.Ok(map[string]any{
		"maxLevel": pol.MaxLevel,
		"disabled": pol.DisabledNames(),
		"tools":    tools,
		"domains":  domains,
	})
}

// AISetToolEnabled 单独开启/关闭某个工具（只影响内置 AI）。
func (a *AIService) AISetToolEnabled(name string, enabled bool) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return services.FailMsg("缺少工具名")
	}
	pol := a.aiToolPolicy()
	next := make([]string, 0, len(pol.Disabled)+1)
	for _, n := range pol.DisabledNames() {
		if n != name {
			next = append(next, n)
		}
	}
	if !enabled {
		next = append(next, name)
	}
	if err := a.saveAIToolPolicy(mcpsrv.NewPolicy(pol.MaxLevel, next)); err != nil {
		return services.Fail(err)
	}
	return services.OkMsg(nil, "已更新")
}

// AISetDomainEnabled 域级总开关：一次开/关整组工具。
//
// 用「把该域每个工具名写进 disabled」实现，而不是引入第二套域级状态：
// 两份状态并存时，用户会遇到「域开着但单工具关着 / 域关着但单工具开着」的
// 组合，界面必须解释四种情况，而其中两种毫无意义。
func (a *AIService) AISetDomainEnabled(domain string, enabled bool) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return services.FailMsg("缺少域标识")
	}
	pol := a.aiToolPolicy()
	set := map[string]bool{}
	for _, n := range pol.DisabledNames() {
		set[n] = true
	}
	hit := 0
	for _, t := range mcpsrv.Catalog() {
		if t.Domain != domain {
			continue
		}
		hit++
		if enabled {
			delete(set, t.Name)
		} else {
			set[t.Name] = true
		}
	}
	if hit == 0 {
		return services.FailMsg("未知的域：" + domain)
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	if err := a.saveAIToolPolicy(mcpsrv.NewPolicy(pol.MaxLevel, names)); err != nil {
		return services.Fail(err)
	}
	return services.OkMsg(nil, "已更新")
}

// AISetToolMaxLevel 调整内置 AI 的等级上限（只影响 AI）。
func (a *AIService) AISetToolMaxLevel(level int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if level < mcpsrv.LevelRead || level > mcpsrv.LevelRisk {
		return services.FailMsg(fmt.Sprintf("工具等级超出范围: %d", level))
	}
	pol := a.aiToolPolicy()
	if err := a.saveAIToolPolicy(mcpsrv.NewPolicy(level, pol.DisabledNames())); err != nil {
		return services.Fail(err)
	}
	return services.OkMsg(nil, "已更新")
}

// aiConversationPermission 取会话上落库的执行权限（conv 可能为空——新建对话的第一条消息）。
func aiConversationPermission(conv *db.AIConversation) string {
	if conv == nil {
		return ""
	}
	return strings.TrimSpace(conv.Permission)
}

// aiConversationLegacyScene 取合并前的能力档列，只用于兜住老数据。
func aiConversationLegacyScene(conv *db.AIConversation) string {
	if conv == nil {
		return ""
	}
	return conv.Scene
}
