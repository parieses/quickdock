// Package script 脚本任务门面（青龙式脚本面板）。
//
// 分层：
//   - internal/db        三张表：scripts / script_envs / script_runs
//   - internal/scriptrun 执行引擎（解释器解析、日志落盘、流式输出）
//   - internal/platform  StreamCommand（跨平台起进程 + 逐行回调）
//   - 本包               编排：文件读写、并发闸门、事件推送、通知、AI 自愈、HTTP 触发
//
// 脚本正文在 dataDir/scripts/<id>/ 磁盘上，是唯一真相源。浏览器式的「数据库存正文 +
// 执行时导出临时文件」在这里是错的：用户会用外部编辑器改脚本，双真相源只会在
// 下一次保存时把外部修改静默覆盖掉。
package script

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/scriptrun"
	"quickdock/services"
)

// 事件名。
const (
	// EventLog 运行输出（100ms 合批）。前端订阅后按 runId 分流。
	EventLog = "quickdock:script:log"
	// EventRun 运行状态变化（开始/收尾）。收尾时带 summary 与 exitCode。
	EventRun = "quickdock:script:run"
	// EventChanged 脚本列表或某个脚本的文件发生变化（外部编辑器改完也靠它刷新）。
	EventChanged = "quickdock:script:changed"
)

// ScriptService 脚本域门面。
type ScriptService struct {
	App *services.AppService

	// gate 并发闸门：每个脚本最多同时跑 Concurrency 个实例（默认 1 = 互斥）。
	// 撞上限时新的触发直接让位，而不是排队——定时任务排队会在上一次超时后
	// 雪崩成一串补跑。
	gate runGate
	// cancels runID -> context.CancelFunc，取消用。执行结束即删。
	cancels sync.Map

	triggerMu sync.Mutex
	trigger   *triggerServer

	// healing 防止同一次运行被重复丢给 AI（用户连点「AI 分析」时）。
	healing sync.Map

	// 事件触发监听状态（见 watch.go）。
	watchMu     sync.Mutex
	watchStates map[string]*watchState
	watchOn     bool
}

// NewService 创建门面服务。
func NewService(app *services.AppService) *ScriptService {
	return &ScriptService{App: app}
}

// Root 脚本根目录。所有脚本各占一个子目录。
func (s *ScriptService) Root() string {
	return filepath.Join(platform.DefaultDataDir(), "scripts")
}

func (s *ScriptService) dir(id string) string {
	return filepath.Join(s.Root(), id)
}

// Startup 应用启动时调用：收尾上次进程遗留的 running 记录、清理过期回收目录、
// 恢复 HTTP 触发服务。
//
// 三件事都必须做：
//   - 不收尾 running，并发闸门会永久卡死（用户点运行永远得到「正在运行中」）；
//   - 不清理 .deleted，删除过的脚本目录会一直躺在磁盘上；
//   - 不恢复触发服务，用户上次开的 webhook 入口静默失效——而外部调用方
//     只会看到连接失败，完全联想不到「应用重启后需要重新点一下」。
func (s *ScriptService) Startup() {
	if s.App.DB == nil {
		return
	}
	if n, err := s.App.DB.FailStaleScriptRuns(); err != nil {
		logger.W("[script] 收尾遗留运行记录失败: %v", err)
	} else if n > 0 {
		logger.I("[script] 收尾 %d 条上次退出时未结束的运行记录", n)
	}
	go s.cleanTrash()
	s.StartWatcher()
	if cfg, err := s.loadTriggerConfig(); err == nil && cfg.Enabled {
		if err := s.startTrigger(cfg); err != nil {
			logger.W("[script] 恢复 HTTP 触发失败: %v", err)
		}
	}
}

// cleanTrash 删除 30 天前的脚本回收目录。
func (s *ScriptService) cleanTrash() {
	entries, err := os.ReadDir(s.Root())
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	for _, e := range entries {
		if !e.IsDir() || !strings.Contains(e.Name(), ".deleted-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.Root(), e.Name())); err != nil {
			logger.W("[script] 清理回收目录失败 %s: %v", e.Name(), err)
		}
	}
}

// emit 推一个前端事件。app 未就绪（单元测试/启动早期）时静默跳过。
func (s *ScriptService) emit(name string, payload any) {
	if app := s.App.App(); app != nil {
		app.Event.Emit(name, payload)
	}
}

// ScriptInput 新建/保存脚本的入参（比 db.Script 少运行聚合列，那些不让前端写）。
type ScriptInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Lang        string `json:"lang"`
	Entry       string `json:"entry"`
	Interpreter string `json:"interpreter"`
	DefaultArgs string `json:"defaultArgs"`
	TimeoutSec  int    `json:"timeoutSec"`
	// Concurrency 同时运行实例上限（1 = 互斥，最大 8）。
	Concurrency int    `json:"concurrency"`
	AutoInstall bool   `json:"autoInstall"`
	Pinned      bool   `json:"pinned"`
	MCPAllowed  bool   `json:"mcpAllowed"`
	Tags        string `json:"tags"`
	Sort        int    `json:"sort"`
	// WatchPath 事件触发监听的路径（空 = 不监听）。
	WatchPath string `json:"watchPath"`
}

// ScriptListItem 列表项：脚本本体 + 列表页需要的一次性附加信息。
//
// 为什么要附加：列表上「这个脚本有没有在定时跑、下次什么时候跑」是每天扫一眼
// 就要看的信息，而它存在 scheduled_tasks 里。让前端逐个脚本再查一次就是 N+1，
// 这里一次查全表组装好随列表返回。
type ScriptListItem struct {
	db.Script
	// Trusted 是否已确认可运行（外部导入的脚本为 false）。
	Trusted bool `json:"trusted"`
	// ScheduleCount 引用本脚本的定时任务数（含已停用的）。
	ScheduleCount int `json:"scheduleCount"`
	// NextRun 最近一次待运行时间（YYYY-MM-DD HH:MM:SS，空 = 没有排期）。
	NextRun string `json:"nextRun"`
}

// ListScripts 列出全部脚本。
func (s *ScriptService) ListScripts() *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	list, err := s.App.DB.ListScripts()
	if err != nil {
		return services.Fail(err)
	}

	type agg struct {
		count int
		next  string
	}
	byScript := make(map[string]*agg, len(list))
	if tasks, err := s.App.DB.ListScheduledTasks(); err == nil {
		for _, t := range tasks {
			if t.Action != "script" || t.Target == "" {
				continue
			}
			a := byScript[t.Target]
			if a == nil {
				a = &agg{}
				byScript[t.Target] = a
			}
			a.count++
			// next_run 是 'YYYY-MM-DD HH:MM:SS'，字典序即时间序，直接比较即可，
			// 不必解析成 time（格式由调度器统一写，没有时区后缀）。
			if t.Enabled && t.NextRun != "" && (a.next == "" || t.NextRun < a.next) {
				a.next = t.NextRun
			}
		}
	}

	out := make([]ScriptListItem, 0, len(list))
	for _, sc := range list {
		item := ScriptListItem{Script: sc, Trusted: s.scriptTrusted(sc.ID)}
		if a := byScript[sc.ID]; a != nil {
			item.ScheduleCount = a.count
			item.NextRun = a.next
		}
		out = append(out, item)
	}
	return services.Ok(out)
}

// GetScript 取单个脚本（附带文件清单与依赖探测结果，前端一次拉齐）。
func (s *ScriptService) GetScript(id string) *services.ApiResult {
	sc, err := s.App.DB.GetScript(id)
	if err != nil {
		return services.Fail(err)
	}
	files, _ := s.listFiles(sc.ID)
	return services.Ok(map[string]any{
		"script":  sc,
		"files":   files,
		"deps":    s.detectDeps(sc),
		"trusted": s.scriptTrusted(sc.ID),
		// running 当前实例数：前端据此显示「运行中 2/3」，也让「停止」按钮
		// 在有并行实例时不会因为其中一条结束就整块消失。
		"running": s.gate.count(sc.ID),
	})
}

// CreateScript 新建脚本：建目录、按语言写初始文件、落库。
//
// 顺序是「先落库再写文件」还是反过来？这里先落库：id 由库生成，目录名必须
// 用这个 id；反过来要先生成 id 再写库，一旦写库失败就留下一个孤儿目录。
func (s *ScriptService) CreateScript(in ScriptInput) *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	sc := &db.Script{
		Name:        strings.TrimSpace(in.Name),
		Description: in.Description,
		Lang:        in.Lang,
		Entry:       in.Entry,
		Interpreter: in.Interpreter,
		DefaultArgs: in.DefaultArgs,
		TimeoutSec:  in.TimeoutSec,
		Concurrency: in.Concurrency,
		AutoInstall: in.AutoInstall,
		Pinned:      in.Pinned,
		MCPAllowed:  in.MCPAllowed,
		Tags:        in.Tags,
		Sort:        in.Sort,
		WatchPath:   strings.TrimSpace(in.WatchPath),
	}
	created, err := s.App.DB.CreateScript(sc)
	if err != nil {
		return services.Fail(err)
	}
	// 落库时 normalizeScript 可能补了 lang/entry，必须用返回值建文件。
	dir := s.dir(created.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// 目录建不出来就把库记录删掉：留着一条点开就报错的脚本比没有更糟。
		_ = s.App.DB.DeleteScript(created.ID)
		return services.Fail(fmt.Errorf("创建脚本目录失败: %w", err))
	}
	entry := filepath.Join(dir, filepath.FromSlash(created.Entry))
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		return services.Fail(err)
	}
	if err := os.WriteFile(entry, []byte(templateFor(created.Lang, created.Name)), 0o644); err != nil {
		return services.Fail(fmt.Errorf("写入入口文件失败: %w", err))
	}
	// 用户自己新建的脚本直接可信：模板是他自己的。导入路径会紧接着删掉这个
	// 标记（见 ImportScript），于是「自己建」与「外面来的」在这里分道。
	_ = s.markTrusted(created.ID)
	s.emit(EventChanged, map[string]any{"scriptId": created.ID, "reason": "create"})
	return services.Ok(created)
}

// CreateScriptWithFiles 新建脚本并一次性写入多个文件（AI 建脚本走这里）。
//
// 与 CreateScript 的差别只有三点，其余（校验、目录、信任标记、事件）全部复用，
// 免得「AI 建的脚本」和「手建的脚本」逐渐长出两套规则：
//   - files 里的内容覆盖模板，于是 AI 不必先建再改；
//   - 入口文件自动推断（见 entryFromFiles）：AI 最容易犯的错是写了 index.js
//     却让模板占着 main.js，现象是「跑起来没有任何输出」；
//   - Go 脚本自动补 go.mod（版本号必须是本机这个，模型猜不出来）。
//
// 路径与体积在这里先校验一遍再建库记录：否则写到一半失败会留下一条缺文件的脚本。
func (s *ScriptService) CreateScriptWithFiles(in ScriptInput, files map[string]string) *services.ApiResult {
	if len(files) > maxCreateFiles {
		return services.FailMsg(fmt.Sprintf("一次最多写入 %d 个文件", maxCreateFiles))
	}
	for rel, content := range files {
		if _, err := s.resolveRelForNew(rel); err != nil {
			return services.Fail(err)
		}
		if len(content) > maxToolFileBytes {
			return services.FailMsg(fmt.Sprintf("文件 %s 超过 %dKB，请拆小或改用本地编辑器", rel, maxToolFileBytes/1024))
		}
	}
	if strings.TrimSpace(in.Entry) == "" {
		in.Entry = entryFromFiles(in.Lang, files)
	}
	res := s.CreateScript(in)
	if res.Code != 0 {
		return res
	}
	sc, _ := res.Data.(*db.Script)
	if sc == nil {
		return services.FailMsg("脚本已创建但返回值缺失")
	}
	written := make([]string, 0, len(files))
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths) // 固定顺序：返回值与日志可复现，模型重试时能对上
	for _, p := range paths {
		if r := s.WriteScriptFile(sc.ID, p, files[p]); r.Code != 0 {
			return services.FailMsg(fmt.Sprintf("脚本已创建，但写 %s 失败：%s", p, r.Msg))
		}
		written = append(written, p)
	}
	goMod := false
	if sc.Lang == scriptrun.LangGo {
		if r := s.EnsureGoMod(sc.ID); r.Code == 0 {
			if m, ok := r.Data.(map[string]any); ok {
				goMod, _ = m["created"].(bool)
			}
		}
		if goMod {
			written = append(written, "go.mod")
		}
	}
	deps := s.detectDeps(sc)
	return services.Ok(map[string]any{
		"script": sc,
		"files":  written,
		"goMod":  goMod,
		"deps":   deps,
		// 单列一份文件名：MCP 那边要据此写「首次运行会自动装依赖」这句提示，
		// 而 deps 是包内结构体，跨包拿不到字段。
		"depsFileName": deps.FileName,
	})
}

// maxCreateFiles 一次建脚本最多带多少个文件。
//
// 20 是个务实上限：真实的「脚本 + 依赖文件 + 一两个辅助模块」远用不到，
// 而放开到几十个只会让一次工具调用的参数大到审批卡都放不下、出了问题也难回滚。
const maxCreateFiles = 20

// maxToolFileBytes MCP 单文件写入上限（512KB）。
//
// 比编辑器那条 10MB 严得多：工具参数要进模型上下文与审批卡，几 MB 的字符串
// 既读不完也审不了；真有大文件，让用户用外部编辑器打开。
const maxToolFileBytes = 512 * 1024

// resolveRelForNew 只做路径合法性校验，不依赖已存在的脚本目录。
//
// 建脚本时目录名还得等库生成 id，没法走 resolveRel；但「路径逃逸」与
// 「伪造内部标记」这两条必须在建记录之前拦住。
func (s *ScriptService) resolveRelForNew(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("文件路径不能为空")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", fmt.Errorf("只能写相对路径：%s", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("非法路径：%s", rel)
	}
	if hasInternalMarker(rel) {
		return "", fmt.Errorf("不能写入内部标记文件：%s", rel)
	}
	return clean, nil
}

// hasInternalMarker 判断路径里有没有 .qd- 前缀的段。
//
// 为什么单列一条：`.qd-trusted` 是「这个脚本可信、手动运行不用再确认」的凭证，
// `.qd-deps-ok` 是「依赖已装好」的跳过标记。任何一条写入口（编辑器、AI、导入）
// 能写它们，就等于自己给自己发豁免。
func hasInternalMarker(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(seg, ".qd-") {
			return true
		}
	}
	return false
}

// entryFromFiles 在用户/模型没指定入口时，从要写进去的文件里挑一个。
//
// 只挑与语言匹配的扩展名：一次写 .py + requirements.txt 时，入口必须是 .py
// 而不是字典序更靠前的 requirements.txt。
func entryFromFiles(lang string, files map[string]string) string {
	if len(files) == 0 {
		return ""
	}
	exts := entryExts(lang)
	if len(exts) == 0 {
		return ""
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	// 先找 main/index/app/run 这类惯用名，再退到第一个扩展名匹配的。
	preferred := map[string]bool{"main": true, "index": true, "app": true, "run": true, "start": true}
	for _, p := range paths {
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if preferred[strings.ToLower(base)] && matchExt(p, exts) {
			return filepath.ToSlash(p)
		}
	}
	for _, p := range paths {
		if matchExt(p, exts) {
			return filepath.ToSlash(p)
		}
	}
	return ""
}

func entryExts(lang string) []string {
	switch lang {
	case scriptrun.LangPython:
		return []string{".py"}
	case scriptrun.LangJavaScript:
		return []string{".js", ".mjs", ".cjs"}
	case scriptrun.LangGo:
		return []string{".go"}
	case scriptrun.LangPHP:
		return []string{".php"}
	case scriptrun.LangShell:
		return []string{".sh", ".bash"}
	}
	return nil
}

func matchExt(p string, exts []string) bool {
	e := strings.ToLower(filepath.Ext(p))
	for _, x := range exts {
		if e == x {
			return true
		}
	}
	return false
}

// EnsureGoMod 给 Go 脚本补一个最小 go.mod，返回 {created,module,go}。
//
// 为什么由宿主写而不是让模型写：go 指令的版本号必须是本机装的这个——写高了
// 当前 go 直接拒绝执行，写低了 `go mod tidy` 又会自行改写，两种都会让模型
// 困惑于「我写的文件怎么变了」。module 路径用固定前缀避免与用户的真实模块撞名。
func (s *ScriptService) EnsureGoMod(scriptID string) *services.ApiResult {
	sc, err := s.App.DB.GetScript(scriptID)
	if err != nil {
		return services.Fail(err)
	}
	if sc.Lang != scriptrun.LangGo {
		return services.Ok(map[string]any{"created": false})
	}
	dir := s.dir(sc.ID)
	if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
		return services.Ok(map[string]any{"created": false})
	}
	ver := s.goLangVersion(sc)
	module := "quickdock.local/" + strings.ReplaceAll(sc.ID, "-", "")
	content := fmt.Sprintf("module %s\n\ngo %s\n", module, ver)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		return services.Fail(fmt.Errorf("写 go.mod 失败: %w", err))
	}
	s.emit(EventChanged, map[string]any{"scriptId": sc.ID, "reason": "create-file", "path": "go.mod"})
	return services.Ok(map[string]any{"created": true, "module": module, "go": ver})
}

// goLangVersion 取当前 Go 的 major.minor（如 1.27）。解析不出来时给 1.21：
// 那是 go.mod 里最常见的下限，且任何现代 go 都认。
func (s *ScriptService) goLangVersion(sc *db.Script) string {
	const fallback = "1.21"
	ip, err := scriptrun.Resolve(scriptrun.Spec{
		Lang:        scriptrun.LangGo,
		Interpreter: s.interpreterFor(sc),
	}, s.locateRuntime)
	if err != nil {
		return fallback
	}
	out, err := exec.Command(ip.Exe, "version").Output()
	if err != nil {
		return fallback
	}
	// 形如 "go version go1.27.2 windows/amd64"
	fields := strings.Fields(string(out))
	for _, f := range fields {
		if !strings.HasPrefix(f, "go1.") {
			continue
		}
		v := strings.TrimPrefix(f, "go")
		parts := strings.Split(v, ".")
		if len(parts) < 2 {
			continue
		}
		return parts[0] + "." + parts[1]
	}
	return fallback
}

// UpdateScript 保存脚本元数据。
func (s *ScriptService) UpdateScript(in ScriptInput) *services.ApiResult {
	if strings.TrimSpace(in.ID) == "" {
		return services.FailMsg("缺少脚本 id")
	}
	old, err := s.App.DB.GetScript(in.ID)
	if err != nil {
		return services.Fail(err)
	}
	old.Name = in.Name
	old.Description = in.Description
	old.Lang = in.Lang
	old.Entry = in.Entry
	old.Interpreter = in.Interpreter
	old.DefaultArgs = in.DefaultArgs
	old.TimeoutSec = in.TimeoutSec
	old.Concurrency = in.Concurrency
	old.AutoInstall = in.AutoInstall
	old.Pinned = in.Pinned
	old.MCPAllowed = in.MCPAllowed
	old.Tags = in.Tags
	old.Sort = in.Sort
	old.WatchPath = strings.TrimSpace(in.WatchPath)
	if err := s.App.DB.UpdateScript(old); err != nil {
		return services.Fail(err)
	}
	s.emit(EventChanged, map[string]any{"scriptId": old.ID, "reason": "update"})
	return services.Ok(old)
}

// DeleteScript 删除脚本：库记录真删，目录改名进回收区（保留 30 天）。
//
// 不直接 RemoveAll：脚本是用户资产，误点一次的代价应该是「去回收区捞回来」，
// 而不是「重写一遍」。
func (s *ScriptService) DeleteScript(id string) *services.ApiResult {
	sc, err := s.App.DB.GetScript(id)
	if err != nil {
		return services.Fail(err)
	}
	if err := s.App.DB.DeleteScript(id); err != nil {
		return services.Fail(err)
	}
	dir := s.dir(sc.ID)
	if _, err := os.Stat(dir); err == nil {
		trash := filepath.Join(s.Root(), fmt.Sprintf("%s.deleted-%d", sc.ID, time.Now().Unix()))
		if err := os.Rename(dir, trash); err != nil {
			logger.W("[script] 移动脚本目录到回收区失败 %s: %v", sc.ID, err)
		}
	}
	s.emit(EventChanged, map[string]any{"scriptId": id, "reason": "delete"})
	return services.Ok(nil)
}

// SetScriptPin 置顶。
func (s *ScriptService) SetScriptPin(id string, pinned bool) *services.ApiResult {
	if err := s.App.DB.SetScriptPin(id, pinned); err != nil {
		return services.Fail(err)
	}
	s.emit(EventChanged, map[string]any{"scriptId": id, "reason": "pin"})
	return services.Ok(nil)
}

// OpenScriptDir 在文件管理器中打开脚本目录（配 git / 外部编辑器）。
func (s *ScriptService) OpenScriptDir(id string) *services.ApiResult {
	result := s.GetScript(id)
	if result.Code != 0 {
		return result
	}
	if err := platform.RevealInExplorer(s.dir(id)); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ---------- 文件读写 ----------

// ScriptFile 脚本目录下的一个文件。
type ScriptFile struct {
	Path     string `json:"path"` // 相对脚本目录，斜杠分隔
	Size     int64  `json:"size"`
	IsDir    bool   `json:"isDir"`
	Modified string `json:"modified"`
	// Entry 是否为当前入口文件（前端高亮用）。
	Entry bool `json:"entry"`
}

// maxFileDepth 目录遍历深度上限，防止误拖 node_modules 把文件树撑爆。
const maxFileDepth = 4

// maxFileBytes 单文件读写上限（10MB）。脚本要读的数据文件不该走编辑器。
const maxFileBytes = 10 << 20

func (s *ScriptService) listFiles(scriptID string) ([]ScriptFile, error) {
	root := s.dir(scriptID)
	sc, _ := s.App.DB.GetScript(scriptID)
	entry := ""
	if sc != nil {
		entry = filepath.ToSlash(sc.Entry)
	}
	out := make([]ScriptFile, 0, 16)
	var walk func(dir, rel string, depth int) error
	walk = func(dir, rel string, depth int) error {
		if depth > maxFileDepth {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			// .git 与 node_modules 是脚本的附庸而不是「文件」：列出来只会让
			// 面板变成第二个资源管理器，而且 node_modules 能有几万个条目。
			if name == ".git" || name == "node_modules" || name == "__pycache__" || name == ".venv" {
				continue
			}
			// 内部标记（.qd-trusted / .qd-deps-ok）不是用户资产：列出来只会
			// 让人误删——删掉 .qd-trusted 会重新弹出确认，删掉 .qd-deps-ok
			// 会让下次运行重装一遍依赖，两个后果都很莫名。
			if strings.HasPrefix(name, ".qd-") {
				continue
			}
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, ScriptFile{
				Path:     childRel,
				Size:     info.Size(),
				IsDir:    e.IsDir(),
				Modified: info.ModTime().Format(time.RFC3339),
				Entry:    !e.IsDir() && childRel == entry,
			})
			if e.IsDir() {
				if err := walk(filepath.Join(dir, name), childRel, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, "", 1); err != nil {
		return out, err
	}
	// 文件在前、目录在后；同类按名字。入口文件靠 Entry 标记让前端置顶。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return !out[i].IsDir
		}
		if out[i].Entry != out[j].Entry {
			return out[i].Entry
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// resolveRel 把前端传来的相对路径解析成脚本目录内的绝对路径。
//
// 双重校验（Clean 后不许以 .. 开头 + Rel 结果不许逃逸）：脚本面板的路径来自
// 前端，而前端的值可能被插件或外部调用方构造——只靠 Clean 挡不住 `a/../../b`
// 这种「Clean 完仍在界内但语义可疑」的写法吗？挡得住，但显式比一次 Rel
// 最稳，代价可以忽略。
func (s *ScriptService) resolveRel(scriptID, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("文件路径不能为空")
	}
	// 内部标记文件（.qd-trusted / .qd-deps-ok）不许经这条路径写：前者等于
	// 自己给自己发「可信」凭证，后者能让依赖被跳过安装。编辑器、MCP、导入
	// 都走这里，一处拦住等于全拦住。
	if hasInternalMarker(rel) {
		return "", fmt.Errorf("不能写入内部标记文件：%s", rel)
	}
	root := filepath.Clean(s.dir(scriptID))
	abs := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	r, err := filepath.Rel(root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("非法路径：%s", rel)
	}
	return abs, nil
}

// ReadScriptFile 读一个文件（编辑器打开时用）。
func (s *ScriptService) ReadScriptFile(scriptID, rel string) *services.ApiResult {
	abs, err := s.resolveRel(scriptID, rel)
	if err != nil {
		return services.Fail(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return services.Fail(err)
	}
	if info.IsDir() {
		return services.FailMsg("目标是一个目录")
	}
	if info.Size() > maxFileBytes {
		return services.FailMsg("文件超过 10MB，请用外部编辑器打开")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]any{
		"path":     rel,
		"content":  string(data),
		"size":     info.Size(),
		"modified": info.ModTime().Format(time.RFC3339),
		// 前端据此选 CodeMirror 语言。
		"lang": langForFile(rel),
	})
}

// WriteScriptFile 保存文件内容。
func (s *ScriptService) WriteScriptFile(scriptID, rel, content string) *services.ApiResult {
	abs, err := s.resolveRel(scriptID, rel)
	if err != nil {
		return services.Fail(err)
	}
	if len(content) > maxFileBytes {
		return services.FailMsg("内容超过 10MB，拒绝写入")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return services.Fail(err)
	}
	// 保留原文件权限位，至少保住可执行位：WriteFile 用固定 0644 会把
	// 用户 chmod +x 过的 shell 脚本变成不可执行，而这种变化在 Windows 上
	// 看不出来、在 Unix 上要等到运行才报 permission denied。
	mode := os.FileMode(0o644)
	if info, err := os.Stat(abs); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(abs, []byte(content), mode); err != nil {
		return services.Fail(err)
	}
	s.emit(EventChanged, map[string]any{"scriptId": scriptID, "reason": "save", "path": rel})
	return services.Ok(nil)
}

// CreateScriptFile 新建文件（含空目录）。
func (s *ScriptService) CreateScriptFile(scriptID, rel, content string) *services.ApiResult {
	abs, err := s.resolveRel(scriptID, rel)
	if err != nil {
		return services.Fail(err)
	}
	if _, err := os.Stat(abs); err == nil {
		return services.FailMsg("同名文件已存在")
	}
	// 以 / 结尾 = 建目录。
	if strings.HasSuffix(rel, "/") {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return services.Fail(err)
		}
		s.emit(EventChanged, map[string]any{"scriptId": scriptID, "reason": "create-file"})
		return services.Ok(nil)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return services.Fail(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return services.Fail(err)
	}
	s.emit(EventChanged, map[string]any{"scriptId": scriptID, "reason": "create-file", "path": rel})
	return services.Ok(nil)
}

// DeleteScriptFile 删除文件/空目录。
func (s *ScriptService) DeleteScriptFile(scriptID, rel string) *services.ApiResult {
	abs, err := s.resolveRel(scriptID, rel)
	if err != nil {
		return services.Fail(err)
	}
	// 入口文件不许删：删了脚本就永远跑不了，而错误信息只会是一句
	// 「找不到文件」，用户很难联想到「我删的是入口」。
	sc, err := s.App.DB.GetScript(scriptID)
	if err != nil {
		return services.Fail(err)
	}
	if filepath.ToSlash(sc.Entry) == rel {
		return services.FailMsg("不能删除入口文件，请先在设置里换入口")
	}
	if err := os.RemoveAll(abs); err != nil {
		return services.Fail(err)
	}
	s.emit(EventChanged, map[string]any{"scriptId": scriptID, "reason": "delete-file", "path": rel})
	return services.Ok(nil)
}

// ---------- 模板 ----------

func templateFor(lang, name string) string {
	switch lang {
	case scriptrun.LangJavaScript:
		return fmt.Sprintf(`// %s
// QuickDock 内置变量：QD_SCRIPT_ID / QD_SCRIPT_DIR / QD_RUN_ID / QD_DATA_DIR
// 变量在右侧「变量」面板配置，运行时注入 process.env。
console.log('hello from', process.env.QD_SCRIPT_ID)
`, name)
	case scriptrun.LangShell:
		return fmt.Sprintf(`#!/usr/bin/env bash
# %s
# QuickDock 内置变量：QD_SCRIPT_ID / QD_SCRIPT_DIR / QD_RUN_ID / QD_DATA_DIR
set -euo pipefail
echo "hello from ${QD_SCRIPT_ID}"
`, name)
	case scriptrun.LangGo:
		return fmt.Sprintf(`// %s
//
// QuickDock 内置变量：QD_SCRIPT_ID / QD_SCRIPT_DIR / QD_RUN_ID / QD_DATA_DIR
// 变量在右侧「变量」面板配置，运行时注入 os.Environ。
//
// 运行方式是 go run（源码不编译成独立程序），所以这里是 package main。
// 依赖：同目录放 go.mod 时，点「安装依赖」会执行 go mod tidy；
// 只用标准库则不需要 go.mod。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("hello from", os.Getenv("QD_SCRIPT_ID"))
}
`, name)
	case scriptrun.LangPHP:
		return fmt.Sprintf(`<?php
// %s
//
// QuickDock 内置变量：QD_SCRIPT_ID / QD_SCRIPT_DIR / QD_RUN_ID / QD_DATA_DIR
// 变量在右侧「变量」面板配置，运行时注入进程环境，用 getenv() 读取
//（$_ENV 取决于 php.ini 的 variables_order，不一定有值）。
//
// 依赖：同目录放 composer.json 时，点「安装依赖」会执行 composer install。
echo "hello from ", getenv('QD_SCRIPT_ID'), PHP_EOL;
`, name)
	case scriptrun.LangCustom:
		return ""
	}
	return fmt.Sprintf(`# -*- coding: utf-8 -*-
"""%s

QuickDock 内置变量：QD_SCRIPT_ID / QD_SCRIPT_DIR / QD_RUN_ID / QD_DATA_DIR
变量在右侧「变量」面板配置，运行时注入 os.environ。

依赖：在同目录放 requirements.txt，点「安装依赖」即可。
"""

import os


def main():
    print("hello from", os.environ.get("QD_SCRIPT_ID"))


if __name__ == "__main__":
    main()
`, name)
}

// langForFile 按扩展名给编辑器语言提示。
func langForFile(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".go":
		return "go"
	case ".php":
		return "php"
	case ".sh", ".bash":
		return "shell"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".sql":
		return "sql"
	case ".md":
		return "markdown"
	case ".txt", ".log":
		return "text"
	}
	return "text"
}

// ---------- 依赖探测 ----------

// depsInfo 依赖文件探测结果。
type depsInfo struct {
	Python bool `json:"python"` // 有 requirements.txt
	Node   bool `json:"node"`   // 有 package.json
	Go     bool `json:"go"`     // 有 go.mod
	PHP    bool `json:"php"`    // 有 composer.json
	// FileName 命中的文件名；空表示这个脚本没有依赖声明。
	FileName  string `json:"fileName"`
	Installed bool   `json:"installed"` // 当前内容对应的依赖已装过
	// Stale 依赖文件在安装之后被改过（或从未装过）。前端据此提示
	// 「依赖有变化，建议重新安装」，而不是等运行时 ImportError。
	Stale bool `json:"stale"`
}

// depFile 一种依赖文件与它对应的安装器。
type depFile struct {
	Name string
	Lang string
}

// depFiles 依赖文件白名单。语言与文件一一对应，顺序只在「语言本身没有依赖
// 约定」（shell / custom）时起兜底作用，见 detectDeps。
//
// Go 用 go.mod 而不是 go.sum：go.sum 只是校验和，`go mod tidy` 认的是 go.mod；
// PHP 用 composer.json 而不是 composer.lock（lock 由 install 自己读）。
var depFiles = []depFile{
	{"requirements.txt", scriptrun.LangPython},
	{"package.json", scriptrun.LangJavaScript},
	{"go.mod", scriptrun.LangGo},
	{"composer.json", scriptrun.LangPHP},
}

// depsMarker 依赖已安装的标记文件（脚本目录内，用户可见）。
const depsMarker = ".qd-deps-ok"

// depsFingerprint 依赖文件的内容指纹（sha256 前 16 位十六进制）。
//
// 为什么不用 mtime：`git checkout`、解压备份、从网盘同步都会把 mtime 刷成
// 「刚刚」，内容却没变——按 mtime 判断等于每次都要白装一遍依赖（pip install
// 几十秒到几分钟）；反过来同一秒内改两次文件 mtime 可能不变，按 mtime 就会
// 漏装，表现是运行时 ImportError，用户不可能联想到「依赖没重装」。
func depsFingerprint(dir, fileName string) string {
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// depsMarkerValue 读当前标记内容；没有则返回空串。
func (s *ScriptService) depsMarkerValue(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, depsMarker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// markDepsInstalled 依赖装成功后写入指纹。
func (s *ScriptService) markDepsInstalled(sc *db.Script) {
	dir := s.dir(sc.ID)
	deps := s.detectDeps(sc)
	if deps.FileName == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, depsMarker), []byte(depsFingerprint(dir, deps.FileName)), 0o644); err != nil {
		logger.W("[script] 写依赖标记失败 %s: %v", sc.ID, err)
	}
}

// DepsFileName 返回该脚本命中的依赖文件名（空 = 没有依赖文件）。
// 给 MCP 的审批文案用：卡上要写清「这一下会执行哪条安装命令」。
func (s *ScriptService) DepsFileName(id string) string {
	sc, err := s.App.DB.GetScript(id)
	if err != nil {
		return ""
	}
	return s.detectDeps(sc).FileName
}

// detectDeps 探测脚本目录里的依赖声明。
//
// 语言与依赖文件是一一对应的（Go→go.mod、PHP→composer.json…），所以只认本语言
// 那一个：否则把一个脚本从 Python 改成 Go 之后，目录里遗留的 requirements.txt
// 会让它每次运行前都去 pip install 一遍。只有语言本身没有依赖约定（shell /
// custom）时才按 depFiles 顺序兜底——导入来的历史脚本 lang 常被写成 custom，
// 但目录里摆着 requirements.txt，那也是它的依赖。
func (s *ScriptService) detectDeps(sc *db.Script) depsInfo {
	dir := s.dir(sc.ID)
	langHasDep := false
	for _, d := range depFiles {
		if d.Lang == sc.Lang {
			langHasDep = true
			break
		}
	}
	var hit *depFile
	for i := range depFiles {
		if langHasDep && depFiles[i].Lang != sc.Lang {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, depFiles[i].Name)); err == nil {
			hit = &depFiles[i]
			break
		}
	}
	if hit == nil {
		return depsInfo{}
	}
	want := depsFingerprint(dir, hit.Name)
	installed := want != "" && s.depsMarkerValue(dir) == want
	out := depsInfo{FileName: hit.Name, Installed: installed, Stale: !installed}
	switch hit.Lang {
	case scriptrun.LangPython:
		out.Python = true
	case scriptrun.LangJavaScript:
		out.Node = true
	case scriptrun.LangGo:
		out.Go = true
	case scriptrun.LangPHP:
		out.PHP = true
	}
	return out
}

// defaultArgsSlice 解析脚本默认参数（JSON 数组）。坏数据按空处理并记日志，
// 不让一句手改坏的 JSON 让整个运行失败。
func defaultArgsSlice(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		logger.W("[script] 默认参数不是合法 JSON 数组，按空处理: %v", err)
		return nil
	}
	return args
}
