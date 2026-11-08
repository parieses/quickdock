package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"quickdock/internal/env"
)

// embedding 客户端。
//
// 只做一件简单的事：把一批文本换成向量。批的划分、token 预算、
// 事务边界都在 services/kb 里——那里才知道「哪些块还没算」以及
// 「一批写库要持多久的锁」。这里只负责发请求、重试、把响应按
// index 摆回原位。
//
// 代理必须走 env.ProxyTransport()：WebView2 跟随 WinINET 系统代理，
// 而 Go 默认只认 HTTP_PROXY 环境变量。曾经有环境 ProxyEnable=1 但端口
// 没起，直连超时、走代理也超时，表现是「AI 能聊天但知识库一条都建不了」
// ——因为聊天的请求是前端发的，走的不是同一条网络栈。

const (
	embedTimeout  = 60 * time.Second
	embedMaxRetry = 2
)

// EmbedConfig 一份 embedding 配置（与 chat 共用 BaseURL/APIKey，只有 model 不同）。
type EmbedConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Embedder 无状态客户端，可复用。dim 在第一次成功响应后记住，
// 之后每次校验——维度漂移是换模型最典型的后果，必须在写入前拦住。
type Embedder struct {
	cfg    EmbedConfig
	client *http.Client
	dim    int
}

// NewEmbedder 创建客户端。
func NewEmbedder(cfg EmbedConfig) *Embedder {
	return &Embedder{
		cfg: cfg,
		client: &http.Client{
			Timeout:   embedTimeout,
			Transport: env.ProxyTransport(),
		},
	}
}

// Dim 已知维度（0 = 还没成功调用过）。
func (e *Embedder) Dim() int { return e.dim }

// Model 当前模型名。
func (e *Embedder) Model() string { return e.cfg.Model }

// EmbedEndpoint 由 BaseURL 推出 embeddings 端点。
func EmbedEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	return base + "/embeddings"
}

// EmbedBatch 把 texts 换成向量，返回顺序与入参一一对应。
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if e.cfg.APIKey == "" && !isLocalEndpoint(e.cfg.BaseURL) {
		return nil, errors.New("未配置 API Key")
	}
	if e.cfg.Model == "" {
		return nil, errors.New("未配置 embedding 模型")
	}

	var lastErr error
	for attempt := 0; attempt <= embedMaxRetry; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		vecs, err := e.do(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
		var ee *embedError
		if errors.As(err, &ee) && !ee.retry {
			break
		}
	}
	return nil, lastErr
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (e *Embedder) do(ctx context.Context, texts []string) ([][]float32, error) {
	endpoint := EmbedEndpoint(e.cfg.BaseURL)
	if endpoint == "" {
		return nil, &embedError{msg: "未配置接口地址", retry: false}
	}

	body, err := json.Marshal(embedRequest{Model: e.cfg.Model, Input: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Azure 走 api-key 头，其余走标准 Bearer。与 ai_config.go 的
	// chat 端点保持同一套判断，避免「聊天能通、embedding 401」。
	if strings.Contains(strings.ToLower(e.cfg.BaseURL), "openai.azure.com") {
		req.Header.Set("api-key", e.cfg.APIKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, &embedError{status: resp.StatusCode, msg: msg, retry: retry}
	}

	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embedding 响应解析失败: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, &embedError{msg: out.Error.Message, retry: false}
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embedding 返回条数不符：要 %d 条，回 %d 条", len(texts), len(out.Data))
	}

	vecs := make([][]float32, len(texts))
	for i, d := range out.Data {
		// 按 index 摆位，不能按数组顺序：OpenAI 明确不保证返回有序，
		// 错位的后果是「A 块的向量写进 B 块」——索引看着成功，
		// 检索结果全乱，而且没有任何报错可供排查。
		idx := d.Index
		if idx < 0 || idx >= len(texts) {
			idx = i
		}
		v := make([]float32, len(d.Embedding))
		for j, f := range d.Embedding {
			v[j] = float32(f)
		}
		vecs[idx] = v
	}
	for i, v := range vecs {
		if len(v) == 0 {
			return nil, fmt.Errorf("第 %d 条文本没有返回向量", i+1)
		}
	}

	if e.dim == 0 {
		e.dim = len(vecs[0])
	} else if e.dim != len(vecs[0]) {
		return nil, fmt.Errorf("%w：期望 %d 维，实际 %d 维", ErrDimMismatch, e.dim, len(vecs[0]))
	}
	return vecs, nil
}

// embedError 带重试判定的接口错误。
type embedError struct {
	status int
	msg    string
	retry  bool
}

func (e *embedError) Error() string {
	if e.status > 0 {
		return fmt.Sprintf("embedding 接口返回 %d：%s", e.status, e.msg)
	}
	return e.msg
}

// isLocalEndpoint 本地推理（Ollama / LM Studio / 自建网关）通常不校验 Key。
func isLocalEndpoint(baseURL string) bool {
	b := strings.ToLower(baseURL)
	return strings.Contains(b, "localhost") || strings.Contains(b, "127.0.0.1") ||
		strings.Contains(b, "0.0.0.0") || strings.Contains(b, "[::1]")
}

// EstimateTokens 粗略估算一段文本的 token 数。
//
// 中文 1 字约 1 token，英文约 4 字符 1 token。这里统一按 rune 数当上界：
// 高估只会多切几批（每批少几条），低估会撞 400 让整批重试——
// 代价明显不对称，所以宁高不低。
func EstimateTokens(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
