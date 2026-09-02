package env

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"quickdock/internal/platform"
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
	ActiveSource string       `json:"activeSource"`
	HasService   bool         `json:"hasService"` // 是否支持服务启停/状态监听
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
}

func NewManager() *Manager {
	m := &Manager{
		adapters: map[Runtime]RuntimeAdapter{
			RuntimeNode:  NewNodeRuntime(),
			RuntimePHP:   NewPHPRuntime(),
			RuntimeGo:    NewGoRuntime(),
			RuntimeRedis: NewRedisRuntime(),
			RuntimeNginx: NewNginxRuntime(),
			RuntimeGit:   NewGitRuntime(),
		},
		links: map[Runtime][]linkEntry{},
	}
	m.loadLinks()
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

func (m *Manager) saveLinks() error {
	m.linksMu.RLock()
	tmp := m.links
	m.linksMu.RUnlock()
	data, err := json.MarshalIndent(tmp, "", "  ")
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
func importExeName(rt Runtime) string {
	switch rt {
	case RuntimeNode:
		return "node.exe"
	case RuntimePHP:
		return "php.exe"
	case RuntimeGo:
		return "go.exe"
	case RuntimeRedis:
		return "redis-server.exe"
	case RuntimeNginx:
		return "nginx.exe"
	case RuntimeGit:
		return "git.exe"
	}
	return ""
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
	lk := m.links[rt]
	m.linksMu.RUnlock()
	for _, l := range lk {
		installs = append(installs, Install{Version: l.Version, Scope: "linked", Path: l.Dir})
	}
	return installs
}

func (m *Manager) adapter(rt Runtime) (RuntimeAdapter, error) {
	a, ok := m.adapters[rt]
	if !ok {
		return nil, fmt.Errorf("不支持的运行时: %s", rt)
	}
	return a, nil
}

// List 返回所有运行时概览（顺序：Node → PHP → Go → Redis → Nginx → Git）
func (m *Manager) List() []RuntimeInfo {
	order := []Runtime{RuntimeNode, RuntimePHP, RuntimeGo, RuntimeRedis, RuntimeNginx, RuntimeGit}
	var out []RuntimeInfo
	for _, rt := range order {
		a := m.adapters[rt]
		var si []SourceInfo
		for _, s := range ListSources(rt) {
			si = append(si, SourceInfo{ID: s.ID, Name: s.Name})
		}
		_, hasSvc := a.(ServiceController)
		installed := m.mergeLinks(rt, a.InstalledVersions())
		out = append(out, RuntimeInfo{
			ID:           string(rt),
			Name:         a.DisplayName(),
			Group:        registry[rt].group,
			Platforms:    orEmpty(a.SupportedPlatforms()),
			Recommended:  orEmpty(a.Recommended()),
			Installed:    orEmpty(mergeMeta(rt, installed)),
			Sources:      orEmpty(si),
			ActiveSource: ActiveSource(rt),
			HasService:   hasSvc,
		})
	}
	return out
}

// mergeMeta 将用户元数据（激活版本/别名/备注）合并进已安装版本列表，
// 并计算每个版本是否真正注册进系统 PATH（InSystemPath）。
func mergeMeta(rt Runtime, installs []Install) []Install {
	active := activeVersion(rt)
	pathVal, _ := sysReadPath()
	entries := splitSystemPath(pathVal)
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
	return a.Install(context.Background(), version, cb)
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
func (m *Manager) InstalledVersions(rt Runtime) ([]Install, error) {
	a, err := m.adapter(rt)
	if err != nil {
		return nil, err
	}
	return mergeMeta(rt, m.mergeLinks(rt, a.InstalledVersions())), nil
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
	return SetActive(rt, version)
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
	return SetActive(rt, "")
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
func (m *Manager) DeleteVersion(rt Runtime, version string) error {
	if dir, ok := m.linkedDir(rt, version); ok {
		// 导入版本：仅移除登记，保留外部目录
		m.removeLink(rt, version)
		if activeVersion(rt) == version {
			_ = sysUnregisterPath(dir)
			SetActive(rt, "")
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
	if err := a.DeleteVersion(version); err != nil {
		return err
	}
	return ClearVersionMeta(rt, version)
}

// removeLink 从 links 中移除某运行时的某导入版本并持久化。
func (m *Manager) removeLink(rt Runtime, version string) {
	m.linksMu.Lock()
	defer m.linksMu.Unlock()
	entries := m.links[rt]
	out := entries[:0]
	for _, l := range entries {
		if l.Version != version {
			out = append(out, l)
		}
	}
	m.links[rt] = out
	_ = m.saveLinks()
}

// ImportVersion 导入一个已存在的外部安装目录：探测其版本号后登记为 linked 安装，
// 使其出现在环境管理的版本列表中（可查看、可切换环境变量），但不复制/删除用户文件。
func (m *Manager) ImportVersion(rt Runtime, dir string) (string, error) {
	if _, err := m.adapter(rt); err != nil {
		return "", err
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("目录为空")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("目录不存在: %s", dir)
	}
	exeName := importExeName(rt)
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
	version, err := detectVersion(rt, exePath)
	if err != nil {
		return "", err
	}
	if version == "" {
		return "", fmt.Errorf("无法识别 %s 的版本号", exeName)
	}
	m.linksMu.Lock()
	m.links[rt] = append(m.links[rt], linkEntry{Version: version, Dir: dir})
	m.linksMu.Unlock()
	if err := m.saveLinks(); err != nil {
		return "", err
	}
	return version, nil
}

// detectVersion 运行外部 exe 探测其版本号（不同运行时命令/输出格式不同）。
func detectVersion(rt Runtime, exe string) (string, error) {
	var out []byte
	var err error
	switch rt {
	case RuntimeNode:
		out, err = exec.Command(exe, "-v").Output()
	case RuntimePHP:
		out, err = exec.Command(exe, "-v").Output()
	case RuntimeGo:
		out, err = exec.Command(exe, "version").Output()
	case RuntimeRedis:
		out, err = exec.Command(exe, "--version").Output()
	case RuntimeNginx:
		out, err = exec.Command(exe, "-v").Output()
	case RuntimeGit:
		out, err = exec.Command(exe, "--version").Output()
	default:
		return "", fmt.Errorf("不支持的运行时")
	}
	if err != nil {
		return "", fmt.Errorf("执行版本探测失败: %w", err)
	}
	s := string(out)
	switch rt {
	case RuntimePHP:
		return parsePHPVersion(s), nil
	case RuntimeRedis:
		return parseRedisVersion(s), nil
	case RuntimeGo:
		// "go version go1.23.4 windows/amd64"
		if i := strings.Index(s, "go version go"); i >= 0 {
			rest := s[i+len("go version go"):]
			if sp := strings.IndexByte(rest, ' '); sp > 0 {
				return rest[:sp], nil
			}
			return strings.TrimSpace(rest), nil
		}
	case RuntimeNginx:
		// "nginx version: nginx/1.27.5"
		if i := strings.Index(s, "nginx/"); i >= 0 {
			return strings.TrimSpace(s[i+len("nginx/"):]), nil
		}
	case RuntimeGit:
		// "git version 2.45.0.windows.1"
		if i := strings.Index(s, "git version "); i >= 0 {
			return strings.TrimSpace(s[i+len("git version "):]), nil
		}
	case RuntimeNode:
		// "v22.22.2"
		return strings.TrimSpace(strings.TrimPrefix(s, "v")), nil
	}
	return "", nil
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

// Start 启动某运行时的服务（仅 nginx/redis 支持）。
func (m *Manager) Start(rt Runtime, version string, onLog func(string)) error {
	a, err := m.adapter(rt)
	if err != nil {
		return err
	}
	sc, ok := a.(ServiceController)
	if !ok {
		return fmt.Errorf("该运行时不支持服务管理")
	}
	return sc.Start(context.Background(), version, onLog)
}

// Stop 停止某运行时的服务。
func (m *Manager) Stop(rt Runtime, version string) error {
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
