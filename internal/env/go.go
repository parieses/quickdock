package env

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const goBaseRel = "runtime/go"

// GoRuntime 管理便携 Go 运行时（官方发行包），支持多版本共存于 runtime/go/<version>。
type GoRuntime struct {
	baseDir string
}

func NewGoRuntime() *GoRuntime {
	return &GoRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), goBaseRel)}
}

func (g *GoRuntime) Kind() Runtime        { return RuntimeGo }
func (g *GoRuntime) DetectArgs() []string { return []string{"version"} }
func (g *GoRuntime) ParseVersion(out string) (string, error) {
	const p = "go version go"
	if i := strings.Index(out, p); i >= 0 {
		rest := out[i+len(p):]
		if sp := strings.IndexByte(rest, ' '); sp > 0 {
			return rest[:sp], nil
		}
		return strings.TrimSpace(rest), nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeGo))
}
func (g *GoRuntime) DisplayName() string          { return DisplayName(RuntimeGo) }
func (g *GoRuntime) SupportedPlatforms() []string { return []string{"windows", "linux", "darwin"} }
func (g *GoRuntime) Recommended() []string        { return Versions(RuntimeGo) }

func (g *GoRuntime) versionDir(version string) string {
	return filepath.Join(g.baseDir, version)
}

func (g *GoRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.versionDir(version), "bin", "go.exe")
	}
	return filepath.Join(g.versionDir(version), "bin", "go")
}

// InstalledVersions 同时探测便携目录（runtime/go/<v>）与系统 PATH 上的 go。
// 这正是修复「本机已装 go 1.25 却提示未安装」的关键——不再只认便携目录。
func (g *GoRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(g.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(g.ExeFor(v)); err == nil {
				// Path 必须是 bin 目录（go.exe 所在目录），与 SetActive 注册的 exeDirFor(=Dir(ExeFor)) 保持一致：
				// Go 的发行包把 go.exe 放在 <version>/bin 下，若这里填版本根目录，
				// 后端 binInSystemPath 与前端路径面板都会拿版本根目录去比对 PATH 而恒不命中，
				// 表现为「设置环境变量没反应」（同 jdk.go / erlang.go 的处理）。
				out = append(out, Install{Version: v, Scope: "portable", Path: filepath.Dir(g.ExeFor(v))})
				dirs.record(filepath.Dir(g.ExeFor(v)))
			}
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		if v := parseGoVersion(RunVersion(p, "version")); v != "" {
			// LookPath 可能命中本就由 QuickDock 托管并写入 PATH 的便携版（与上面 ReadDir 收录的是同一份），
			// 此时不要重复登记为 system，否则同一版本出现两条“取消环境变量”。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

// DeleteVersion 删除某便携 Go 版本目录（系统 PATH 上的版本无目录可删，返回错误）。
func (g *GoRuntime) DeleteVersion(version string) error {
	dir := g.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (g *GoRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeGo)[0]
	}
	dir := g.versionDir(version)
	if _, err := os.Stat(g.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Go " + version + " 已安装: " + g.ExeFor(version))
		}
		return nil
	}
	// 残留的半成品目录先清掉再装
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeGo, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Go 下载源")
	}
	ext := ".zip"
	if runtime.GOOS != "windows" {
		ext = ".tar.gz"
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-go-"+version+ext)
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Go "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Go " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Go 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Go…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Go 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Go 失败: %w", err)
	}
	if _, err := os.Stat(g.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", g.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("Go " + version + " 解压完成")
	}
	// 安装完成后自动配置该版本的 GOPATH（按版本隔离，落在该版本目录下），
	// 并给国内网络预设 GOPROXY，省去手动配置。写入用户级 go.env，对所有 Go 版本统一生效；
	// 若安装多个 Go 版本，以最后一次安装写入的 GOPATH 为准（严格按版本隔离需在激活时再注入 GOPATH 环境变量）。
	gopath := filepath.Join(g.versionDir(version), "gopath")
	_ = os.MkdirAll(gopath, 0o755)
	if _, e := g.runGo(g.ExeFor(version), "env", "-w", "GOPATH="+gopath); e != nil && cb.OnLog != nil {
		cb.OnLog("自动设置 GOPATH 失败（可稍后在「配置环境变量」中设置）: " + e.Error())
	}
	if _, e := g.runGo(g.ExeFor(version), "env", "-w", "GOPROXY=https://goproxy.cn,direct"); e != nil && cb.OnLog != nil {
		cb.OnLog("自动设置 GOPROXY 失败: " + e.Error())
	}
	if cb.OnLog != nil {
		cb.OnLog("已自动设置 GOPATH=" + gopath + "，GOPROXY=https://goproxy.cn,direct")
	}
	return nil
}

func parseGoVersion(out string) string {
	// "go version go1.23.4 windows/amd64"
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "go") && strings.Contains(tok, ".") {
			return strings.TrimPrefix(tok, "go")
		}
	}
	return ""
}

// ---- Go 环境变量配置（GoEnvProvider 实现）----

// goEnvKeyDefs 可经 `go env -w` 维护的常见环境变量。
// 注意：go env 配置写入「用户级」配置文件（$HOME/go/env，Windows 为 %USERPROFILE%\go\env），
// 对所有 Go 版本统一生效，与具体是哪个 go 二进制执行 -w 无关。
func (g *GoRuntime) GoEnvKeys() []EnvKeyDef {
	return []EnvKeyDef{
		{Key: "GOPROXY", Label: "模块代理 GOPROXY", Desc: "拉取公共模块时的代理地址；多个用逗号分隔，末尾 direct 表示回退源站。国内可改用 goproxy.cn。", Placeholder: "https://proxy.golang.org,direct"},
		{Key: "GOSUMDB", Label: "校验库 GOSUMDB", Desc: "模块校验数据库地址/公钥；设为 off 可关闭校验（配合私有源使用）。", Placeholder: "sum.golang.org"},
		{Key: "GOPRIVATE", Label: "私有模块 GOPRIVATE", Desc: "匹配前缀的模块走直连、不走代理也不校验 sumdb，如 git.company.com/*。", Placeholder: "git.company.com/*"},
		{Key: "GONOSUMDB", Label: "跳过校验 GONOSUMDB", Desc: "逗号分隔的模块前缀，跳过 sumdb 校验（与 GOPRIVATE 类似，旧写法）。", Placeholder: "git.company.com/*"},
		{Key: "GONOSUMCHECK", Label: "关闭 sum 校验 GONOSUMCHECK", Desc: "跳过模块 checksum 校验（旧写法，建议改用 GOPRIVATE）。", Placeholder: ""},
		{Key: "GOFLAGS", Label: "默认参数 GOFLAGS", Desc: "每次 go 命令默认附加的参数，如 -mod=mod、-p=8。", Placeholder: "-mod=mod"},
		{Key: "GOMODCACHE", Label: "模块缓存 GOMODCACHE", Desc: "下载模块存放目录，留空使用默认。", Placeholder: ""},
		{Key: "GOCACHE", Label: "构建缓存 GOCACHE", Desc: "编译产物缓存目录，留空使用默认。", Placeholder: ""},
		{Key: "GOBIN", Label: "可执行输出 GOBIN", Desc: "go install 产物的安装目录，留空使用默认。", Placeholder: ""},
		{Key: "GOPATH", Label: "工作区 GOPATH", Desc: "Go 工作区根目录（默认 ~/go），留空使用默认。", Placeholder: ""},
		{Key: "GO111MODULE", Label: "模块模式 GO111MODULE", Desc: "是否启用 Go Modules。", Placeholder: "", Choices: []string{"on", "off", "auto"}},
		{Key: "CGO_ENABLED", Label: "CGO CGO_ENABLED", Desc: "是否启用 cgo；交叉编译静态二进制常设为 0。", Placeholder: "", Choices: []string{"0", "1"}},
		{Key: "GOOS", Label: "目标系统 GOOS", Desc: "交叉编译目标操作系统，留空=当前系统。", Placeholder: "linux", Choices: []string{"windows", "linux", "darwin", "freebsd", "android", "js"}},
		{Key: "GOARCH", Label: "目标架构 GOARCH", Desc: "交叉编译目标架构，留空=当前架构。", Placeholder: "amd64", Choices: []string{"amd64", "arm64", "386", "arm", "riscv64", "loong64"}},
		{Key: "GOTOOLCHAIN", Label: "工具链 GOTOOLCHAIN", Desc: "Go 1.21+ 工具链策略：auto 自动按需下载、local 只用本地、或指定 go1.x.y / 绝对路径。", Placeholder: "auto", Choices: []string{"auto", "local", "path"}},
		{Key: "GOINSECURE", Label: "非安全源 GOINSECURE", Desc: "逗号分隔的模块前缀，允许经 http（非 https）拉取，用于内网私有源。", Placeholder: "git.corp.com,*.*.corp"},
		{Key: "GOVCS", Label: "版本控制 GOVCS", Desc: "限制模块拉取可使用的 VCS 命令，如 git:git,svn:all。", Placeholder: "git:git,svn:all"},
	}
}

// goBin 解析出执行 `go env` 所用的二进制：优先用便携版（存在即可），
// 否则回退系统 PATH 上的 go；都没有则报错。go env 的写入位置（用户级 go.env）与具体
// 哪个二进制执行无关，故两者等效；这样在「系统 PATH 上的 go」场景下也能配置。
func (g *GoRuntime) goBin(version string) (string, error) {
	if p := g.ExeFor(version); fileExists(p) {
		return p, nil
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("未找到可用的 go 可执行文件（版本 %s 未安装且系统 PATH 中无 go）", version)
}

// runGo 拉起 go 二进制并捕获标准输出；stderr 一并带回错误，便于排查。
//
// 版本隔离：自动从 bin 推导版本号并注入 GOENV，指向该版本专属的 go.env。
// 这是多版本严格隔离的关键——Go 默认让所有版本共享同一份 $HOME/go/env（无论哪个 go 二进制执行
// go env -w，写的都是那一个文件），且 OS 级 GOPATH 还会再盖一层；用 GOENV 指向版本专属文件后，
// go env 的读与写都只作用于该版本。推导不出版本（如系统 PATH 上的 go）时不注入，回退默认行为。
func (g *GoRuntime) runGo(bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = g.goVersionEnv(g.versionFromBin(bin))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("执行 go %s 失败: %v: %s", strings.Join(args, " "), err, out.String())
	}
	return out.String(), nil
}

// goGopath 该版本专属的工作区目录（runtime/go/<version>/gopath），模块缓存随之按版本隔离。
func (g *GoRuntime) goGopath(version string) string {
	return filepath.Join(g.versionDir(version), "gopath")
}

// goEnvFile 该版本专属的 go.env 文件路径（runtime/go/<version>/gopath/env）。
func (g *GoRuntime) goEnvFile(version string) string {
	return filepath.Join(g.goGopath(version), "env")
}

// versionFromBin 从 go 二进制路径反推版本号：便携版形如 <baseDir>/<version>/bin/go(.exe)。
// 推不出来（系统 PATH 上的 go）返回 ""，此时不注入版本专属环境、沿用共享 go.env。
func (g *GoRuntime) versionFromBin(bin string) string {
	rel, err := filepath.Rel(g.baseDir, bin)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 || parts[0] == "" {
		return ""
	}
	return parts[0]
}

// goVersionEnv 构造某版本 go 的进程环境：注入 GOENV 指向版本专属 go.env。
//
// 只注入 GOENV 而**不**注入 GOPATH，并且把父进程继承来的 GOPATH 剔除——原因很实际：
// 只要进程里存在 GOPATH 环境变量，`go env -w GOPATH=...` 就会报
// "does not override conflicting OS environment variable" 而拒绝写入。
// 让 GOPATH 以一条配置的形式落在版本专属 go.env 里、由 go 读取时自行应用，
// 既能达到「该版本用它自己的 GOPATH」，又不会让写入被拒绝。
// version 为空时原样返回父进程环境。
func (g *GoRuntime) goVersionEnv(version string) []string {
	if version == "" {
		return os.Environ()
	}
	// go.env 的父目录必须先存在，否则 go 无法创建/写入该文件
	_ = os.MkdirAll(g.goGopath(version), 0o755)
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GOPATH=") {
			continue // 剔除继承的 GOPATH，避免与 go env -w 冲突
		}
		env = append(env, kv)
	}
	return append(env, "GOENV="+g.goEnvFile(version))
}

// ActiveEnvVars 实现 ActiveEnvProvider：该版本被设为「环境变量指向」时，
// 除把 bin 注册进 PATH 外，还要写入的用户级环境变量。
// 作用是把版本隔离延伸到用户终端——否则终端里跑 go 仍会用共享的 $HOME/go/env 与默认 GOPATH。
// 键名恒定返回：version 为空时各键值为空串，交由调用方据此删除（清除激活时要知道该删哪些键）。
func (g *GoRuntime) ActiveEnvVars(version string) map[string]string {
	vars := map[string]string{"GOPATH": "", "GOENV": ""}
	if version != "" {
		vars["GOPATH"] = g.goGopath(version)
		vars["GOENV"] = g.goEnvFile(version)
	}
	return vars
}

// GoEnvGet 读取当前生效的 go env（go env -json 解析），仅回填可编辑清单里的 key。
func (g *GoRuntime) GoEnvGet(version string) (map[string]string, error) {
	bin, err := g.goBin(version)
	if err != nil {
		return nil, err
	}
	raw, err := g.runGo(bin, "env", "-json")
	if err != nil {
		return nil, err
	}
	var all map[string]string
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		return nil, fmt.Errorf("解析 go env 输出失败: %w", err)
	}
	res := make(map[string]string, len(g.GoEnvKeys()))
	for _, k := range g.GoEnvKeys() {
		res[k.Key] = all[k.Key]
	}
	return res, nil
}

// GoEnvSet 写回：patch 中 value 非空则 `go env -w key=value`，空则 `go env -u key` 清除。
// 健壮性：空值仅在「该 key 当前确有自定义值」时才执行 -u，避免对未设置过的变量调 -u 报错
// （go env -u 对不存在的条目会返回错误，会中断整批写入）。
func (g *GoRuntime) GoEnvSet(version string, patch map[string]string) error {
	bin, err := g.goBin(version)
	if err != nil {
		return err
	}
	for k, v := range patch {
		if v == "" {
			cur, e := g.runGo(bin, "env", k)
			if e != nil || strings.TrimSpace(cur) == "" {
				continue // 当前本就未设置（或读不到），无需清除
			}
			// 清除动作尽力而为：若该项只是 Go 内置默认值、go.env 中并无自定义条目，
			// `go env -u` 可能报错；但「回退到默认」的目标本就已达成，故忽略错误继续。
			if _, uerr := g.runGo(bin, "env", "-u", k); uerr != nil {
				logger.W("[env go] 清除 %s 失败（可忽略）: %v", k, uerr)
			}
			continue
		}
		if _, err := g.runGo(bin, "env", "-w", k+"="+v); err != nil {
			return err
		}
	}
	return nil
}
