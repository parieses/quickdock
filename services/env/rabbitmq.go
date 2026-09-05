package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"quickdock/internal/platform"
)

const rabbitmqBaseRel = "runtime/rabbitmq"
const rabbitmqPort = 5672

// RabbitMQRuntime 管理便携 RabbitMQ 消息代理（rabbitmq/rabbitmq-server）。
// RabbitMQ 依赖 Erlang 运行时——安装时若未检测到任何可用 Erlang（QuickDock 管理的 Erlang 运行时 /
// 系统 ERLANG_HOME / PATH 上的 erl.exe），会自动安装 QuickDock 管理的 Erlang（otp_win64_*.zip，
// 解压即用、不写注册表、无需管理员权限）。启动前通过 ERLANG_HOME 注入，保持零配置便携。
// 服务型，通过 sbin/rabbitmq-server.bat 拉起（cmd /c）；默认端口 5672。
//
// 检测用 rabbitmqctl.bat（ExeFor 指向它，配合 DetectArgs=["version"]）；
// 启动用同目录的 rabbitmq-server.bat（Start 内部拼路径，不受 ExeFor 影响）。
type RabbitMQRuntime struct {
	baseDir string
}

// rabbitMqErlang 把 RabbitMQ 版本映射到兼容的 Erlang/OTP 便携构建版本。
// 依据 RabbitMQ 官方兼容矩阵：4.x 系列需 Erlang 27.x；3.13.x 系列需 Erlang 26.2.x。
var rabbitMqErlang = map[string]string{
	"4.3.5":  "27.3.4",
	"4.2.0":  "27.3.4",
	"3.13.7": "26.2.5",
}

func NewRabbitMQRuntime() *RabbitMQRuntime {
	return &RabbitMQRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), rabbitmqBaseRel)}
}

func (r *RabbitMQRuntime) Kind() Runtime                 { return RuntimeRabbitMQ }
func (r *RabbitMQRuntime) DetectArgs() []string          { return []string{"version"} }
func (r *RabbitMQRuntime) ParseVersion(out string) (string, error) {
	// `rabbitmqctl version` 输出即 "3.13.7"
	for _, tok := range strings.Fields(out) {
		if v := strings.TrimPrefix(tok, "v"); v != "" && strings.Contains(v, ".") {
			return v, nil
		}
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeRabbitMQ))
}
func (r *RabbitMQRuntime) DisplayName() string          { return DisplayName(RuntimeRabbitMQ) }
func (r *RabbitMQRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (r *RabbitMQRuntime) Recommended() []string        { return Versions(RuntimeRabbitMQ) }

func (r *RabbitMQRuntime) versionDir(version string) string { return filepath.Join(r.baseDir, version) }

// ExeFor 指向 rabbitmqctl.bat（版本检测用）；启动用的 rabbitmq-server.bat 由 Start 自行拼接。
func (r *RabbitMQRuntime) ExeFor(version string) string {
	return filepath.Join(r.versionDir(version), "sbin", "rabbitmqctl.bat")
}

func (r *RabbitMQRuntime) serverBat(version string) string {
	return filepath.Join(r.versionDir(version), "sbin", "rabbitmq-server.bat")
}

// erlangVersion 返回该 RabbitMQ 版本需搭配的 Erlang 便携构建版本号（用于自动安装）。
func (r *RabbitMQRuntime) erlangVersion(version string) string {
	if v, ok := rabbitMqErlang[version]; ok {
		return v
	}
	return "27.3.4" // 未知版本默认按 4.x 处理
}

// erlangMajor 返回该 RabbitMQ 版本所需 Erlang 的主版本号（依据 rabbitMqErlang 映射的精确版本）。
// 例如 4.3.5 -> 27.3.4 -> 27；3.13.7 -> 26.2.5 -> 26。RabbitMQ 只接受落在官方支持区间内的主版本。
func (r *RabbitMQRuntime) erlangMajor(version string) int {
	return parseMajor(r.erlangVersion(version))
}

// findErlangHomeFor 定位与指定 RabbitMQ 版本「兼容」的 Erlang OTP 根目录。
// 与 findErlangHome()（只看是否存在）不同，本函数要求版本落在 RabbitMQ 的官方支持区间内——
// 否则即便找到了 erl.exe 也会在 BOOT 阶段因 feature flags 不兼容而崩溃（如 Erlang 29 跑 RabbitMQ 4.3.5）。
// 按优先级：① QuickDock 已装的精确映射版本（rabbitMqErlang）② 同主版本任意已装 Erlang
// ③ 系统 ERLANG_HOME 且主版本匹配 ④ PATH 上主版本匹配的 erl.exe。皆无则返回空。
func findErlangHomeFor(rabbitVersion string) string {
	ev, ok := rabbitMqErlang[rabbitVersion]
	if !ok {
		ev = "27.3.4"
	}
	major := parseMajor(ev)
	if major == 0 {
		major = 27
	}
	er := NewErlangRuntime()
	if r := er.FindRoot(ev); r != "" {
		return r
	}
	if r := er.FindRootByMajor(major); r != "" {
		return r
	}
	if home := os.Getenv("ERLANG_HOME"); home != "" {
		if fileExists(filepath.Join(home, "bin", "erl.exe")) && parseMajor(filepath.Base(home)) == major {
			return home
		}
	}
	if p, err := exec.LookPath("erl.exe"); err == nil && p != "" {
		root := filepath.Dir(filepath.Dir(p))
		if fileExists(filepath.Join(root, "bin", "erl.exe")) && parseMajor(filepath.Base(root)) == major {
			return root
		}
	}
	return ""
}

func (r *RabbitMQRuntime) InstalledVersions() []Install {
	var out []Install
	entries, err := os.ReadDir(r.baseDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !fileExists(r.serverBat(e.Name())) {
			continue
		}
		out = append(out, Install{Version: e.Name(), Scope: "portable", Path: r.versionDir(e.Name())})
	}
	return out
}

func (r *RabbitMQRuntime) DeleteVersion(version string) error {
	dir := r.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (r *RabbitMQRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeRabbitMQ)[0]
	}
	dir := r.versionDir(version)
	server := r.serverBat(version)

	if fileExists(server) && findErlangHomeFor(version) != "" {
		if cb.OnLog != nil {
			cb.OnLog("RabbitMQ " + version + " 已安装且兼容的 Erlang 已就绪")
		}
		return nil
	}
	// 仅当 RabbitMQ 主体缺失时才整体清理，避免误删已下好的数据。
	if !fileExists(server) && fileExists(dir) {
		os.RemoveAll(dir)
	}

	// 1) RabbitMQ 主体（Windows 便携 zip）
	if !fileExists(server) {
		urls := CandidateURLs(RuntimeRabbitMQ, version)
		if len(urls) == 0 {
			return fmt.Errorf("无可用 RabbitMQ 下载源")
		}
		zipPath := filepath.Join(os.TempDir(), "quickdock-rabbitmq-"+version+".zip")
		if cb.OnStage != nil {
			cb.OnStage("download", "正在下载 RabbitMQ "+version+"…")
		}
		if cb.OnLog != nil {
			cb.OnLog("正在下载 RabbitMQ " + version + "…")
		}
		if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
			return fmt.Errorf("下载 RabbitMQ 失败: %w", err)
		}
		defer os.Remove(zipPath)
		if cb.OnStage != nil {
			cb.OnStage("extract", "正在解压 RabbitMQ…")
		}
		if cb.OnLog != nil {
			cb.OnLog("解压 RabbitMQ 到 " + dir)
		}
		if err := Extract(zipPath, dir); err != nil {
			return fmt.Errorf("解压 RabbitMQ 失败: %w", err)
		}
		if !fileExists(server) {
			return fmt.Errorf("解压完成但未找到 %s", server)
		}
		if cb.OnLog != nil {
			cb.OnLog("RabbitMQ " + version + " 主体解压完成")
		}
	}

	// 2) 确保 Erlang「兼容」版本就绪：优先复用已存在的（QuickDock 管理的 / 系统 ERLANG_HOME / PATH），
	//    版本须落在 RabbitMQ 官方支持区间内（如 4.3.5 需 Erlang 27.x）；否则自动安装 QuickDock
	//    管理的 Erlang（按 rabbitMqErlang 映射挑选精确版本）。注意：不能只看"有没有 Erlang"，
	//    装了 Erlang 29 之类的越界版本同样会启动失败，必须按兼容矩阵匹配。
	ev := r.erlangVersion(version)
	er := NewErlangRuntime()
	if findErlangHomeFor(version) == "" {
		if cb.OnLog != nil {
			if any := findErlangHome(); any != "" {
				cb.OnLog("已检测到 Erlang（" + filepath.Base(any) + "），但与 RabbitMQ " + version + " 不兼容（需要 Erlang " + ev + "），正在安装匹配版本…")
			} else {
				cb.OnLog("未检测到 Erlang，正在自动安装 QuickDock 管理的 Erlang " + ev + "…")
			}
		}
		if err := er.Install(ctx, ev, cb); err != nil {
			return fmt.Errorf("自动安装 Erlang 失败: %w", err)
		}
	} else {
		if cb.OnLog != nil {
			cb.OnLog("已检测到兼容的 Erlang，跳过自动安装")
		}
	}

	if cb.OnLog != nil {
		cb.OnLog("RabbitMQ " + version + " 安装完成")
	}
	return nil
}

// rabbitmqMgmtPlugin 管理后台插件名，启用后监听 15672（Web 管理界面）。
const rabbitmqMgmtPlugin = "rabbitmq_management"

// EnableManagementPlugin 启用管理后台插件（rabbitmq_management，端口 15672）。
// 节点运行中则通过 rabbitmq-plugins.bat enable 在线启用（15672 立即可用，无需重启）；
// 无论节点是否运行，都会把 rabbitmq_management 写入 etc/rabbitmq/enabled_plugins，
// 使下次启动自动加载管理后台（离线场景也能生效）。
func (r *RabbitMQRuntime) EnableManagementPlugin(version string, onLog func(string)) error {
	if version == "" {
		if ins := r.InstalledVersions(); len(ins) > 0 {
			version = ins[0].Version
		}
	}
	if version == "" {
		return fmt.Errorf("请先安装 RabbitMQ")
	}
	erlRoot := findErlangHomeFor(version)
	pluginsBat := filepath.Join(r.versionDir(version), "sbin", "rabbitmq-plugins.bat")

	// 1) 节点运行中：在线启用（立即生效）
	if r.Status(version).Running && erlRoot != "" {
		if _, err := os.Stat(pluginsBat); err == nil {
			env := mergeEnv(os.Environ(), []string{
				"ERLANG_HOME=" + erlRoot,
				"PATH=" + filepath.Join(erlRoot, "bin") + ";" + os.Getenv("PATH"),
			})
			cmd := exec.Command("cmd.exe", "/c", "call", pluginsBat, "enable", rabbitmqMgmtPlugin)
			cmd.Dir = r.versionDir(version)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if onLog != nil {
				onLog(strings.TrimSpace(string(out)))
			}
			if err != nil {
				if onLog != nil {
					onLog("在线启用失败（" + err.Error() + "），将仅写入配置文件")
				}
			}
		} else if onLog != nil {
			onLog("未找到 " + pluginsBat)
		}
	} else if onLog != nil {
		onLog("节点未运行，已写入配置：重启 RabbitMQ 后自动启用 15672 管理后台")
	}

	// 2) 持久化写入 enabled_plugins（覆盖离线场景，保证重启后仍启用）
	if plugins, err := r.readEnabledPlugins(version); err == nil {
		if !containsStr(plugins, rabbitmqMgmtPlugin) {
			plugins = append(plugins, rabbitmqMgmtPlugin)
			if werr := r.writeEnabledPlugins(version, plugins); werr != nil && onLog != nil {
				onLog("写入 enabled_plugins 失败：" + werr.Error())
			}
		}
	} else {
		if werr := r.writeEnabledPlugins(version, []string{rabbitmqMgmtPlugin}); werr != nil && onLog != nil {
			onLog("写入 enabled_plugins 失败：" + werr.Error())
		}
	}
	return nil
}

// DisableManagementPlugin 关闭管理后台插件（rabbitmq_management）。
// 节点运行中则在线 disable（立即生效）；同时把 rabbitmq_management 从 etc/rabbitmq/enabled_plugins
// 移除，使下次启动不再自动加载 15672。
func (r *RabbitMQRuntime) DisableManagementPlugin(version string, onLog func(string)) error {
	if version == "" {
		if ins := r.InstalledVersions(); len(ins) > 0 {
			version = ins[0].Version
		}
	}
	if version == "" {
		return fmt.Errorf("请先安装 RabbitMQ")
	}
	erlRoot := findErlangHomeFor(version)
	pluginsBat := filepath.Join(r.versionDir(version), "sbin", "rabbitmq-plugins.bat")

	// 1) 节点运行中：在线禁用（立即生效）
	if r.Status(version).Running && erlRoot != "" {
		if _, err := os.Stat(pluginsBat); err == nil {
			env := mergeEnv(os.Environ(), []string{
				"ERLANG_HOME=" + erlRoot,
				"PATH=" + filepath.Join(erlRoot, "bin") + ";" + os.Getenv("PATH"),
			})
			cmd := exec.Command("cmd.exe", "/c", "call", pluginsBat, "disable", rabbitmqMgmtPlugin)
			cmd.Dir = r.versionDir(version)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if onLog != nil {
				onLog(strings.TrimSpace(string(out)))
			}
			if err != nil {
				if onLog != nil {
					onLog("在线禁用失败（" + err.Error() + "），将仅更新配置文件")
				}
			}
		}
	}

	// 2) 离线：从 enabled_plugins 移除 rabbitmq_management（依赖由节点回收），下次启动不再自动加载 15672
	plugins, _ := r.readEnabledPlugins(version)
	kept := plugins[:0]
	for _, p := range plugins {
		if p != rabbitmqMgmtPlugin {
			kept = append(kept, p)
		}
	}
	if err := r.writeEnabledPlugins(version, kept); err != nil {
		return fmt.Errorf("写入 enabled_plugins 失败: %w", err)
	}
	if onLog != nil {
		onLog("已禁用管理后台（15672）；下次启动将不再自动加载")
	}
	return nil
}

// IsManagementEnabled 返回管理后台插件（rabbitmq_management）是否已启用（离线配置层面）。
func (r *RabbitMQRuntime) IsManagementEnabled(version string) (bool, error) {
	plugins, err := r.readEnabledPlugins(version)
	if err != nil {
		return false, nil // 文件不存在视为未启用
	}
	return containsStr(plugins, rabbitmqMgmtPlugin), nil
}

// readEnabledPlugins 解析 etc/rabbitmq/enabled_plugins（Erlang 列表项，如 [a,b].），返回插件名切片。
func (r *RabbitMQRuntime) readEnabledPlugins(version string) ([]string, error) {
	ep := filepath.Join(r.versionDir(version), "etc", "rabbitmq", "enabled_plugins")
	data, err := os.ReadFile(ep)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(data))
	s = strings.TrimSuffix(s, ".")
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// writeEnabledPlugins 写回 etc/rabbitmq/enabled_plugins，格式与 RabbitMQ 原生一致：[a,b].
func (r *RabbitMQRuntime) writeEnabledPlugins(version string, plugins []string) error {
	ep := filepath.Join(r.versionDir(version), "etc", "rabbitmq", "enabled_plugins")
	dir := filepath.Dir(ep)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	quoted := make([]string, 0, len(plugins))
	for _, p := range plugins {
		quoted = append(quoted, `"`+p+`"`)
	}
	return os.WriteFile(ep, []byte("["+strings.Join(quoted, ",")+"].\n"), 0o644)
}

// containsStr 判断字符串切片是否包含目标（避免引入 slices 依赖）。
func containsStr(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

// ensureManagementPlugin 离线安全：在 etc/rabbitmq/enabled_plugins 写入 rabbitmq_management，
// 使 RabbitMQ 下次启动自动加载管理后台（端口 15672）。节点未运行时也能生效。
// 仅当文件不存在或不含 rabbitmq_management 时写入，避免覆盖用户已有的其它插件配置；
// 若文件已存在但缺管理插件，则提示用户在「操作」中点击「启用管理后台」（在线启用）。
func (r *RabbitMQRuntime) ensureManagementPlugin(version string, onLog func(string)) {
	ep := filepath.Join(r.versionDir(version), "etc", "rabbitmq", "enabled_plugins")
	if data, err := os.ReadFile(ep); err == nil && strings.Contains(string(data), rabbitmqMgmtPlugin) {
		return // 已启用
	}
	if _, err := os.Stat(ep); err == nil {
		if onLog != nil {
			onLog("提示：管理后台插件尚未启用，启动后可在「操作」中点击「启用管理后台」")
		}
		return
	}
	dir := filepath.Dir(ep)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if onLog != nil {
			onLog("警告：创建 etc/rabbitmq 失败：" + err.Error())
		}
		return
	}
	if err := os.WriteFile(ep, []byte("[rabbitmq_management].\n"), 0o644); err != nil {
		if onLog != nil {
			onLog("警告：写入 enabled_plugins 失败：" + err.Error())
		}
		return
	}
	if onLog != nil {
		onLog("已写入 enabled_plugins（rabbitmq_management），重启后自动启用 15672 管理后台")
	}
}

func (r *RabbitMQRuntime) DefaultPort() int { return rabbitmqPort }

// WebConsolePort 返回管理后台端口 15672（仅当管理插件启用时；否则无 Web 控制台）。
func (r *RabbitMQRuntime) WebConsolePort(version string) int {
	if enabled, _ := r.IsManagementEnabled(version); enabled {
		return 15672
	}
	return 0
}

func (r *RabbitMQRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := r.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 RabbitMQ 版本")
		}
		version = installs[0].Version
	}
	var wd string
	for _, ins := range installs {
		if ins.Version != version {
			continue
		}
		if ins.Scope == "system" {
			wd = filepath.Dir(ins.Path)
		} else {
			wd = r.versionDir(version)
		}
		break
	}
	server := r.serverBat(version)
	if _, err := os.Stat(server); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	// Erlang 必须「兼容」就绪（RabbitMQ 强依赖，且版本须落在官方支持区间）。
	// 例如装了 Erlang 29 虽能找到 erl.exe，但跑 RabbitMQ 4.3.5 会因 feature flags 不兼容而 BOOT 失败。
	erlRoot := findErlangHomeFor(version)
	if erlRoot == "" {
		ev := r.erlangVersion(version)
		if any := findErlangHome(); any != "" {
			return fmt.Errorf("已检测到 Erlang（%s），但与 RabbitMQ %s 不兼容（需要 Erlang %s 系列）。请在「语言」分组安装/切换到 Erlang %s，或将系统 ERLANG_HOME 指向该版本后再启动",
				filepath.Base(any), version, ev, ev)
		}
		return fmt.Errorf("RabbitMQ 依赖的 Erlang 尚未就绪：请先在「语言」分组安装 Erlang %s（或设置系统 ERLANG_HOME 指向该版本），再启动 RabbitMQ", ev)
	}
	running, _ := svcMgr.info(RuntimeRabbitMQ)
	if running != "" && running != version {
		return fmt.Errorf("RabbitMQ 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if running == "" && isPortOpen(rabbitmqPort) {
		if pid := findListenPID(rabbitmqPort); pid != 0 && !processImageMatches(pid, "beam.smp.exe") && !processImageMatches(pid, "erl.exe") {
			return fmt.Errorf("端口 %d 已被占用，无法启动 RabbitMQ", rabbitmqPort)
		}
	}
	// 确保管理后台插件（rabbitmq_management，端口 15672）已就绪：离线写入 enabled_plugins，
	// 使本次启动后管理界面可用。若节点已运行且需即时生效，用户可在「操作」中点击「启用管理后台」。
	r.ensureManagementPlugin(version, onLog)
	if onLog != nil {
		onLog("启动 RabbitMQ " + version + "（Erlang " + filepath.Base(erlRoot) + "）…")
	}
	// RabbitMQ 通过 bat 启动（Erlang VM 常驻），用 cmd /c 拉起。
	// 宿主不抢日志文件（RabbitMQ 自己写 var/log/rabbitmq/*.log），故 logPath 传空；
	// 通过环境变量注入 ERLANG_HOME 与 PATH，使其找到便携版 erl.exe。
	env := mergeEnv(os.Environ(), []string{
		"ERLANG_HOME=" + erlRoot,
		"PATH=" + filepath.Join(erlRoot, "bin") + ";" + os.Getenv("PATH"),
	})
	return svcMgr.startWithEnv(RuntimeRabbitMQ, version, "cmd.exe", wd,
		[]string{"/c", "call", server}, "", onLog, env)
}

func (r *RabbitMQRuntime) Stop(version string) error {
	// 占用 5672 的进程即 Erlang VM（beam.smp.exe/erl.exe），按端口强杀整棵树；
	// 空 expectImage 表示不限制镜像名（RabbitMQ 专用端口，直接杀占用者）。
	stopByPort(rabbitmqPort, "")
	svcMgr.forget(RuntimeRabbitMQ)
	return nil
}

func (r *RabbitMQRuntime) Status(version string) ServiceStatus {
	st := ServiceStatus{Running: false, Port: rabbitmqPort}
	if v, _ := svcMgr.info(RuntimeRabbitMQ); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeRabbitMQ)
		return st
	}
	if isPortOpen(rabbitmqPort) {
		st.Running = true
		st.Version = version
		if pid := findListenPID(rabbitmqPort); pid != 0 {
			st.PID = pid
		}
	}
	return st
}

// LogPath RabbitMQ 日志位于解压目录 var/log/rabbitmq/ 下（文件名含主机名），
// 此处返回目录，LogGet 内部定位首个 .log。
func (r *RabbitMQRuntime) LogPath(version string) string {
	return filepath.Join(r.versionDir(version), "var", "log", "rabbitmq")
}

// LogGet 读取 RabbitMQ 运行日志（实现 LogProvider），定位 var/log/rabbitmq 下首个 .log。
func (r *RabbitMQRuntime) LogGet(version string) (string, error) {
	dir := r.LogPath(version)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("暂无运行日志（启动后生成于 %s）", dir)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
			if rerr != nil {
				return "", rerr
			}
			if len(data) > 8192 {
				data = data[len(data)-8192:]
			}
			return strings.TrimSpace(string(data)), nil
		}
	}
	return "", fmt.Errorf("暂无运行日志（启动后生成于 %s）", dir)
}
