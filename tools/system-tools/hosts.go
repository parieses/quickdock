package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func handleHostsCommand(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "hosts-list":
		hostsList(id)
	case "hosts-toggle":
		hostsToggle(id, input)
	case "hosts-add":
		hostsAdd(id, input)
	case "hosts-save":
		hostsSave(id, input)
	default:
		respondError(id, -32601, "unknown hosts command: "+cmd)
	}
}

func hostsPath() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
}

type HostsEntry struct {
	Line    int    `json:"line"`
	IP      string `json:"ip"`
	Host    string `json:"host"`
	Enabled bool   `json:"enabled"`
	Comment string `json:"comment,omitempty"`
}

func hostsList(id int64) {
	path := hostsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		respondError(id, -1, "读取 hosts 文件失败: "+err.Error())
		return
	}

	lines := strings.Split(string(data), "\n")
	var entries []HostsEntry
	var rawLines []string

	re := regexp.MustCompile(`^\s*(#?)\s*(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|[0-9a-fA-F]*:[0-9a-fA-F:]*)\s+(\S+)`)

	for i, line := range lines {
		rawLines = append(rawLines, line)
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") && !re.MatchString(trimmed) {
			continue
		}

		m := re.FindStringSubmatch(trimmed)
		if len(m) >= 4 {
			entry := HostsEntry{
				Line:    i,
				IP:      m[2],
				Host:    m[3],
				Enabled: m[1] != "#",
			}
			if m[1] == "#" {
				entry.Comment = "disabled"
			}
			entries = append(entries, entry)
		}
	}

	respond(id, map[string]interface{}{
		"path":    path,
		"entries": entries,
		"raw":     strings.Join(rawLines, "\n"),
	})
}

func hostsToggle(id int64, input map[string]interface{}) {
	lineIdx, _ := input["line"].(float64)
	if lineIdx == 0 {
		respondError(id, -1, "缺少 line 参数")
		return
	}

	path := hostsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		respondError(id, -1, "读取 hosts 文件失败: "+err.Error())
		return
	}

	lines := strings.Split(string(data), "\n")
	idx := int(lineIdx)
	if idx < 0 || idx >= len(lines) {
		respondError(id, -1, fmt.Sprintf("行号 %d 超出范围", idx))
		return
	}

	line := lines[idx]
	trimmed := strings.TrimSpace(line)

	if strings.HasPrefix(trimmed, "#") {
		lines[idx] = strings.TrimLeft(line, "# \t")
	} else {
		lines[idx] = "# " + line
	}

	result := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(result), 0644); err != nil {
		respondError(id, -1, "写入 hosts 文件失败: "+err.Error())
		return
	}

	respond(id, map[string]interface{}{
		"success": true,
		"line":    idx,
	})
}

func hostsAdd(id int64, input map[string]interface{}) {
	ip, _ := input["ip"].(string)
	host, _ := input["host"].(string)

	if ip == "" || host == "" {
		respondError(id, -1, "需要 ip 和 host 参数")
		return
	}

	path := hostsPath()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		respondError(id, -1, "打开 hosts 文件失败: "+err.Error())
		return
	}
	defer f.Close()

	writer := bufio.NewWriter(f)
	fmt.Fprintf(writer, "\n%s\t%s\n", ip, host)
	writer.Flush()

	respond(id, map[string]interface{}{"success": true, "ip": ip, "host": host})
}

func hostsSave(id int64, input map[string]interface{}) {
	entriesRaw, ok := input["entries"].([]interface{})
	if !ok {
		respondError(id, -1, "缺少 entries 参数")
		return
	}

	path := hostsPath()
	// 读取原始 hosts 文件
	data, err := os.ReadFile(path)
	if err != nil {
		respondError(id, -1, "读取 hosts 文件失败: "+err.Error())
		return
	}
	lines := strings.Split(string(data), "\n")

	// 遍历 entries 更新 hosts 文件的行状态
	for _, raw := range entriesRaw {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		lineFloat, _ := entry["line"].(float64)
		lineIdx := int(lineFloat)
		enabled, _ := entry["enabled"].(bool)
		if lineIdx < 0 || lineIdx >= len(lines) {
			continue
		}
		trimmed := strings.TrimSpace(lines[lineIdx])
		if enabled && strings.HasPrefix(trimmed, "#") {
			lines[lineIdx] = strings.TrimLeft(lines[lineIdx], "# \t")
		} else if !enabled && !strings.HasPrefix(trimmed, "#") {
			lines[lineIdx] = "# " + lines[lineIdx]
		}
	}

	result := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(result), 0644); err != nil {
		respondError(id, -1, "写入 hosts 文件失败: "+err.Error())
		return
	}

	respond(id, map[string]interface{}{"success": true})
}
