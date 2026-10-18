package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// dataDirEnv 允许通过环境变量在启动前覆盖数据目录（便携版 / 高级用法）。
// 优先级高于持久化配置文件。
const dataDirEnv = "QUICKDOCK_HOME"

// dataDirConfigFile 持久化数据目录覆盖值的位置。
// 关键约束：该文件必须位于「数据目录之外」，否则出现鸡生蛋问题——
// DefaultDataDir 自身依赖它来读取覆盖值，不能放在数据目录内。
// 放在系统配置目录（Windows 上为 %APPDATA%\QuickDock），与可执行程序同机。
func dataDirConfigFile() string {
	if d := DefaultConfigDir(); d != "" {
		return filepath.Join(d, "quickdock.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".quickdock.json")
	}
	return "quickdock.json"
}

type dataDirConfig struct {
	DataDir     string `json:"dataDir"`     // 自定义数据目录（绝对路径）
	MigrateFrom string `json:"migrateFrom"` // 待迁移的旧目录（下次启动拷贝完成后清空）
}

var (
	dataDirMu       sync.RWMutex
	dataDirOverride string // 非空表示用户自定义的数据目录（绝对路径）
	dataDirLoaded   bool
)

func cleanAbs(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func readDataDirConfig() dataDirConfig {
	var cfg dataDirConfig
	b, err := os.ReadFile(dataDirConfigFile())
	if err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	return cfg
}

func writeDataDirConfig(cfg dataDirConfig) error {
	if err := os.MkdirAll(filepath.Dir(dataDirConfigFile()), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dataDirConfigFile(), b, 0600)
}

// loadDataDirOverride 仅解析一次：环境变量 QUICKDOCK_HOME 优先，其次配置文件。
// 任何调用 DefaultDataDir / DataDirInfo 前都会先触发它，故无需显式初始化。
func loadDataDirOverride() {
	dataDirMu.Lock()
	defer dataDirMu.Unlock()
	if dataDirLoaded {
		return
	}
	dataDirLoaded = true
	if v := strings.TrimSpace(os.Getenv(dataDirEnv)); v != "" {
		dataDirOverride = cleanAbs(v)
		return
	}
	if c := cleanAbs(readDataDirConfig().DataDir); c != "" {
		dataDirOverride = c
	}
}

// resolveDataDir 返回最终生效的数据目录（含自定义覆盖）。
func resolveDataDir() string {
	loadDataDirOverride()
	dataDirMu.RLock()
	ov := dataDirOverride
	dataDirMu.RUnlock()
	if ov != "" {
		return ov
	}
	return defaultDataDirFallback()
}

// DefaultDataDirHome 返回「默认」数据目录（home/.quickdock），供 UI 显示。
func DefaultDataDirHome() string { return defaultDataDirFallback() }

// DataDirInfo 返回当前生效的数据目录与是否为自定义。
// 注意：不能先持 RLock 再调用 resolveDataDir（其内部会尝试 Lock），避免同 goroutine 重入死锁。
func DataDirInfo() (string, bool) {
	cur := resolveDataDir()
	dataDirMu.RLock()
	custom := dataDirOverride != ""
	dataDirMu.RUnlock()
	return cur, custom
}

// SetDataDir 设置自定义数据目录。copyExisting=true 时会在下次启动把旧目录数据整体迁移过来。
// 选回与默认目录相同的路径，等同于清除覆盖（恢复默认）。
func SetDataDir(raw string, copyExisting bool) error {
	p := cleanAbs(raw)
	if p == "" {
		return errors.New("数据目录不能为空")
	}
	if !filepath.IsAbs(p) {
		return errors.New("数据目录必须是绝对路径")
	}
	if def := defaultDataDirFallback(); p == def {
		return ResetDataDir()
	}
	if err := os.MkdirAll(p, 0700); err != nil {
		return fmt.Errorf("无法创建目录 %s: %w", p, err)
	}

	cfg := readDataDirConfig()
	old := resolveDataDir()
	if copyExisting && old != "" && old != p {
		cfg.MigrateFrom = old
	} else {
		cfg.MigrateFrom = ""
	}
	cfg.DataDir = p
	if err := writeDataDirConfig(cfg); err != nil {
		return err
	}
	dataDirMu.Lock()
	dataDirOverride = p
	dataDirMu.Unlock()
	return nil
}

// ResetDataDir 清除自定义，恢复默认数据目录。
func ResetDataDir() error {
	dataDirMu.Lock()
	dataDirOverride = ""
	dataDirMu.Unlock()
	cfg := readDataDirConfig()
	cfg.DataDir = ""
	cfg.MigrateFrom = ""
	if err := writeDataDirConfig(cfg); err != nil {
		return err
	}
	return nil
}

// ApplyPendingDataDirMigration 须在 main 启动最早期、logger.Init 与 DB 打开之前调用：
// 若上一次设置勾选了「迁移现有数据」，此处把旧目录整体拷贝进新目录再清除标记。
// 早于数据目录被任何消费者读取，保证迁移发生在应用尚未打开 DB / 写入日志之前。
func ApplyPendingDataDirMigration() error {
	loadDataDirOverride()
	dataDirMu.RLock()
	cur := dataDirOverride
	dataDirMu.RUnlock()

	cfg := readDataDirConfig()
	from := cleanAbs(cfg.MigrateFrom)
	if from == "" || from == cur {
		return nil
	}
	if _, err := os.Stat(from); err != nil {
		// 旧目录已不存在，仅清标记即可。
		cfg.MigrateFrom = ""
		return writeDataDirConfig(cfg)
	}
	dst := cur
	if dst == "" {
		dst = defaultDataDirFallback()
	}
	if err := copyTree(from, dst); err != nil {
		return fmt.Errorf("迁移数据失败（旧目录=%s）: %w", from, err)
	}
	cfg.MigrateFrom = ""
	return writeDataDirConfig(cfg)
}

// copyTree 把 src 目录整体递归拷贝到 dst（dst 必须已存在）。
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil // 跳过符号链接，避免跨设备 dangling
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
