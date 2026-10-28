package platform

import (
	"os"
	"path/filepath"
	"strings"
)

// 数据目录迁移时用户选择的目标路径规范化。
//
// 用户的心智模型：「我选 D 盘，数据就放 D:\.quickdock」。
// 而不是让人手动敲一个完整路径再自己补 .quickdock 后缀——那是最容易出错的地方
// （少打一个字母就变成 D:\quickdock，与默认目录名不一致，后续升级对不上）。
//
// 规则：
//   - 选的是盘符根目录（如 D:\ 或 D:）→ 追加 .quickdock
//   - 选的是普通目录 → 若末段已经是 .quickdock 则原样保留，
//     否则同样追加 .quickdock（保持「数据永远叫 .quickdock」的一致性）
//   - 空路径 → 返回默认目录

// DataDirFolderName 数据目录的固定目录名。
const DataDirFolderName = ".quickdock"

// NormalizeTargetDir 把用户选择的目录规范化成最终的数据目录路径。
//
// 例：
//
//	"D:"           → "D:\.quickdock"
//	"D:\"          → "D:\.quickdock"
//	"D:\data"      → "D:\data\.quickdock"
//	"D:\data\.quickdock" → "D:\data\.quickdock"（原样）
//	""             → 默认目录（~/.quickdock）
func NormalizeTargetDir(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return DefaultDataDirHome()
	}
	// 统一分隔符并去掉尾部斜杠（根目录 D:\ 要保留）。
	p = filepath.Clean(p)
	// filepath.Clean("D:\\") → "D:\\"，Clean("D:") → "D:."
	vol := filepath.VolumeName(p)
	if p == vol+string(os.PathSeparator) || p == vol+"." {
		// 盘符根目录 → D:\.quickdock
		return filepath.Join(vol+string(os.PathSeparator), DataDirFolderName)
	}
	base := filepath.Base(p)
	if strings.EqualFold(base, DataDirFolderName) {
		return p
	}
	return filepath.Join(p, DataDirFolderName)
}

// EnsureDataDir 创建数据目录（不存在则建，含父目录）。
func EnsureDataDir(dir string) error {
	if dir == "" {
		return os.ErrInvalid
	}
	return os.MkdirAll(dir, 0700)
}
