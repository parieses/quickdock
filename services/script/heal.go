package script

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/scriptrun"
	"quickdock/services"
)

// ScriptPatch AI 自愈产出的补丁建议（存 script_runs.ai_patch 的 JSON）。
//
// 为什么给「完整新文件内容」而不是 unified diff：diff 要正确应用得处理
// 上下文行、fuzzy match、行尾差异（CRLF/LF），任何一处错位都会把文件改成
// 语法错误的样子；而全文替换的判定最直白——用户看着 diff 面板点确认，
// 不对就不点。脚本通常几百行，这个代价可以接受。
type ScriptPatch struct {
	Diagnosis string `json:"diagnosis"`
	Fix       string `json:"fix"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	// Model 记录是哪个模型给的，方便判断「同一个模型老给错方案」。
	Model string `json:"model"`
}

// healSystemPrompt 自愈的系统提示。
//
// 明确要求「只输出 JSON」并给出结构，是因为散文式回答没法程序化应用；
// 而要求「无法确定时 content 留空」，避免模型硬编一个更糟的版本。
const healSystemPrompt = `你是脚本排障助手。用户会给你一个失败脚本的源码、运行日志和退出码。
请定位失败原因并给出最小修复。

只输出一个 JSON 对象，不要 markdown 代码块，不要任何额外文字：
{"diagnosis":"一句话说清为什么失败","fix":"一句话说明改了什么","path":"要修改的文件相对路径","content":"修改后的完整文件内容；若无法确定修复方案则填空字符串"}

要求：
- content 必须是能直接替换原文件的完整内容，不要省略号、不要 diff 片段。
- 只改与失败有关的部分，不要顺手重构、不要改无关格式。
- 如果失败原因是外部因素（网络、凭证过期、目标网站改版），content 留空，在 diagnosis 里说清该怎么办。`

// HealScriptRun 手动触发「AI 分析这次失败」。
func (s *ScriptService) HealScriptRun(runID string) *services.ApiResult {
	if s.App.AskAIFn == nil {
		return services.FailMsg("AI 未配置，请先到 AI 设置里填写模型")
	}
	patch, err := s.healRun(runID)
	if err != nil {
		return services.Fail(err)
	}
	if patch == nil {
		return services.Ok(map[string]any{"running": true})
	}
	return services.Ok(patch)
}

// healRun 生成补丁建议并落库。并发去重：用户连点按钮只跑一次。
func (s *ScriptService) healRun(runID string) (*ScriptPatch, error) {
	if s.App.AskAIFn == nil {
		return nil, fmt.Errorf("AI 未配置")
	}
	if _, loaded := s.healing.LoadOrStore(runID, struct{}{}); loaded {
		return nil, nil
	}
	defer s.healing.Delete(runID)

	run, err := s.App.DB.GetScriptRun(runID)
	if err != nil {
		return nil, err
	}
	sc, err := s.App.DB.GetScript(run.ScriptID)
	if err != nil {
		return nil, err
	}
	src, entry, err := s.readEntryForHeal(sc)
	if err != nil {
		return nil, err
	}
	logTail, _ := scriptrun.ReadTail(run.LogPath, 16<<10)

	var b strings.Builder
	fmt.Fprintf(&b, "脚本名：%s\n语言：%s\n入口：%s\n退出码：%d\n运行状态：%s\n\n",
		sc.Name, sc.Lang, entry, run.ExitCode, run.Status)
	fmt.Fprintf(&b, "=== 源码（%s）===\n%s\n\n", entry, src)
	fmt.Fprintf(&b, "=== 日志尾部 ===\n%s\n", logTail)
	b.WriteString("\n请严格按约定输出 JSON。")

	answer, model, err := s.App.AskAIFn(healSystemPrompt, b.String(), 90)
	if err != nil {
		return nil, fmt.Errorf("AI 分析失败: %w", err)
	}
	patch := parsePatch(answer)
	if patch == nil {
		return nil, fmt.Errorf("AI 返回内容无法解析为补丁，原始回复：%s", truncateRunes(answer, 300))
	}
	patch.Model = model
	// 模型偶尔会把路径写成绝对路径或不写，统一兜回入口文件。
	if strings.TrimSpace(patch.Path) == "" {
		patch.Path = entry
	}

	data, _ := json.Marshal(patch)
	if err := s.App.DB.SetScriptRunAIPatch(runID, string(data)); err != nil {
		logger.W("[script] 保存 AI 建议失败 %s: %v", runID, err)
	}
	s.emit(EventRun, map[string]any{
		"runId": runID, "scriptId": sc.ID, "status": run.Status,
		"aiPatch": patch, "healed": true,
	})
	return patch, nil
}

// readEntryForHeal 取入口文件内容（超过 64KB 截断——更大的文件已经不适合
// 整份丢给模型，硬塞只会挤掉日志这个更有用的证据）。
func (s *ScriptService) readEntryForHeal(sc *db.Script) (string, string, error) {
	abs, err := s.resolveRel(sc.ID, sc.Entry)
	if err != nil {
		return "", sc.Entry, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", sc.Entry, err
	}
	src := string(data)
	if len(src) > 64<<10 {
		src = src[:64<<10] + "\n…（内容过长已截断）"
	}
	return src, filepathToSlash(sc.Entry), nil
}

// parsePatch 从模型回复里提取 JSON。
//
// 容错而不是直接 json.Unmarshal(answer)：即使提示词说了「不要 markdown」，
// 仍有相当比例的回复会包一层 ```json，直接解析等于把好答案扔掉。
func parsePatch(answer string) *ScriptPatch {
	s := strings.TrimSpace(answer)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		// 去掉语言标记（```json 的 json）
		if j := strings.IndexByte(s, '\n'); j >= 0 {
			s = s[j+1:]
		}
		if k := strings.LastIndex(s, "```"); k >= 0 {
			s = s[:k]
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil
	}
	var patch ScriptPatch
	if err := json.Unmarshal([]byte(s[start:end+1]), &patch); err != nil {
		return nil
	}
	return &patch
}

// ApplyScriptPatch 应用 AI 建议并可选重跑。
//
// 绝不自动应用：脚本是用户资产，模型改错一行的代价可能远大于「继续报错」。
// 这个函数只由用户点确认触发。
func (s *ScriptService) ApplyScriptPatch(runID string, rerun bool) *services.ApiResult {
	run, err := s.App.DB.GetScriptRun(runID)
	if err != nil {
		return services.Fail(err)
	}
	if strings.TrimSpace(run.AIPatch) == "" {
		return services.FailMsg("这次运行没有 AI 建议")
	}
	var patch ScriptPatch
	if err := json.Unmarshal([]byte(run.AIPatch), &patch); err != nil {
		return services.Fail(fmt.Errorf("AI 建议格式损坏: %w", err))
	}
	if strings.TrimSpace(patch.Content) == "" {
		return services.FailMsg("这次建议没有可应用的代码改动")
	}
	// 路径必须落在脚本目录内：模型完全可能给出 /etc/passwd 这种路径。
	if res := s.WriteScriptFile(run.ScriptID, patch.Path, patch.Content); res.Code != 0 {
		return res
	}
	out := map[string]any{"path": patch.Path}
	if rerun {
		if res := s.RunScriptWithTrigger(run.ScriptID, "", TriggerAI); res.Code == 0 {
			out["runId"] = res.Data
		} else {
			out["rerunError"] = res.Msg
		}
	}
	return services.Ok(out)
}

func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
