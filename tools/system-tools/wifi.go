package main

import (
	"strings"
)

type WifiNetwork struct {
	SSID     string `json:"ssid"`
	Signal   string `json:"signal"`
	Auth     string `json:"auth"`
	Channel  string `json:"channel,omitempty"`
	Security string `json:"security,omitempty"`
}

type WifiStatus struct {
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`
	Interface string `json:"interface,omitempty"`
	Signal    string `json:"signal,omitempty"`
}

func handleWifiCommand(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "wifi-list", "wifi-scan":
		wifiList(id)
	case "wifi-status", "wifi-current":
		wifiStatus(id)
	case "wifi-password", "wifi-pwd":
		wifiPassword(id, input)
	default:
		respondError(id, -32601, "unknown wifi command: "+cmd)
	}
}

func wifiList(id int64) {
	out, err := hiddenCmd("netsh", "wlan", "show", "networks", "mode=Bssid").Output()
	if err != nil {
		respondError(id, -1, "执行 netsh 失败: "+err.Error())
		return
	}

	lines := strings.Split(string(out), "\n")
	var networks []WifiNetwork
	var current *WifiNetwork

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SSID") && strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				ssid := strings.TrimSpace(parts[1])
				if ssid != "" {
					if current != nil {
						networks = append(networks, *current)
					}
					current = &WifiNetwork{SSID: ssid}
				}
			}
		} else if current != nil {
			if strings.Contains(line, "Signal") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					current.Signal = strings.TrimSpace(line[idx+1:])
				}
			} else if strings.Contains(line, "Authentication") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					current.Auth = strings.TrimSpace(line[idx+1:])
				}
			} else if strings.Contains(line, "Channel") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					current.Channel = strings.TrimSpace(line[idx+1:])
				}
			}
		}
	}
	if current != nil {
		networks = append(networks, *current)
	}

	respond(id, map[string]interface{}{
		"networks": networks,
		"count":    len(networks),
	})
}

func wifiStatus(id int64) {
	out, err := hiddenCmd("netsh", "wlan", "show", "interfaces").Output()
	if err != nil {
		respondError(id, -1, "获取 WiFi 状态失败: "+err.Error())
		return
	}

	lines := strings.Split(string(out), "\n")
	status := WifiStatus{}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "SSID") && strings.Contains(line, ":") && !strings.Contains(line, "BSSID") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				ssid := strings.TrimSpace(parts[1])
				if ssid != "" {
					status.Connected = true
					status.SSID = ssid
				}
			}
		}
		if strings.Contains(line, "Signal") && strings.Contains(line, ":") {
			if idx := strings.Index(line, ":"); idx >= 0 {
				status.Signal = strings.TrimSpace(line[idx+1:])
			}
		}
		if strings.Contains(line, "Name") && strings.Contains(line, ":") && !strings.Contains(line, "Hosted") {
			if idx := strings.Index(line, ":"); idx >= 0 {
				status.Interface = strings.TrimSpace(line[idx+1:])
			}
		}
	}

	respond(id, status)
}

func wifiPassword(id int64, input map[string]interface{}) {
	ssid, _ := input["ssid"].(string)
	if ssid == "" {
		respondError(id, -1, "需要 ssid 参数")
		return
	}

	// 使用 Windows WlanGetProfile API 获取密码（netsh 在管道模式下隐藏安全设置段）
	password, err := getWifiPassword(ssid)
	if err != nil {
		respondError(id, -1, "获取 WiFi 密码失败: "+err.Error())
		return
	}

	if password == "" {
		respond(id, map[string]interface{}{
			"ssid":     ssid,
			"password": "",
			"message":  "配置文件中未找到密钥",
		})
		return
	}

	respond(id, map[string]interface{}{
		"ssid":     ssid,
		"password": password,
	})
}
