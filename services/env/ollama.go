// Package env 环境管理服务 —— Ollama 模型管理（/api/tags|ps|pull|delete）。
//
// 模型与程序版本是两件事：这里只经 Ollama 自己的 HTTP API 操作模型库，
// 不碰 runtime/ollama/<version> 下的程序目录（反之见 EnvDeleteVersion）。
package env

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	envmgr "quickdock/internal/env"
	"quickdock/services"
)

// ollamaQueryTimeout 查询类调用（模型列表 / 运行中模型）的超时。
const ollamaQueryTimeout = 15 * time.Second

// ollamaDeleteTimeout 删除模型的超时。delete 会顺带把已加载的模型卸载出显存，
// 比普通查询慢一点，但远不到拉取的量级。
const ollamaDeleteTimeout = 60 * time.Second

// ollamaPullTimeout 拉取模型的总时限上界。模型动辄几十 GB，给足 2 小时；
// 设上界是为了避免前端关掉后 goroutine 永久挂着（到点 ctx 取消会中断流，
// 已下完的分层留在磁盘上，下次拉取自动续传）。
const ollamaPullTimeout = 2 * time.Hour

// ollamaPullProgress 拉取进度事件载荷，经 quickdock:env:ollama:pull 推送到前端。// Status 原样透传 Ollama 的协议文案（pulling manifest / downloading / verifying sha256 digest…）——
// 那是协议文本，与界面语言无关，不做翻译。
type ollamaPullProgress struct {
	Model     string  `json:"model"`
	Stage     string  `json:"stage"` // progress | done | error
	Status    string  `json:"status"`
	Message   string  `json:"message"`
	Completed int64   `json:"completed"`
	Total     int64   `json:"total"`
	Percent   float64 `json:"percent"`
}

// emitOllamaPull 推送拉取进度事件。
func (s *EnvironmentService) emitOllamaPull(p ollamaPullProgress) {
	if s.App.App() != nil {
		s.App.App().Event.Emit("quickdock:env:ollama:pull", p)
	}
}

// EnvOllamaModels 列出本地已下载模型（GET /api/tags）。
func (s *EnvironmentService) EnvOllamaModels() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), ollamaQueryTimeout)
	defer cancel()
	models, err := s.App.Env.OllamaModels(ctx)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(models)
}

// EnvOllamaRunningModels 列出当前已加载进显存的模型（GET /api/ps）。
func (s *EnvironmentService) EnvOllamaRunningModels() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), ollamaQueryTimeout)
	defer cancel()
	models, err := s.App.Env.OllamaRunningModels(ctx)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(models)
}

// EnvOllamaDeleteModel 删除某个本地模型（DELETE /api/delete）。
func (s *EnvironmentService) EnvOllamaDeleteModel(model string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	name := strings.TrimSpace(model)
	if name == "" {
		return services.FailMsg("模型名不能为空")
	}
	ctx, cancel := context.WithTimeout(context.Background(), ollamaDeleteTimeout)
	defer cancel()
	if err := s.App.Env.OllamaDeleteModel(ctx, name); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvOllamaPullModel 拉取模型（POST /api/pull）。
// 异步执行：立即返回，进度经 quickdock:env:ollama:pull 事件推送
// （与 EnvInstall 同一范式，故不需要 taskId + 轮询）。
func (s *EnvironmentService) EnvOllamaPullModel(model string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	name := strings.TrimSpace(model)
	if name == "" {
		return services.FailMsg("模型名不能为空")
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.emitOllamaPull(ollamaPullProgress{
					Model: name, Stage: "error",
					Message: fmt.Sprintf("拉取 %s 异常: %v", name, rec),
				})
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), ollamaPullTimeout)
		defer cancel()
		err := s.App.Env.OllamaPullModel(ctx, name, func(p envmgr.OllamaPullProgress) {
			s.emitOllamaPull(ollamaPullProgress{
				Model: name, Stage: "progress", Status: p.Status,
				Completed: p.Completed, Total: p.Total, Percent: p.Percent,
			})
		})
		if err != nil {
			msg := err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "拉取超过 2 小时已中断（已下载的分层保留，可重新拉取续传）"
			}
			s.emitOllamaPull(ollamaPullProgress{Model: name, Stage: "error", Message: msg})
			return
		}
		s.emitOllamaPull(ollamaPullProgress{Model: name, Stage: "done", Message: "拉取完成", Percent: 100})
	}()
	return services.Ok(nil)
}

// EnvOllamaSearchLibrary 搜索 Ollama 官方模型库，供拉取输入框做联想候选。
// q 为空返回官方默认排序（热门），非空按其过滤；查询失败返回错误由前端静默降级为纯文本输入。
func (s *EnvironmentService) EnvOllamaSearchLibrary(q string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	models, err := s.App.Env.OllamaSearchLibrary(q)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(models)
}

// EnvOllamaCheckUpdate 检查 Ollama 是否有新版本可更新。
// 只检测不替换：前端据此显示「有新版本」徽标，用户确认后走 EnvInstall 原地更新。
func (s *EnvironmentService) EnvOllamaCheckUpdate() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	info, err := s.App.Env.OllamaCheckUpdate()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(info)
}
