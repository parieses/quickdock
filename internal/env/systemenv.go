package env

import (
	"errors"
	"strings"
)

// UserEnvVar 一条「当前用户」环境变量。
//
// 之所以要单独建模而不直接丢 map：UI 需要区分 PATH（按条目管理）与普通变量（整值编辑），
// 还要标出哪些是 QuickDock 自己写进去的——用户排查「这个路径是谁设的」时，
// 能一眼看出是自己加的、还是 QuickDock 激活版本时自动写的。
type UserEnvVar struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Expand  bool   `json:"expand"`  // REG_EXPAND_SZ（值里含 %USERPROFILE% 这类占位符）
	Managed bool   `json:"managed"` // 值指向 QuickDock 的运行目录
}

// UserEnvPath PATH 单独建模：它是以分号分隔的目录列表，UI 要逐条增删。
type UserEnvPath struct {
	Value   string   `json:"value"`
	Expand  bool     `json:"expand"`
	Entries []string `json:"entries"`
}

// UserEnvReport 当前用户环境变量总览。
type UserEnvReport struct {
	Path UserEnvPath  `json:"path"`
	Vars []UserEnvVar `json:"vars"`
}

var (
	errEmptyEnvName   = errors.New("环境变量名不能为空")
	errPathNotAllowed = errors.New("PATH 请通过路径条目接口修改")
)

// managedEnvNames 这些变量由 ActiveEnvProvider 在激活版本时写入，值会指向 .quickdock 目录。
var managedEnvNames = map[string]bool{
	"GOROOT":      true,
	"GOPATH":      true,
	"GOENV":       true,
	"GOPROXY":     true,
	"CARGO_HOME":  true,
	"RUSTUP_HOME": true,
	"JAVA_HOME":   true,
	"PHP_HOME":    true,
	"NODE_HOME":   true,
}

// isManagedValue 判断一个环境变量值是否指向 QuickDock 的运行目录。
// 用宽松匹配（含 ".quickdock"）而不是硬编码盘符：不同机器数据目录位置不同。
func isManagedValue(v string) bool {
	low := strings.ToLower(v)
	return strings.Contains(low, `.quickdock\`) || strings.Contains(low, `.quickdock/`)
}

// IsManagedEnvEntry 判断某条 PATH 条目是否由 QuickDock 写入（供 UI 打标）。
func IsManagedEnvEntry(entry string) bool { return isManagedValue(entry) }
