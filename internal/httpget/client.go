// Package httpget 封装「直连优先 + 失败走代理」的 GET JSON 重试骨架。
//
// 为什么单独成包：天气 (internal/weather) 与市场索引 (services/plugin) 各写了一遍
// 同样的循环——依次试多个 http.Client，传输失败 / 读失败 / 非 200 / JSON 损坏都记下
// 最后一个错误并换下一个 client，ctx 取消则立即停。两份代码曾连注释都几乎逐字相同。
//
// 刻意的边界：
//   - 只做传输与解析，**不**包装最终错误文案。各调用点的文案不同（天气用 %w 保留
//     HTTPStatusError 错误链给 errors.As，市场用 %v 带 sourceID），留在调用点。
//   - StatusErr / DecodeErr 用回调注入：上游返回非 200 时的错误类型各业务不同，
//     不能绑死某一种（天气要带响应体、市场只要 HTTP 码）。
//   - 不 import internal 下其它包，保持叶子包，避免 env / services 反向依赖成环。
package httpget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Options 描述一次 GET JSON 的所有可调差异；零值的 StatusErr/DecodeErr 有默认实现。
type Options struct {
	// Clients 依次尝试的 client 列表。直连优先、代理兜底的顺序由调用方决定
	//（是否加代理那一次取决于机器上有没有配代理，属于业务判断）。
	Clients []*http.Client
	// UserAgent 写入 User-Agent 头；留空则不设置。
	UserAgent string
	// Headers 额外请求头，如天气 API key。
	Headers map[string]string
	// MaxBytes 响应体上限，0 时不限制（调用方通常传一个明确上限防超大响应）。
	MaxBytes int64
	// StatusErr 非 200 时的错误构造。留空用 fmt.Errorf("HTTP %d")。
	StatusErr func(statusCode int, body []byte) error
	// DecodeErr JSON 解析失败时的错误构造。留空用 fmt.Errorf("响应不是有效 JSON: %w")。
	DecodeErr func(err error) error
}

// GetJSON 依次尝试 cfg.Clients，把第一个 200 + 合法 JSON 的响应解到 out。
// 全部失败时返回最后一个错误（未包装，调用方自行加业务文案并决定是否 %w）。
func GetJSON(ctx context.Context, rawURL string, out any, cfg Options) error {
	var lastErr error
	for _, c := range cfg.Clients {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if cfg.UserAgent != "" {
			req.Header.Set("User-Agent", cfg.UserAgent)
		}
		for k, v := range cfg.Headers {
			req.Header.Set(k, v)
		}

		resp, err := c.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := readBody(resp, cfg.MaxBytes)
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			if cfg.StatusErr != nil {
				lastErr = cfg.StatusErr(resp.StatusCode, body)
			} else {
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			if cfg.DecodeErr != nil {
				lastErr = cfg.DecodeErr(err)
			} else {
				lastErr = fmt.Errorf("响应不是有效 JSON: %w", err)
			}
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的请求通道")
	}
	return lastErr
}

func readBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	defer resp.Body.Close()
	if maxBytes > 0 {
		return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	}
	return io.ReadAll(resp.Body)
}
