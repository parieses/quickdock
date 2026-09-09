package env

import (
	"os"
	"path/filepath"
	"strings"
)

// RuntimeRunningProbe 描述一次「某运行时是否真的在跑、跑的是哪个版本」的探测所需信息。
// 统一封装端口 + 进程路径 + 软件名三重判定，取代原先散落在 redis/nginx/sql/mongo/postgres
// 各处的近乎重复的版本归属逻辑。各运行时 Status 只需填好这份描述，复用同一套判定。
type RuntimeRunningProbe struct {
	// Kind 运行时种类（svcMgr 会话内进程查询键）。
	Kind Runtime
	// Port 默认/实际侦听端口；<=0 表示跳过端口探测，仅查 svcMgr 会话内句柄。
	Port int
	// ExeNames 候选可执行文件名（含 .exe 与去后缀两种形态），用于软件名匹配，
	// 如 []string{"mysqld.exe","mysqld"}。
	ExeNames []string
	// Installs 该运行时已安装版本（Install.Path 便携=版本目录、系统=exe 路径、导入=外部目录），
	// 用于将运行中的进程归属到具体版本目录。多版本场景下据此分辨「到底哪个版本在跑」。
	Installs []Install
}

// ProbeResult 是 Probe 的返回值。
type ProbeResult struct {
	Running bool   // 端口上确有该运行时进程在跑（或本会话已拉起）
	Version string // 实际运行版本；多版本场景中无法归属性具体版本时为空串
	PID     int    // 运行进程 PID（0=未知）
}

// Probe 通过端口 + 进程路径/软件名三重判定某运行时是否在运行，并归属到具体版本。
//
// 判定顺序：
//  1. 本会话经 svcMgr 拉起的进程（最权威，版本由 svcMgr 记录）。
//  2. 端口探测：找到监听进程 → 取其 exe 路径。
//  3. 软件名匹配：exe 基名命中 ExeNames（防伪装/同名进程误报）。
//  4. 归属版本：exe 路径落在某已安装版本目录下（便携=版本目录、系统=bin 目录、导入=外部目录），
//     取匹配路径最长者（避免版本号前缀串味，如 "8" 串到 "8.0.36"）。
//
// 仅当能确定具体版本时才返回 Version；若软件名匹配但路径未落在任何已登记版本下，
// Version 留空，由调用方据「已安装版本数」决定如何归因（单版本则归查询版本，多版本不归因以免全亮）。
func Probe(p RuntimeRunningProbe) ProbeResult {
	res := ProbeResult{}

	// 1) 本会话句柄：最权威，版本由 svcMgr 记录。
	if v, _ := svcMgr.info(p.Kind); v != "" {
		res.Running = true
		res.Version = v
		res.PID = svcMgr.pid(p.Kind)
		if res.PID == 0 {
			res.PID = findListenPID(p.Port)
		}
		return res
	}

	if p.Port <= 0 {
		return res
	}

	// 2) 端口探测
	if !isPortOpen(p.Port) {
		return res
	}
	pid := findListenPID(p.Port)
	if pid == 0 {
		return res
	}
	exe := processExePath(pid)
	if exe == "" {
		return res
	}

	// 3) 软件名匹配（防伪装/同名进程）
	if !processIsExeAny(pid, p.ExeNames...) {
		return res
	}

	res.Running = true
	res.PID = pid

	// 4) 归属版本：运行进程 exe 路径落在某已安装版本目录下，取匹配路径最长者。
	var best string
	bestLen := 0
	for _, ins := range p.Installs {
		dir := ins.Path
		if ins.Scope == "system" {
			dir = filepath.Dir(ins.Path)
		}
		if dir == "" {
			continue
		}
		if pathHasPrefix(exe, dir) && len(dir) > bestLen {
			best = ins.Version
			bestLen = len(dir)
		}
	}
	res.Version = best
	return res
}

// pathHasPrefix 判断 path 是否等于 prefix 或以 prefix/ 开头（跨平台，归一化路径分隔符）。
func pathHasPrefix(path, prefix string) bool {
	path = strings.ToLower(filepath.Clean(path))
	prefix = strings.ToLower(filepath.Clean(prefix))
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+string(os.PathSeparator))
}
