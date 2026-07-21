package webdav

import "encoding/json"

// MarshalConfig 将 Config 序列化为 JSON 字符串
func MarshalConfig(cfg *Config) (string, error) {
	if cfg == nil {
		return "{}", nil
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalConfig 从 JSON 字符串解析 Config
func UnmarshalConfig(s string) *Config {
	cfg := &Config{}
	if s == "" || s == "{}" {
		return cfg
	}
	if err := json.Unmarshal([]byte(s), cfg); err != nil {
		return cfg
	}
	return cfg
}
