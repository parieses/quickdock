package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MoveToTrash 把文件或目录移入系统回收站（Windows）/ 废纸篓（macOS）/ XDG Trash（Linux）。
//
// 与 os.Remove / os.RemoveAll 的本质区别是**可恢复**。宿主侧要删除用户文件时
// 统一走这里，插件的 host.fs.remove 也复用同一实现——
// 插件能删的东西不该比宿主删得更彻底。
//
// 契约：
//   - path 必须已存在；不存在返回错误，不静默成功（否则插件无法判断到底删没删）
//   - 卷根（C:\ / /）一律拒绝：误删整个盘的代价无法接受
//   - 失败时**不回退到永久删除**。宁可报错，也不让「可恢复」这个承诺落空
func MoveToTrash(path string) error {
	raw := strings.TrimSpace(path)
	if raw == "" {
		return fmt.Errorf("路径不能为空")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return fmt.Errorf("路径 %q 无效: %w", path, err)
	}
	if isVolumeRoot(abs) {
		return fmt.Errorf("拒绝把卷根目录 %s 移入回收站", abs)
	}
	if _, err := os.Lstat(abs); err != nil {
		return fmt.Errorf("路径不存在或无法访问: %w", err)
	}
	return moveToTrash(abs)
}

// isVolumeRoot 判断是否为卷根。
// 去掉盘符/UNC 前缀后只剩分隔符（或无剩余）即为卷根——例如 C:\ 或 /。
func isVolumeRoot(abs string) bool {
	rest := strings.Trim(abs[len(filepath.VolumeName(abs)):], `/\`)
	return rest == ""
}

// uniqueTrashName 在 dir 里为 name 找一个不冲突的名字。
// 回收站可能同时收到多个同名文件（从不同目录删来的），直接覆盖会丢数据。
// 采用与 Finder 类似的 `name 2` / `name 3` 递增后缀，超过上限返回错误而不是无限循环。
func uniqueTrashName(dir, name string) (string, error) {
	if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
		if os.IsNotExist(err) {
			return name, nil
		}
		return "", err
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i <= 1000; i++ {
		candidate := fmt.Sprintf("%s %d%s", stem, i, ext)
		if _, err := os.Lstat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s 中同名文件过多，无法确定回收站内的唯一名称", dir)
}
