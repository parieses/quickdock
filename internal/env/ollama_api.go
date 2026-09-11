package env

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---- Ollama 本地 REST API 客户端（/api/tags|ps|pull|delete）----
//
// 这里管的是**模型**，与程序版本管理彻底解耦：
//   - 模型库全局共享一份（默认 ~/.ollama/models，动辄几十 GB，跨版本/系统安装版共用）；
//   - DeleteVersion 只删程序目录、绝不碰模型（见 ollama.go）；
//   - DeleteModel 只经 Ollama 自己的 /api/delete 删模型，绝不碰程序目录。
// 两者互不越界，是本文件与 ollama.go 的分工底线。

// ollamaAPITimeout 短请求（tags/ps/delete）超时。delete 会顺带把已加载的模型卸载出显存，
// 所以比普通查询给得宽一些。
const ollamaAPITimeout = 30 * time.Second

// ollamaLocalClient 本机 API 专用客户端：显式把 Proxy 置 nil。
// 默认 transport 会读 HTTP_PROXY/HTTPS_PROXY，一旦用户配了代理，127.0.0.1 的请求也会被
// 送去代理（必然失败）。本机回环地址绝不能走代理。
var ollamaLocalClient = &http.Client{
	Timeout:   ollamaAPITimeout,
	Transport: &http.Transport{Proxy: nil},
}

// ollamaStreamClient 流式请求专用：不设总超时——拉一个 7B 模型在国内可能几十分钟，
// 30s 的 Timeout 会在中途掐断。生命周期交给 ctx（服务层给 2h 上界 + 前端可取消）。
var ollamaStreamClient = &http.Client{
	Transport: &http.Transport{Proxy: nil},
}

// OllamaModelDetails 模型详情（/api/tags 条目的 details 字段）。
type OllamaModelDetails struct {
	Format            string `json:"format"`
	Family            string `json:"family"`
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
}

// OllamaModel /api/tags 返回的本地已下载模型。
type OllamaModel struct {
	Name       string             `json:"name"`
	Model      string             `json:"model"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	ModifiedAt string             `json:"modified_at"`
	Details    OllamaModelDetails `json:"details"`
}

// OllamaRunningModel /api/ps 返回的当前已加载（占着显存）的模型。
type OllamaRunningModel struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Size      int64  `json:"size"`
	SizeVRAM  int64  `json:"size_vram"`
	ExpiresAt string `json:"expires_at"`
}

// OllamaPullEvent /api/pull 的流式事件（NDJSON 每行一个）：
//
//	{"status":"pulling manifest"}
//	{"status":"downloading","digest":"sha256:...","total":N,"completed":M}
//	{"status":"verifying sha256 digest"}
//	{"status":"success"}
//	{"error":"..."}
type OllamaPullEvent struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// OllamaPullProgress 聚合后的拉取进度。
type OllamaPullProgress struct {
	Status    string  `json:"status"`
	Completed int64   `json:"completed"`
	Total     int64   `json:"total"`
	Percent   float64 `json:"percent"`
}

// ollamaPullAgg 把逐层事件聚合成全局进度。
// Ollama 并行下多个层（每层一个 digest），只看最新一层的 completed/total 会在各层之间来回跳，
// 故按 digest 记下每层的最新进度再求和。
type ollamaPullAgg struct {
	layers map[string][2]int64 // digest -> {completed, total}
}

func (a *ollamaPullAgg) add(ev OllamaPullEvent) OllamaPullProgress {
	if a.layers == nil {
		a.layers = make(map[string][2]int64)
	}
	if ev.Digest != "" && ev.Total > 0 {
		a.layers[ev.Digest] = [2]int64{ev.Completed, ev.Total}
	}
	var done, total int64
	for _, v := range a.layers {
		done += v[0]
		total += v[1]
	}
	p := OllamaPullProgress{Status: ev.Status, Completed: done, Total: total}
	if total > 0 {
		p.Percent = float64(done) / float64(total) * 100
	}
	return p
}

// apiPort 当前应访问的端口。
// 优先用 QuickDock 正在托管的那一版配置的端口（用户可能在 ollama.env 里改过 OLLAMA_HOST）；
// 没有托管实例时回退 11434——覆盖「用户自己起的官方安装版」这一最常见情况。
func (o *OllamaRuntime) apiPort() int {
	if v, _ := svcMgr.info(RuntimeOllama); v != "" {
		return o.port(v)
	}
	return ollamaDefaultPort
}

func (o *OllamaRuntime) apiBase() string {
	return fmt.Sprintf("http://127.0.0.1:%d", o.apiPort())
}

// doAPI 统一请求入口：先探端口给出「未运行」这种可操作的提示，再发请求。
func (o *OllamaRuntime) doAPI(ctx context.Context, client *http.Client, method, path string, body any) (*http.Response, error) {
	if !isPortOpen(o.apiPort()) {
		return nil, fmt.Errorf("Ollama 未运行：%s 无响应，请先在版本表点「启动」", o.apiBase())
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.apiBase()+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Ollama %s%s 失败：%w", o.apiBase(), path, err)
	}
	return resp, nil
}

// apiJSON 发请求并反序列化响应；非 2xx 时把响应体当 Ollama 的错误文案返回。
func (o *OllamaRuntime) apiJSON(ctx context.Context, method, path string, body, out any) error {
	resp, err := o.doAPI(ctx, ollamaLocalClient, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Ollama 返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Models 列出本地已下载模型（GET /api/tags）。
func (o *OllamaRuntime) Models(ctx context.Context) ([]OllamaModel, error) {
	var r struct {
		Models []OllamaModel `json:"models"`
	}
	if err := o.apiJSON(ctx, http.MethodGet, "/api/tags", nil, &r); err != nil {
		return nil, err
	}
	if r.Models == nil {
		r.Models = []OllamaModel{}
	}
	return r.Models, nil
}

// RunningModels 列出当前已加载进显存的模型（GET /api/ps）。
func (o *OllamaRuntime) RunningModels(ctx context.Context) ([]OllamaRunningModel, error) {
	var r struct {
		Models []OllamaRunningModel `json:"models"`
	}
	if err := o.apiJSON(ctx, http.MethodGet, "/api/ps", nil, &r); err != nil {
		return nil, err
	}
	if r.Models == nil {
		r.Models = []OllamaRunningModel{}
	}
	return r.Models, nil
}

// DeleteModel 删除本地模型（DELETE /api/delete）。
// 只作用于 Ollama 自己的模型库，不碰程序目录；删完磁盘立即释放。
func (o *OllamaRuntime) DeleteModel(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("模型名不能为空")
	}
	return o.apiJSON(ctx, http.MethodDelete, "/api/delete", map[string]string{"model": name}, nil)
}

// PullModel 拉取模型（POST /api/pull，NDJSON 流式）。
// onEvent 收到的是聚合后的整体进度；ctx 取消即中断（已下完的分层保留，下次续传）。
func (o *OllamaRuntime) PullModel(ctx context.Context, name string, onEvent func(OllamaPullProgress)) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("模型名不能为空")
	}
	resp, err := o.doAPI(ctx, ollamaStreamClient, http.MethodPost, "/api/pull",
		map[string]any{"model": name, "stream": true})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Ollama 返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	agg := &ollamaPullAgg{}
	sc := bufio.NewScanner(resp.Body)
	// 单行含 digest 也就几百字节，1 MiB 上限足够，防的是异常超长行把内存吃爆。
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev OllamaPullEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // 解析不了的行跳过，不因一行异常中断整个下载
		}
		if ev.Error != "" {
			return fmt.Errorf("%s", ev.Error)
		}
		if onEvent != nil {
			onEvent(agg.add(ev))
		}
		if ev.Status == "success" {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("读取 Ollama 进度流失败：%w", err)
	}
	return nil
}

// OllamaLibraryModel 官方模型库条目（GET ollama.com/api/tags）。
// 该接口无需鉴权、支持 ?q= 关键字过滤，是本项目唯一可用的模型检索来源
// （ollama.com/search 是 HTML 页面，无公开 JSON API）。
type OllamaLibraryModel struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// ollamaLibraryURL 官方模型库列表地址。
const ollamaLibraryURL = "https://ollama.com/api/tags"

// SearchLibrary 搜索官方模型库。q 为空时返回热门（官方默认排序），非空则按其过滤。
// 走宿主通用 fetchURL（带代理容错），与版本列表同一套网络策略。
func (o *OllamaRuntime) SearchLibrary(q string) ([]OllamaLibraryModel, error) {
	u := ollamaLibraryURL
	if s := strings.TrimSpace(q); s != "" {
		u += "?q=" + url.QueryEscape(s)
	}
	body, err := fetchURL(u)
	if err != nil {
		return nil, fmt.Errorf("获取 Ollama 模型库失败：%w", err)
	}
	var r struct {
		Models []OllamaLibraryModel `json:"models"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 Ollama 模型库失败：%w", err)
	}
	if r.Models == nil {
		r.Models = []OllamaLibraryModel{}
	}
	return r.Models, nil
}

// OllamaUpdateInfo Ollama 版本更新检查结果。
type OllamaUpdateInfo struct {
	Installed string `json:"installed"` // 当前已装版本，未装为空
	Latest    string `json:"latest"`    // 官方最新可用版本
	HasUpdate bool   `json:"hasUpdate"` // 是否可更新（已装且 Latest 更新）
}

// CheckUpdate 检查是否有新版本可更新。只做检测，不自动替换——由用户确认后调 Install 原地替换。
// 版本列表取 AvailableVersions（上游实时拉取 + 1h 缓存），而非 registry 的静态兜底列表。
func (o *OllamaRuntime) CheckUpdate() OllamaUpdateInfo {
	info := OllamaUpdateInfo{Installed: o.DetectInstalledVersion()}
	vs := AvailableVersions(RuntimeOllama, "", "")
	if len(vs) == 0 {
		vs = Versions(RuntimeOllama)
	}
	if len(vs) == 0 {
		return info
	}
	info.Latest = vs[0]
	info.HasUpdate = info.Installed != "" && semverLess(info.Installed, info.Latest)
	return info
}

// ---- Manager 转发（运行时专属能力的既有范式，同 GitStatus / EnableRabbitMQManagement）----

// ollama 取 Ollama 适配器实例；未注册时返回错误。
func (m *Manager) ollama() (*OllamaRuntime, error) {
	a, err := m.adapter(RuntimeOllama)
	if err != nil {
		return nil, err
	}
	o, ok := a.(*OllamaRuntime)
	if !ok {
		return nil, fmt.Errorf("Ollama 适配器类型异常")
	}
	return o, nil
}

// OllamaModels 列出本地已下载模型。
func (m *Manager) OllamaModels(ctx context.Context) ([]OllamaModel, error) {
	o, err := m.ollama()
	if err != nil {
		return nil, err
	}
	return o.Models(ctx)
}

// OllamaRunningModels 列出当前已加载进显存的模型。
func (m *Manager) OllamaRunningModels(ctx context.Context) ([]OllamaRunningModel, error) {
	o, err := m.ollama()
	if err != nil {
		return nil, err
	}
	return o.RunningModels(ctx)
}

// OllamaDeleteModel 删除本地模型。
func (m *Manager) OllamaDeleteModel(ctx context.Context, name string) error {
	o, err := m.ollama()
	if err != nil {
		return err
	}
	return o.DeleteModel(ctx, name)
}

// OllamaPullModel 拉取模型，进度经 onProgress 回调。
func (m *Manager) OllamaPullModel(ctx context.Context, name string, onProgress func(OllamaPullProgress)) error {
	o, err := m.ollama()
	if err != nil {
		return err
	}
	return o.PullModel(ctx, name, onProgress)
}

// OllamaSearchLibrary 搜索 Ollama 官方模型库（供拉取输入框做联想）。
func (m *Manager) OllamaSearchLibrary(q string) ([]OllamaLibraryModel, error) {
	o, err := m.ollama()
	if err != nil {
		return nil, err
	}
	return o.SearchLibrary(q)
}

// OllamaCheckUpdate 检查 Ollama 是否有新版本可更新。
func (m *Manager) OllamaCheckUpdate() (OllamaUpdateInfo, error) {
	o, err := m.ollama()
	if err != nil {
		return OllamaUpdateInfo{}, err
	}
	return o.CheckUpdate(), nil
}
