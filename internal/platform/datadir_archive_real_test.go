package platform

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zipEntries 列出压缩包里的全部条目名。
func zipEntries(path string) ([]string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := make([]string, 0, len(r.File))
	for _, f := range r.File {
		out = append(out, f.Name)
	}
	return out, nil
}

// readZipEntry 读取压缩包里某个条目的文本内容。
func readZipEntry(path, name string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		b := make([]byte, 64*1024)
		n, _ := rc.Read(b)
		return string(b[:n]), nil
	}
	return "", os.ErrNotExist
}

// TestArchiveExtractRealDataDir 用真实数据目录验证「打包 → 解压」整体搬运。
//
// 目的：确认整体搬运在真实数据上可行且正确：
//   - 除数据库三件套外的内容全部进了压缩包
//   - 写死旧路径的文本配置（runtime/go/*/gopath/env）被改写成新路径
//
// 用 DefaultDataDir()（当前生效目录）而不是 DefaultDataDirHome()（固定的家目录）：
// 用户把数据目录迁到别处后，家目录只剩旧残留。
//
// 只打包不解压到真实位置——解压 5GB/17 万文件太慢，本测试验证 zip 内容即可。
//
// ⚠️ 这个测试要打包整个真实数据目录（实测 5.2GB / 17 万文件，约 9 分钟），
// 属于「按需手工跑」的重型验证，默认跳过。用下面这行跑：
//
//	QD_TEST_ARCHIVE_REAL=1 go test ./internal/platform/ -run TestArchiveExtractRealDataDir -v -timeout 40m
func TestArchiveExtractRealDataDir(t *testing.T) {
	if os.Getenv("QD_TEST_ARCHIVE_REAL") == "" {
		t.Skip("重型测试（需打包整个真实数据目录，约 9 分钟）。设置 QD_TEST_ARCHIVE_REAL=1 后运行")
	}
	real := DefaultDataDir()
	if _, err := os.Stat(real); err != nil {
		t.Skipf("真实数据目录不存在，跳过: %v", err)
	}

	work := t.TempDir()
	zipPath := filepath.Join(work, ArchiveName)
	newDir := filepath.Join(work, "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}

	var lastDone int
	var lastTotal int
	n, size, err := ArchiveDataDirWithRewrite(real, zipPath, real, newDir, func(done, total int, doneBytes, totalBytes int64, cur string) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	t.Logf("打包 %d 文件 / %.1f MB（进度终值 %d/%d）", n, float64(size)/(1024*1024), lastDone, lastTotal)

	if n == 0 {
		t.Fatalf("打包结果为空")
	}
	// 进度终值必须等于总数，否则前端进度条会停在 90% 上下不动。
	if lastDone != lastTotal || lastTotal != n {
		t.Errorf("进度终值 %d/%d 与打包条目数 %d 不一致", lastDone, lastTotal, n)
	}

	// 数据库三件套不该进压缩包（正被 SQLite 独占）。
	entries, err := zipEntries(zipPath)
	if err != nil {
		t.Fatalf("读取压缩包失败: %v", err)
	}
	for _, e := range entries {
		if isDatabaseFile(e) {
			t.Errorf("数据库文件不该进压缩包: %s", e)
		}
	}

	// 写死旧路径的配置必须被改写成新路径。
	envFound := false
	for _, e := range entries {
		if strings.HasSuffix(e, "/gopath/env") || e == "runtime/go/env" {
			envFound = true
			content, cerr := readZipEntry(zipPath, e)
			if cerr != nil {
				t.Fatalf("读取压缩包条目失败 %s: %v", e, cerr)
			}
			if strings.Contains(content, real) {
				t.Errorf("压缩包里的 %s 仍含旧目录路径:\n%s", e, content)
			}
			if !strings.Contains(content, newDir) {
				t.Errorf("压缩包里的 %s 未写入新目录路径:\n%s", e, content)
			}
			t.Logf("已改写 %s:\n%s", e, content)
		}
	}
	if !envFound {
		t.Log("真实目录里没有 runtime/go/*/gopath/env（未装 Go 运行时），跳过改写校验")
	}
}
