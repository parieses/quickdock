package platform

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 数据目录迁移：把旧数据目录整体搬到新目录。
//
// 为什么整体搬而不是「只搬核心数据 + 重新下载环境数据」：
//   - 早期实现跳过可重建内容（运行时/模块缓存/依赖树），理由是它们有 16 万余个
//     小文件、约 5GB，逐文件复制慢到分钟级。改成「打包 → 复制 → 解压」后，
//     跨盘搬运变成一次顺序读写（单个 zip 文件），比逐文件复制快一个数量级，
//     也比重新下载快（网络往返 + 解压）。
//   - 于是不再需要区分「核心数据 / 可重建数据」，整个目录一起搬最简单也最安全：
//     不会漏文件、不会漏配置、用户不需要等下载。
//
// 唯一的例外仍是数据库三件套（quickdock.db / -wal / -shm）：
// 它们正被 SQLite 独占，普通复制会得到撕裂快照，删除也会失败。
// 由 internal/db 的 VACUUM INTO 在重启后导出一致快照，见 db.FinishDataDirMigration。

// MigrateProgress 迁移进度快照，供前端渲染进度条。
type MigrateProgress struct {
	// Phase 当前阶段：scanning / packing / extracting / cleanup / done。
	Phase string `json:"phase"`
	// TotalFiles / DoneFiles 文件级计数（扫描后才有意义）。
	TotalFiles int `json:"totalFiles"`
	DoneFiles  int `json:"doneFiles"`
	// TotalBytes / DoneBytes 字节级计数。
	TotalBytes int64 `json:"totalBytes"`
	DoneBytes  int64 `json:"doneBytes"`
	// Current 正在处理的文件（相对路径），用于让用户看到「没卡住」。
	Current string `json:"current"`
	// Skipped 被判定为可重建、因而跳过的条目（相对路径）。
	Skipped []string `json:"skipped,omitempty"`
	// Message 阶段说明文案。
	Message string `json:"message,omitempty"`
	// Err 非空表示失败。
	Err string `json:"err,omitempty"`
}

// 迁移阶段常量。
const (
	MigratePhaseScanning   = "scanning"
	MigratePhaseCopying    = "copying"
	MigratePhasePacking    = "packing"
	MigratePhaseExtracting = "extracting"
	MigratePhaseCleanup    = "cleanup"
	MigratePhaseDone       = "done"
)

// rebuildablePrefixes 「可重建」的数据目录子路径（相对数据目录，斜杠分隔）。
//
// 现在只用于旧目录清理（搬完之后把旧目录里这些内容删掉），不再用于跳过搬运。
// 保留这张清单的价值在于：整体搬运后，这些内容在新目录已就位，
// 旧目录里的同名内容就是纯冗余，留着等于磁盘占用翻倍。
//
//   - runtime/go/...      Go 工具链本体 + 其 GOMODCACHE
//   - runtime/node/...    便携 Node
//   - runtime/git/...     便携 Git
//   - gopath/pkg/...      根级 GOPATH 的模块缓存
//   - dsh/node_modules    pnpm 依赖树
//
// 判定边界（改这个清单前先想清楚「旧目录里这份删了会不会丢东西」）：
//   - `runtime/everything`、`runtime/composer` 不能列进来：Everything 是
//     104MB 的独立可执行文件，删了旧目录就再也回不去了。
//   - `gopath/src` 是用户的 Go 工作区代码，不能跳过；只跳过其下的 `pkg`（模块缓存）。
//   - `dsh_token` 是授权令牌，不能跳过——它与目录无关，跳过等于强制用户重新登录。
var rebuildablePrefixes = []string{
	"runtime/go",
	"runtime/node",
	"runtime/git",
	"gopath/pkg",
	"dsh/node_modules",
}

// rebuildableFiles 按精确相对路径排除的条目。
//
// 判定边界：这个表里的每一项都必须有明确的重建途径，否���就等于丢文件。
// 曾经的错误：`bin/wails3.exe` 被列在这里，但全仓库没有任何代码会重新下载它
// （它既不属于 runtime/go、node、git 任何一条重装链路，也不是 dsh 的自装包），
// 迁移后新目录里就没有它了。现在这里保持为空——
// 有疑问的条目一律不要排除，宁可多搬 54MB。
var rebuildableFiles = map[string]bool{}

// DataDirFileName 主数据库文件名（数据目录根下）。
//
// 权威来源放在 platform 层：internal/db 与 internal/platform 都要用它
// （前者做快照导出，后者做扫描排除），避免两处各写一份字符串而漂移。
const DataDirFileName = "quickdock.db"

// isDatabaseFile 判断相对路径是否属于 SQLite 数据库的任一附属文件
// （主库 / -wal 预写日志 / -shm 共享内存索引）。
//
// 这三个文件既不能普通复制也不能删除：
//   - 复制：数据库正被本进程打开，WAL 模式下主库文件可能落后于 -wal，
//     逐字节复制三者会得到「撕裂」的快照（丢事务或损坏）。
//     正确做法见 internal/db 的 ExportDatabaseFile（VACUUM INTO，SQLite 自己保证一致性）。
//   - 删除：SQLite 持有独占句柄，Windows 上直接报「另一个程序正在使用此文件」。
//     故它们不参与普通复制，也不参与旧目录清理，改由重启后统一处理。
func isDatabaseFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	// 主库文件本身，以及 WAL 模式的两个附属文件。
	return rel == DataDirFileName ||
		rel == DataDirFileName+"-wal" ||
		rel == DataDirFileName+"-shm"
}

// isRebuildable 判断相对路径是否属于「可重建」内容（应跳过复制）。
//
// 前缀匹配同时命中目录本身与目录下的内容——只匹配目录下的内容会导致目标目录里
// 留下空壳目录（如 runtime/go/），既没意义又让用户以为迁了东西。
func isRebuildable(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rebuildableFiles[rel] {
		return true
	}
	for _, p := range rebuildablePrefixes {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// migrateFile 一条待复制的文件。
type migrateFile struct {
	rel   string
	size  int64
	isDir bool
}

// scanMigrateFiles 遍历旧数据目录，收集需要搬走的文件清单，同时统计总量。
//
// 现在只跳过数据库三件套（见 isDatabaseFile），其余一律纳入——
// 包括此前跳过的可重建内容（运行时/模块缓存/依赖树）。原因见文件头注释：
// 打包成 zip 后跨盘是一次顺序读写，比逐文件复制快，也比重新下载快。
func scanMigrateFiles(src string) (files []migrateFile, totalBytes int64, skipped []string, err error) {
	err = filepath.Walk(src, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// 数据库三件套：既不复制也不清理，交给 db.Snapshot + 重启后清理。
		if isDatabaseFile(rel) {
			return nil
		}
		if info.IsDir() {
			files = append(files, migrateFile{rel: rel, isDir: true})
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil // 符号链接不复制，避免跨设备悬空
		}
		files = append(files, migrateFile{rel: rel, size: info.Size()})
		totalBytes += info.Size()
		return nil
	})
	return
}

// MigrateDataDir 把 src（当前数据目录）里不可再生的核心数据复制到 dst，
// 完成后删除 src 中已复制的内容。
//
// 与旧实现（整目录 copyTree）的关键差别：
//  1. 跳过可重建的环境数据（运行时/模块缓存/依赖树）——旧实现要搬 16 万余个
//     小文件、约 5GB，实测慢到分钟级；新版只搬核心数据，通常只有几百个文件。
//  2. 复制完成后删除旧文件——旧实现只 copy 不删，磁盘占用翻倍。
//  3. 全程上报进度，旧实现在窗口出现前静默执行，用户看不到任何反馈。
//
// 数据库文件（quickdock.db 及 -wal/-shm）不在本次复制范围内：它们正被 SQLite
// 独占，普通复制会得到不一致快照，删除也会失败。数据库的迁移由 internal/db
// 的 Snapshot 单独完成（重启后执行），详见 DataDirCleanupPending。
//
// progress 可为 nil。为 nil 时不做任何进度回调。
func MigrateDataDir(src, dst string, progress func(MigrateProgress)) error {
	if src == "" || dst == "" {
		return fmt.Errorf("数据目录不能为空")
	}
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	if src == dst {
		return nil
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		return fmt.Errorf("无法创建目标目录 %s: %w", dst, err)
	}

	report := func(p MigrateProgress) {
		if progress != nil {
			progress(p)
		}
	}

	// --- 阶段 1：扫描 ---
	report(MigrateProgress{Phase: MigratePhaseScanning, Message: "正在扫描需要迁移的数据…"})
	files, totalBytes, skipped, err := scanMigrateFiles(src)
	if err != nil {
		return fmt.Errorf("扫描旧数据目录失败: %w", err)
	}
	report(MigrateProgress{
		Phase:      MigratePhaseCopying,
		TotalFiles: len(files),
		TotalBytes: totalBytes,
		Message:    fmt.Sprintf("开始迁移 %d 个文件（%.1f MB）", len(files), float64(totalBytes)/(1024*1024)),
	})

	// --- 阶段 2：复制 ---
	// 进度节流：核心数据通常只有几百个文件，但插件目录里可能有上万个小文件；
	// 每个文件都回调会把前端刷爆（也拖慢复制本身）。按 100ms 节流，
	// 最后一个文件必定回调（保证进度条能走到 100%）。
	throttle := newProgressThrottle()
	var doneFiles int
	var doneBytes int64
	for i, f := range files {
		if f.isDir {
			if err := os.MkdirAll(filepath.Join(dst, filepath.FromSlash(f.rel)), 0700); err != nil {
				return fmt.Errorf("创建目录失败 %s: %w", f.rel, err)
			}
			doneFiles++
			continue
		}
		s := filepath.Join(src, filepath.FromSlash(f.rel))
		d := filepath.Join(dst, filepath.FromSlash(f.rel))
		n, err := copyFileWithMode(s, d)
		if err != nil {
			return fmt.Errorf("复制失败 %s: %w", f.rel, err)
		}
		doneFiles++
		doneBytes += n
		last := i == len(files)-1
		if last || throttle.should() {
			report(MigrateProgress{
				Phase:      MigratePhaseCopying,
				TotalFiles: len(files),
				DoneFiles:  doneFiles,
				TotalBytes: totalBytes,
				DoneBytes:  doneBytes,
				Current:    f.rel,
			})
		}
	}

	// --- 阶段 3：清理旧目录中已复制的内容 ---
	report(MigrateProgress{
		Phase:      MigratePhaseCleanup,
		TotalFiles: len(files),
		DoneFiles:  doneFiles,
		TotalBytes: totalBytes,
		DoneBytes:  doneBytes,
		Message:    "正在清理旧目录…",
	})
	if err := cleanupMigratedFiles(src, files); err != nil {
		return fmt.Errorf("清理旧目录失败: %w", err)
	}

	report(MigrateProgress{
		Phase:      MigratePhaseDone,
		TotalFiles: len(files),
		DoneFiles:  doneFiles,
		TotalBytes: totalBytes,
		DoneBytes:  doneBytes,
		Skipped:    skipped,
		Message:    "迁移完成",
	})
	return nil
}

// cleanupMigratedFiles 删除旧目录里已被复制走的文件与空目录。
// 被跳过的（可重建）内容保留在原地——它们要么会被重新下载覆盖，要么用户还要用；
// 真正的旧数据（数据库/插件）已经搬到新目录，留着只会让磁盘占用翻倍。
//
// 删除失败不视为致命错误：Windows 上正在被进程占用的文件（日志、数据库等）
// 必然删不掉，此时跳过即可——旧目录的彻底清理由重启后统一完成（见 CleanupDataDir）。
func cleanupMigratedFiles(src string, files []migrateFile) error {
	// 先删文件，再自底向上删空目录（files 是 Walk 的前序遍历，目录在文件之前，
	// 这里倒序遍历保证先删文件后删目录）。
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		if f.isDir {
			continue
		}
		p := filepath.Join(src, filepath.FromSlash(f.rel))
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			// 被占用或权限不足：跳过，不中断迁移。
			continue
		}
	}
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		if !f.isDir {
			continue
		}
		p := filepath.Join(src, filepath.FromSlash(f.rel))
		// 只删空目录：非空说明里面还有被跳过的可重建内容，留着。
		entries, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		if len(entries) == 0 {
			_ = os.Remove(p)
		}
	}
	return nil
}

// CleanupRebuildableDirs 删除旧数据目录里的「可重建」内容（运行时/模块缓存/依赖树）。
//
// 调用时机：迁移完成、且运行时已在新目录重新下载成功之后。此时旧目录里的
// 这些内容已经完全冗余——留着等于磁盘占用翻倍（实测近 5GB）。
//
// 只删可重建内容，不碰别的：旧目录里的数据库、日志等由 CleanupDataDir 负责。
// 删除失败一律忽略（文件可能正被占用），残留由用户手动处理。
func CleanupRebuildableDirs(dir string) error {
	if dir == "" {
		return nil
	}
	for _, p := range rebuildablePrefixes {
		target := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.RemoveAll(target); err != nil {
			continue
		}
	}
	return nil
}

// CleanupOldDataDir 清理旧数据目录里已经搬走的内容。
//
// 整体搬运下，旧目录里的内容除了数据库三件套外全是冗余（都已在新目录就位），
// 所以这里删的是「除数据库三件套外的一切」。
//
// 数据库三件套（quickdock.db / -wal / -shm）保留不动：它们正被本进程独占，
// Windows 上既删不掉也不能删（重启后还要靠它们做 VACUUM INTO 导出一致快照）。
// 旧目录的最终删除由 db.FinishDataDirMigration 在下次启动时完成。
//
// dropRebuildable 参数保留但已无实际作用（整体搬运后不再区分内容类别）：
// 传 false 时会保留「可重建」内容不动，供调用方在重装失败时兜底。
func CleanupOldDataDir(dir string, dropRebuildable bool) error {
	if dir == "" {
		return nil
	}
	dir = filepath.Clean(dir)

	// 逐项删除（跳过数据库三件套）。不用 os.RemoveAll(dir)：
	// 那会把数据库三件套也一起删掉——它们删不掉会报错，
	// 而留着它们才能让重启后的 VACUUM INTO 导出一致快照。
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		name := e.Name()
		if isDatabaseFile(name) {
			continue
		}
		// dropRebuildable=false：保留可重建内容（旧目录那份是唯一副本时用）。
		if !dropRebuildable && isRebuildable(name) {
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.RemoveAll(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// 残留的压缩包/半成品：迁移中途失败可能留下 .part，一并清掉。
	for _, name := range []string{ArchiveName, ArchiveName + ".part"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	return firstErr
}

// copyFileWithMode 复制单个文件并保留权限位，返回复制的字节数。
func copyFileWithMode(src, dst string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return 0, err
	}
	defer out.Close()
	return io.Copy(out, in)
}

// MigrateEnvVarsResult 描述迁移后需要改写的环境变量。
type MigrateEnvVarsResult struct {
	// Updated 变量名 → 新值。
	Updated map[string]string `json:"updated"`
	// Removed 变量名（值指向旧目录、已删除）。
	Removed []string `json:"removed"`
}

// RewriteEnvVarsForDataDir 把所有指向旧数据目录的用户级环境变量改写为新目录。
//
// 只处理「值里包含旧数据目录路径」的那些变量（典型如 GOPATH / GOENV / GOROOT
// 指向 runtime/go/<v>/gopath），其余变量原样不动——绝不去改用户自己的 PATH 等。
//
// 由 platform 层提供与具体运行时无关的通用实现：扫描用户环境变量，凡是值包含
// oldDir 前缀的，就把该前缀替换为 newDir。
func RewriteEnvVarsForDataDir(oldDir, newDir string) (MigrateEnvVarsResult, error) {
	res := MigrateEnvVarsResult{Updated: map[string]string{}}
	if oldDir == "" || newDir == "" || oldDir == newDir {
		return res, nil
	}
	vars, err := readUserEnvVars()
	if err != nil {
		return res, err
	}
	for name, value := range vars {
		if value == "" || !strings.Contains(value, oldDir) {
			continue
		}
		newValue := strings.ReplaceAll(value, oldDir, newDir)
		if err := setUserEnvVar(name, newValue); err != nil {
			return res, fmt.Errorf("改写环境变量 %s 失败: %w", name, err)
		}
		res.Updated[name] = newValue
	}
	return res, nil
}

// envVarUpdater 供平台无关的测试注入。
var (
	readUserEnvVars = platformReadUserEnvVars
	setUserEnvVar   = platformSetUserEnvVar
)

// migrateProgressThrottle 限制进度回调频率：每 100ms 最多放行一次，
// 避免几万个文件把前端刷爆（调用方负责保证最后一个文件必定回调）。
type migrateProgressThrottle struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

func newProgressThrottle() *migrateProgressThrottle {
	return &migrateProgressThrottle{interval: 100 * time.Millisecond}
}

// should 判断距上次放行是否已超过间隔。
func (t *migrateProgressThrottle) should() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if now.Sub(t.last) < t.interval {
		return false
	}
	t.last = now
	return true
}
