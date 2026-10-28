package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// dataDirEnv 允许通过环境变量在启动前覆盖数据目录（便携版 / 高级用法）。
// 优先级低于配置文件中的 dataDir：仅当配置未设置时才作为兜底生效，
// 保证「设置 → 存储与同步」里用户显式选择的路径始终优先。
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
	// CleanupFrom 待彻底删除的旧目录（数据目录迁移后清理残留）。
	// 与 MigrateFrom 的区别：MigrateFrom 是「还没搬」，CleanupFrom 是「搬完了，
	// 只剩数据库等被占用、删不掉的文件」，下次启动时句柄已释放才能删干净。
	CleanupFrom string `json:"cleanupFrom"`
	// UiZoom 全局界面缩放系数（WebView ZoomFactor），见 uizoom.go 的档位定义。
	// 放在这个「数据目录之外」的配置文件里是有意的：窗口在创建时就固定了 Zoom 与尺寸，
	// 必须能在打开数据库之前读到（数据库要等 Wails 服务启动才就绪，那时窗口早已建好）。
	UiZoom float64 `json:"uiZoom,omitempty"`
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

// loadDataDirOverride 仅解析一次：配置文件 dataDir 优先，其次环境变量 QUICKDOCK_HOME（仅兜底），
// 最后才是安装器注册表种子。任何调用 DefaultDataDir / DataDirInfo 前都会先触发它，故无需显式初始化。
func loadDataDirOverride() {
	dataDirMu.Lock()
	defer dataDirMu.Unlock()
	if dataDirLoaded {
		return
	}
	dataDirLoaded = true
	if c := cleanAbs(readDataDirConfig().DataDir); c != "" {
		dataDirOverride = c
		return
	}
	if v := strings.TrimSpace(os.Getenv(dataDirEnv)); v != "" {
		dataDirOverride = cleanAbs(v)
		return
	}
	// 安装器在首次启动前写入的机器级/用户级目录（Windows 注册表，见 datadir_installer_*.go），
	// 作为「一次性种子」：固化进用户配置 quickdock.json，之后完全由应用自身管理。
	// 仅当环境变量与配置文件都未设置时才生效，因此升级安装（配置已存在）不会回退到安装器值。
	if r := cleanAbs(readInstallerDataDir()); r != "" {
		dataDirOverride = r
		cfg := readDataDirConfig()
		cfg.DataDir = r
		_ = writeDataDirConfig(cfg)
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

// HasPendingDataDirMigration 报告是否存在待执行的启动期迁移标记（migrateFrom）。
//
// 正常流程是用户在「设置 → 存储与同步」里点迁移，由 services 层在运行时执行
// （那样才有进度条）；此标记仅用于兼容旧版本留下的待迁移状态。
func HasPendingDataDirMigration() bool {
	cfg := readDataDirConfig()
	return cleanAbs(cfg.MigrateFrom) != ""
}

// PendingCleanupDir 返回待清理的旧数据目录（迁移后残留、需下次启动删除），无则返回空串。
func PendingCleanupDir() string {
	return cleanAbs(readDataDirConfig().CleanupFrom)
}

// MarkPendingCleanup 记录待清理的旧目录，下次启动时执行删除。
//
// 为什么需要它：迁移发生在应用运行时，此时数据库与日志文件正被本进程独占，
// Windows 上删不掉（报「另一个程序正在使用此文件」）。故迁移阶段只搬走能搬的，
// 旧目录的彻底删除推迟到下次启动——那时句柄已释放。
func MarkPendingCleanup(dir string) error {
	cfg := readDataDirConfig()
	p := cleanAbs(dir)
	if p == "" {
		cfg.CleanupFrom = ""
	} else {
		cfg.CleanupFrom = p
	}
	return writeDataDirConfig(cfg)
}

// ClearPendingCleanup 清除待清理标记。
func ClearPendingCleanup() error {
	cfg := readDataDirConfig()
	if cfg.CleanupFrom == "" {
		return nil
	}
	cfg.CleanupFrom = ""
	return writeDataDirConfig(cfg)
}

// ApplyPendingDataDirMigration 须在 main 启动最早期、logger.Init 与 DB 打开之前调用：
// 若上一次设置勾选了「迁移现有数据」，此处把旧目录的核心数据迁移到新目录。
// 早于数据目录被任何消费者读取，保证迁移发生在应用尚未打开 DB / 写入日志之前。
//
// 迁移只搬运「不可再生」的数据（数据库、插件、用户图片等）；运行时、模块缓存、
// 依赖树这类可重新下载的内容留在原地不动——旧实现整目录 copy，实测要搬 16 万余个
// 小文件、约 5GB，慢到分钟级且没有进度反馈。
//
// progress 可为 nil。
func ApplyPendingDataDirMigration(progress func(MigrateProgress)) error {
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
	if err := MigrateDataDir(from, dst, progress); err != nil {
		return fmt.Errorf("迁移数据失败（旧目录=%s）: %w", from, err)
	}
	// 迁移成功：清掉旧目录标记，并改写指向旧目录的环境变量。
	cfg.MigrateFrom = ""
	if err := writeDataDirConfig(cfg); err != nil {
		return err
	}
	if _, err := RewriteEnvVarsForDataDir(from, dst); err != nil {
		return fmt.Errorf("改写环境变量失败: %w", err)
	}
	return nil
}

// copyTree / copyFile 已由 datadir_migrate.go 的 MigrateDataDir 取代：
// 新实现跳过可重建的环境数据、复制后清理旧目录、并上报进度。
