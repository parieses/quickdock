package env

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

// Install 一个已安装的运行时节（便携目录或系统 PATH 上检测到的）
type Install struct {
	Version string `json:"version"`
	Scope   string `json:"scope"` // "portable" 便携目录 | "system" 系统 PATH
	Path    string `json:"path"`  // 便携=目录，系统=可执行文件
	Active  bool   `json:"active"` // 是否为当前激活（环境变量指向）版本
	// InSystemPath 表示该版本的 bin 目录是否真正出现在系统 PATH（HKCU）中。
	// 由后端读取真实系统 PATH 并比对得出（含 %变量% 展开），用于列表「环境变量设置」列如实回显。
	InSystemPath bool   `json:"inSystemPath"`
	Alias        string `json:"alias"` // 用户设置的别名
	Note         string `json:"note"`  // 用户设置的备注
}

// ServiceStatus 服务运行状态（nginx/redis 等支持以服务方式启动的运行时）
type ServiceStatus struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	Version string `json:"version"`
}

// RuntimeAdapter 单一受管运行时的统一接口。支持多版本：安装落到 runtime/<rt>/<version>，
// 检测同时覆盖便携目录与系统 PATH（修复「本机已装但提示未安装」）。
type RuntimeAdapter interface {
	Kind() Runtime
	DisplayName() string
	SupportedPlatforms() []string
	Recommended() []string
	InstalledVersions() []Install
	ExeFor(version string) string
	Install(ctx context.Context, version string, cb InstallCallback) error
	// DeleteVersion 删除某已安装版本（便携目录）。系统 PATH 上的版本通常无法在此删除，返回错误。
	DeleteVersion(version string) error
	// DetectArgs 返回导入外部安装时探测版本号所用的命令行参数（如 []string{"--version"}）。
	// 返回空切片表示该运行时不可通过版本探测导入（如 FTPDMIN 无 --version 且启动即服务）。
	DetectArgs() []string
	// ParseVersion 将 DetectArgs 探测到的标准输出解析为纯版本号（如 "7.4.0"）。
	// 解析失败时返回 error（导入流程据此拒绝无法识别的目录）。
	ParseVersion(output string) (string, error)
}

// ServiceController 可选能力：以服务方式启动/停止并查询运行状态（nginx/redis 实现）。
type ServiceController interface {
	Start(ctx context.Context, version string, onLog func(string)) error
	Stop(version string) error
	Status(version string) ServiceStatus
	DefaultPort() int
}

// RuntimeInfo 提供给前端的运行时概览
type RuntimeInfo struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Group        string       `json:"group"` // 分组：language / webserver / cache / tool
	Platforms    []string     `json:"platforms"`
	Recommended  []string     `json:"recommended"` // 兜底可下载版本（拉取失败时使用）
	Installed    []Install    `json:"installed"`   // 已装版本列表（可多个）
	Sources      []SourceInfo `json:"sources"`
	ActiveSource   string `json:"activeSource"`
	HasService     bool   `json:"hasService"`     // 是否支持服务启停/状态监听
	HasLog         bool   `json:"hasLog"`         // 是否支持运行日志查询（实现 LogProvider）
	WebConsolePort int    `json:"webConsolePort"` // Web 管理后台端口（0=无，实现 WebConsoleProvider）
}

type SourceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// linkEntry 用户「导入已有安装」时登记的外部目录（不复制文件，仅记录路径以便查看/切换）。
type linkEntry struct {
	Version string `json:"version"`
	Dir     string `json:"dir"` // 外部安装目录（其下含 exe 与配套文件）
}

// Manager 环境管理门面：聚合各运行时 adapter，供 AppService 调用。
type Manager struct {
	adapters map[Runtime]RuntimeAdapter
	// links 记录用户导入的外部安装目录（按运行时），loadLinks/saveLinks 持久化到 env/links.json
	linksMu sync.RWMutex
	links   map[Runtime][]linkEntry
	// detectCache 缓存真实检测出的已装版本（detect_cache.go），List/InstalledVersions 读缓存
	// 避免每次 spawn exe 探测版本；RefreshDetected 在触发点重扫并持久化到 env/detected.json
	detectMu    sync.RWMutex
	detectCache map[Runtime][]Install
}

// runtimeOrder 运行时固定展示顺序
var runtimeOrder = []Runtime{RuntimeNode, RuntimePHP, RuntimeGo, RuntimeBun, RuntimeErlang, RuntimeRedis, RuntimeNginx, RuntimeGit, RuntimeCaddy, RuntimeTraefik, RuntimeComposer,
	RuntimeFFmpeg, RuntimePython, RuntimeApache, RuntimeMemcached, RuntimeMariaDB, RuntimeMySQL, RuntimePostgreSQL, RuntimeMongoDB,
	RuntimeMailpit, RuntimeMinIO, RuntimeRabbitMQ, RuntimeFrpc, RuntimeFTP, RuntimeGh, RuntimeMkcert}

func NewManager() *Manager {
	m := &Manager{
		adapters: map[Runtime]RuntimeAdapter{
			RuntimeNode:  NewNodeRuntime(),
			RuntimePHP:   NewPHPRuntime(),
			RuntimeGo:    NewGoRuntime(),
			RuntimeRedis: NewRedisRuntime(),
			RuntimeNginx: NewNginxRuntime(),
			RuntimeGit:   NewGitRuntime(),
			RuntimeCaddy:    NewCaddyRuntime(),
			RuntimeComposer: NewComposerRuntime(),
			RuntimeFFmpeg:     NewFFmpegRuntime(),
			RuntimePython:     NewPythonRuntime(),
			RuntimeApache:     NewApacheRuntime(),
			RuntimeMemcached:  NewMemcachedRuntime(),
			RuntimeMariaDB:    NewMariaDBRuntime(),
			RuntimeMySQL:      NewMySQLRuntime(),
			RuntimePostgreSQL: NewPostgresRuntime(),
			RuntimeMongoDB:    NewMongoRuntime(),
			RuntimeMailpit:   NewMailpitRuntime(),
			RuntimeMinIO:     NewMinioRuntime(),
			RuntimeFrpc:      NewFrpcRuntime(),
			RuntimeFTP:       NewFTPRuntime(),
			RuntimeGh:        NewGhRuntime(),
			RuntimeBun:       NewBunRuntime(),
			RuntimeErlang:    NewErlangRuntime(),
			RuntimeTraefik:   NewTraefikRuntime(),
			RuntimeMkcert:    NewMkcertRuntime(),
			RuntimeRabbitMQ:  NewRabbitMQRuntime(),
		},
		links:       map[Runtime][]linkEntry{},
		detectCache: map[Runtime][]Install{},
	}
	m.loadLinks()
	m.loadDetected()
	m.syncPHPLinks()
	return m
}

// linksFile 返回 links 持久化文件路径（env/links.json）。
func (m *Manager) linksFile() string {
	return filepath.Join(platform.DefaultDataDir(), "env", "links.json")
}

func (m *Manager) loadLinks() {
	data, err := os.ReadFile(m.linksFile())
	if err != nil {
		return
	}
	var tmp map[Runtime][]linkEntry
	if json.Unmarshal(data, &tmp) == nil {
		m.linksMu.Lock()
		m.links = tmp
		m.linksMu.Unlock()
	}
}

// snapshotLinksLocked 在调用方已持有 linksMu（读或写）时返回 links 的深拷贝：
// map 与每个运行时的条目切片都不与 live 数据共享底层数组，使解锁后序列化/遍历不再触碰
// live map，避免与持写锁的写入方（removeLink/ImportVersion）并发触发
// concurrent map read and map write（runtime fatal error，不可 recover）。
func (m *Manager) snapshotLinksLocked() map[Runtime][]linkEntry {
	out := make(map[Runtime][]linkEntry, len(m.links))
	for rt, entries := range m.links {
		out[rt] = append([]linkEntry(nil), entries...)
	}
	return out
}

// saveLinks 持久化 links（自行取读锁）。未持锁时调用这个。
func (m *Manager) saveLinks() error {
	m.linksMu.RLock()
	defer m.linksMu.RUnlock()
	return m.saveLinksLocked()
}

// saveLinksLocked 序列化并落盘。调用方必须已持有 linksMu（读或写均可）。
// 单独拆出是因为 RWMutex 不可重入：removeLink 持写锁时不能再经 saveLinks 取读锁。
func (m *Manager) saveLinksLocked() error {
	data, err := json.MarshalIndent(m.snapshotLinksLocked(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.linksFile()), 0755); err != nil {
		return err
	}
	return os.WriteFile(m.linksFile(), data, 0644)
}

// linkedDir 返回某运行时某版本「导入目录」的真实路径（找到时 ok=true）。
func (m *Manager) linkedDir(rt Runtime, version string) (string, bool) {
	m.linksMu.RLock()
	defer m.linksMu.RUnlock()
	for _, l := range m.links[rt] {
		if l.Version == version {
			return l.Dir, true
		}
	}
	return "", false
}

// importExeName 返回某运行时可执行文件名（用于拼接导入目录下的 exe 路径）。
// 直接由 adapter.ExeFor 推导可执行文件名，去掉了原先维护一份「运行时→exe 名」映射的巨型 switch：
// 新增运行时只需在 adapter 实现 ExeFor 与 DetectArgs，无需再同步 importExeName。
// 返回 "" 表示该运行时不可导入（DetectArgs 为空，如 FTPDMIN）。
func (m *Manager) importExeName(rt Runtime) string {
	a, err := m.adapter(rt)
	if err != nil {
		return ""
	}
	if len(a.DetectArgs()) == 0 {
		return ""
	}
	// ExeFor 需要一个版本占位符；可执行文件名与具体版本无关，任意非空占位即可。
	return filepath.Base(a.ExeFor("0"))
}

// exeDirFor 返回某版本 bin 目录：导入版本取外部目录，其余取 adapter 约定目录。
func (m *Manager) exeDirFor(rt Runtime, version string) string {
	if dir, ok := m.linkedDir(rt, version); ok {
		return dir
	}
	a, err := m.adapter(rt)
	if err != nil {
		return ""
	}
	return filepath.Dir(a.ExeFor(version))
}

// scopeFor 返回某版本 scope：导入版本为 "linked"，其余取 adapter 判定。
func (m *Manager) scopeFor(rt Runtime, version string) string {
	if _, ok := m.linkedDir(rt, version); ok {
		return "linked"
	}
	a, err := m.adapter(rt)
	if err != nil {
		return ""
	}
	// 优先查检测结果缓存（无 spawn），缓存未命中（外部新装的版本）再兜底真实扫描
	for _, ins := range m.cachedInstalls(rt) {
		if ins.Version == version {
			return ins.Scope
		}
	}
	for _, ins := range a.InstalledVersions() {
		if ins.Version == version {
			return ins.Scope
		}
	}
	return ""
}

// mergeLinks 将用户导入的外部目录作为 installed 项追加（scope=linked）。
func (m *Manager) mergeLinks(rt Runtime, installs []Install) []Install {
	m.linksMu.RLock()
	// 持锁内拷贝出独立切片：解锁后再遍历会触碰 live 底层数组，
	// 而 removeLink 可能同时持写锁改写它。
	lk := append([]linkEntry(nil), m.links[rt]...)
	m.linksMu.RUnlock()
	for _, l := range lk {
		installs = append(installs, Install{Version: l.Version, Scope: "linked", Path: l.Dir})
	}
	return installs
}

// linksToMap 把 linkEntry 列表压成 version→dir 映射，便于注入到具体运行时适配器。
func linksToMap(entries []linkEntry) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.Version] = e.Dir
	}
	return m
}

// syncPHPLinks 把导入版 PHP 目录同步给 PHPRuntime，使其也能以 php-fpm 方式启停（自行取读锁）。
func (m *Manager) syncPHPLinks() {
	m.linksMu.RLock()
	defer m.linksMu.RUnlock()
	m.syncPHPLinksLocked()
}

// syncPHPLinksLocked 同 syncPHPLinks，但调用方必须已持有 linksMu（读或写均可）。
// 单独拆出是因为 RWMutex 不可重入：removeLink 持写锁时不能再经 syncPHPLinks 取读锁。
func (m *Manager) syncPHPLinksLocked() {
	if p, ok := m.adapters[RuntimePHP].(*PHPRuntime); ok {
		p.SetLinkedDirs(linksToMap(m.links[RuntimePHP]))
	}
}

func (m *Manager) adapter(rt Runtime) (RuntimeAdapter, error) {
	a, ok := m.adapters[rt]
	if !ok {
		return nil, fmt.Errorf("不支持的运行时: %s", rt)
	}
	return a, nil
}

// List 返回所有运行时概览（顺序：Node → PHP → Go → Redis → Nginx → Git）。
// 已装版本读检测结果缓存（detect_cache.go），不做真实探测——真实探测由触发点
// （启动后台扫描/导入/安装/删除）执行并重新保存，保证本方法毫秒级返回、前端列表不闪。
func (m *Manager) List() []RuntimeInfo {
	// 系统 PATH 只读取一次（注册表读操作），供所有运行时复用，避免 List 遍历 6 个运行时时重复读注册表。
	pathVal, _ := sysReadPath()
	entries := splitSystemPath(pathVal)
	var out []RuntimeInfo
	for _, rt := range runtimeOrder {
		a := m.adapters[rt]
		var si []SourceInfo
		for _, s := range ListSources(rt) {
			si = append(si, SourceInfo{ID: s.ID, Name: s.Name})
		}
		_, hasSvc := a.(ServiceController)
		_, hasLog := a.(LogProvider)
		wcPort := 0
		if wp, ok := a.(WebConsoleProvider); ok {
			wcPort = wp.WebConsolePort("")
		}
		installed := m.mergeLinks(rt, m.cachedInstalls(rt))
		out = append(out, RuntimeInfo{
			ID:             string(rt),
			Name:           a.DisplayName(),
			Group:          registry[rt].group,
			Platforms:      orEmpty(a.SupportedPlatforms()),
			Recommended:    orEmpty(a.Recommended()),
			Installed:      orEmpty(mergeMeta(rt, installed, activeVersion(rt), entries)),
			Sources:        orEmpty(si),
			ActiveSource:   ActiveSource(rt),
			HasService:     hasSvc,
			HasLog:         hasLog,
			WebConsolePort: wcPort,
		})
	}
	return out
}

// mergeMeta 将用户元数据（激活版本/别名/备注）合并进已安装版本列表，
// 并计算每个版本是否真正注册进系统 PATH（InSystemPath）。
// active 为该运行时当前激活版本，entries 为已展开+Clean 的系统 PATH 目录集合（调用方一次性算好传入，
// 避免 List 遍历 6 个运行时时重复读注册表）。
func mergeMeta(rt Runtime, installs []Install, active string, entries map[string]bool) []Install {
	for i := range installs {
		vm := versionMetaOf(rt, installs[i].Version)
		installs[i].Alias = vm.Alias
		installs[i].Note = vm.Note
		installs[i].Active = active != "" && active == installs[i].Version
		installs[i].InSystemPath = binInSystemPath(installs[i], entries)
	}
	return installs
}

// binDirOf 返回某安装版本的 bin 目录：便携=Path 本身；系统=可执行文件所在目录。
func binDirOf(ins Install) string {
	if ins.Scope == "system" {
		return filepath.Dir(ins.Path)
	}
	return ins.Path
}

// splitSystemPath 把 PATH 原始字符串拆成已展开（%变量%→真实值）、已 Clean 的目录集合（小写键），
// 便于 O(1) 判断某个 bin 目录是否在 PATH 中。PATH 含 %USERPROFILE% 等占位符时也能正确比对。
func splitSystemPath(p string) map[string]bool {
	out := map[string]bool{}
	for _, e := range strings.Split(p, ";") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		e = expandEnvVars(e)
		out[strings.ToLower(filepath.Clean(e))] = true
	}
	return out
}

// expandEnvVars 将 Windows 风格 %VAR% 占位符替换为进程环境变量值（未知变量原样保留）。
func expandEnvVars(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		if v := os.Getenv(name); v != "" {
			return v
		}
		return m
	})
}

var envVarRe = regexp.MustCompile(`%([^%]+)%`)

// binInSystemPath 判断 ins 的 bin 目录是否出现在系统 PATH 条目集合中（大小写不敏感）。
func binInSystemPath(ins Install, entries map[string]bool) bool {
	if ins.Path == "" {
		return false
	}
	bin := binDirOf(ins)
	return entries[strings.ToLower(filepath.Clean(bin))]
}

// orEmpty 将 nil 切片转为空切片，避免 JSON 序列化为 null 导致前端数组访问报错
// （例如未安装任何版本时 InstalledVersions 返回 nil，前端 r.installed.length 会在 null 上抛错）。
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Install 安装指定运行时的指定版本（先切换下载源，再委托 adapter）。
func (m *Manager) Install(rt Runtime, version, sourceID, custom string, cb InstallCallback) error {
	if err := validateVersion(version); err != nil {
		return err
	}
	a, err := m.adapter(rt)
	if err != nil {
		return err
	}
	if sourceID != "" {
		SetActiveSource(rt, sourceID)
	}
	if custom != "" {
		SetCustomSource(rt, custom)
	}
	if err := a.Install(context.Background(), version, cb); err != nil {
		return err
	}
	// 安装完成后重扫并重新保存检测结果，列表立即含新版本（触发点之一）
	m.RefreshDetected(rt)
	return nil
}

// SetSource 切换下载源 / 设置自定义源（custom=="" 且 sourceID!="custom" 时清除自定义源）
func (m *Manager) SetSource(rt Runtime, sourceID, custom string) error {
	if _, err := m.adapter(rt); err != nil {
		return err
	}
	if sourceID != "" {
		SetActiveSource(rt, sourceID)
	}
	if custom != "" {
		SetCustomSource(rt, custom)
	} else if sourceID != "custom" {
		SetCustomSource(rt, "")
	}
	return nil
}

// Sources 返回某运行时的可用下载源
func (m *Manager) Sources(rt Runtime) ([]SourceInfo, error) {
	if _, err := m.adapter(rt); err != nil {
		return nil, err
	}
	var si []SourceInfo
	for _, s := range ListSources(rt) {
		si = append(si, SourceInfo{ID: s.ID, Name: s.Name})
	}
	return si, nil
}

// AvailableVersions 返回某运行时的全量可下载版本（上游拉取，失败兜底推荐列表）。
func (m *Manager) AvailableVersions(rt Runtime) []string {
	if _, err := m.adapter(rt); err != nil {
		return nil
	}
	return AvailableVersions(rt, "", "")
}

// InstalledVersions 返回某运行时已安装的全部版本（便携 + 系统 + 导入目录，已合并别名/备注/激活状态）。
// 读检测结果缓存，与 List 同源。
func (m *Manager) InstalledVersions(rt Runtime) ([]Install, error) {
	if _, err := m.adapter(rt); err != nil {
		return nil, err
	}
	pathVal, _ := sysReadPath()
	entries := splitSystemPath(pathVal)
	return mergeMeta(rt, m.mergeLinks(rt, m.cachedInstalls(rt)), activeVersion(rt), entries), nil
}

// SetActive 设置某运行时的激活版本（其 bin 目录即“环境变量指向”的版本）。version=="" 清除激活。
// 副作用：将激活版本的 bin 目录写入当前用户（HKCU）的 PATH 环境变量，使系统全局可用；
// 同时把上一个激活的便携/导入版本从 PATH 移除。系统 PATH 上的版本不重复注册（其本身已在 PATH 中）。
func (m *Manager) SetActive(rt Runtime, version string) error {
	prev := activeVersion(rt)
	binOf := func(v string) string { return m.exeDirFor(rt, v) }
	scopeOf := func(v string) string { return m.scopeFor(rt, v) }
	if version == "" {
		// 清除激活：从系统 PATH 移除旧版本 bin
		if prev != "" && scopeOf(prev) != "system" {
			_ = sysUnregisterPath(binOf(prev))
		}
	} else if version != prev {
		// 设置/切换激活：先写入新版本 bin，再移除旧版本
		if scopeOf(version) != "system" {
			if e := sysRegisterPath(binOf(version)); e != nil {
				return fmt.Errorf("写入系统环境变量失败: %w", e)
			}
		}
		if prev != "" && scopeOf(prev) != "system" {
			_ = sysUnregisterPath(binOf(prev))
		}
	}
	return writeActiveMeta(rt, version)
}

// UnsetActive 取消某版本的"环境变量指向"状态：直接把该版本 bin 目录从系统 PATH 注销
// （不依赖 activeVersion 元数据，避免元数据漂移导致注销失败、PATH 中残留旧版本 bin）。
// 随后清空 active 元数据。系统 PATH 上的版本不写入过 PATH，无需注销。
func (m *Manager) UnsetActive(rt Runtime, version string) error {
	if _, err := m.adapter(rt); err != nil {
		return err
	}
	scope := m.scopeFor(rt, version)
	if scope != "system" {
		_ = sysUnregisterPath(m.exeDirFor(rt, version))
	}
	return writeActiveMeta(rt, "")
}

// SetVersionMeta 更新某版本的别名与备注
func (m *Manager) SetVersionMeta(rt Runtime, version, alias, note string) error {
	if _, err := m.adapter(rt); err != nil {
		return err
	}
	return SetVersionMeta(rt, version, alias, note)
}

// DeleteVersion 删除某已安装版本：便携目录直接删除；导入的外部目录仅移除登记（不删用户文件）。
// 系统 PATH 上的版本无法在此删除。若删除的是当前激活的便携/导入版本，会先将其 bin 目录从系统 PATH 注销。
func (m *Manager) DeleteVersion(rt Runtime, version string, removeData bool) error {
	if err := validateVersion(version); err != nil {
		return err
	}
	if dir, ok := m.linkedDir(rt, version); ok {
		// 导入版本：仅移除登记，保留外部目录
		m.removeLink(rt, version)
		if activeVersion(rt) == version {
			_ = sysUnregisterPath(dir)
			writeActiveMeta(rt, "")
		}
		return ClearVersionMeta(rt, version)
	}
	a, err := m.adapter(rt)
	if err != nil {
		return err
	}
	if activeVersion(rt) == version {
		scope := ""
		for _, ins := range a.InstalledVersions() {
			if ins.Version == version {
				scope = ins.Scope
				break
			}
		}
		if scope == "portable" {
			_ = sysUnregisterPath(filepath.Dir(a.ExeFor(version)))
		}
	}
	// 选择性清理数据目录（仅当用户勾选 removeData，且运行时实现了 DataDirProvider）
	if removeData {
		if dd, ok := a.(DataDirProvider); ok {
			if dir := dd.DataDir(version); dir != "" {
				if rerr := os.RemoveAll(dir); rerr != nil {
					logger.W("[env] 删除数据目录失败 %s: %v", dir, rerr)
				} else {
					logger.I("[env] 已删除数据目录 %s", dir)
				}
			}
		}
	}
	if err := a.DeleteVersion(version); err != nil {
		return err
	}
	if err := ClearVersionMeta(rt, version); err != nil {
		return err
	}
	// 删除后重扫并重新保存检测结果，缓存与磁盘一致（避免已删版本从缓存复活）
	m.RefreshDetected(rt)
	return nil
}

// removeLink 从 links 中移除某运行时的某导入版本并持久化。
func (m *Manager) removeLink(rt Runtime, version string) {
	m.linksMu.Lock()
	defer m.linksMu.Unlock()
	entries := m.links[rt]
	// 新建切片而非原地过滤（entries[:0]）：原地过滤会写 entries 的底层数组，
	// 而该数组可能正被持读锁的读者遍历，造成读到撕裂数据。
	out := make([]linkEntry, 0, len(entries))
	for _, l := range entries {
		if l.Version != version {
			out = append(out, l)
		}
	}
	m.links[rt] = out
	// 已持写锁，走 Locked 版本：RWMutex 不可重入，此处再取读锁会永久死锁。
	_ = m.saveLinksLocked()
	m.syncPHPLinksLocked()
}

// ImportVersion 导入一个已存在的外部安装目录：探测其版本号后登记为 linked 安装，
// 使其出现在环境管理的版本列表中（可查看、可切换环境变量），但不复制/删除用户文件。
func (m *Manager) ImportVersion(rt Runtime, dir string) (string, error) {
	if _, err := m.adapter(rt); err != nil {
		return "", err
	}
	dir = strings.TrimSpace(dir)
	if err := validateImportDir(dir); err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("目录不存在: %s", dir)
	}
	exeName := m.importExeName(rt)
	if exeName == "" {
		return "", fmt.Errorf("不支持导入该运行时: %s", rt)
	}
	// 在目录及其常见子目录中寻找可执行文件
	candidates := []string{
		filepath.Join(dir, exeName),
		filepath.Join(dir, "bin", exeName),
	}
	exePath := ""
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			exePath = c
			break
		}
	}
	if exePath == "" {
		return "", fmt.Errorf("在 %s 下未找到 %s", dir, exeName)
	}
	version, err := m.detectVersion(rt, exePath)
	if err != nil {
		return "", err
	}
	if version == "" {
		return "", fmt.Errorf("无法识别 %s 的版本号", exeName)
	}
	m.linksMu.Lock()
	// 去重：同版本已登记则跳过（避免重复列示与激活歧义），仅更新目录
	dup := false
	for i, l := range m.links[rt] {
		if l.Version == version {
			m.links[rt][i].Dir = dir
			dup = true
			break
		}
	}
	if !dup {
		m.links[rt] = append(m.links[rt], linkEntry{Version: version, Dir: dir})
	}
	m.linksMu.Unlock()
	if err := m.saveLinks(); err != nil {
		return "", err
	}
	// 导入后同步给 PHP 适配器，使其可被 php-fpm 启停
	m.syncPHPLinks()
	// 导入后触发一次真实检测并重新保存（用户要求的触发点之一），使系统 PATH 上的同运行时
	// 版本、以及缓存与磁盘尽快对齐；导入的外部目录本身经 links.json 在 List 时合并展示。
	m.RefreshDetected(rt)
	return version, nil
}

// detectVersion 运行外部 exe 探测其版本号。版本探测参数与输出解析已下沉到 adapter
// （DetectArgs / ParseVersion），此处仅负责拉起进程并调用 adapter 的解析器，
// 不再维护一份「运行时→参数/解析」的巨型 switch。
func (m *Manager) detectVersion(rt Runtime, exe string) (string, error) {
	a, err := m.adapter(rt)
	if err != nil {
		return "", err
	}
	args := a.DetectArgs()
	if len(args) == 0 {
		return "", fmt.Errorf("不支持导入该运行时: %s", rt)
	}
	// Windows GUI 进程（正式版无控制台）拉起 console 子进程会闪出黑框，必须隐藏
	cmd := sysutil.Command(exe, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("执行版本探测失败: %w", err)
	}
	return a.ParseVersion(string(out))
}

// ---- 通用配置读写（适用于实现了 ConfigProvider 的 runtime）----
// 新增一个可编辑配置的 runtime 只需让它实现 ConfigProvider（声明配置文件路径），
// 无需再各自加一对 API 与前端弹窗。

// ConfigSupport 判断某 runtime 是否具备可编辑的配置文件。
func (m *Manager) ConfigSupport(rt Runtime) bool {
	a, err := m.adapter(rt)
	if err != nil {
		return false
	}
	_, ok := a.(ConfigProvider)
	return ok
}

// ConfigGet 读取某 runtime 某版本的配置文件。
func (m *Manager) ConfigGet(rt Runtime, version string) (*RuntimeConfig, error) {
	if err := validateVersion(version); err != nil {
		return nil, err
	}
	a, err := m.adapter(rt)
	if err != nil {
		return nil, err
	}
	p, ok := a.(ConfigProvider)
	if !ok {
		return nil, fmt.Errorf("%s 不支持配置编辑", DisplayName(rt))
	}
	return ReadConfig(p, version)
}

// ConfigSet 写回某 runtime 某版本的配置文件（整体覆盖）。
func (m *Manager) ConfigSet(rt Runtime, version, raw string) error {
	if err := validateVersion(version); err != nil {
		return err
	}
	a, err := m.adapter(rt)
	if err != nil {
		return err
	}
	p, ok := a.(ConfigProvider)
	if !ok {
		return fmt.Errorf("%s 不支持配置编辑", DisplayName(rt))
	}
	return WriteConfig(p, version, raw)
}

// PHPConfigGet 读取某已装 PHP 版本的配置（php.ini 正文、禁用函数、错误日志、扩展列表）。
func (m *Manager) PHPConfigGet(rt Runtime, version string) (*PHPConfig, error) {
	if rt != RuntimePHP {
		return nil, fmt.Errorf("仅 PHP 支持配置编辑")
	}
	dir := m.exeDirFor(rt, version)
	if dir == "" {
		return nil, fmt.Errorf("未找到 PHP 版本: %s", version)
	}
	return readPHPConfig(dir)
}

// PHPConfigSet 写回某已装 PHP 版本的配置（指定字段；Raw 非空时整体覆盖 php.ini）。
func (m *Manager) PHPConfigSet(rt Runtime, version string, patch PHPConfigPatch) error {
	if rt != RuntimePHP {
		return fmt.Errorf("仅 PHP 支持配置编辑")
	}
	dir := m.exeDirFor(rt, version)
	if dir == "" {
		return fmt.Errorf("未找到 PHP 版本: %s", version)
	}
	return writePHPConfig(dir, patch)
}

// RedisConfigGet / RedisConfigSet 已废弃：Redis 实现了通用 ConfigProvider，
// 统一走 Manager.ConfigGet / ConfigSet（EnvConfigGet / EnvConfigSet），无需特化 API。

// LogProvider 可选能力：运行时可将自身运行日志（如 redis.log）提供给前端查询。
// 实现后即通过通用 Manager.LogGet 暴露，无需为每个运行时单独加一对 API。
type LogProvider interface {
	// LogGet 读取某版本的运行日志（尾部内容），用于前端日志弹窗查询。
	LogGet(version string) (string, error)
}

// LogGet 读取某运行时某版本的运行日志（仅实现了 LogProvider 的运行时可用）。
func (m *Manager) LogGet(rt Runtime, version string) (string, error) {
	a, err := m.adapter(rt)
	if err != nil {
		return "", err
	}
	p, ok := a.(LogProvider)
	if !ok {
		return "", fmt.Errorf("%s 不支持日志查询", DisplayName(rt))
	}
	return p.LogGet(version)
}

// WebConsoleProvider 可选能力：返回该运行时 Web 管理后台端口（0=无）。
// 实现后通过 RuntimeInfo.WebConsolePort 暴露给前端，运行时运行中且端口已知时显示「打开控制台」按钮。
type WebConsoleProvider interface {
	WebConsolePort(version string) int
}

// readLogTail 读取日志文件尾部最多 8KB，供各运行时实现 LogProvider 时复用（避免重复样板）。
func readLogTail(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) > 8192 {
		data = data[len(data)-8192:]
	}
	return string(data), nil
}

// ConfigValidator 可选能力：启动前对配置文件做语法校验（如 nginx -t / apache -t / caddy validate）。
// 实现后由 Manager.Start 在拉起服务前统一调用，校验失败则拦下并提示校验输出（含错误行号/原因）。
type ConfigValidator interface {
	// ValidateConfig 校验指定版本的配置，合法返回 nil，否则返回可读性错误（含出错位置/原因）。
	ValidateConfig(version string) error
}

// DataDirProvider 可选能力：返回指定版本的数据目录（如 PostgreSQL/MongoDB 的 data 目录）。
// 实现后，卸载版本时若用户勾选「同时删除数据」，Manager.DeleteVersion 会一并清理该目录。
type DataDirProvider interface {
	DataDir(version string) string
}

// PathEntry 描述某运行时当前激活版本在系统 PATH 中的状态（供 PATH 可视化面板展示）。
type PathEntry struct {
	Runtime string `json:"runtime"` // 运行时 id
	Version string `json:"version"` // 激活版本
	BinDir  string `json:"binDir"`  // 该版本 bin 目录（即写入 PATH 的条目）
	InPath  bool   `json:"inPath"`  // 该 bin 目录是否真实出现在系统 PATH 中
}

// PathInfo 返回所有已设置激活版本的运行时，其 bin 目录及是否真正注册进系统 PATH。
func (m *Manager) PathInfo() []PathEntry {
	out := make([]PathEntry, 0, len(runtimeOrder))
	for _, rt := range runtimeOrder {
		v := activeVersion(rt)
		if v == "" {
			continue
		}
		dir := m.exeDirFor(rt, v)
		inPath := false
		if a, err := m.adapter(rt); err == nil {
			for _, ins := range a.InstalledVersions() {
				if ins.Version == v && ins.InSystemPath {
					inPath = true
					break
				}
			}
		}
		out = append(out, PathEntry{Runtime: string(rt), Version: v, BinDir: dir, InPath: inPath})
	}
	return out
}

// Start 启动某运行时的服务（仅 nginx/redis 支持）。
func (m *Manager) Start(rt Runtime, version string, onLog func(string)) error {
	if err := validateVersion(version); err != nil {
		return err
	}
	logger.I("[env] Manager.Start rt=%s version=%s", rt, version)
	a, err := m.adapter(rt)
	if err != nil {
		return err
	}
	sc, ok := a.(ServiceController)
	if !ok {
		return fmt.Errorf("该运行时不支持服务管理")
	}
	// 启动前端口冲突检测：默认服务端口被「外部程序」占用时提前拦截，避免 bind 失败被误判 running。
	// 本会话已拉起（Ours=true）的单例场景交由运行时内部的多版本互斥逻辑处理，这里不拦截。
	if pc, perr := m.PortConflict(rt, version); perr == nil && pc.Occupied && !pc.Ours {
		who := pc.Image
		switch {
		case pc.PID != 0:
			who = fmt.Sprintf("PID %d · %s", pc.PID, pc.Image)
		case who == "":
			who = "未知程序"
		}
		return fmt.Errorf("端口 %d 已被占用（%s），无法启动 %s，请先释放该端口", pc.Port, who, a.DisplayName())
	}
	// 启动前配置校验（如 nginx -t / apache -t / caddy validate）；不通过则拦下并提示校验输出。
	if v, ok := a.(ConfigValidator); ok {
		if cerr := v.ValidateConfig(version); cerr != nil {
			return fmt.Errorf("配置校验未通过: %w", cerr)
		}
	}
	return sc.Start(context.Background(), version, onLog)
}

// Stop 停止某运行时的服务。
func (m *Manager) Stop(rt Runtime, version string) error {
	logger.I("[env] Manager.Stop rt=%s version=%s", rt, version)
	a, err := m.adapter(rt)
	if err != nil {
		return err
	}
	sc, ok := a.(ServiceController)
	if !ok {
		return fmt.Errorf("该运行时不支持服务管理")
	}
	return sc.Stop(version)
}

// Restart 重启某运行时的服务：先停止（忽略停止错误——可能本就未运行），再复用 Start 的端口冲突与配置校验重新拉起。
func (m *Manager) Restart(rt Runtime, version string, onLog func(string)) error {
	if err := validateVersion(version); err != nil {
		return err
	}
	logger.I("[env] Manager.Restart rt=%s version=%s", rt, version)
	a, err := m.adapter(rt)
	if err != nil {
		return err
	}
	if _, ok := a.(ServiceController); !ok {
		return fmt.Errorf("该运行时不支持服务管理")
	}
	// 先停（忽略错误：未运行时 Stop 可能报“未在运行”，不应阻断重启）
	_ = m.Stop(rt, version)
	// 端口冲突检测与配置校验交由 Start 统一处理
	return m.Start(rt, version, onLog)
}

// Status 查询某运行时服务状态。非服务类运行时返回 running=false。
func (m *Manager) Status(rt Runtime, version string) (ServiceStatus, error) {
	a, err := m.adapter(rt)
	if err != nil {
		return ServiceStatus{}, err
	}
	sc, ok := a.(ServiceController)
	if !ok {
		return ServiceStatus{}, nil
	}
	return sc.Status(version), nil
}

// GitStatus 返回当前 Git 环境的综合状态（版本/路径/SSH/Git LFS），供环境管理页状态表展示。
func (m *Manager) GitStatus() GitStatusInfo {
	if a, err := m.adapter(RuntimeGit); err == nil {
		if g, ok := a.(*GitRuntime); ok {
			return g.Status()
		}
	}
	return GitStatusInfo{}
}

// PortConflict 查询某运行时默认服务端口是否被「其它程序」占用（启动前可视化提示用）。
// 仅服务类运行时有意义；非服务类返回零值。Occupied=true 且 Ours=false 表示端口被外部/孤儿进程占据，
// 此时直接启动会因 bind 失败而误判 running，前端应给出明确冲突警告。
type PortConflict struct {
	Occupied bool   `json:"occupied"` // 端口是否被占用
	Port     int    `json:"port"`     // 被探测的端口
	PID      int    `json:"pid"`      // 占用者 PID（0=未知）
	Image    string `json:"image"`    // 占用者可执行文件路径
	Ours     bool   `json:"ours"`     // 占用者是否就是本会话拉起的该运行时（此时不算冲突）
}

// EnableRabbitMQManagement 针对运行中的 RabbitMQ 节点启用管理后台插件（rabbitmq_management，端口 15672）。
func (m *Manager) EnableRabbitMQManagement(version string, onLog func(string)) error {
	a, ok := m.adapters[RuntimeRabbitMQ]
	if !ok {
		return fmt.Errorf("RabbitMQ 运行时未注册")
	}
	r, ok := a.(*RabbitMQRuntime)
	if !ok {
		return fmt.Errorf("RabbitMQ 运行时类型异常")
	}
	return r.EnableManagementPlugin(version, onLog)
}

// DisableRabbitMQManagement 关闭 RabbitMQ 管理后台插件（rabbitmq_management，端口 15672）。
func (m *Manager) DisableRabbitMQManagement(version string, onLog func(string)) error {
	a, ok := m.adapters[RuntimeRabbitMQ]
	if !ok {
		return fmt.Errorf("RabbitMQ 运行时未注册")
	}
	r, ok := a.(*RabbitMQRuntime)
	if !ok {
		return fmt.Errorf("RabbitMQ 运行时类型异常")
	}
	return r.DisableManagementPlugin(version, onLog)
}

// IsRabbitMQManagementEnabled 返回 RabbitMQ 管理后台是否已启用。
func (m *Manager) IsRabbitMQManagementEnabled(version string) bool {
	a, ok := m.adapters[RuntimeRabbitMQ]
	if !ok {
		return false
	}
	r, ok := a.(*RabbitMQRuntime)
	if !ok {
		return false
	}
	enabled, _ := r.IsManagementEnabled(version)
	return enabled
}

func (m *Manager) PortConflict(rt Runtime, version string) (PortConflict, error) {
	a, err := m.adapter(rt)
	if err != nil {
		return PortConflict{}, err
	}
	sc, ok := a.(ServiceController)
	if !ok {
		return PortConflict{}, nil
	}
	port := sc.DefaultPort()
	pc := PortConflict{Port: port}
	if !isPortOpen(port) {
		return pc, nil
	}
	pc.Occupied = true
	if v, _ := svcMgr.info(rt); v != "" {
		pc.Ours = true
	}
	if pid := findListenPID(port); pid != 0 {
		pc.PID = pid
		pc.Image = processExePath(pid)
	}
	return pc, nil
}
