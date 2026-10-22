package env

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"quickdock/internal/platform"
)

// 磁盘占用统计（2026-10 引入）：环境管理装的是真实运行时（MySQL/Ollama/JDK 动辄数百 MB
// 到数 GB），用户最常问的就是「这些东西占了我多少空间、哪个能删」。
// 统计口径为 <数据目录>/runtime/<runtime>/<version> 整棵树的实际字节数，
// 含运行数据（MySQL data、Redis dump、Ollama 模型）——它们才是真正的大头。

// VersionDiskUsage 某已装版本的磁盘占用。
type VersionDiskUsage struct {
	Version string `json:"version"`
	Bytes   int64  `json:"bytes"`
}

// RuntimeDiskUsage 某运行时的磁盘占用（各版本合计 + 明细）。
type RuntimeDiskUsage struct {
	Runtime  string             `json:"runtime"`
	Name     string             `json:"name"`
	Bytes    int64              `json:"bytes"`
	Versions []VersionDiskUsage `json:"versions"`
}

// DiskUsageReport 全量磁盘占用报告。
type DiskUsageReport struct {
	TotalBytes int64              `json:"totalBytes"`
	Runtimes   []RuntimeDiskUsage `json:"runtimes"`
}

// RuntimeRoot 返回受管运行时的根目录（<数据目录>/runtime）。
func RuntimeRoot() string {
	return filepath.Join(platform.DefaultDataDir(), "runtime")
}

// DiskUsage 统计每个运行时及其各版本的磁盘占用，按占用降序排列。
// 只统计磁盘上真实存在目录的运行时；不存在的一律不返回（避免面板全是 0 值行）。
func DiskUsage() DiskUsageReport {
	root := RuntimeRoot()
	report := DiskUsageReport{Runtimes: []RuntimeDiskUsage{}}

	entries, err := os.ReadDir(root)
	if err != nil {
		return report
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rt := Runtime(e.Name())
		rtDir := filepath.Join(root, e.Name())
		item := RuntimeDiskUsage{
			Runtime:  string(rt),
			Name:     DisplayName(rt),
			Versions: []VersionDiskUsage{},
		}
		if item.Name == "" {
			item.Name = e.Name() // 未注册目录（历史残留/内置服务数据），照实展示
		}
		vers, verr := os.ReadDir(rtDir)
		if verr == nil {
			for _, v := range vers {
				if !v.IsDir() {
					continue
				}
				size := dirSize(filepath.Join(rtDir, v.Name()))
				if size == 0 {
					continue
				}
				item.Versions = append(item.Versions, VersionDiskUsage{Version: v.Name(), Bytes: size})
				item.Bytes += size
			}
		}
		// 版本子目录之外的文件（如 links.json 之类）不计入：占比可忽略，且容易让人误读
		if item.Bytes == 0 {
			continue
		}
		sort.Slice(item.Versions, func(i, j int) bool {
			return item.Versions[i].Bytes > item.Versions[j].Bytes
		})
		report.Runtimes = append(report.Runtimes, item)
		report.TotalBytes += item.Bytes
	}
	sort.Slice(report.Runtimes, func(i, j int) bool {
		return report.Runtimes[i].Bytes > report.Runtimes[j].Bytes
	})
	return report
}

// dirSize 递归统计目录内所有常规文件的字节数。读不到的条目直接跳过
// （正在运行的服务会锁日志文件，不能因此让整个统计失败）。
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 权限/占用问题：跳过该项，继续统计
		}
		if d.IsDir() {
			return nil
		}
		// 符号链接不跟随（避免循环），只算真实文件大小
		if info, ierr := d.Info(); ierr == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}
