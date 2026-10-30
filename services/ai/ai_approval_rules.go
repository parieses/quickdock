package ai

// 按工具名覆盖审批策略（方案 A1 的第三态配置）。
//
// 默认策略由等级推导：读类 auto、写类 ask、高危 ask 且只给单次。这套默认值对
// 大多数人够用，但总有两类人被它卡住：
//
//   - 把 run_command 改成 auto 的人：他在一个只有测试命令的项目里工作，每条
//     npm test 都要点一次是纯粹的摩擦；
//   - 把 robot_notify 改成 deny 的人：他确实不想让模型往群里发任何东西。
//
// 于是留一层覆盖：显式设过的以设置为准，没设过的仍按等级推导。覆盖表存 settings
// 的一张 JSON，跟 ai_profiles 同一个位置——不另开文件，免得出现「配置在两处、
// 改了一处不生效」的老毛病。

import (
	"encoding/json"
	"sort"
	"strings"

	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/services"
)

// aiApprovalRulesKey 审批策略覆盖表的落库键。
const aiApprovalRulesKey = "ai_approval_rules"

// aiApprovalRuleView 一条可配置的工具，回给设置页。
type aiApprovalRuleView struct {
	Tool string `json:"tool"`
	// Level 工具等级（1=写 2=高危），前端据此给高风险项标红。
	Level int    `json:"level"`
	Desc  string `json:"desc"`
	// Mode 当前生效的策略：auto / ask / deny。
	Mode string `json:"mode"`
	// Custom 是否来自用户覆盖。false 表示这一行显示的是按等级推导的默认值。
	Custom bool `json:"custom"`
	// AgentOnly 只给内置 AI 用（外部 MCP 客户端看不到）。
	AgentOnly bool `json:"agentOnly"`
	// Editable 这一行的策略能否在这里改。
	//
	// 读类工具恒 auto：把它们列出来是为了让人看得见「git_diff 也在管着」，
	// 而不是留一块空白让人怀疑「git 相关的是不是漏了」。要拦读类请到 MCP
	// 面板关掉那个工具——那样连「模型知道有这个能力」都一并去掉。
	Editable bool `json:"editable"`
}

// loadApprovalRules 读覆盖表。
//
// 解析失败按「没有任何覆盖」处理：一条写坏的 JSON 不该把整道闸门改成全放行
// 或全拦截——两种都比回到默认值危险，而默认值本来就是安全的。
func (a *AIService) loadApprovalRules() map[string]approvalMode {
	if a == nil || a.App == nil || a.App.DB == nil {
		return nil
	}
	raw, err := a.App.DB.GetSetting(aiApprovalRulesKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		logger.W("[ai-approval] 策略配置解析失败，按默认值处理: %v", err)
		return nil
	}
	out := make(map[string]approvalMode, len(stored))
	for name, m := range stored {
		switch approvalMode(m) {
		case approvalAuto, approvalAsk, approvalDeny:
			out[name] = approvalMode(m)
		default:
			// 认不出的取值直接丢掉，不要让一个手改错的字符串变成「未知策略」
			// 然后在闸门里走到 default 分支——那种分支的语义以后一定会变。
			logger.W("[ai-approval] 忽略无法识别的策略 %q=%q", name, m)
		}
	}
	return out
}

// saveApprovalRules 写覆盖表。空表就删掉这个键，不留一个 {} 在库里。
func (a *AIService) saveApprovalRules(rules map[string]approvalMode) error {
	flat := make(map[string]string, len(rules))
	for name, m := range rules {
		flat[name] = string(m)
	}
	if len(flat) == 0 {
		return a.App.DB.SetSetting(aiApprovalRulesKey, "")
	}
	b, err := json.Marshal(flat)
	if err != nil {
		return err
	}
	return a.App.DB.SetSetting(aiApprovalRulesKey, string(b))
}

// AIListApprovalRules 列出所有工具及当前生效策略（bound）。
//
// 读类也在列表里，但标 editable=false：一开始的做法是整个过滤掉，结果设置页
// 只剩十几个写工具，「git 相关的一个都没有」——而项目里根本没有 git_commit /
// git_push（提交与推送是用户自己的事），只有 git_status / git_diff 两个只读的。
// 用户看不到它们，只能怀疑是漏了。列出来、锁定、配一句说明，比空着好。
//
// 排序仍把高危排最前：要调策略的时候，人找的几乎总是那几个能删库的。
func (a *AIService) AIListApprovalRules() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	custom := a.loadApprovalRules()
	all := mcpsrv.Catalog()
	out := make([]aiApprovalRuleView, 0, len(all))
	for _, t := range all {
		pol := aiApprovalPolicyFor(t.Name, t.Level, custom)
		_, isCustom := custom[t.Name]
		out = append(out, aiApprovalRuleView{
			Tool:      t.Name,
			Level:     t.Level,
			Desc:      approvalToolDesc(t.Description),
			Mode:      string(pol.mode),
			Custom:    isCustom,
			AgentOnly: t.AgentOnly,
			Editable:  t.Level >= mcpsrv.LevelWrite,
		})
	}
	// 高危排前面：要调策略的时候，人找的几乎总是那几个能删库的。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level > out[j].Level
		}
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return false
	})
	return services.Ok(out)
}

// AISetApprovalRule 设置一个工具的审批策略（bound）。
//
// mode 取 auto / ask / deny；传空串表示「恢复默认」，即从覆盖表里删掉这一项。
// 不提供「批量设置」：逐项点是为了让每一次放宽都过一次手——一个「全部改成 auto」
// 的按钮，按下去之后这道闸门就只剩装饰作用了。
func (a *AIService) AISetApprovalRule(tool, mode string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	tool = strings.TrimSpace(tool)
	mode = strings.TrimSpace(mode)
	if tool == "" {
		return services.FailMsg("缺少工具名")
	}
	level, ok := approvalToolLevel(tool)
	if !ok {
		return services.FailMsg("没有这个工具：" + tool)
	}
	if level < mcpsrv.LevelWrite {
		// 读类不开放覆盖：设成 deny 的正确做法是去 MCP 面板关掉这个工具，
		// 那样连「模型知道有这个能力」都一并去掉，比执行前拦更彻底。
		return services.FailMsg("读类工具请在 MCP 面板里直接关闭")
	}
	rules := a.loadApprovalRules()
	if rules == nil {
		rules = make(map[string]approvalMode, 4)
	}
	switch approvalMode(mode) {
	case "":
		delete(rules, tool)
	case approvalAuto, approvalAsk, approvalDeny:
		rules[tool] = approvalMode(mode)
	default:
		return services.FailMsg("未知策略：" + mode)
	}
	if err := a.saveApprovalRules(rules); err != nil {
		return services.Fail(err)
	}
	return services.OkMsg(nil, "已保存")
}

// approvalToolLevel 查一个已注册工具的等级。
//
// 不用 mcpsrv.ToolLevel：那个函数查不到时返回 LevelRisk（闸门需要「保守猜测」），
// 而这里需要区分「高危」和「根本没有这个名字」——把拼错的工具名当高危存下来，
// 用户会以为设置生效了，实际什么都没发生。
func approvalToolLevel(name string) (int, bool) {
	for _, t := range mcpsrv.Catalog() {
		if t.Name == name {
			return t.Level, true
		}
	}
	return 0, false
}

// approvalToolDesc 把工具描述压成一行。
//
// 工具描述是给模型看的长句（往往三四行），设置页每行只有一行的位置；
// 不截的话一张表会被撑成一条一条的段落，反而看不出差异。
func approvalToolDesc(desc string) string {
	s := strings.TrimSpace(desc)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if rs := []rune(s); len(rs) > 48 {
		s = string(rs[:48]) + "…"
	}
	return s
}
