package env

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PHPConfig 某 PHP 版本的配置快照（供前端 4 个标签页：php.ini / 禁用函数 / 错误日志 / 扩展）。
type PHPConfig struct {
	PhpIniPath      string         `json:"phpIniPath"`      // php.ini 绝对路径
	Raw             string         `json:"raw"`             // php.ini 全文（用于「编辑 php.ini」标签页）
	DisableFunctions string        `json:"disableFunctions"` // disable_functions 指令的值
	ErrorLog        string         `json:"errorLog"`        // error_log 指令的值（路径）
	ErrorLogContent string         `json:"errorLogContent"` // 错误日志文件尾部内容（只读预览）
	Extensions      []PHPExtension `json:"extensions"`      // ext/ 下的全部扩展及启用状态
}

// PHPExtension 单个 PHP 扩展的状态。
type PHPExtension struct {
	Name    string `json:"name"`    // 展示名（去 php_ 前缀、去 .dll）
	File    string `json:"file"`    // 文件全名，如 php_curl.dll
	Enabled bool   `json:"enabled"` // 是否在 php.ini 中启用
}

// PHPConfigPatch 写回 php.ini 的增量补丁。
// Raw 非空时整体覆盖 php.ini（「编辑 php.ini」标签页保存）；
// 否则按 DisableFunctions / ErrorLog / Extensions(启用列表) 结构化改写。
type PHPConfigPatch struct {
	Raw              string   `json:"raw"`
	DisableFunctions string   `json:"disableFunctions"`
	ErrorLog         string   `json:"errorLog"`
	Extensions       []string `json:"extensions"` // 启用扩展的展示名（base name）列表
}

var (
	extLineRe    = regexp.MustCompile(`(?i)^\s*;?\s*extension\s*=\s*(.+?)\s*$`)
	disableRe    = regexp.MustCompile(`(?i)^\s*;?\s*disable_functions\s*=\s*(.*?)\s*$`)
	errorLogRe   = regexp.MustCompile(`(?i)^\s*;?\s*error_log\s*=\s*(.*?)\s*$`)
	commentLineRe = regexp.MustCompile(`^\s*;`)
)

// extBaseName 把扩展文件名归一化为展示名：php_curl.dll → curl。
func extBaseName(file string) string {
	n := strings.TrimSuffix(strings.ToLower(file), ".dll")
	n = strings.TrimPrefix(n, "php_")
	return n
}

// readPHPConfig 读取并解析某 PHP 版本目录（dir 为该版本根目录）下的配置。
func readPHPConfig(dir string) (*PHPConfig, error) {
	iniPath := filepath.Join(dir, "php.ini")
	cfg := &PHPConfig{PhpIniPath: iniPath}

	// 已启用扩展集合（按 base name 索引）
	enabled := map[string]bool{}

	data, err := os.ReadFile(iniPath)
	if err == nil {
		cfg.Raw = string(data)
		for _, line := range strings.Split(cfg.Raw, "\n") {
			if m := extLineRe.FindStringSubmatch(line); m != nil && !commentLineRe.MatchString(line) {
				// 取值可能是 php_curl.dll 或 curl
				val := strings.Trim(m[1], `"`)
				enabled[extBaseName(val)] = true
				enabled[strings.TrimSuffix(strings.ToLower(val), ".dll")] = true
			}
			if m := disableRe.FindStringSubmatch(line); m != nil && !commentLineRe.MatchString(line) {
				cfg.DisableFunctions = strings.Trim(m[1], `"`)
			}
			if m := errorLogRe.FindStringSubmatch(line); m != nil && !commentLineRe.MatchString(line) {
				cfg.ErrorLog = strings.Trim(m[1], `"`)
			}
		}
	}

	// 扫描 ext/ 目录，列举全部可用扩展
	if entries, err := os.ReadDir(filepath.Join(dir, "ext")); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".dll") {
				continue
			}
			base := extBaseName(e.Name())
			if base == "" {
				continue
			}
			cfg.Extensions = append(cfg.Extensions, PHPExtension{
				Name:    base,
				File:    e.Name(),
				Enabled: enabled[base],
			})
		}
	}

	// 错误日志文件尾部预览（最多 4KB）
	if cfg.ErrorLog != "" {
		if lb, rerr := os.ReadFile(cfg.ErrorLog); rerr == nil {
			if len(lb) > 4096 {
				lb = lb[len(lb)-4096:]
			}
			cfg.ErrorLogContent = strings.TrimSpace(string(lb))
		}
	}

	return cfg, nil
}

// writePHPConfig 写回 php.ini。Raw 非空整体覆盖；否则按结构化字段改写。
func writePHPConfig(dir string, patch PHPConfigPatch) error {
	iniPath := filepath.Join(dir, "php.ini")

	// Raw 模式：直接整体覆盖
	if strings.TrimSpace(patch.Raw) != "" {
		return os.WriteFile(iniPath, []byte(patch.Raw), 0644)
	}

	enabled := map[string]bool{}
	for _, n := range patch.Extensions {
		enabled[strings.ToLower(strings.TrimSpace(n))] = true
	}

	// 读取现有行（不存在则视为空）
	var lines []string
	if data, err := os.ReadFile(iniPath); err == nil {
		lines = strings.Split(string(data), "\n")
	}

	var out []string
	hasDisable, hasErrorLog := false, false
	for _, line := range lines {
		// 仅丢弃**激活**的 extension 行（稍后统一按启用量重写）；
		// 注释的 ;extension= 模板行保留，避免改一次就把可用扩展清单注释弄丢。
		if extLineRe.MatchString(line) && !commentLineRe.MatchString(line) {
			continue
		}
		if m := disableRe.FindStringSubmatch(line); m != nil {
			if patch.DisableFunctions != "" {
				out = append(out, "disable_functions = \""+patch.DisableFunctions+"\"")
			} else {
				out = append(out, ";disable_functions =")
			}
			hasDisable = true
			continue
		}
		if m := errorLogRe.FindStringSubmatch(line); m != nil {
			if patch.ErrorLog != "" {
				out = append(out, "error_log = \""+patch.ErrorLog+"\"")
			} else {
				out = append(out, ";error_log =")
			}
			hasErrorLog = true
			continue
		}
		out = append(out, line)
	}

	if !hasDisable {
		if patch.DisableFunctions != "" {
			out = append(out, "disable_functions = \""+patch.DisableFunctions+"\"")
		} else {
			out = append(out, ";disable_functions =")
		}
	}
	if !hasErrorLog {
		if patch.ErrorLog != "" {
			out = append(out, "error_log = \""+patch.ErrorLog+"\"")
		} else {
			out = append(out, ";error_log =")
		}
	}

	// 重写启用扩展（仅在 ext/ 中真实存在的才写，避免写错文件名）
	extDir := filepath.Join(dir, "ext")
	if entries, err := os.ReadDir(extDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".dll") {
				continue
			}
			base := extBaseName(e.Name())
			if enabled[base] {
				out = append(out, "extension="+e.Name())
			}
		}
	}

	return os.WriteFile(iniPath, []byte(strings.Join(out, "\n")), 0644)
}
