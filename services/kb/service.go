// Package kb 知识库门面服务。
//
// 面向桌面的本地文档知识库：把 Word / Excel / PDF / Markdown 解析成切片，
// 建关键词索引与（可选的）向量索引，供检索与问答使用。
//
// 拆成两层：
//   - internal/kb  纯引擎：解析、切片、向量序列化、embedding 客户端
//   - internal/db  持久层：kb_sources / kb_chunks / kb_chunks_fts 与两路召回
//
// 本包只做编排：决定何时重建、按什么顺序写库、两路结果怎么融合、
// 以及把状态推给前端。所有会写库的分支都要注意一件事——Database.Transaction
// 全程持全库写锁，绝不能把网络调用包在事务里。
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/db"
	ikb "quickdock/internal/kb"
	"quickdock/internal/logger"
	"quickdock/services"
)

const (
	// kbSettingsKey 设置存 app_state 的哪个键。
	kbSettingsKey = "kb_settings"
	// searchTopK 每路召回条数。30 是「够 RRF 有区分度」与「不浪费算力」的折中：
	// 取 10 条时两路交集太小，融合没有意义；取 100 条时后段全是噪声。
	searchTopK = 30
	// perSourceMaxHits 同一个文件最多贡献几块。
	// 不限制的话，一份超长文档会霸占全部结果位，用户看不到别的来源的佐证。
	perSourceMaxHits = 3
	// maxFilesPerAdd 单次导入的文件数上限，防止误拖一个 node_modules。
	maxFilesPerAdd = 500
)

// Settings 知识库设置。
type Settings struct {
	// EmbedModel embedding 模型名。空 = 不做向量检索（纯关键词）。
	// 单独配置而不是复用聊天模型：聊天模型做不了 embedding，
	// 而让用户去改聊天档案来换 embedding 模型会把两件事绑死。
	EmbedModel string `json:"embedModel"`
	// EmbedBaseURL / EmbedAPIKey 独立的 embedding 端点，留空则复用
	// 激活 AI 档案的地址与 Key。很多供应商（DeepSeek、部分中转站）
	// 只提供聊天没有 /embeddings，绑死在激活档案上会让「想配向量
	// 检索的用户被迫连聊天一起换供应商」（实测：两个档案都没有
	// embedding 模型，bge-m3 无处安放）。
	EmbedBaseURL string `json:"embedBaseURL"`
	EmbedAPIKey  string `json:"embedAPIKey"`
	// TopN 检索返回条数。
	TopN int `json:"topN"`
	// VectorOn 向量检索总开关。关掉后连"补向量"也不做，纯本地跑。
	VectorOn bool `json:"vectorOn"`
}

func defaultSettings() Settings {
	return Settings{EmbedModel: "", TopN: 5, VectorOn: true}
}

// KBService 知识库服务。
type KBService struct {
	App *services.AppService

	queue chan string
	quit  chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once

	// 索引运行时状态（前端进度条读它）。
	runMu   sync.Mutex
	running bool
	stage   string
	current string
	curName string
	done    int
	total   int
	// cancelFlag 1 = 用户点了「停止」。用 atomic 而不是锁：
	// 流水线每个批次都判一次，锁竞争不划算。
	cancelFlag int32

	// 向量矩阵缓存。1 万块 × 1024 维 = 40MB，每次检索全量读盘不可接受。
	// 失效点在「整份索引完成」和「删除源」两处，不在每批回填之后——
	// 每批都失效会让索引期间每次检索都重新读一遍几十 MB。
	cacheMu  sync.RWMutex
	vecIndex *ikb.VectorIndex
	// vecIndexModel 缓存矩阵对应的 embedding 模型名。换模型后旧矩阵
	// 必须作废——同维度的两个模型混算相似度是噪声（11-12 修）。
	vecIndexModel string
}

// NewKBService 创建知识库服务。
func NewKBService(app *services.AppService) *KBService {
	return &KBService{
		App:   app,
		queue: make(chan string, 1024),
		quit:  make(chan struct{}),
	}
}

func (s *KBService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// emit 推事件给前端。App 未就绪时静默跳过（启动早期的索引不该崩）。
func (s *KBService) emit(name string, data interface{}) {
	if app := s.App.App(); app != nil {
		app.Event.Emit(name, data)
	}
}

// ---- 设置 ----

// kbKeyMask 掩码占位，与 AI 档案列表的口径一致：前端回传掩码 = 不改 Key。
const kbKeyMask = "******"

// GetKBSettings 读取设置（未设置时返回默认值）。API Key 掩码返回，
// 明文只留在后端——前端是 WebView，任何能注入脚本的代码都能读响应体。
func (s *KBService) GetKBSettings() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	cfg := s.settings()
	if cfg.EmbedAPIKey != "" {
		cfg.EmbedAPIKey = kbKeyMask
	}
	return services.Ok(cfg)
}

func (s *KBService) settings() Settings {
	cfg := defaultSettings()
	raw, err := s.App.DB.GetValue(kbSettingsKey)
	if err != nil {
		// 读失败不能静默吞掉：白名单漏配时曾让整轮设置形同不存在——
		// 用户填了模型名、检索却一直走纯关键词（11-11 事故）。
		logger.E("[kb] 读取设置失败，回退默认值: %v", err)
		return cfg
	}
	if strings.TrimSpace(raw) == "" {
		return cfg
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return defaultSettings()
	}
	if cfg.TopN <= 0 || cfg.TopN > 20 {
		cfg.TopN = 5
	}
	return cfg
}

// SaveKBSettings 保存设置。换 embedding 模型，或同名模型换了端点/Key，
// 会把已有向量的源标成 stale：旧向量与新配置不在同一空间，混用算出来的
// 相似度是噪声。
func (s *KBService) SaveKBSettings(cfg Settings) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if cfg.TopN <= 0 || cfg.TopN > 20 {
		cfg.TopN = 5
	}
	cfg.EmbedModel = strings.TrimSpace(cfg.EmbedModel)
	cfg.EmbedBaseURL = strings.TrimSpace(cfg.EmbedBaseURL)
	cfg.EmbedBaseURL = strings.TrimSuffix(cfg.EmbedBaseURL, "/")

	old := s.settings()
	// 前端回传的是掩码（GetKBSettings 返回的形态），不是新 Key：保留原值。
	// 传空串 = 用户主动清掉独立 Key（回退到激活档案）。
	if cfg.EmbedAPIKey == kbKeyMask {
		cfg.EmbedAPIKey = old.EmbedAPIKey
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return services.Fail(err)
	}
	if err := s.App.DB.SetValue(kbSettingsKey, string(raw)); err != nil {
		return services.Fail(err)
	}

	// 标 stale 的两个触发条件：
	// ① 模型名变了：旧向量与新模型不在同一空间（MarkKBSourcesStale 按
	//    embed_model 过滤，精确命中）。
	// ② 模型名没变但端点/Key 变了：同名模型跨供应商的向量空间同样无法
	//    保证一致，而切片上只记了模型名，DB 层分辨不出来——传空模型名
	//    让「有任何向量的源」全量标 stale，宁可重算也不混用。
	modelChanged := old.EmbedModel != cfg.EmbedModel && cfg.EmbedModel != ""
	endpointChanged := old.EmbedBaseURL != cfg.EmbedBaseURL || old.EmbedAPIKey != cfg.EmbedAPIKey
	if modelChanged {
		if _, err := s.App.DB.MarkKBSourcesStale(cfg.EmbedModel); err != nil {
			return services.Fail(err)
		}
	} else if endpointChanged && cfg.EmbedModel != "" {
		if _, err := s.App.DB.MarkKBSourcesStale(""); err != nil {
			return services.Fail(err)
		}
	}
	s.invalidateVecCache()
	return services.Ok(nil)
}

// ---- 分组 ----

// ListKBLibraries 列出知识库分组（带文档数与切片数）。
func (s *KBService) ListKBLibraries() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListKBLibraries()
	return services.Wrap(list, err)
}

// CreateKBLibrary 新建分组。返回新分组本身，前端拿到 id 后可直接切过去。
func (s *KBService) CreateKBLibrary(name, icon string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	lib, err := s.App.DB.CreateKBLibrary(name, icon)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(lib)
}

// RenameKBLibrary 改名 / 换图标。
//
// 允许把 name 传空：默认库的清空就是「恢复成本地化的默认名」，
// 前端据此显示「默认知识库」或「Default library」。
func (s *KBService) RenameKBLibrary(id, name, icon string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.FailMsg("缺少知识库 id")
	}
	if err := s.App.DB.RenameKBLibrary(id, name, icon); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DeleteKBLibrary 删除整个分组（含其下全部索引）。磁盘上的原文件不动。
func (s *KBService) DeleteKBLibrary(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.FailMsg("缺少知识库 id")
	}
	if err := s.App.DB.DeleteKBLibrary(id); err != nil {
		return services.Fail(err)
	}
	s.invalidateVecCache()
	return services.Ok(nil)
}

// MoveKBSource 把文档挪到另一个分组。
//
// 必须让向量缓存失效：缓存里每行都带着库归属标签，挪完不重建索引，
// 「只搜这个库」会继续把这份文档算进去（反之亦然），而且现象是「偶尔多几条」，
// 极难归因。
func (s *KBService) MoveKBSource(sourceID, libraryID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.MoveKBSource(sourceID, libraryID); err != nil {
		return services.Fail(err)
	}
	s.invalidateVecCache()
	return services.Ok(nil)
}

// resolveLibraryID 把「空 id」翻译成一个可写入的具体分组。
//
// 前端停在「全部」视图时点添加文件，落点应该是排序第一的库，
// 而不是拒绝或建一个悬空的 library_id（那种行在列表里谁都看不见）。
func (s *KBService) resolveLibraryID(libraryID string) string {
	if strings.TrimSpace(libraryID) != "" {
		if lib, err := s.App.DB.GetKBLibrary(libraryID); err == nil && lib != nil {
			return lib.ID
		}
	}
	id, err := s.App.DB.FirstKBLibraryID()
	if err != nil {
		return ""
	}
	return id
}

// ---- 源管理 ----

// ListKBSources 列出源文件。libraryID 为空 = 全部（跨库）。
func (s *KBService) ListKBSources(libraryID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListKBSources(libraryID)
	return services.Wrap(list, err)
}

// AddKBResult 导入结果。
type AddKBResult struct {
	Added       int      `json:"added"`
	Skipped     int      `json:"skipped"`
	Unsupported []string `json:"unsupported"`
	Truncated   bool     `json:"truncated"`
}

// AddKBFiles 登记并排队索引一批文件。目录会被展开。
// libraryID 为空时落到排序第一的分组（「全部」视图里点添加的默认行为）。
func (s *KBService) AddKBFiles(libraryID string, paths []string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if len(paths) == 0 {
		return services.FailMsg("没有选择文件")
	}
	lib := s.resolveLibraryID(libraryID)
	if lib == "" {
		return services.FailMsg("知识库分组不可用")
	}

	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			files = append(files, collectFiles(p, true)...)
			if len(files) >= maxFilesPerAdd {
				files = files[:maxFilesPerAdd]
				break
			}
			continue
		}
		files = append(files, p)
		if len(files) >= maxFilesPerAdd {
			break
		}
	}

	res := AddKBResult{Unsupported: []string{}}
	if len(files) >= maxFilesPerAdd {
		res.Truncated = true
	}

	for _, p := range files {
		ext := strings.ToLower(filepath.Ext(p))
		if !ikb.IsSupported(ext) {
			res.Unsupported = append(res.Unsupported, filepath.Base(p))
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			res.Skipped++
			continue
		}
		existing, err := s.App.DB.GetKBSourceByPath(p)
		if err != nil {
			return services.Fail(err)
		}
		src := db.KBSource{
			Name:      filepath.Base(p),
			LibraryID: lib,
			Path:      p,
			Ext:       ext,
			Size:      st.Size(),
			Mtime:     st.ModTime().Unix(),
		}
		if existing != nil {
			src.ID = existing.ID
			// 保留已存的 hash：Upsert 会用本次算的 hash（可能为空）覆盖，
			// 把「按 hash 判变化」的依据抹掉，重复添加就会白白重建整份索引。
			// 指纹没变且已经就绪 → 什么都不用做，计入 skipped。
			if existing.Hash != "" {
				src.Hash = existing.Hash
			}
			if existing.Status == db.KBSourceReady &&
				existing.Size == src.Size && existing.Mtime == src.Mtime {
				res.Skipped++
				continue
			}
		}
		if err := s.App.DB.UpsertKBSource(src); err != nil {
			return services.Fail(err)
		}
		if existing != nil {
			src.ID = existing.ID
			s.enqueue(existing.ID)
		} else {
			// Upsert 里可能因为 path 冲突复用了旧 id，重新取一次。
			got, _ := s.App.DB.GetKBSourceByPath(p)
			if got != nil {
				s.enqueue(got.ID)
			}
		}
		res.Added++
	}

	s.resume()
	return services.Ok(res)
}

// AddKBFolder 导入一个目录（递归）。
func (s *KBService) AddKBFolder(libraryID, dir string, recursive bool) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return services.FailMsg("目录不存在")
	}
	files := collectFiles(dir, recursive)
	if len(files) == 0 {
		return services.FailMsg("目录里没有可导入的文档")
	}
	return s.AddKBFiles(libraryID, files)
}

// RemoveKBSource 移除源（连同切片与索引），不动磁盘上的原文件。
func (s *KBService) RemoveKBSource(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteKBSource(id); err != nil {
		return services.Fail(err)
	}
	s.invalidateVecCache()
	return services.Ok(nil)
}

// collectFiles 展开目录，只收受支持的扩展名。
func collectFiles(dir string, recursive bool) []string {
	var out []string
	if recursive {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				// 跳过版本库与依赖目录：这些地方动辄几万个文件，
				// 而且里面的文档基本不是用户想检索的东西。
				if name == ".git" || name == "node_modules" || name == ".svn" {
					return filepath.SkipDir
				}
				return nil
			}
			if ikb.IsSupported(filepath.Ext(p)) {
				out = append(out, p)
			}
			if len(out) >= maxFilesPerAdd {
				return filepath.SkipAll
			}
			return nil
		})
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if ikb.IsSupported(filepath.Ext(p)) {
			out = append(out, p)
		}
	}
	return out
}

// ---- 索引控制 ----

// ScanKB 全量比对指纹，把变化的源重新排队。跨全部知识库。
func (s *KBService) ScanKB() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListKBSources("")
	if err != nil {
		return services.Fail(err)
	}
	queued, missing := 0, 0
	// ready 但向量没建全的源（上次重建时 embedding 批次失败）：指纹比对会
	// 直接跳过它们，部分块永远没有向量。这里按当前模型统计一次，一并补队。
	var pendingVecs map[string]int
	if cfg := s.settings(); cfg.VectorOn && cfg.EmbedModel != "" {
		if m, err := s.App.DB.KBSourcesPendingVecs(cfg.EmbedModel); err == nil {
			pendingVecs = m
		}
	}
	for _, src := range list {
		st, statErr := os.Stat(src.Path)
		if statErr != nil {
			// 文件不在了：标记但保留索引。用户把文件放回来时
			// 指纹一比就知道要不要重建，不必重新导入一次。
			if src.Status != db.KBSourceMissing {
				_ = s.App.DB.UpdateKBSourceStatus(src.ID, db.KBSourceMissing, "文件不存在")
				missing++
			}
			continue
		}
		if src.Status == db.KBSourceReady &&
			src.Size == st.Size() && src.Mtime == st.ModTime().Unix() {
			// 指纹没变，但向量可能没建全（上次 embedding 失败）→ 只补向量。
			if pendingVecs == nil || pendingVecs[src.ID] == 0 {
				continue
			}
			s.enqueue(src.ID)
			queued++
			continue
		}
		if src.Status == db.KBSourceMissing {
			_ = s.App.DB.UpdateKBSourceStatus(src.ID, db.KBSourcePending, "")
		}
		s.enqueue(src.ID)
		queued++
	}
	s.resume()
	return services.Ok(map[string]interface{}{"queued": queued, "missing": missing})
}

// ReindexKBSource 强制重建一个源（重新解析、重新切、重新算向量）。
func (s *KBService) ReindexKBSource(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.FailMsg("缺少源 id")
	}
	if err := s.App.DB.UpdateKBSourceStatus(id, db.KBSourcePending, ""); err != nil {
		return services.Fail(err)
	}
	s.enqueue(id)
	s.resume()
	return services.Ok(nil)
}

// ReindexAll 全部重建。只重排未就绪或指纹变化的，已就绪且没变的跳过。
func (s *KBService) ReindexAll() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListKBSources("")
	if err != nil {
		return services.Fail(err)
	}
	for _, src := range list {
		if err := s.App.DB.UpdateKBSourceStatus(src.ID, db.KBSourcePending, ""); err != nil {
			return services.Fail(err)
		}
		s.enqueue(src.ID)
	}
	s.resume()
	return services.Ok(map[string]interface{}{"queued": len(list)})
}

// CancelKBIndex 停止当前索引（队列里剩下的保持 pending，下次扫描继续）。
func (s *KBService) CancelKBIndex() *services.ApiResult {
	atomic.StoreInt32(&s.cancelFlag, 1)
	// 清空队列：否则"停止"之后 worker 还会继续把剩下的一个个处理掉。
	for {
		select {
		case <-s.queue:
		default:
			return services.Ok(nil)
		}
	}
}

// KBStatus 索引运行时状态。
type KBStatus struct {
	Running     bool       `json:"running"`
	Stage       string     `json:"stage"`
	CurrentID   string     `json:"currentId"`
	CurrentName string     `json:"currentName"`
	Done        int        `json:"done"`
	Total       int        `json:"total"`
	Queue       int        `json:"queue"`
	Stats       db.KBStats `json:"stats"`
	VectorOn    bool       `json:"vectorOn"`
	EmbedModel  string     `json:"embedModel"`
	// VecMissing 还没用当前模型建向量的切片数（含断点续做失败的部分）。
	// 非零意味着语义召回不完整，前端可提示用户扫一遍补齐。
	VecMissing int `json:"vecMissing"`
	// ProfileReady 有无可用的 AI 档案（接口地址 + Key）。
	// 与 EmbedReady 分开报：两者都缺时都显示「尚未配置 AI 档案」，
	// 但「档案有、模型名没填」是另一种状态，提示得指对地方，否则用户
	// 会跑去 AI 设置里反复确认自己明明配了模型。
	ProfileReady bool `json:"profileReady"`
	// EmbedReady 供应商配置是否齐全（不齐时前端提示「语义检索不可用」）。
	EmbedReady bool `json:"embedReady"`
}

// GetKBStatus 状态汇总。
func (s *KBService) GetKBStatus() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	stats, err := s.App.DB.GetKBStats()
	if err != nil {
		return services.Fail(err)
	}
	cfg := s.settings()
	s.runMu.Lock()
	st := KBStatus{
		Running:     s.running,
		Stage:       s.stage,
		CurrentID:   s.current,
		CurrentName: s.curName,
		Done:        s.done,
		Total:       s.total,
		Queue:       len(s.queue),
	}
	s.runMu.Unlock()
	st.Stats = stats
	st.VectorOn = cfg.VectorOn
	st.EmbedModel = cfg.EmbedModel
	if cfg.VectorOn && cfg.EmbedModel != "" {
		if m, err := s.App.DB.KBSourcesPendingVecs(cfg.EmbedModel); err == nil {
			for _, n := range m {
				st.VecMissing += n
			}
		}
	}
	_, _, ok := s.embedConfig()
	st.ProfileReady = ok
	st.EmbedReady = ok && cfg.EmbedModel != ""
	return services.Ok(st)
}

// contextWithTimeout 统一的 embedding 请求超时（d<=0 时取 60 秒）。
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = 60 * time.Second
	}
	return context.WithTimeout(context.Background(), d)
}

var errNoEmbedConfig = errors.New("未配置 embedding 模型或 AI 档案")

// embedConfig 取 embedding 所需的三个值。任一缺失就代表不可用。
//
// 端点优先级：知识库设置里的独立地址 > 激活 AI 档案。独立 Key 允许为空
// ——本地推理服务（ollama 之类）不需要 Key。
func (s *KBService) embedConfig() (baseURL, apiKey string, ok bool) {
	cfg := s.settings()
	if cfg.EmbedBaseURL != "" {
		return cfg.EmbedBaseURL, cfg.EmbedAPIKey, true
	}
	if s.App.EmbedConfigFn == nil {
		return "", "", false
	}
	baseURL, apiKey, ok = s.App.EmbedConfigFn()
	if !ok || baseURL == "" {
		return "", "", false
	}
	return baseURL, apiKey, true
}

// ---- 检索 ----

// SearchHit 一条检索结果。
type SearchHit struct {
	ChunkID    string  `json:"chunkId"`
	SourceID   string  `json:"sourceId"`
	SourceName string  `json:"sourceName"`
	LibraryID  string  `json:"libraryId"`
	Ext        string  `json:"ext"`
	Heading    string  `json:"heading"`
	Text       string  `json:"text"`
	Page       int     `json:"page"`
	Score      float64 `json:"score"`
	FromFTS    bool    `json:"fromFts"`
	FromVec    bool    `json:"fromVec"`
}

// SearchResult 检索返回。
type SearchResult struct {
	Query     string      `json:"query"`
	Hits      []SearchHit `json:"hits"`
	Mode      string      `json:"mode"` // hybrid | fts
	ElapsedMs int64       `json:"elapsedMs"`
	// VecFallback 为真表示本来要走向量但失败了（多用于「模型名填错」）。
	VecFallback bool `json:"vecFallback"`
}

// SearchKB 混合检索。libraryID 为空 = 跨全部知识库（前端入口）。
func (s *KBService) SearchKB(query string, topN int, libraryID string) *services.ApiResult {
	return s.searchKB(query, topN, libraryID, "")
}

// searchKB 混合检索实现。sourceID 非空时限定在某一份文档内
// （问答的「只问这份文档」场景）。
//
// 两路各取 TopK，用 RRF 按名次融合——不做分数加权：BM25 是负分、
// 余弦是 [-1,1]，量纲不同，归一化再调权重是给自己找麻烦，
// 而且某一路没召回时加权会直接把结果带偏。
func (s *KBService) searchKB(query string, topN int, libraryID, sourceID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	q := strings.TrimSpace(query)
	if len([]rune(q)) < 3 {
		return services.Ok(SearchResult{Query: q, Hits: []SearchHit{}, Mode: "fts"})
	}
	cfg := s.settings()
	if topN <= 0 || topN > 20 {
		topN = cfg.TopN
	}
	libraryID = strings.TrimSpace(libraryID)
	start := time.Now()

	ftsHits, err := s.App.DB.SearchChunksFTS(q, searchTopK, libraryID, sourceID)
	if err != nil {
		return services.Fail(err)
	}

	byRow := make(map[int64]db.KBHit, len(ftsHits))
	ftsIDs := make([]int64, 0, len(ftsHits))
	for _, h := range ftsHits {
		byRow[h.RowID] = h
		ftsIDs = append(ftsIDs, h.RowID)
	}

	mode := "fts"
	vecFallback := false
	var vecIDs []int64
	if s.vectorUsable() {
		ids, ok := s.vectorSearch(q, searchTopK, libraryID, sourceID)
		if ok {
			missing := make([]int64, 0, len(ids))
			for _, id := range ids {
				if _, exists := byRow[id]; !exists {
					missing = append(missing, id)
				}
			}
			// 回表失败就把向量路整条丢弃并如实降级：带着 hybrid 的 mode
			// 却丢掉一半召回，调用方拿到的是一份假象（11-12 修）。
			if len(missing) > 0 {
				extra, dbErr := s.App.DB.ChunksByRowIDs(missing)
				if dbErr != nil {
					logger.W("[kb] 向量命中回表失败，降级纯关键词: %v", dbErr)
					vecFallback = true
					ids = nil
				} else {
					for _, h := range extra {
						byRow[h.RowID] = h
					}
				}
			}
			if ids != nil {
				vecIDs = ids
				mode = "hybrid"
			}
		} else {
			vecFallback = true
		}
	}

	scores := ikb.RRF(ftsIDs, vecIDs)
	ftsSet := make(map[int64]bool, len(ftsIDs))
	for _, id := range ftsIDs {
		ftsSet[id] = true
	}
	vecSet := make(map[int64]bool, len(vecIDs))
	for _, id := range vecIDs {
		vecSet[id] = true
	}

	type ranked struct {
		rowID int64
		score float64
	}
	rankedList := make([]ranked, 0, len(scores))
	for id, sc := range scores {
		if _, ok := byRow[id]; !ok {
			continue // 索引残留：chunk 已删但 FTS 行还在（理论上不该出现）
		}
		rankedList = append(rankedList, ranked{rowID: id, score: sc})
	}
	sort.Slice(rankedList, func(i, j int) bool { return rankedList[i].score > rankedList[j].score })

	hits := make([]SearchHit, 0, topN)
	perSource := make(map[string]int)
	for _, r := range rankedList {
		h := byRow[r.rowID]
		if perSource[h.SourceID] >= perSourceMaxHits {
			continue
		}
		perSource[h.SourceID]++
		hits = append(hits, SearchHit{
			ChunkID:    h.ChunkID,
			SourceID:   h.SourceID,
			SourceName: h.SourceName,
			LibraryID:  h.LibraryID,
			Ext:        h.Ext,
			Heading:    h.Heading,
			Text:       h.Text,
			Page:       h.Page,
			Score:      r.score,
			FromFTS:    ftsSet[r.rowID],
			FromVec:    vecSet[r.rowID],
		})
		if len(hits) >= topN {
			break
		}
	}

	return services.Ok(SearchResult{
		Query:       q,
		Hits:        hits,
		Mode:        mode,
		ElapsedMs:   time.Since(start).Milliseconds(),
		VecFallback: vecFallback,
	})
}

// vectorUsable 向量路是否可用：开关打开 + 配了模型 + 库里真有向量。
func (s *KBService) vectorUsable() bool {
	cfg := s.settings()
	if !cfg.VectorOn || cfg.EmbedModel == "" {
		return false
	}
	if _, _, ok := s.embedConfig(); !ok {
		return false
	}
	return s.getVectorIndex().Len() > 0
}

// vectorSearch 查向量路，返回按相似度降序的 rowid。
// ok=false 表示这次查不了（embedding 失败），调用方降级为纯 FTS。
//
// libraryID / sourceID 直接传给索引做扫描期过滤，不在这里事后剔除：
// 事后剔除会让 TopK 名额被别的范围占满，小范围检索直接返回空。
func (s *KBService) vectorSearch(query string, k int, libraryID, sourceID string) ([]int64, bool) {
	baseURL, apiKey, ok := s.embedConfig()
	if !ok {
		return nil, false
	}
	cfg := s.settings()
	emb := ikb.NewEmbedder(ikb.EmbedConfig{BaseURL: baseURL, APIKey: apiKey, Model: cfg.EmbedModel})

	ctx, cancel := contextWithTimeout(12 * time.Second)
	defer cancel()
	vecs, err := emb.EmbedBatch(ctx, []string{query})
	if err != nil {
		// 此前 err 被直接丢弃，用户只看到「语义不亮」，日志里一个字都没有。
		logger.W("[kb] 查询向量计算失败（model=%s）: %v", cfg.EmbedModel, err)
		return nil, false
	}
	if len(vecs) == 0 {
		return nil, false
	}
	qv := vecs[0]
	ikb.Normalize(qv)

	idx := s.getVectorIndex()
	rows := idx.TopK(qv, k, ikb.VectorFilter{LibraryID: libraryID, SourceID: sourceID})
	if len(rows) == 0 {
		return nil, false
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RowID)
	}
	return out, true
}

// getVectorIndex 取（或懒加载）向量矩阵缓存。
// 缓存按模型名绑定：换模型后必须重读，否则旧模型的向量会被拿去和新模型的
// 查询向量算相似度（AllVectors 已按模型过滤，这里的双保险是避免用错缓存）。
func (s *KBService) getVectorIndex() *ikb.VectorIndex {
	model := s.settings().EmbedModel
	s.cacheMu.RLock()
	if s.vecIndex != nil && s.vecIndexModel == model {
		idx := s.vecIndex
		s.cacheMu.RUnlock()
		return idx
	}
	s.cacheMu.RUnlock()

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.vecIndex != nil && s.vecIndexModel == model {
		return s.vecIndex
	}
	rows, err := s.App.DB.AllVectors(model)
	if err != nil {
		// 读失败不缓存空结果：下次检索再试，否则一次瞬时错误会让
		// 向量检索在本次进程生命周期内永久失效。但要留日志——
		// 此前静默返回空索引，语义检索暗下去没人知道为什么。
		logger.W("[kb] 读取向量矩阵失败: %v", err)
		return &ikb.VectorIndex{}
	}
	entries := make([]ikb.VectorEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, ikb.VectorEntry{RowID: r.RowID, Vec: r.Vec, Dim: r.Dim, Tag: r.LibraryID, SourceID: r.SourceID})
	}
	s.vecIndex = ikb.BuildIndex(entries)
	s.vecIndexModel = model
	return s.vecIndex
}

func (s *KBService) invalidateVecCache() {
	s.cacheMu.Lock()
	s.vecIndex = nil
	s.vecIndexModel = ""
	s.cacheMu.Unlock()
}

// ListKBChunks 列出某个源的全部切片（预览用）。
func (s *KBService) ListKBChunks(sourceID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListChunksBySource(sourceID)
	return services.Wrap(list, err)
}

// ---- 问答 ----

// AskKBRequest 知识库问答请求。
type AskKBRequest struct {
	Question string `json:"question"`
	// SourceID 限定在某一份文档内提问（空 = 全库）。
	SourceID string `json:"sourceId"`
	// LibraryID 限定在某个知识库内提问（空 = 跨全部库）。
	LibraryID string `json:"libraryId"`
	// StreamID 非空时经 Wails 事件推增量。
	StreamID string `json:"streamId"`
	TopN     int    `json:"topN"`
}

// AskKBResult 问答返回。
type AskKBResult struct {
	Answer    string      `json:"answer"`
	Citations []SearchHit `json:"citations"`
	Model     string      `json:"model"`
	ElapsedMs int64       `json:"elapsedMs"`
}

// AskKB 检索 + 生成。
//
// 走「一次检索、一次生成」而不是让模型自己调工具：知识库问答的使用场景是
// 「我文档里怎么写的」，一轮召回通常就够，多轮工具调用会让首字延迟翻倍。
// 需要多轮检索的场景走 AI 工具里的 kb_search（那里模型自己决定查几次）。
func (s *KBService) AskKB(req AskKBRequest) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	q := strings.TrimSpace(req.Question)
	if q == "" {
		return services.FailMsg("问题不能为空")
	}
	if s.App.AskAIFnStreamReason == nil {
		return services.FailMsg("AI 能力不可用，请先在设置里配置模型")
	}

	search, err := unwrapSearch(s.searchKB(q, req.TopN, req.LibraryID, req.SourceID))
	if err != nil {
		return services.Fail(err)
	}
	if len(search.Hits) == 0 {
		return services.Ok(AskKBResult{
			Answer:    "",
			Citations: []SearchHit{},
			Model:     "",
		})
	}

	prompt, cited := buildKBPrompt(q, search.Hits)

	var buf strings.Builder
	var deltaBuf strings.Builder
	onToken := func(delta string) {
		if req.StreamID == "" {
			buf.WriteString(delta)
			return
		}
		buf.WriteString(delta)
		deltaBuf.WriteString(delta)
		// 合批 ~40 字节：逐 token 推事件会把 Wails 事件总线打满。
		if deltaBuf.Len() >= 40 {
			s.emit("ai:ask-delta", map[string]interface{}{"id": req.StreamID, "delta": deltaBuf.String()})
			deltaBuf.Reset()
		}
	}

	start := time.Now()
	out, model, err := s.App.AskAIFnStreamReason(kbAskSystemPrompt, prompt, 120, onToken, nil)
	if req.StreamID != "" && deltaBuf.Len() > 0 {
		s.emit("ai:ask-delta", map[string]interface{}{"id": req.StreamID, "delta": deltaBuf.String()})
	}
	if err != nil {
		return services.Fail(err)
	}
	if out == "" {
		out = buf.String()
	}
	return services.Ok(AskKBResult{
		Answer:    out,
		Citations: cited,
		Model:     model,
		ElapsedMs: time.Since(start).Milliseconds(),
	})
}

// unwrapSearch 从 ApiResult 里取出 SearchResult。
func unwrapSearch(r *services.ApiResult) (SearchResult, error) {
	if r == nil {
		return SearchResult{}, fmt.Errorf("检索无响应")
	}
	if r.Code != 0 {
		if r.Msg == "" {
			return SearchResult{}, fmt.Errorf("检索失败")
		}
		return SearchResult{}, errors.New(r.Msg)
	}
	if v, ok := r.Data.(SearchResult); ok {
		return v, nil
	}
	// Data 类型不对（如 nil）时此前返回空结果 + nil error，
	// AskKB 会把它当成「0 命中」给用户一句「资料中没有提到」，
	// 真实故障被伪装成正常空结果。
	return SearchResult{}, fmt.Errorf("检索结果类型异常")
}

const kbAskSystemPrompt = `你是文档问答助手，基于用户提供的资料回答问题。

规则：
1. 只使用资料里的信息，不要引入外部知识，也不要猜测。
2. 每个结论后面用 [#序号] 标注它来自哪条资料，例如「默认端口是 5432 [#2]」。
3. 资料里找不到答案时，直接说「资料中没有提到」，不要编造。
4. 回答用中文，简洁分点，不要复述问题。`

// buildKBPrompt 把检索结果编成带编号的资料区。
//
// 编号必须和 Citations 的下标一一对应：前端点 [#2] 时要能定位到
// 第二个结果并跳到原文档的对应页。
func buildKBPrompt(question string, hits []SearchHit) (string, []SearchHit) {
	var b strings.Builder
	b.WriteString("资料：\n\n")
	used := make([]SearchHit, 0, len(hits))
	for i, h := range hits {
		label := h.SourceName
		if h.Page > 0 {
			label = fmt.Sprintf("%s 第 %d 页", label, h.Page)
		} else if h.Heading != "" {
			label = label + " · " + h.Heading
		}
		// 单块送模型时截到 1200 字：5 块 × 1200 = 6000 字，
		// 加上问题与系统提示仍在常见上下文窗口的安全区。
		text := h.Text
		if len([]rune(text)) > 1200 {
			text = string([]rune(text)[:1200]) + "…"
		}
		fmt.Fprintf(&b, "[#%d] %s\n%s\n\n", i+1, label, text)
		used = append(used, h)
	}
	b.WriteString("问题：")
	b.WriteString(question)
	return b.String(), used
}
