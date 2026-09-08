package env

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 本文件集中放置「不可信输入」的校验。
//
// 背景：Manager 的所有方法都经 Wails 绑定暴露给前端，而插件窗口与主界面同源、
// 共享同一套绑定，因此 runtime/version/dir 都属于不可信输入。version 最终会拼进
// filepath.Join(baseDir, version) 并交给 os.RemoveAll / os.OpenFile，缺失校验即可
// 通过 `..\..\Users\xxx` 之类实现任意目录删除或读取。

// validVersionRe 允许的版本号字符集：字母数字开头，其余只允许字母数字与 . _ + -
// （覆盖 8.3.0 / 27.3.4 / 1.20.0-nts / 0.1.1-rc.2 等真实版本形态）。
var validVersionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// validateVersion 校验运行时版本号，拒绝任何可用于路径穿越的字符。
// 所有接受 version 的 Manager 公开方法入口都必须调用它。
func validateVersion(version string) error {
	if version == "" {
		return errors.New("版本号为空")
	}
	if len(version) > 64 {
		return errors.New("版本号过长（上限 64 字符）")
	}
	if !validVersionRe.MatchString(version) {
		return fmt.Errorf("版本号含非法字符（只允许字母、数字、. _ + -）: %s", version)
	}
	// 正则允许单个 '.'，".." 仍可用于向上穿越，单独拦一道。
	if strings.Contains(version, "..") {
		return fmt.Errorf("版本号非法（不能含连续的 '.'）: %s", version)
	}
	return nil
}

// validateImportDir 校验「导入已有安装」的目录参数：必须是已存在的绝对目录。
// 相对路径会基于不可控的 cwd 解析，且该目录下的可执行文件会被拉起做版本探测。
func validateImportDir(dir string) error {
	if dir == "" {
		return errors.New("目录为空")
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("目录必须是绝对路径: %s", dir)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("目录不可访问: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("不是目录: %s", dir)
	}
	return nil
}
