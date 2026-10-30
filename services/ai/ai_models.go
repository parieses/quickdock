package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"quickdock/internal/platform"
	"quickdock/services"
)

// AIFetchModelsRequest 拉取模型列表的入参。
//
// 直接带上 provider / baseURL / apiKey 而不是只给 profileID，是为了支持
// 用户刚在编辑框里填好 BaseURL+Key、尚未保存 就想看模型的场景——
// 此时库里还没有这份配置。ID 用于「Key 是回填掩码」时回退到库里解密后的 Key。
type AIFetchModelsRequest struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	BaseURL  string `json:"baseURL"`
	APIKey   string `json:"apiKey"`
}

// AIFetchModelsResult 拉取结果。
type AIFetchModelsResult struct {
	Models []string `json:"models"`
}

// AIFetchModels 拉取指定档案可用的模型列表（OpenAI 兼容的 GET /models）。
//
// 为什么放在后端而不是前端直连：
//   - API Key 存的是 DPAPI 密文，前端只拿到掩码；由后端代发请求，明文 Key 不出后端。
//   - 不同服务商的模型列表端点不一致（Azure 走 /openai/models），统一在这里处理。
//
// 覆盖范围：OpenAI / DeepSeek / Kimi / 通义等兼容接口都是 GET {base}/models；
// Azure 走 GET {base}/openai/models?api-version=...；Ollama 的 /v1/models 同样兼容。
func (a *AIService) AIFetchModels(req AIFetchModelsRequest) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}

	provider := req.Provider
	baseURL := req.BaseURL
	apiKey := strings.TrimSpace(req.APIKey)

	// 回填的档案：Key 是掩码（含 ***）或空时，取库里该档案解密后的 Key。
	var stored *aiProfileStored
	if req.ID != "" {
		profiles := a.loadAIProfiles()
		for i := range profiles {
			if profiles[i].ID == req.ID {
				stored = &profiles[i]
				break
			}
		}
	}
	if stored != nil {
		if provider == "" {
			provider = stored.Provider
		}
		if baseURL == "" {
			baseURL = stored.BaseURL
		}
		if apiKey == "" || strings.Contains(apiKey, "***") {
			apiKey = stored.APIKey
			if apiKey != "" {
				dec, e := platform.DecryptSecret(apiKey)
				if e != nil {
					return services.Fail(fmt.Errorf("API Key 解密失败，请重新输入: %w", e))
				}
				apiKey = dec
			}
		}
	}

	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return services.FailMsg("请先填写 Base URL")
	}
	if apiKey == "" && provider != "ollama" {
		return services.FailMsg("请先填写 API Key")
	}

	// Azure 的模型列表不在 /v1 下；其余兼容接口统一 {base}/models。
	modelsURL := baseURL + "/models"
	if provider == "azure" {
		modelsURL = baseURL + "/openai/models?api-version=2024-02-15-preview"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return services.Fail(err)
	}
	httpReq.Header.Set("Accept", "application/json")
	// 本地 Ollama / 免鉴权网关：无 Key 时不发空 Bearer（部分网关会因空 token 直接 401）。
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := a.aiHTTPClient.Do(httpReq)
	if err != nil {
		return services.Fail(fmt.Errorf("网络错误: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200])
		}
		return services.Fail(fmt.Errorf("%s", msg))
	}

	// 兼容两种返回：OpenAI 风格的 {data:[{id}]}，以及少数网关的 {models:[...]}。
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
			// 个别本地兼容层返回 name 而非 id。
			Name string `json:"name"`
		} `json:"data"`
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&parsed); err != nil {
		return services.Fail(fmt.Errorf("响应解析失败: %w", err))
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(parsed.Data)+len(parsed.Models))
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, d := range parsed.Data {
		if d.ID != "" {
			add(d.ID)
		} else {
			add(d.Name)
		}
	}
	for _, m := range parsed.Models {
		add(m)
	}
	if len(out) == 0 {
		return services.FailMsg("该接口未返回可用模型，请手动填写模型名")
	}
	sort.Strings(out)
	return services.Ok(AIFetchModelsResult{Models: out})
}
