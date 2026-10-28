//go:build !windows

package platform

import "os"

// platformReadUserEnvVars 读取当前进程的环境变量（非 Windows 平台不区分用户级）。
func platformReadUserEnvVars() (map[string]string, error) {
	return map[string]string{}, nil
}

// platformSetUserEnvVar 非 Windows 平台不持久化环境变量（避免改动用户的 shell 配置）。
func platformSetUserEnvVar(name, value string) error {
	_ = name
	_ = value
	return nil
}
