package main

import (
	"strconv"
	"strings"
)

type PortInfo struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	PID      int    `json:"pid,omitempty"`
	Process  string `json:"process,omitempty"`
}

func handlePortCommand(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "port-list":
		portList(id)
	case "port-check":
		portCheck(id, input)
	case "port-kill":
		portKill(id, input)
	default:
		respondError(id, -32601, "unknown port command: "+cmd)
	}
}

func portList(id int64) {
	// Use `netstat -ano` to list all listening ports
	out, err := hiddenCmd("netstat", "-ano").Output()
	if err != nil {
		respondError(id, -1, "执行 netstat 失败: "+err.Error())
		return
	}

	lines := strings.Split(string(out), "\n")
	var ports []PortInfo

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		state := fields[3]
		if state != "LISTENING" && !strings.Contains(line, "LISTEN") {
			continue
		}

		// Parse port from local address (e.g., "0.0.0.0:8080" or "[::]:8080")
		localAddr := fields[1]
		portStr := ""
		if idx := strings.LastIndex(localAddr, ":"); idx >= 0 {
			portStr = localAddr[idx+1:]
		}

		port := 0
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}

		pid := 0
		if len(fields) >= 5 {
			pidStr := fields[len(fields)-1]
			if p, err := strconv.Atoi(pidStr); err == nil {
				pid = p
			}
		}

		proto := "tcp"
		if strings.Contains(line, "UDP") || state == "" {
			proto = "udp"
		}

		processName := ""
		if pid > 0 {
			processName = getProcessName(pid)
		}

		ports = append(ports, PortInfo{
			Port:     port,
			Protocol: proto,
			State:    state,
			PID:      pid,
			Process:  processName,
		})
	}

	respond(id, map[string]interface{}{
		"ports": ports,
		"count": len(ports),
	})
}

// resolvePositiveInt 从输入中解析正整数参数。
// 依次尝试传入的 key（如 "port"/"pid"），并兼容命令面板内联匹配时
// 前端把原始文本放在 input["text"]（例如输入 "1" 命中端口检查）。
func resolvePositiveInt(input map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		if raw, ok := input[key].(float64); ok {
			if p := int(raw); p > 0 {
				return p, true
			}
		}
		if s, ok := input[key].(string); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && p > 0 {
				return p, true
			}
		}
	}
	if s, ok := input["text"].(string); ok {
		if p, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && p > 0 {
			return p, true
		}
	}
	return 0, false
}

func portCheck(id int64, input map[string]interface{}) {
	targetPort, ok := resolvePositiveInt(input, "port")
	if !ok {
		respondError(id, -1, "需要有效的 port 参数")
		return
	}

	out, err := hiddenCmd("netstat", "-ano").Output()
	if err != nil {
		respondError(id, -1, "执行 netstat 失败: "+err.Error())
		return
	}

	lines := strings.Split(string(out), "\n")
	found := false
	var matched PortInfo

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		localAddr := fields[1]
		portStr := ""
		if idx := strings.LastIndex(localAddr, ":"); idx >= 0 {
			portStr = localAddr[idx+1:]
		}

		if p, err := strconv.Atoi(portStr); err == nil && p == targetPort {
			found = true
			pid := 0
			if len(fields) >= 5 {
				pidStr := fields[len(fields)-1]
				if p2, err2 := strconv.Atoi(pidStr); err2 == nil {
					pid = p2
				}
			}
			processName := ""
			if pid > 0 {
				processName = getProcessName(pid)
			}
			matched = PortInfo{
				Port:     targetPort,
				Protocol: "tcp",
				State:    fields[3],
				PID:      pid,
				Process:  processName,
			}
			break
		}
	}

	if found {
		respond(id, map[string]interface{}{
			"inUse":  true,
			"port":   matched.Port,
			"pid":    matched.PID,
			"state":  matched.State,
			"name":   matched.Process,
			"detail": matched,
		})
	} else {
		respond(id, map[string]interface{}{
			"inUse": false,
			"port":  targetPort,
		})
	}
}

func getProcessName(pid int) string {
	names := getAllProcessNames()
	if name, ok := names[pid]; ok {
		return name
	}
	return ""
}

var processNameCache map[int]string
var processNameCacheDone bool

func getAllProcessNames() map[int]string {
	if processNameCacheDone {
		return processNameCache
	}
	out, err := hiddenCmd("tasklist", "/NH", "/FO", "CSV").Output()
	if err != nil {
		processNameCacheDone = true
		processNameCache = make(map[int]string)
		return processNameCache
	}
	processNameCache = make(map[int]string)
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		name := strings.Trim(parts[0], "\"")
		pidStr := strings.Trim(parts[1], "\"")
		pid, err := strconv.Atoi(pidStr)
		if err == nil && name != "" && !strings.Contains(name, "INFO") {
			processNameCache[pid] = name
		}
	}
	processNameCacheDone = true
	return processNameCache
}

func portKill(id int64, input map[string]interface{}) {
	pid, ok := resolvePositiveInt(input, "pid")
	if !ok {
		respondError(id, -1, "需要有效的 pid 参数")
		return
	}

	// 先获取进程名
	procName := getProcessName(pid)

	err := hiddenCmd("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	if err != nil {
		respondError(id, -1, "结束进程失败: "+err.Error())
		return
	}

	respond(id, map[string]interface{}{
		"success": true,
		"pid":     pid,
		"name":    procName,
	})
}
