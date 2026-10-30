package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"quickdock/internal/platform"
)

// 数据目录迁移服务：把旧数据目录整体搬到新目录。
//
// 与早期实现的差别：
//   - 早期：只搬「核心数据」，可重建的环境数据（运行时/模块缓存/依赖树）跳过，
//     迁移后自动重新下载。实测那 17 万余个小文件、约 5GB 的搬运是分钟级的。
//   - 现在：整个目录打包成 zip → 复制到新目录 → 解压。跨盘搬运变成一次
//     顺序读写（单文件），比逐文件复制快一个数量级，也比重新下载快（省掉网络往返）。
//     于是不再区分「核心 / 可重建」，整体搬过去：不漏文件、不漏配置、用户不用等下载。
//   - 全程通过 quickdock:storage:migrate 事件推送进度，前端显示进度条。
//
// 两个阶段与「重启」的分工：
//  1. 运行时（此刻）：打包 → 复制 → 解压 → 切目录 → 改环境变量 → 重新绑定运行时
//     → 清理旧目录。数据库不在这一步搬——它正被本进程独占，Windows 上
//     既不能复制也不能删。
//  2. 重启时：数据库用 VACUUM INTO 导出到新目录 → 整个旧目录删除。
//  3. 用户重启后一切生效（新目录下的数据库、日志、运行时）。

// storageMigrateProgress 迁移进度事件载荷。
type storageMigrateProgress struct {
	Phase      string   `json:"phase"` // scanning / packing / extracting / envvars / rebind / cleanup / done / error
	TotalFiles int      `json:"totalFiles"`
	DoneFiles  int      `json:"doneFiles"`
	TotalBytes int64    `json:"totalBytes"`
	DoneBytes  int64    `json:"doneBytes"`
	Current    string   `json:"current"` // 正在处理的文件（相对路径）
	Message    string   `json:"message"` // 阶段说明
	Skipped    []string `json:"skipped"` // 兼容字段（整体搬运后没有跳过项）
	Runtime    string   `json:"runtime"` // 正在重新绑定的环境运行时（node/go/...）
	Err        string   `json:"err"`
}

// StorageService 承载数据目录迁移领域方法。
type StorageService struct {
	App *AppService

	// migrateMu 串行化迁移：同一时刻只允许一个迁移任务在跑。
	migrateMu      sync.Mutex
	migrateRunning bool
}

// NewStorageService 创建存储服务实例。
func NewStorageService(app *AppService) *StorageService {
	return &StorageService{App: app}
}

// StorageMigrateRunning 检查是否已有迁移任务在跑。
func (s *StorageService) StorageMigrateRunning() bool {
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	return s.migrateRunning
}

// StorageMigrate 把旧数据目录的核心数据迁移到 path，并切换数据目录。
//
// 流程（用户需求：把旧目录打包 → 复制到新目录 → 解压 → 改环境变量 → 删旧目录和压缩包）：
//  1. 打包：旧目录整体打成 zip，直接落到新目录（跨盘只走一遍读写）。
//  2. 解压：zip 解压到新目录，随即删除该 zip（不留压缩文件）。
//  3. 切换数据目录并改写环境变量（删除指向旧目录的项，设置指向新目录的项）。
//  4. 重新绑定运行时：运行时本体已经搬过来了，只需把「哪个版本激活」重新注册一次，
//     不重新下载任何东西。
//  5. 清理旧目录；数据库正被占用删不掉，登记为待清理，重启后由
//     db.FinishDataDirMigration 导出快照后整体删除。
//
// 异步执行：立即返回，进度经 quickdock:storage:migrate 事件推送。
func (s *StorageService) StorageMigrate(path string) *ApiResult {
	if path == "" {
		return FailMsg("数据目录不能为空")
	}
	s.migrateMu.Lock()
	if s.migrateRunning {
		s.migrateMu.Unlock()
		return FailMsg("已有迁移任务在进行中")
	}
	s.migrateRunning = true
	s.migrateMu.Unlock()

	go func() {
		defer func() {
			s.migrateMu.Lock()
			s.migrateRunning = false
			s.migrateMu.Unlock()
			if r := recover(); r != nil {
				s.emit(storageMigrateProgress{Phase: "error", Err: fmt.Sprintf("迁移异常: %v", r)})
			}
		}()

		old := platform.DefaultDataDir()
		// 用户选 D 盘 → D:\.quickdock；选 D:\data → D:\data\.quickdock。
		newDir := platform.NormalizeTargetDir(path)
		if old == "" {
			s.emit(storageMigrateProgress{Phase: "error", Err: "无法确定当前数据目录"})
			return
		}
		if strings.EqualFold(old, newDir) {
			s.emit(storageMigrateProgress{Phase: "error", Err: "新数据目录与当前目录相同"})
			return
		}
		if err := platform.EnsureDataDir(newDir); err != nil {
			s.emit(storageMigrateProgress{Phase: "error", Err: fmt.Sprintf("无法创建新目录 %s: %v", newDir, err)})
			return
		}

		// --- 第一步：把旧目录打包成压缩包，直接落到新目录 ---
		//
		// 为什么直接打在新目录而不是先打系统临时目录：
		// 「打包 → 复制到新目录 → 解压」这条链路上，数据要经过三次搬运。
		// 先打临时目录（C 盘）的话是：C 读→C 写(zip) → C 读→D 写(复制) → D 读→D 写(解压)，
		// 同一份数据在 C 盘写两遍再读一遍纯属浪费。直接打在新目录则是：
		// C 读→D 写(zip) → D 读→D 写(解压)，跨盘只走一遍。
		//
		// 打包中断时的半成品是 .part 文件，失败会被清掉；旧目录始终完好，可重试。
		//
		// rewriteFrom/rewriteTo：把运行时配置里写死的旧目录绝对路径改写成新目录。
		// 不改的话 Go 的 GOPATH 仍指向 C 盘旧路径，模块缓存会继续写回旧目录——
		// 相当于只搬了一半（数据在 D 盘、缓存回写 C 盘）。
		zipPath := filepath.Join(newDir, platform.ArchiveName)

		s.emit(storageMigrateProgress{Phase: "packing", Message: "正在打包旧数据到新目录…"})
		totalFiles, totalBytes, err := platform.ArchiveDataDirWithRewrite(old, zipPath, old, newDir, func(doneFiles, tf int, doneBytes, tb int64, cur string) {
			s.emit(storageMigrateProgress{
				Phase:      "packing",
				TotalFiles: tf,
				DoneFiles:  doneFiles,
				TotalBytes: tb,
				DoneBytes:  doneBytes,
				Current:    cur,
			})
		})
		if err != nil {
			_ = os.Remove(zipPath)
			_ = os.Remove(zipPath + ".part")
			s.emit(storageMigrateProgress{Phase: "error", Err: "打包失败：" + err.Error()})
			return
		}
		s.emit(storageMigrateProgress{
			Phase:      "packing",
			TotalFiles: totalFiles,
			DoneFiles:  totalFiles,
			TotalBytes: totalBytes,
			DoneBytes:  totalBytes,
			Message:    fmt.Sprintf("打包完成：%d 个文件（%.1f MB），已复制到 %s", totalFiles, float64(totalBytes)/(1024*1024), newDir),
		})

		// --- 第二步：在新目录解压 ---
		s.emit(storageMigrateProgress{Phase: "extracting", Message: "正在解压到新目录…"})
		newZip := filepath.Join(newDir, platform.ArchiveName)
		err = platform.ExtractDataDir(newZip, newDir, func(doneFiles, tf int, doneBytes, tb int64, cur string) {
			s.emit(storageMigrateProgress{
				Phase:      "extracting",
				TotalFiles: tf,
				DoneFiles:  doneFiles,
				TotalBytes: tb,
				DoneBytes:  doneBytes,
				Current:    cur,
			})
		})
		if err != nil {
			_ = os.Remove(newZip)
			s.emit(storageMigrateProgress{Phase: "error", Err: "解压失败：" + err.Error()})
			return
		}
		// 解压完成 → 删掉新目录里的压缩包（用户要求：不留压缩文件）。
		_ = os.Remove(newZip)

		// --- 第四步：切换数据目录（写配置） ---
		// 配置在解压成功后再写，中途失败不会留下半配置。
		if err := platform.SetDataDir(newDir, false); err != nil {
			s.emit(storageMigrateProgress{Phase: "error", Err: err.Error()})
			return
		}

		// --- 第五步：删除旧环境变量，设置新环境变量 ---
		envRes, err := platform.ApplyEnvVarsForDataDir(old, newDir)
		if err != nil {
			s.emit(storageMigrateProgress{Phase: "error", Err: "改写环境变量失败：" + err.Error()})
			return
		}
		for _, c := range envRes.Changes {
			s.emit(storageMigrateProgress{
				Phase:   "envvars",
				Message: fmt.Sprintf("环境变量 %s：%s → %s", c.Name, c.Old, c.New),
			})
		}

		// --- 第六步：把运行时的「环境变量指向」重新绑定到新目录 ---
		//
		// 运行时本体（便携 Go/Node/Git）已经随整体打包搬过来了，不需要重新下载。
		// 但「哪个版本处于激活状态」（即 PATH 指向哪个 bin）记在适配器元数据里，
		// 迁移后必须重新注册一次，否则 PATH 还指着旧目录——等于迁移没生效。
		//
		// 必须重建适配器表：多数适配器在构造时把 baseDir 固化成当时的
		// platform.DefaultDataDir()，不重建的话注册会指向旧目录。
		s.App.Env.RebuildAdapters()
		rebindOK := s.rebindEnvRuntimes(old, newDir)

		// --- 第七步：删除旧目录 ---
		// 数据库与日志正被本进程占用，整目录删不掉；
		// 这里先删能删的（数据已搬到新目录），剩下的下次启动由
		// db.FinishDataDirMigration 导出数据库快照后整体删除。
		if err := platform.CleanupOldDataDir(old, rebindOK); err != nil {
			s.emit(storageMigrateProgress{Phase: "cleanup", Message: "旧目录清理部分失败：" + err.Error()})
		}
		// 登记待清理：重启后导数据库快照 + 整体删旧目录。
		if err := platform.MarkPendingCleanup(old); err != nil {
			s.emit(storageMigrateProgress{Phase: "error", Err: "记录待清理目录失败：" + err.Error()})
			return
		}

		if !rebindOK {
			s.emit(storageMigrateProgress{
				Phase:   "rebind",
				Message: "部分运行时的环境变量未重新指向新目录，可在「环境」页重新设置「环境变量指向」",
			})
		}

		s.emit(storageMigrateProgress{
			Phase:   "done",
			Message: "迁移完成，重启应用后生效",
		})
	}()
	return Ok(nil)
}

// rebindEnvRuntimes 把各运行时的「环境变量指向」重新绑定到新目录。
//
// 运行时本体已经随整体打包搬到新目录了，这里只重新注册环境变量
// （PATH 条目 + 版本专属变量如 Go 的 GOPATH/GOENV），不再重新下载任何东西。
//
// 返回是否全部成功。失败时调用方会保留旧目录里的对应内容（不敢删用户唯一的副本）。
func (s *StorageService) rebindEnvRuntimes(oldDir, newDir string) bool {
	// 扫新目录：运行时本体已经搬过去了，那才是它现在真正的位置。
	// 扫旧目录也能拿到版本号，但旧目录在第 7 步就会被清空，
	// 且期间可能因异常残留而状态不一致——新目录才是事实来源。
	targets := detectEnvRuntimes(newDir)
	if len(targets) == 0 {
		return true
	}
	allOK := true
	for _, t := range targets {
		s.emit(storageMigrateProgress{
			Phase:   "rebind",
			Runtime: t.runtime,
			Message: fmt.Sprintf("正在把 %s %s 的环境变量指向新目录…", t.runtime, t.version),
		})
		if err := s.App.Env.RebindRuntime(t.runtime, t.version); err != nil {
			allOK = false
			s.emit(storageMigrateProgress{
				Phase:   "rebind",
				Runtime: t.runtime,
				Message: fmt.Sprintf("%s %s 环境变量重新指向失败：%v（可在「环境」页手动设置）", t.runtime, t.version, err),
			})
			continue
		}
	}
	// 兜底：把残留引用旧目录的环境变量（PATH 里的旧 bin 等）统一换成新目录。
	if _, err := platform.ApplyEnvVarsForDataDir(oldDir, newDir); err != nil {
		s.emit(storageMigrateProgress{Phase: "rebind", Message: "环境变量改写失败：" + err.Error()})
		allOK = false
	}
	return allOK
}

// envRuntimeTarget 一个待重新绑定环境变量的运行时。
type envRuntimeTarget struct {
	runtime string // go / node / git / ...
	version string // 版本号
}

// detectEnvRuntimes 扫描数据目录，找出其中已安装的环境运行时及版本。
// 只识别便携运行时目录（runtime/<name>），不做网络探测。
//
// 注意 Go/Node 是「多版本」布局（runtime/<name>/<version>/…），
// 而 Git 是「单版本」布局（runtime/git/cmd/git.exe，没有版本子目录），
// 故两者取版本号的方式不同。
func detectEnvRuntimes(dir string) []envRuntimeTarget {
	runtimeDir := filepath.Join(dir, "runtime")
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil
	}
	var out []envRuntimeTarget
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// 只认已知运行时，避免把 composer/everything 这类小工具也算进来。
		switch name {
		case "go", "node", "git":
		default:
			continue
		}
		base := filepath.Join(runtimeDir, name)
		verEntries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		// 多版本运行时（go/node）：取其下的版本子目录名。
		if name == "go" || name == "node" {
			for _, v := range verEntries {
				if !v.IsDir() {
					continue
				}
				out = append(out, envRuntimeTarget{runtime: name, version: v.Name()})
			}
			continue
		}
		// 单版本运行时（git）：没有版本子目录。版本号传空，
		// RebindRuntime 会回落到「当前激活版本」（读激活元数据）。
		if len(verEntries) == 0 {
			continue
		}
		out = append(out, envRuntimeTarget{runtime: name, version: ""})
	}
	return out
}

// emit 推送迁移进度事件。
func (s *StorageService) emit(p storageMigrateProgress) {
	if s.App == nil || s.App.App() == nil {
		return
	}
	s.App.App().Event.Emit("quickdock:storage:migrate", p)
}
