package aria2

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiskSpace 是目标盘的容量快照：新建任务提交前用它做「盘快满了」的拦截。
// 不预估文件大小（HTTP HEAD 不可靠且会阻塞 UI），只按剩余空间给风险提示。
type DiskSpace struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Used  uint64 `json:"used"`
}

// CheckDiskSpace 查询 path 所在卷的可用空间。
// 目录还不存在时逐级向上找最近的已存在目录——新建任务常填一个还没建好的子目录。
func CheckDiskSpace(path string) (DiskSpace, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return DiskSpace{}, fmt.Errorf("路径为空")
	}
	p = filepath.Clean(p)
	for {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			return DiskSpace{}, fmt.Errorf("找不到可用的上级目录: %s", path)
		}
		p = parent
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return DiskSpace{}, err
	}
	total, free, err := freeSpace(abs)
	if err != nil {
		return DiskSpace{}, err
	}
	used := uint64(0)
	if total > free {
		used = total - free
	}
	return DiskSpace{Path: abs, Total: total, Free: free, Used: used}, nil
}
