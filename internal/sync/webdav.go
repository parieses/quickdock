package sync

import (
	"context"

	"quickdock/internal/webdav"
)

// WebDAVBackend 把 internal/webdav 的 HTTP 客户端适配为统一的 Backend 接口。
// 这样 internal/db 的核心导出/恢复逻辑与具体传输协议解耦。
type WebDAVBackend struct {
	cfg *webdav.Config
}

// NewWebDAVBackend 构造 WebDAV 后端实现。
func NewWebDAVBackend(cfg *webdav.Config) *WebDAVBackend {
	return &WebDAVBackend{cfg: cfg}
}

func (b *WebDAVBackend) Type() string { return "webdav" }

func (b *WebDAVBackend) Test(_ context.Context) error {
	return webdav.TestConnection(b.cfg)
}

func (b *WebDAVBackend) List(_ context.Context) ([]FileInfo, error) {
	files, err := webdav.ListBackups(b.cfg)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(files))
	for _, f := range files {
		out = append(out, FileInfo{Name: f.Name, Size: f.Size, Time: f.Time})
	}
	return out, nil
}

func (b *WebDAVBackend) Upload(_ context.Context, data []byte) (string, error) {
	return webdav.UploadBackup(b.cfg, string(data))
}

func (b *WebDAVBackend) Download(_ context.Context, name string) ([]byte, error) {
	s, err := webdav.DownloadBackup(b.cfg, name)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

func (b *WebDAVBackend) Delete(_ context.Context, name string) error {
	return webdav.DeleteBackup(b.cfg, name)
}
