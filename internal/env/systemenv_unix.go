//go:build !windows

package env

import "errors"

// 非 Windows 平台的环境变量由 shell 配置文件管理，QuickDock 不做改写。
// 这里保持与 Windows 完全一致的 API 形状（仓库约定：跨平台编译不能断），
// 只返回「不支持」而不是静默成功，避免 UI 误以为写入生效。

var errUserEnvUnsupported = errors.New("当前平台不支持在此管理用户环境变量")

func UserEnvList() (UserEnvReport, error) {
	return UserEnvReport{Vars: []UserEnvVar{}}, nil
}

func UserEnvSetVar(name, value string) error { return errUserEnvUnsupported }

func UserEnvDeleteVar(name string) error { return errUserEnvUnsupported }

func UserEnvSetPath(entries []string) error { return errUserEnvUnsupported }
