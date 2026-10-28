package services

import (
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// RemoveClipboardImages 删除剪贴板条目对应的图片文件，返回实际删除的个数。
//
// 为什么集中在这里：条目删除有三条路径（单条删除 / 清空历史 / 过期清理），
// 每处各写一遍 os.Remove 很容易漏掉一处，而漏掉的后果是图片目录只涨不消——
// 用户看不到任何报错，只是磁盘慢慢被占满。
//
// 安全边界：只删 platform.GetImageDir() 之下的文件。image_path 是历史数据，
// 万一被改过（手工改库、旧版本 bug），不加这层校验就会变成"删除任意文件"的入口。
func RemoveClipboardImages(paths []string) int {
	if len(paths) == 0 {
		return 0
	}
	dir, err := filepath.Abs(platform.GetImageDir())
	if err != nil || dir == "" {
		return 0
	}
	prefix := dir + string(os.PathSeparator)
	removed := 0
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, aerr := filepath.Abs(p)
		if aerr != nil || !strings.HasPrefix(abs, prefix) {
			logger.W("QuickDock: 跳过剪贴板图片清理（不在图片目录内）: %s", p)
			continue
		}
		if rerr := os.Remove(abs); rerr != nil {
			// 文件已不存在是最常见的情况（手工清过），不算错误
			if !os.IsNotExist(rerr) {
				logger.W("QuickDock: 删除剪贴板图片失败 %s: %v", abs, rerr)
			}
			continue
		}
		removed++
	}
	return removed
}
