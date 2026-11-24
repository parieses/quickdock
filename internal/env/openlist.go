package env

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"quickdock/internal/credentials"
	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

const openlistBaseRel = "runtime/openlist"

// openlistDataSub 账号数据目录，相对 baseDir。放 exe 同级是因为 OpenList 默认把
// data/ 建在工作目录下：固定 cwd=baseDir 后，网关的 sqlite 库、配置、缩略图缓存
// 全在这一个目录里，卸载/升级都只影响 exe，不动用户已配好的网盘账号。
const openlistDataSub = "data"

// openlistDefaultPort OpenList 默认监听端口（config.json 里的 scheme.http_port）。
const openlistDefaultPort = 5244

// openlistCredSource 凭据域里标记「这条密码属于 OpenList 网关」的来源标识。
const openlistCredSource = "openlist"

// openlistVersionLineRe 优先匹配 `Version: v4.2.9` 这种明确标注的行。
//
// 为什么不能直接扫第一个 x.y.z：`openlist version` 的输出里先出现
// `Go Version: go1.27.3`，直接扫会把 Go 版本当成 OpenList 版本
// （实际踩过：界面显示 1.27.3，而装的是 4.2.9）。
var openlistVersionLineRe = regexp.MustCompile(`(?mi)^Version:\s*v?(\d+\.\d+\.\d+)`)

// openlistVerRe 兜底：输出里任何形如 x.y.z 的版本号。
var openlistVerRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// openlistVersionOf 从 `openlist version` 输出里取 OpenList 自身版本。
func openlistVersionOf(out string) string {
	if m := openlistVersionLineRe.FindStringSubmatch(out); len(m) > 1 {
		return m[1]
	}
	return openlistVerRe.FindString(out)
}

// OpenListRuntime 网盘聚合网关（AList 社区分支）。
//
// 它把百度/夸克等网盘统一转成标准 WebDAV + 网页后台：「网盘」页从 /dav 列目录与
// 下载，环境管理页负责安装/启停。QuickDock 不实现任何网盘的私有协议 —— 加新网盘
// 只需在 OpenList 后台加一个驱动。
//
// 单版本语义：装新版本原地覆盖 exe，data/ 目录（账号与配置）保留。
type OpenListRuntime struct {
	baseDir string
}

func NewOpenListRuntime() *OpenListRuntime {
	return &OpenListRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), openlistBaseRel)}
}

func (o *OpenListRuntime) Kind() Runtime         { return RuntimeOpenList }
func (o *OpenListRuntime) DisplayName() string   { return DisplayName(RuntimeOpenList) }
func (o *OpenListRuntime) Recommended() []string { return Versions(RuntimeOpenList) }
func (o *OpenListRuntime) SupportedPlatforms() []string {
	return []string{"windows", "darwin", "linux"}
}

// DetectArgs `openlist version` 打印版本号（cmd/version.go）。
func (o *OpenListRuntime) DetectArgs() []string { return []string{"version"} }

func (o *OpenListRuntime) ParseVersion(out string) (string, error) {
	if v := openlistVersionOf(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 OpenList 版本")
}

func (o *OpenListRuntime) exeName() string {
	if runtime.GOOS == "windows" {
		return "openlist.exe"
	}
	return "openlist"
}

// ExeFor 单版本语义：忽略 version，目录恒为 runtime/openlist。
func (o *OpenListRuntime) ExeFor(version string) string {
	return filepath.Join(o.baseDir, o.exeName())
}

// DataDir 实现 DataDirProvider：卸载时勾选「同时删除数据」才会清掉账号与配置。
func (o *OpenListRuntime) DataDir(version string) string {
	return filepath.Join(o.baseDir, openlistDataSub)
}

// ConfigPath 实现 ConfigProvider：OpenList 的端口/HTTPS/站点 URL 都在这里。
func (o *OpenListRuntime) ConfigPath(version string) string {
	return filepath.Join(o.DataDir(version), "config.json")
}

func (o *OpenListRuntime) LogPath(version string) string {
	return filepath.Join(o.baseDir, "openlist.log")
}

func (o *OpenListRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if _, err := os.Stat(o.ExeFor("")); err == nil {
		v := openlistVersionOf(RunVersion(o.ExeFor(""), "version"))
		if v == "" {
			v = "unknown"
			if b, e := os.ReadFile(filepath.Join(o.baseDir, "VERSION")); e == nil {
				if s := strings.TrimSpace(string(b)); s != "" {
					v = s
				}
			}
		}
		out = append(out, Install{Version: v, Scope: "portable", Path: o.baseDir})
		dirs.record(o.baseDir)
	}
	if p, err := exec.LookPath("openlist"); err == nil {
		if v := openlistVersionOf(RunVersion(p, "version")); v != "" {
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

// DeleteVersion 删掉 exe、VERSION 与运行日志，但**保留 data/**：
// 那里面是用户已经录好的百度/夸克账号与挂载配置，卸载网关不等于不要账号。
// 真要连数据一起删，由 Manager.DeleteVersion 的 removeData 走 DataDirProvider。
func (o *OpenListRuntime) DeleteVersion(version string) error {
	if _, err := os.Stat(o.ExeFor("")); err != nil {
		return fmt.Errorf("未安装 OpenList")
	}
	entries, err := os.ReadDir(o.baseDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == openlistDataSub {
			continue
		}
		if err := os.RemoveAll(filepath.Join(o.baseDir, e.Name())); err != nil {
			return fmt.Errorf("删除 %s 失败（可能服务正在运行，请先停止）: %w", e.Name(), err)
		}
	}
	return nil
}

// Install 下载官方 zip 并解压到 runtime/openlist。
//
// 解压先进暂存目录再搬入，避免解压中断把 exe 弄成半个；data/ 不参与搬运，
// 升级时用户账号因此原样保留。
func (o *OpenListRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeOpenList)[0]
	}
	urls := CandidateURLs(RuntimeOpenList, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 OpenList 下载源（当前平台暂未支持）")
	}
	if err := os.MkdirAll(o.baseDir, 0o755); err != nil {
		return err
	}
	arcPath := filepath.Join(o.baseDir, ".openlist-"+version+describeArchive())
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 OpenList "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 OpenList " + version + "…")
	}
	if err := Download(ctx, arcPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 OpenList 失败: %w", err)
	}
	defer os.Remove(arcPath)

	// 暂存区用 MkdirTemp 而非固定名：环境页与下载页可能同时发起安装，
	// 固定目录会被后到的调用 RemoveAll 清掉前一个还没搬完的内容。
	staging, err := os.MkdirTemp(filepath.Dir(o.baseDir), ".openlist-staging-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 OpenList…")
	}
	if err := Extract(ctx, arcPath, staging); err != nil {
		return fmt.Errorf("解压 OpenList 失败: %w", err)
	}
	// zip/tar.gz 的顶层可能是单层目录，Extract 已自动剥离；exe 应在暂存根目录。
	if _, err := os.Stat(filepath.Join(staging, o.exeName())); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", o.exeName())
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == openlistDataSub {
			continue // 发布包万一带 data/ 也不覆盖用户数据
		}
		src := filepath.Join(staging, e.Name())
		dst := filepath.Join(o.baseDir, e.Name())
		if err := o.replaceEntry(src, dst, cb.OnLog); err != nil {
			return fmt.Errorf("安装 OpenList 失败（%s 写入被拒绝，通常是网关仍在运行）: %w", e.Name(), err)
		}
	}
	_ = os.WriteFile(filepath.Join(o.baseDir, "VERSION"), []byte(version), 0o644)
	if cb.OnLog != nil {
		cb.OnLog("OpenList " + version + " 安装完成")
	}
	return nil
}

// replaceEntry 把暂存目录里的条目搬到安装目录，覆盖同名旧文件。
//
// 为什么不直接用 os.Rename：Windows 上有两个坑。
//  1. 旧 openlist.exe 还在跑（升级时它通常正是在跑）——覆盖一个已加载的映像
//     只会得到 ERROR_ACCESS_DENIED，Go 把它翻译成 "Access is denied."，
//     界面上就是一句完全指不出原因的话；
//  2. 刚解压出来的 exe 常被杀软实时扫描短暂独占，重命名会瞬时失败。
//
// 所以先试一次；失败就停掉运行中的网关，再把旧文件改名让位（运行中的 exe
// 允许改名，只是不允许被覆盖），最后带间隔重试。
func (o *OpenListRuntime) replaceEntry(src, dst string, onLog func(string)) error {
	err := os.Rename(src, dst)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	o.stopForReplace(onLog)

	aside := ""
	if _, statErr := os.Stat(dst); statErr == nil {
		cand := dst + ".old"
		_ = os.Remove(cand) // 上次升级残留（当时旧进程还占着，删不掉）
		if e := os.Rename(dst, cand); e == nil {
			aside = cand
		}
	}
	for i := 0; i < 20; i++ { // 20 × 150ms ≈ 3s，覆盖杀软扫描的常见占用窗口
		time.Sleep(150 * time.Millisecond)
		if e := os.Rename(src, dst); e == nil {
			if aside != "" {
				_ = os.Remove(aside) // 旧进程若还活着会删不掉，留着不影响运行
			}
			return nil
		} else {
			err = e
		}
	}
	if aside != "" {
		_ = os.Rename(aside, dst) // 让位后仍搬不进去：还原，别留下「正式文件不见了」
	}
	return err
}

// stopForReplace 在覆盖程序文件前停掉正在运行的网关。
//
// 触发点只在 replaceEntry 的第一次重命名失败之后：文件没被占用就不该动服务。
// 停完不等端口（Stop 内部已等），镜像段释放的余量交给 replaceEntry 的重试。
// 只记日志、不返回错误 —— 停不下来时替换仍有「让位 + 重试」兜底。
func (o *OpenListRuntime) stopForReplace(onLog func(string)) {
	running := false
	if v, _ := svcMgr.info(RuntimeOpenList); v != "" {
		running = true
	} else if findListenPID(o.configPort()) != 0 {
		running = true
	}
	if !running {
		return
	}
	logger.W("[env][openlist] 替换程序文件前停止运行中的网关")
	if onLog != nil {
		onLog("正在停止运行中的 OpenList，以便替换程序文件…")
	}
	_ = o.Stop("")
}

// describeArchive 按当前平台给出归档扩展名（Windows 是 zip，其余是 tar.gz）。
func describeArchive() string {
	if runtime.GOOS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

// ---- 端口与连接信息 ----

// configPort 从 data/config.json 读取实际监听端口；文件缺失或解析失败时给默认值。
// OpenList 的配置形如 {"scheme":{"http_port":5244,...}, ...}，个别版本把 http_port
// 放在顶层，故两条路径都认。
func (o *OpenListRuntime) configPort() int {
	b, err := os.ReadFile(o.ConfigPath(""))
	if err != nil {
		return openlistDefaultPort
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(b, &root) != nil {
		return openlistDefaultPort
	}
	if raw, ok := root["http_port"]; ok {
		var p int
		if json.Unmarshal(raw, &p) == nil && p > 0 {
			return p
		}
	}
	if raw, ok := root["scheme"]; ok {
		var scheme struct {
			HTTPPort int `json:"http_port"`
		}
		if json.Unmarshal(raw, &scheme) == nil && scheme.HTTPPort > 0 {
			return scheme.HTTPPort
		}
	}
	return openlistDefaultPort
}

// Port 返回网关当前监听端口（供 services/netdisk 拼 WebDAV 地址）。
func (o *OpenListRuntime) Port() int { return o.configPort() }

// BaseURL 返回网关根地址，如 http://127.0.0.1:5244（不带尾斜杠）。
func (o *OpenListRuntime) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", o.Port())
}

// AdminUser OpenList 的管理员账号名固定为 admin（首次初始化时创建）。
func (o *OpenListRuntime) AdminUser() string { return "admin" }

// AdminPassword 返回已托管的管理员密码；未托管时返回空串。
// 只读凭据域，不在此处生成 —— 生成时机在 Start（见 ensureAdminPassword）。
func (o *OpenListRuntime) AdminPassword() string {
	list, err := credentials.List()
	if err != nil {
		return ""
	}
	for _, c := range list {
		if c.Source != openlistCredSource {
			continue
		}
		sec, err := credentials.Reveal(c.ID)
		if err != nil {
			logger.W("[env][openlist] 解密管理员密码失败 id=%s: %v", c.ID, err)
			return ""
		}
		return sec.Secret
	}
	return ""
}

// ensureAdminPassword 保证「凭据库里记的密码」== 「网关库里 admin 的密码」。
//
// 为什么不用 OpenList 首次启动打印的随机密码：那个值只出现在日志里，用户要在网页
// 后台登录时得先去翻日志，且每次重建 data/ 都会变。这里改成「主动设一个」，
// 明文经凭据域 DEK 加密后落库，网页登录与 WebDAV 认证都用同一份。
//
// 为什么每次启动都 set 一遍，而不是「有记录就跳过」：密码会漂。data/ 被重建、
// 用户在网页后台改过密码、上一次 set 因网关还在运行而失败（sqlite 被占），
// 任意一种都会让两边不一致，表现为 WebDAV 永远 401 而界面说不出原因。
// 一次 CLI 调用是毫秒级，用它换掉这类漂移很划算。
//
// 失败不阻断启动：网关照常起来（它自己会打印初始密码），只是 QuickDock 侧拿不到
// 密码，前端会退回「请在日志里查看初始密码」的提示。
func (o *OpenListRuntime) ensureAdminPassword(onLog func(string)) {
	pwd := o.AdminPassword()
	fresh := pwd == ""
	if fresh {
		var err error
		if pwd, err = randomPassword(); err != nil {
			logger.W("[env][openlist] 生成管理员密码失败: %v", err)
			return
		}
	}
	// admin set 在 server 未运行时也能用：它会顺手把 data/ 与配置初始化出来。
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := sysutil.CommandContext(ctx, o.ExeFor(""), "admin", "set", pwd, "--data", o.DataDir(""))
	if out, err := cmd.CombinedOutput(); err != nil {
		logger.W("[env][openlist] 设置管理员密码失败: %v out=%s", err, strings.TrimSpace(string(out)))
		return
	}
	if !fresh {
		if onLog != nil {
			onLog("已同步网关管理员密码（与密码库一致）")
		}
		return
	}
	if _, err := credentials.Save(db.Credential{
		Name:     "OpenList 管理员",
		Kind:     db.CredKindWebDAV,
		Username: o.AdminUser(),
		Host:     o.BaseURL(),
		Source:   openlistCredSource,
		SourceID: o.DataDir(""),
		Secret:   pwd,
		Notes:    "网盘网关（OpenList）的管理员账号，网页后台与 WebDAV 共用",
	}); err != nil {
		// 存不进凭据域（例如整库锁定）：把密码交代清楚，至少用户能手动记下。
		logger.W("[env][openlist] 保存管理员密码到凭据域失败: %v", err)
		if onLog != nil {
			onLog("OpenList 管理员密码：" + pwd + "（未能写入密码库，请自行保存）")
		}
		return
	}
	if onLog != nil {
		onLog("已为 OpenList 生成管理员密码并存入密码库（用户名 admin）")
	}
}

// randomPassword 生成 16 字节随机密码（32 位十六进制）。
func randomPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ---- ServiceController ----

func (o *OpenListRuntime) DefaultPort() int { return o.configPort() }

// Start 以 `openlist server --data <dataDir>`（前台阻塞）拉起，由 svcMgr 记录 PID 与日志。
func (o *OpenListRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	exe := o.ExeFor("")
	wd := o.baseDir
	if _, err := os.Stat(exe); err != nil {
		if p, e := exec.LookPath("openlist"); e == nil {
			exe = p
			wd = filepath.Dir(p)
		} else {
			return fmt.Errorf("请先安装 OpenList")
		}
	}
	if running, _ := svcMgr.info(RuntimeOpenList); running != "" {
		return fmt.Errorf("OpenList 已在运行（%s），请先停止", running)
	}
	dataDir := o.DataDir("")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}
	// admin set 要独占写 sqlite。若还有一个「没被本进程追踪」的实例在跑（dev 重启
	// 后旧进程残留、上次 Stop 还在收尾），CLI 会撞上 SQLITE_BUSY —— 更糟的是
	// OpenList 会把这次失败变成永久写锁，之后所有 PROPFIND 都 500。
	// 所以先把端口上的实例清掉，再动数据库。
	o.ensureNoInstance()
	// 先确保管理员密码由我们托管，再拉服务：顺序反了会先打印随机密码，
	// 用户照着日志登录后，下一次启动密码又被换掉。
	o.ensureAdminPassword(onLog)

	port := o.configPort()
	if onLog != nil {
		onLog(fmt.Sprintf("启动 OpenList（http://127.0.0.1:%d）…", port))
	}
	return svcMgr.start(RuntimeOpenList, version, exe, wd,
		[]string{"server", "--data", dataDir}, o.LogPath(""), onLog)
}

func (o *OpenListRuntime) Stop(version string) error {
	svcMgr.killTracked(RuntimeOpenList)
	svcMgr.forget(RuntimeOpenList)
	port := o.configPort()
	stopByPort(port, o.exeName())
	// 等端口真的空出来：进程退出前仍持有数据文件锁，用户点「启动」抢在前面就会撞库。
	waitPortClosed(port, 10*time.Second)
	return nil
}

// ensureNoInstance 清掉监听网关端口的残留实例（可能不属于本进程的追踪表）。
//
// 只杀镜像名匹配 openlist 的进程，避免误伤恰好占用同端口的其他程序；
// 杀完再等端口释放，确保随后 admin set 独占数据库时不会撞锁。
func (o *OpenListRuntime) ensureNoInstance() {
	port := o.configPort()
	if findListenPID(port) == 0 {
		return
	}
	logger.W("[env][openlist] 端口 %d 上有残留实例，先停掉再启动", port)
	stopByPort(port, o.exeName())
	waitPortClosed(port, 10*time.Second)
}

// DatabaseLocked 报告网关是否处于「sqlite 被自己锁死」的状态。
//
// OpenList 在一次写事务失败后会留下永久写锁：此后每个请求等 5 秒超时，对 WebDAV
// 只回一句 "Internal Server Error"（细节只在它自己的 log.log 里），且无法自愈，
// 只能重启进程。所以由服务层读日志辨认这个状态，给用户一句能照做的提示 ——
// 否则界面只会显示「HTTP 500」，没人知道该重启。
func (o *OpenListRuntime) DatabaseLocked() bool {
	// OpenList 的日志分两处：stdout 落到 openlist.log（只有 INFO），logrus 的 ERRO
	// 行写在 data/log/log.log。只看 stdout 会永远读不到锁死记录，所以两处都试。
	paths := []string{
		filepath.Join(o.DataDir(""), "log", "log.log"),
		o.LogPath(""),
	}
	for _, p := range paths {
		tail, err := readLogTail(p)
		if err != nil || tail == "" {
			continue
		}
		lockIdx := strings.LastIndex(tail, "database is locked")
		if lockIdx < 0 {
			continue
		}
		// 重启会清掉这把锁，但旧报错仍留在日志里。只有「锁死记录晚于最近一次
		// 启动」才算当前还锁着 —— 否则每次 HTTP 500 都会被归因成库锁，误导用户。
		startIdx := strings.LastIndex(tail, "start HTTP server")
		if startIdx > lockIdx {
			continue
		}
		return true
	}
	return false
}

func (o *OpenListRuntime) Status(version string) ServiceStatus {
	port := o.configPort()
	st := ServiceStatus{Running: false, Port: port}
	r := Probe(RuntimeRunningProbe{
		Kind:     RuntimeOpenList,
		Port:     port,
		ExeNames: []string{"openlist.exe", "openlist"},
		Installs: o.InstalledVersions(),
	})
	if !r.Running {
		return st
	}
	st.Running = true
	st.PID = r.PID
	st.Version = r.Version
	return st
}

// LogGet 读取运行日志尾部（实现 LogProvider）。首次启动打印的初始密码就在里面，
// 前端在密码未托管时提示用户来这里查。
func (o *OpenListRuntime) LogGet(version string) (string, error) {
	return readLogTail(o.LogPath(""))
}

// WebConsolePort 实现 WebConsoleProvider：环境管理页的「打开控制台」据此显示。
func (o *OpenListRuntime) WebConsolePort(version string) int { return o.configPort() }

var (
	_ RuntimeAdapter     = (*OpenListRuntime)(nil)
	_ ServiceController  = (*OpenListRuntime)(nil)
	_ ConfigProvider     = (*OpenListRuntime)(nil)
	_ DataDirProvider    = (*OpenListRuntime)(nil)
	_ LogProvider        = (*OpenListRuntime)(nil)
	_ WebConsoleProvider = (*OpenListRuntime)(nil)
)
