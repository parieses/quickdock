package services
import (
	"bufio"
	"context"
	"encoding/json"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"quickdock/internal/db"
)


func (a *AppService) streamAIChat(ctx context.Context, cfg AIProfile, messages []map[string]interface{}, convID string, onToken func(text string), onReasoning func(text string), onUsage func(promptTokens, completionTokens int)) (string, error) {
	body := map[string]interface{}{
		"model":          cfg.Model,
		"messages":       messages,
		"temperature":    cfg.Temperature,
		"stream":         true,
		"stream_options": map[string]interface{}{"include_usage": true},
	}
	if cfg.MaxTokens > 0 {
		body["max_tokens"] = cfg.MaxTokens
	}
	if cfg.TopP > 0 {
		body["top_p"] = cfg.TopP
	}
	if cfg.FrequencyPenalty != 0 {
		body["frequency_penalty"] = cfg.FrequencyPenalty
	}
	if cfg.PresencePenalty != 0 {
		body["presence_penalty"] = cfg.PresencePenalty
	}
	if cfg.ThinkingEnabled {
		body["enable_thinking"] = true
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	ep, authKey, authVal := apiEndpoint(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authKey, authVal)
	// 导致逐字流式退化成"一次性整段"。强制 identity 让上游返回原始分块 SSE。
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")

	client := a.aiHTTPClient
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(eb)))
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return sb.String(), ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Index  int `json:"index"`
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"` // Agnes/DeepSeek thinking 模式
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil && onUsage != nil {
			onUsage(chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		content := chunk.Choices[0].Delta.Content
		reasoning := chunk.Choices[0].Delta.ReasoningContent
		if content != "" {
			sb.WriteString(content)
			if onToken != nil {
				onToken(content)
			}
		}
		if reasoning != "" && onReasoning != nil {
			onReasoning(reasoning)
		}
	}
	if err := scanner.Err(); err != nil {
		return sb.String(), err
	}
	return sb.String(), nil
}

// buildAIMessages 组装发送给模型的消息列表，并自动执行摘要压缩
func (a *AppService) buildAIMessages(ctx context.Context, cfg AIProfile, convID, mode, currentUser string) ([]map[string]interface{}, error) {
	hist, err := a.DB.ListAIMessages(convID)
	if err != nil {
		return nil, err
	}
	conv, err := a.DB.GetAIConversation(convID)
	if err != nil {
		return nil, err
	}

	estTokens := func(msgs []db.AIMessage) int {
		n := 0
		for _, m := range msgs {
			n += len([]rune(m.Content)) * 10 / 16
		}
		return n
	}

	// 摘要压缩：历史超阈值且足够长时，总结最旧部分并清理
	if estTokens(hist) > aiTokenBudget && len(hist) > aiKeepRecent {
		toSum := hist[:len(hist)-aiKeepRecent]
		recent := hist[len(hist)-aiKeepRecent:]
		if summary, serr := a.summarizeAndStore(ctx, cfg, conv, toSum); serr == nil {
			_ = a.DB.DeleteOldAIMessages(convID, aiKeepRecent)
			hist = recent
			conv.Summary = summary
		}
	}

	system := aiModePrompts[mode]
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt + "\n\n" + system
	}
	if conv.Summary != "" {
		system += "\n\n[历史对话摘要]\n" + conv.Summary
	}

	messages := []map[string]interface{}{{"role": "system", "content": system}}
	for _, m := range hist {
		messages = append(messages, map[string]interface{}{"role": m.Role, "content": m.Content})
	}
	messages = append(messages, map[string]interface{}{"role": "user", "content": currentUser})
	return messages, nil
}

// summarizeAndStore 调用模型把旧对话压缩为摘要并写回会话
func (a *AppService) summarizeAndStore(ctx context.Context, cfg AIProfile, conv *db.AIConversation, toSum []db.AIMessage) (string, error) {
	var b strings.Builder
	b.WriteString("请将以下对话压缩为简洁的中文摘要，保留关键事实、结论与待办，便于后续延续上下文。只输出摘要，不要解释：\n\n")
	for _, m := range toSum {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	msgs := []map[string]string{
		{"role": "system", "content": "你是一个压缩助手，负责把对话历史提炼为要点摘要。"},
		{"role": "user", "content": b.String()},
	}
	summary, err := a.callAIOnce(ctx, cfg, msgs)
	if err != nil {
		return "", err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", fmt.Errorf("摘要为空")
	}
	combined := summary
	if conv.Summary != "" {
		combined = conv.Summary + "\n" + summary
	}
	if err := a.DB.UpdateAIConversationMeta(conv.ID, "", combined); err != nil {
		return "", err
	}
	return combined, nil
}

// callAIOnce 一次性（非流式）调用，用于摘要压缩 / 标题生成
func (a *AppService) callAIOnce(ctx context.Context, cfg AIProfile, messages []map[string]string) (string, error) {
	// 兜底：调用方漏传/传 nil ctx 时避免 http.NewRequestWithContext panic（如旧版标题生成）
	if ctx == nil {
		ctx = context.Background()
	}
	body := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": cfg.Temperature,
		"max_tokens":  cfg.MaxTokens,
		"stream":      false,
	}
	if cfg.TopP > 0 {
		body["top_p"] = cfg.TopP
	}
	if cfg.FrequencyPenalty != 0 {
		body["frequency_penalty"] = cfg.FrequencyPenalty
	}
	if cfg.PresencePenalty != 0 {
		body["presence_penalty"] = cfg.PresencePenalty
	}
	if cfg.ThinkingEnabled {
		body["enable_thinking"] = true
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	ep, authKey, authVal := apiEndpoint(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authKey, authVal)

	client := a.aiHTTPClient
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(eb)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("无返回内容")
	}
	return out.Choices[0].Message.Content, nil
}

// callAIStream 已由 streamAIChat 取代（回调式，供本地 HTTP 流式服务逐段写出 NDJSON）。

// AITestConnection 发送一条简单的用户消息验证 API Key 和模型是否可用。
// 使用非流式单次调用，超时 15 秒。
