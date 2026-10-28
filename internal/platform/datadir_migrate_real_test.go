package platform

import (
	"os"
	"testing"
)

// TestMigrateRealDataDirEndToEnd 用真实的数据目录验证筛选规则。
//
// 目的：确认「整体搬运」在真实数据上确实生效——
//   - 除数据库三件套外的内容全部纳入（核心数据 + 运行时/模块缓存/依赖树）
//   - 数据库三件套既没被复制也没被删除
//
// 用 DefaultDataDir()（当前生效目录）而不是 DefaultDataDirHome()（固定的家目录）：
// 用户把数据目录迁到别处后，家目录只剩旧残留，核心数据早已在新目录——
// 用家目录断言「未识别到核心数据」会永远失败。
//
// 不做全目录体积统计：真实目录有 17 万余文件，遍历要几十秒，会拖慢测试。
// 复制行为由 TestMigrateDataDirSkipsRebuildable 覆盖。
//
// ⚠️ 这个测试要遍历整个真实数据目录（实测 17 万文件 / 50 秒），
// 属于「按需手工跑」的重型验证，默认跳过。用下面这行跑：
//
//	QD_TEST_ARCHIVE_REAL=1 go test ./internal/platform/ -run TestMigrateRealDataDirEndToEnd -v
func TestMigrateRealDataDirEndToEnd(t *testing.T) {
	if os.Getenv("QD_TEST_ARCHIVE_REAL") == "" {
		t.Skip("重型测试（需遍历整个真实数据目录，约 50 秒）。设置 QD_TEST_ARCHIVE_REAL=1 后运行")
	}
	real := DefaultDataDir()
	if _, err := os.Stat(real); err != nil {
		t.Skipf("真实数据目录不存在，跳过: %v", err)
	}

	files, totalBytes, _, err := scanMigrateFiles(real)
	if err != nil {
		t.Fatalf("扫描真实数据目录失败: %v", err)
	}
	t.Logf("真实数据目录 %s", real)
	t.Logf("纳入迁移 %d 文件 / %.1f MB", len(files), float64(totalBytes)/(1024*1024))

	// 核心数据必须在清单里。
	hasCore := false
	for _, f := range files {
		switch f.rel {
		case "plugins", "images", "icons", "env":
			hasCore = true
		}
	}
	if !hasCore {
		t.Errorf("真实数据目录里未识别到核心数据")
	}

	// 数据库文件绝不能出现在复制清单里。
	for _, f := range files {
		if isDatabaseFile(f.rel) {
			t.Errorf("数据库文件不应纳入复制清单: %s", f.rel)
		}
	}
}
