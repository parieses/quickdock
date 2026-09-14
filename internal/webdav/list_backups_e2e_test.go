package webdav_test

import (
	"testing"

	"quickdock/internal/webdav"
	"quickdock/internal/webdavsrv"
)

// TestListBackupsE2E 端到端验证：起内置 WebDAV 服务端 → 上传 → 列举。
// 回归 guards 之前的 bug：ListBackups 把文件的 <resourcetype/> 误判为集合而全部跳过，列表恒为空。
func TestListBackupsE2E(t *testing.T) {
	dir := t.TempDir()
	if _, err := webdavsrv.Default.Start(webdavsrv.Config{
		Root: dir, Username: "admin", Password: "secret", Port: 19080,
	}); err != nil {
		t.Fatal(err)
	}
	defer webdavsrv.Default.Stop()

	cfg := &webdav.Config{URL: "http://127.0.0.1:19080", Username: "admin", Password: "secret"}
	if _, err := webdav.UploadBackup(cfg, `{"x":1}`); err != nil {
		t.Fatalf("upload err: %v", err)
	}
	files, err := webdav.ListBackups(cfg)
	if err != nil {
		t.Fatalf("list err: %v", err)
	}
	t.Logf("LIST RESULT: %+v (count=%d)", files, len(files))
	if len(files) == 0 {
		t.Fatalf("BUG: ListBackups returned 0 files although upload succeeded")
	}
}
