//go:build linux

package sysutil

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// userHZ 是 /proc/<pid>/stat 里 utime/stime 的时间单位。
// 该值恒为内核 ABI 固定的 USER_HZ=100，与 CONFIG_HZ 无关，因此可以直接写死。
const userHZ = 100

// SnapshotProcs 遍历 /proc 读取每个进程的父子关系与资源用量。
// 其他用户进程目录无读权限或读取途中退出时静默跳过，不中断整表枚举。
func SnapshotProcs() ([]ProcStat, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]ProcStat, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if st, ok := readProcStat(pid); ok {
			out = append(out, st)
		}
	}
	return out, nil
}

// readProcStat 解析 /proc/<pid>/stat 取出父 PID、累计 CPU 时间与常驻内存。
//
// 第 2 列 comm 是进程名且可能含空格与右括号，故先定位最后一个 ')' 再切分后续字段，
// 否则字段下标会整体错位。以 ')' 之后为准的下标：
//
//	fields[0]=state(3) fields[1]=ppid(4) fields[11]=utime(14) fields[12]=stime(15) fields[21]=rss(24)
func readProcStat(pid int) (ProcStat, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ProcStat{}, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ProcStat{}, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 22 {
		return ProcStat{}, false
	}
	ppid, _ := strconv.Atoi(fields[1])
	utime, _ := strconv.ParseInt(fields[11], 10, 64)
	stime, _ := strconv.ParseInt(fields[12], 10, 64)
	// rss 单位为页数
	rss, _ := strconv.ParseInt(fields[21], 10, 64)
	return ProcStat{
		PID:        pid,
		ParentPID:  ppid,
		MemBytes:   rss * int64(os.Getpagesize()),
		CPUSeconds: float64(utime+stime) / userHZ,
	}, true
}
