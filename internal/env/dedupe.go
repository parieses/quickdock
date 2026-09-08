package env

import (
	"path/filepath"
	"strings"
)

// managedDirs 记录已收录的 QuickDock 托管便携版可执行文件所在目录（小写、Clean），
// 供 dedupeByDir 判断 exec.LookPath 的结果是否来自同一份托管安装，避免重复登记为
// system 作用域。
//
// 背景：InstalledVersions 先用 ReadDir 收录便携版，再用 exec.LookPath 收录系统 PATH 上的
// 同名程序。当某个便携版被注册进系统 PATH 后，LookPath 会再次命中它，导致同一版本在结果中
// 出现两条记录（portable + system），进而在 UI 中重复显示“取消环境变量”等开关。
// 通过把便携版的 exe 目录登记进 managedDirs，LookPath 命中自管目录时即可跳过。
type managedDirs map[string]bool

// record 记录一个便携版可执行文件所在的目录（自动小写、Clean）。
func (m managedDirs) record(exeDir string) {
	if m == nil {
		return
	}
	m[strings.ToLower(filepath.Clean(exeDir))] = true
}

// dedupeByDir 判断 LookPath 命中的 exe 是否落入已收录的托管目录：
// 落入表示同一份便携版已被 portable 作用域登记，不应再登记为 system 作用域。
func (m managedDirs) dedupeByDir(exe string) bool {
	return m[strings.ToLower(filepath.Clean(filepath.Dir(exe)))]
}
