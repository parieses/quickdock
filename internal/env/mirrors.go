package env

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// 国内镜像加速（2026-10 引入）。
//
// 用户在国内真正卡住的往往不是「装不上运行时」，而是装完之后 npm install / pip install /
// composer require 卡在源站。这里把包管理器的源切到国内镜像：
//
//	Go       → 每个已装版本各自的 go.env（GOENV 指向，见下方「Go」段说明）
//	npm      → ~/.npmrc 的 registry
//	pip      → %APPDATA%\pip\pip.ini（Windows）/ ~/.config/pip/pip.conf
//	Composer → %APPDATA%\Composer\config.json 的 repositories.packagist
//
// 入口挂在**各自运行时的操作菜单**下（Node→npm、Python→pip、PHP→composer），
// 而不是集中成一个全局「一键开启四项」的开关：用户真正要加速的通常是某一个包管理器，
// 一刀切会把不相关的配置文件全改一遍，也不容易看清每一项当前是什么状态。
//
// 只改用户级/版本级配置，不动系统级配置、不改 PATH；
// 每一项都支持反向关闭（恢复官方源），且写入前会保留文件里的其它配置。

const (
	goProxyMirror  = "https://goproxy.cn,https://mirrors.aliyun.com/goproxy/,direct"
	goProxyDefault = "https://proxy.golang.org,direct"

	npmMirror  = "https://registry.npmmirror.com"
	npmDefault = "https://registry.npmjs.org/"

	pipMirror  = "https://pypi.tuna.tsinghua.edu.cn/simple"
	pipDefault = "https://pypi.org/simple"

	composerMirror  = "https://mirrors.aliyun.com/composer/"
	composerDefault = "https://repo.packagist.org"
)

// MirrorState 单个包管理器的镜像状态。
type MirrorState struct {
	Key       string `json:"key"`       // go / npm / pip / composer
	Label     string `json:"label"`     // 展示名
	Available bool   `json:"available"` // 是否检测到对应工具（未检测到不会写配置文件）
	Enabled   bool   `json:"enabled"`   // 当前是否已指向国内镜像
	Current   string `json:"current"`   // 当前生效值
	Target    string `json:"target"`    // 一键加速会写入的值
	File      string `json:"file"`      // 配置文件路径（Go 为用户级 go.env，可能为空）
	Detail    string `json:"detail"`    // 附加说明（如未检测到的原因）
}

// MirrorStateFor 读取单个包管理器的镜像现状（只读，不写任何文件）。
//
// 按 key 粒度而不是一次性返回四项：镜像是「哪个运行时用哪个包管理器」的属性，
// 入口也挂在对应运行时的操作菜单下（Node→npm、Python→pip、PHP→composer），
// 前端一次只关心自己那一个。
func MirrorStateFor(key string) (MirrorState, error) {
	switch key {
	case "go":
		return goMirrorState(), nil
	case "npm":
		return npmMirrorState(), nil
	case "pip":
		return pipMirrorState(), nil
	case "composer":
		return composerMirrorState(), nil
	}
	return MirrorState{}, fmt.Errorf("未知的镜像项: %s", key)
}

// MirrorApplyFor 开启/关闭单个包管理器的国内镜像，返回操作后的最新状态。
func MirrorApplyFor(key string, enable bool) (MirrorState, error) {
	var err error
	switch key {
	case "go":
		err = goMirrorSet(enable)
	case "npm":
		err = npmMirrorSet(enable)
	case "pip":
		err = pipMirrorSet(enable)
	case "composer":
		err = composerMirrorSet(enable)
	default:
		return MirrorState{}, fmt.Errorf("未知的镜像项: %s", key)
	}
	if err != nil {
		return MirrorState{}, err
	}
	return MirrorStateFor(key)
}

// ---- Go ----
//
// Go 的镜像写入比其它三项复杂，根因是 QuickDock 自己会给每个 Go 版本注入
// GOENV=<版本目录>/gopath/env（见 go.go 的 goVersionEnv）。GOENV 一旦存在，
// **用户级 go.env 就被该文件整体取代**，于是出现两处错位：
//   - 一键镜像若只写用户级 go.env，对已装托管版本毫无效果（界面说开了，go 其实没读到）；
//   - 用户在「配置环境变量」里改的单版本 GOPROXY 也不会进用户级，镜像面板读不到，
//     显示「未开启」，两边读数打架。
//
// 因此这里按版本逐个写各自的 go.env：读写目标与 QuickDock 实际运行 go 时用的是同一份
// 文件，界面显示值与真实生效值永远一致，也不再有「单版本配置被一键镜像覆盖或反过来
// 覆盖不掉」的冲突。

// goTarget 一个 GOPROXY 写入目标：托管版本（带版本专属 go.env）或系统 go（用户级 go.env）。
type goTarget struct {
	Label   string // 展示用：版本号或「系统 Go」
	Bin     string
	EnvFile string // 空 = 不注入 GOENV，即用户级 go.env
}

// runGoEnv 执行 go env 子命令（不注入 GOENV，即用户级 go.env）。
func runGoEnv(bin string, args ...string) (string, error) {
	return runGoEnvAt(bin, "", args...)
}

// runGoEnvAt 执行 go env 子命令，按需注入 GOENV 指向指定 go.env。
//
// 显式剔除继承的 GOPROXY 与 GOENV：
//   - 进程里只要有 GOPROXY 环境变量，`go env -w GOPROXY=...` 就会被拒绝
//     （"does not override conflicting OS environment variable"）；
//   - 继承的 GOENV 会让「写用户级」悄悄变成「写某个版本的文件」，
//     必须剔除后按目标显式指定，写入位置才可控。
func runGoEnvAt(bin, envFile string, args ...string) (string, error) {
	cmd := exec.Command(bin, append([]string{"env"}, args...)...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GOPROXY=") || strings.HasPrefix(kv, "GOENV=") {
			continue
		}
		env = append(env, kv)
	}
	if envFile != "" {
		env = append(env, "GOENV="+envFile)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// goMirrorTargets 枚举需要同步的 Go 目标。
//
// 只要有 QuickDock 托管的版本，就**不再**动用户级 go.env：多版本下写用户级既不会被
// 版本专属文件采纳，又会让「界面显示已开启、实际未生效」的错觉重现。
// 一个托管版本都没有（用户只用系统 go）时才退回到用户级。
func goMirrorTargets() []goTarget {
	g := NewGoRuntime()
	var out []goTarget
	if entries, err := os.ReadDir(g.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			bin := g.ExeFor(v)
			if _, err := os.Stat(bin); err != nil {
				continue
			}
			out = append(out, goTarget{Label: v, Bin: bin, EnvFile: g.goEnvFile(v)})
		}
	}
	if len(out) == 0 {
		if p, err := exec.LookPath("go"); err == nil {
			out = append(out, goTarget{Label: "系统 Go", Bin: p})
		}
	}
	return out
}

// currentProxy 读取该目标当前生效的 GOPROXY。
// 版本目标是直接解析 go.env 文本（不起进程）；文件或条目缺失时返回 Go 内置默认值，
// 与 `go env GOPROXY` 的输出一致。用户级 go.env 路径交给 go 自己解析。
func (t goTarget) currentProxy() (string, error) {
	if t.EnvFile == "" {
		return runGoEnv(t.Bin, "GOPROXY")
	}
	data, err := os.ReadFile(t.EnvFile)
	if err != nil {
		if os.IsNotExist(err) {
			return goProxyDefault, nil
		}
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "GOPROXY="); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v, nil
			}
		}
	}
	return goProxyDefault, nil
}

// setProxy 写入或清除该目标的 GOPROXY。
// 走 `go env -w/-u` 而不是直接改文件，让 Go 自己负责路径与转义；
// 注入了 GOENV，所以写入的正是该版本运行时读的那份 go.env。
func (t goTarget) setProxy(enable bool) error {
	if t.EnvFile != "" {
		// go.env 的父目录必须存在，否则 go 无法创建该文件
		if err := os.MkdirAll(filepath.Dir(t.EnvFile), 0o755); err != nil {
			return err
		}
	}
	if enable {
		_, err := runGoEnvAt(t.Bin, t.EnvFile, "-w", "GOPROXY="+goProxyMirror)
		return err
	}
	// 关闭：撤掉自定义条目回到 Go 默认。已经是默认值就不必动手——
	// `go env -u` 对不存在的条目会报错（实测「reading go env config: ... not found」）。
	if cur, err := t.currentProxy(); err == nil && !strings.Contains(cur, "goproxy.cn") && cur == goProxyDefault {
		return nil
	}
	if _, err := runGoEnvAt(t.Bin, t.EnvFile, "-u", "GOPROXY"); err != nil {
		// 条目本就不存在时 -u 会报错，但「不在用镜像」的目标已经达成，忽略即可
		return nil
	}
	return nil
}

func goMirrorState() MirrorState {
	st := MirrorState{Key: "go", Label: "Go (GOPROXY)", Target: goProxyMirror}
	targets := goMirrorTargets()
	if len(targets) == 0 {
		st.Detail = "未检测到 go 命令（可在环境管理里安装 Go）"
		return st
	}
	st.Available = true
	vals := make([]string, 0, len(targets))
	allMirror := true
	for _, t := range targets {
		cur, err := t.currentProxy()
		if err != nil {
			st.Detail = "读取 GOPROXY 失败: " + err.Error()
			continue
		}
		vals = append(vals, cur)
		if !strings.Contains(cur, "goproxy.cn") {
			allMirror = false
		}
		if st.File == "" {
			st.File = t.EnvFile
		}
	}
	st.Current = strings.Join(dedupeStrings(vals), " | ")
	st.Enabled = allMirror
	if len(targets) > 1 {
		st.Detail = fmt.Sprintf("%d 个 Go 版本各自独立配置（写入各自的 go.env）", len(targets))
	}
	return st
}

func goMirrorSet(enable bool) error {
	targets := goMirrorTargets()
	if len(targets) == 0 {
		return nil
	}
	var errs []string
	for _, t := range targets {
		if err := t.setProxy(enable); err != nil {
			errs = append(errs, t.Label+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ---- npm ----

func npmrcPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".npmrc")
}

func npmMirrorState() MirrorState {
	st := MirrorState{Key: "npm", Label: "npm (registry)", Target: npmMirror, File: npmrcPath()}
	if _, err := exec.LookPath("npm"); err != nil {
		// npm 通常随 Node 一起装，且 Windows 下是 npm.cmd，LookPath 不到也允许写配置
		st.Detail = "未检测到 npm 命令（如已装 Node.js 可忽略此提示）"
	}
	st.Available = true
	data, err := os.ReadFile(st.File)
	if err != nil {
		return st
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "registry=") || strings.HasPrefix(line, "registry =") {
			st.Current = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "registry"), "="))
			st.Enabled = strings.Contains(st.Current, "registry.npmmirror.com")
			return st
		}
	}
	return st
}

// npmMirrorSet 改写 ~/.npmrc 的 registry 行，其它配置（scope、token 等）原样保留。
func npmMirrorSet(enable bool) error {
	path := npmrcPath()
	if path == "" {
		return fmt.Errorf("无法定位用户目录")
	}
	target := npmDefault
	if enable {
		target = npmMirror
	}
	lines := []string{}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "registry=") || strings.HasPrefix(trimmed, "registry =") {
				continue // 去掉旧的 registry 行，稍后统一追加
			}
			if trimmed == "" && len(lines) > 0 && lines[len(lines)-1] == "" {
				continue // 折叠多余空行
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, "registry="+target)
	return os.WriteFile(path, []byte(strings.Join(trimTrailingEmpty(lines), "\n")+"\n"), 0644)
}

// ---- pip ----

func pipConfPath() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "pip", "pip.ini")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pip", "pip.conf")
}

func pipMirrorState() MirrorState {
	st := MirrorState{Key: "pip", Label: "pip (index-url)", Target: pipMirror, File: pipConfPath(), Available: true}
	if _, err := exec.LookPath("pip"); err != nil {
		st.Detail = "未检测到 pip 命令（如已装 Python 可忽略此提示）"
	}
	data, err := os.ReadFile(st.File)
	if err != nil {
		return st
	}
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "index-url") || strings.HasPrefix(t, "index_url") {
			if i := strings.IndexAny(t, "=:"); i > 0 {
				st.Current = strings.TrimSpace(t[i+1:])
			}
			st.Enabled = strings.Contains(st.Current, "tuna.tsinghua") ||
				strings.Contains(st.Current, "aliyun") || strings.Contains(st.Current, "douban")
			return st
		}
	}
	return st
}

// pipMirrorSet 改写 pip.ini/pip.conf 的 index-url，保留其它配置段。
//
// 关键是「插在 [global] 段内」而不是文件末尾：pip 的 index-url 属于 [global]，
// 直接追加到末尾会落到最后一个段（常见是 [install]）里，读起来像是生效了实际不生效。
func pipMirrorSet(enable bool) error {
	path := pipConfPath()
	if path == "" {
		return fmt.Errorf("无法定位用户目录")
	}
	target := pipDefault
	if enable {
		target = pipMirror
	}
	line := "index-url = " + target

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	}
	lines = trimTrailingEmpty(lines)

	// 定位 [global] 段的起止，同时删除该段内旧的 index-url
	start := -1
	for i, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "[global]") {
			start = i
			break
		}
	}
	if start < 0 {
		lines = append([]string{"[global]", line, ""}, lines...)
	} else {
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			if t := strings.TrimSpace(lines[i]); strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
				end = i
				break
			}
		}
		kept := make([]string, 0, len(lines)+1)
		kept = append(kept, lines[:start+1]...)
		kept = append(kept, line)
		for _, l := range lines[start+1 : end] {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "index-url") || strings.HasPrefix(t, "index_url") {
				continue
			}
			kept = append(kept, l)
		}
		kept = append(kept, lines[end:]...)
		lines = kept
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// ---- Composer ----

func composerConfigPath() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Composer", "config.json")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".composer", "config.json")
}

// composerRepos 从整份配置里取出 repositories 映射。
// 顶层用 map[string]json.RawMessage 承载，未涉及的字段（github-oauth、secure-http 等
// 用户配置）原样透传，写回时不会丢。
func composerRepos(cfg map[string]json.RawMessage) map[string]json.RawMessage {
	repos := map[string]json.RawMessage{}
	if raw, ok := cfg["repositories"]; ok {
		_ = json.Unmarshal(raw, &repos)
	}
	return repos
}

func readComposerConfig(path string) map[string]json.RawMessage {
	cfg := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if json.Unmarshal(data, &cfg) != nil {
			cfg = map[string]json.RawMessage{}
		}
	}
	return cfg
}

func composerMirrorState() MirrorState {
	st := MirrorState{Key: "composer", Label: "Composer (packagist)", Target: composerMirror, File: composerConfigPath(), Available: true}
	if _, err := exec.LookPath("composer"); err != nil {
		st.Detail = "未检测到 composer 命令（如已装 PHP 可忽略此提示）"
	}
	cfg := readComposerConfig(st.File)
	raw, ok := composerRepos(cfg)["packagist.org"]
	if !ok {
		return st
	}
	var repo struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &repo) == nil {
		st.Current = repo.URL
		st.Enabled = strings.Contains(repo.URL, "mirrors.aliyun.com/composer")
	}
	return st
}

func composerMirrorSet(enable bool) error {
	path := composerConfigPath()
	if path == "" {
		return fmt.Errorf("无法定位用户目录")
	}
	cfg := readComposerConfig(path)
	repos := composerRepos(cfg)
	if enable {
		raw, _ := json.Marshal(map[string]string{"type": "composer", "url": composerMirror})
		repos["packagist.org"] = raw
	} else {
		// 删掉自定义 packagist 覆盖即恢复官方源
		delete(repos, "packagist.org")
	}
	if len(repos) == 0 {
		delete(cfg, "repositories")
	} else if raw, err := json.Marshal(repos); err == nil {
		cfg["repositories"] = raw
	} else {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// trimTrailingEmpty 去掉末尾空行，避免反复写入时文件越写越长。
func trimTrailingEmpty(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
