package db

import (
	"fmt"
	"os"
	"path/filepath"

	"quickdock/internal/platform"
)

// 数据目录迁移的「收尾」阶段：把数据库搬到新目录，并删除旧目录。
//
// 为什么必须在重启后做：
//   - 迁移发生在应用运行时，数据库正被本进程独占（-wal/-shm 都有打开的句柄）。
//     Windows 上删不掉这些文件，普通文件复制又会得到不一致快照。
//   - 重启后句柄已释放，此时才能用 VACUUM INTO 导出致快照、再删掉整个旧目录。
//
// 调用时机：main.go 里、db.Open 之前（此时新目录还没有数据库，必须先安置好）。

// FinishDataDirMigration 完成数据目录迁移的收尾：
//  1. 把旧目录的数据库导出到新目录（一致性快照）。
//  2. 删除旧目录（整体删除——此时已无被占用的文件）。
//  3. 清除待清理标记。
//
// oldDir 为空时什么都不做（调用方按幂等处理）。
func FinishDataDirMigration(oldDir, newDir string) error {
	if oldDir == "" || newDir == "" {
		return nil
	}
	oldDir = filepath.Clean(oldDir)
	newDir = filepath.Clean(newDir)
	if oldDir == newDir {
		return nil
	}

	// --- 1. 数据库一致性快照 ---
	srcDB := filepath.Join(oldDir, platform.DataDirFileName)
	dstDB := filepath.Join(newDir, platform.DataDirFileName)
	if err := ExportDatabaseFile(srcDB, dstDB); err != nil {
		return fmt.Errorf("迁移数据库失败: %w", err)
	}

	// --- 2. 删除旧目录 ---
	// 旧目录里可能还有迁移阶段删不掉的文件（数据库三件套、日志等），
	// 此时句柄已释放，可整体删除。删除失败不阻断启动：新目录数据已就位，
	// 旧目录残留只是占地方，用户可自行删除。
	if err := os.RemoveAll(oldDir); err != nil {
		// 保留标记，下次启动再试一次。
		_ = platform.MarkPendingCleanup(oldDir)
		return fmt.Errorf("删除旧数据目录失败（数据已迁移，可稍后手动删除 %s）: %w", oldDir, err)
	}

	// --- 3. 清除标记 ---
	return platform.ClearPendingCleanup()
}
