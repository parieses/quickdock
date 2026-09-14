//go:build darwin

package sysutil

import (
	"strconv"
	"strings"
)

// SnapshotProcs 经一次 ps 拿到全表：PID、父 PID、常驻内存(KB)与累计 CPU 时间。
// darwin 没有 /proc，逐进程调 sysctl 需要大量往返；ps 一次输出整表，配合调用方的短时缓存足够。
func SnapshotProcs() ([]ProcStat, error) {
	out, err := Command("ps", "-axo", "pid=,ppid=,rss=,time=").Output()
	if err != nil {
		return nil, err
	}
	var procs []ProcStat
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		rssKB, _ := strconv.ParseInt(fields[2], 10, 64)
		procs = append(procs, ProcStat{
			PID:        pid,
			ParentPID:  ppid,
			MemBytes:   rssKB * 1024,
			CPUSeconds: parsePSTime(fields[3]),
		})
	}
	return procs, nil
}

// parsePSTime 解析 ps time 列的累计 CPU 时间，形如 "01:23"、"1:02:03"、"2-03:04:05"。
// 最左一段可能是天数（以 '-' 分隔），其余按 [时:]分:秒 由右向左累加。
func parsePSTime(s string) float64 {
	var total float64
	if i := strings.IndexByte(s, '-'); i >= 0 {
		days, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			return 0
		}
		total = days * 86400
		s = s[i+1:]
	}
	mul := 1.0
	parts := strings.Split(s, ":")
	for i := len(parts) - 1; i >= 0; i-- {
		v, err := strconv.ParseFloat(parts[i], 64)
		if err != nil {
			return 0
		}
		total += v * mul
		mul *= 60
	}
	return total
}
