//go:build !windows

package sysutil

import (
	"errors"
	"io"
)

// ConPty 在非 Windows 平台仅为占位：ConPTY 是 Windows 专有能力。
// 保留同名类型是为了让调用方（services/env/service.go）在 darwin/linux 上也能编译，
// 调用方会在非 Windows 下自动回退到普通的 exec.Cmd 启动方式。
type ConPty struct{}

// StartConPty 在非 Windows 平台返回错误，调用方应回退到常规启动。
func StartConPty(exe string, args []string, dir string) (*ConPty, error) {
	return nil, errors.New("ConPTY 仅支持 Windows")
}

// Pid 始终返回 0（非 Windows 不会真正创建伪控制台进程）。
func (c *ConPty) Pid() int { return 0 }

// Read 非 Windows 直接返回 EOF。
func (c *ConPty) Read(p []byte) (int, error) { return 0, io.EOF }

// Write 非 Windows 不支持。
func (c *ConPty) Write(p []byte) (int, error) { return 0, io.EOF }

// Wait 非 Windows 不支持。
func (c *ConPty) Wait() (int, error) { return 0, errors.New("ConPTY 仅支持 Windows") }

// Kill 非 Windows 为空操作。
func (c *ConPty) Kill() error { return nil }

// Close 非 Windows 为空操作。
func (c *ConPty) Close() {}
