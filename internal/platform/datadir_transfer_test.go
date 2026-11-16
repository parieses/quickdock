package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 整机迁移包的核心保证：DB 快照与 manifest 都进包，其余文件原样；
// 导入端能识别 manifest、拒绝普通 zip，并在解压时把写死的源目录路径改写为目标目录。
func TestTransferArchiveRoundTrip(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "restored")
	zipPath := filepath.Join(root, "backup.zip")
	if err := os.MkdirAll(filepath.Join(src, "runtime", "go"), 0700); err != nil {
		t.Fatal(err)
	}

	// 源目录里放一个普通文件、一个写死源路径的运行时配置、以及 DB 三件套（应被扫描排除）。
	write := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "notes", "a.txt"), "hello")
	write(filepath.Join(src, "runtime", "go", "env"), "GOPATH="+filepath.Join(src, "gopath")+"\n")
	write(filepath.Join(src, DataDirFileName), "LIVE-DB-CONTENT")
	write(filepath.Join(src, DataDirFileName+"-wal"), "wal")

	snap := filepath.Join(root, "snapshot.db")
	write(snap, "VACUUM-SNAPSHOT-CONTENT")

	m := TransferManifest{Format: transferFormatVersion, SourceDir: src, CreatedAt: "now", DBBytes: 21}
	if _, _, err := WriteTransferArchive(src, zipPath, snap, "", m, nil); err != nil {
		t.Fatalf("打包失败: %v", err)
	}

	// 包内应含 DB 快照（我们注入的内容），而不是源目录里那份占位 DB。
	names, err := zipEntries(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(names, "|")
	for _, want := range []string{TransferDBName, TransferManifestName, filepath.ToSlash(filepath.Join("notes", "a.txt"))} {
		if !strings.Contains(joined, want) {
			t.Errorf("包内缺少 %s；实际条目: %v", want, names)
		}
	}
	dbInZip, err := readZipEntry(zipPath, TransferDBName)
	if err != nil || dbInZip != "VACUUM-SNAPSHOT-CONTENT" {
		t.Errorf("包内 DB 快照内容不对: %q err=%v", dbInZip, err)
	}

	// manifest 能读回。
	got, err := ReadTransferManifest(zipPath)
	if err != nil || got.SourceDir != src {
		t.Errorf("读 manifest 失败: %+v err=%v", got, err)
	}

	// 解压并改写路径：runtime/go/env 里的源目录应变成 dst。
	if err := ExtractTransferArchive(zipPath, dst, src, nil); err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	envBytes, err := os.ReadFile(filepath.Join(dst, "runtime", "go", "env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envBytes), src) || !strings.Contains(string(envBytes), dst) {
		t.Errorf("路径未改写: %s", envBytes)
	}
	gotDB, err := os.ReadFile(filepath.Join(dst, TransferDBName))
	if err != nil || string(gotDB) != "VACUUM-SNAPSHOT-CONTENT" {
		t.Errorf("解压出的 DB 不对: %q err=%v", gotDB, err)
	}
}

func TestTransferManifestRejectsBadInput(t *testing.T) {
	if _, err := ParseTransferManifest([]byte(`{"format":99,"sourceDir":"x"}`)); err == nil {
		t.Error("应拒绝不兼容的格式版本")
	}
	if _, err := ParseTransferManifest([]byte(`{"format":1,"sourceDir":""}`)); err == nil {
		t.Error("应拒绝缺少源目录的 manifest")
	}
	if _, err := ParseTransferManifest([]byte(`not json`)); err == nil {
		t.Error("应拒绝损坏的 manifest")
	}
}
