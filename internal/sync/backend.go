// Package sync 定义统一的“同步后端”抽象。
//
// 设计目标：把“备份 / 同步”收敛为一个后端接口。当前 WebDAV 是一种实现；
// 未来要接入 Git / 对象存储(OSS/S3) 时，只需在 NewBackend 注册一个新的
// Backend 实现，上层服务（services）与核心导出/恢复逻辑（internal/db）完全不动。
//
// 数据流：
//
//	services(Sync*)  ──►  sync.NewBackend(cfg)  ──►  Backend 接口
//	                           │                       │
//	                  internal/db.ExportFullDataAsJSON()│  Upload/Download/List/Delete
//	                  internal/db.RestoreFromJSON()   ▼
//	                                         具体后端(webdav/git/s3...)
package sync

import (
	"context"
	"fmt"

	"quickdock/internal/webdav"
)

// FileInfo 后端上的备份文件元信息（跨后端统一结构）。
type FileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"`
}

// Backend 同步后端抽象。任何“能把数据存到远端/取回来”的介质都实现本接口。
// 方法统一接收 context 以支持超时与取消；当前 WebDAV 实现未使用 ctx，仅保留扩展性。
type Backend interface {
	// Type 返回后端类型标识（与 Config.Type 对应），用于 UI 展示与配置分发。
	Type() string
	// Test 校验后端连通性 / 配置有效性，成功返回 nil。
	Test(ctx context.Context) error
	// List 列出已有备份文件。
	List(ctx context.Context) ([]FileInfo, error)
	// Upload 上传备份数据，返回后端生成的文件名。
	Upload(ctx context.Context, data []byte) (string, error)
	// Download 按文件名下载备份数据。
	Download(ctx context.Context, name string) ([]byte, error)
	// Delete 删除指定备份文件。
	Delete(ctx context.Context, name string) error
}

// Config 统一同步配置：指定当前启用的后端类型及其专属配置。
// 新增后端时只需在此增加对应字段（如 Git *GitConfig、S3 *S3Config），
// 上层逻辑无需改动。
type Config struct {
	// Type 当前启用的后端类型："webdav" / ""(未启用)。
	Type string `json:"type"`
	// WebDAV 当 Type=="webdav" 时有效。
	WebDAV webdav.Config `json:"webdav"`
}

// BackendInfo 可供 UI 选择的后端元信息（含展示名与说明）。
type BackendInfo struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// AvailableBackends 返回所有已注册的后端类型，供前端渲染选择器。
// 新增后端时在此追加一项即可出现在 UI 中。
func AvailableBackends() []BackendInfo {
	return []BackendInfo{
		{Type: "webdav", Name: "WebDAV", Desc: "Nextcloud / ownCloud 等兼容服务"},
	}
}

// NewBackend 根据配置构造对应的后端实现。
// 新增后端：在此 switch 增加 case 并返回对应实现即可。
func NewBackend(cfg Config) (Backend, error) {
	switch cfg.Type {
	case "webdav":
		return NewWebDAVBackend(&cfg.WebDAV), nil
	case "":
		return nil, fmt.Errorf("未启用任何同步后端，请先在同步设置中选择后端")
	default:
		return nil, fmt.Errorf("不支持的同步后端: %s", cfg.Type)
	}
}
