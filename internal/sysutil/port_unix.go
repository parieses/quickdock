//go:build !windows

package sysutil

import "errors"

// ListListeningPorts 非 Windows 平台暂不支持。
func ListListeningPorts() ([]PortInfo, error) {
	return nil, errors.New("当前平台暂不支持端口占用查询")
}

// KillProcess 非 Windows 平台暂不支持。
func KillProcess(pid int) error {
	return errors.New("当前平台暂不支持结束进程")
}
