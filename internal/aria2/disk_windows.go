//go:build windows

package aria2

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// freeSpace 用卷根目录查容量：GetDiskFreeSpaceEx 接受任意路径也可，
// 但传 "D:\" 最直观，也避免长路径 / UNC 带来的额外限制。
func freeSpace(abs string) (uint64, uint64, error) {
	root := filepath.VolumeName(abs) + string(filepath.Separator)
	ptr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0, err
	}
	var freeAvail, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeAvail, &totalBytes, &totalFree); err != nil {
		return 0, 0, err
	}
	return totalBytes, freeAvail, nil
}
