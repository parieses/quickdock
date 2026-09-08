package services

import (
	"context"
	"encoding/json"
	"fmt"

	"quickdock/internal/platform"
	"quickdock/internal/sync"
	"quickdock/internal/webdav"
)

// ===== 统一同步后端 =====
// 分层: services(服务层) → internal/sync(后端抽象) → internal/webdav(HTTP客户端层)
//
// “备份 / 同步”在此统一为“同步后端”。当前仅 WebDAV 一种实现；
// 未来接入 Git / 对象存储时，只需在 internal/sync 注册新后端，本文件与
// internal/db 的核心导出/恢复逻辑无需改动。

const syncConfigKey = "sync_config"

// getSyncConfig 读取统一同步配置（返回明文密码，供构造后端 / 回填 UI）。
// 向后兼容：首次从旧的 webdav_config 键迁移到统一的 sync_config。
func (a *AppService) getSyncConfig() (*sync.Config, error) {
	val, err := a.DB.GetValue(syncConfigKey)
	if err == nil && val != "" && val != "{}" {
		cfg := &sync.Config{}
		if json.Unmarshal([]byte(val), cfg) == nil {
			if cfg.Type == "" {
				cfg.Type = "webdav"
			}
			decryptSyncConfig(cfg)
			return cfg, nil
		}
	}
	// 向后兼容：迁移旧的 webdav_config
	old, err := a.DB.GetValue("webdav_config")
	if err == nil && old != "" && old != "{}" {
		wcfg := webdav.UnmarshalConfig(old)
		cfg := &sync.Config{Type: "webdav", WebDAV: *wcfg}
		decryptSyncConfig(cfg)
		_ = a.saveSyncConfig(cfg) // 迁移到统一键，避免双份配置漂移
		return cfg, nil
	}
	return &sync.Config{Type: "webdav", WebDAV: webdav.Config{}}, nil
}

// decryptSyncConfig 把配置中的 WebDAV 密码解密为明文（仅内存使用）。
func decryptSyncConfig(cfg *sync.Config) {
	if cfg.WebDAV.Password != "" {
		if dec, e := platform.DecryptSecret(cfg.WebDAV.Password); e == nil {
			cfg.WebDAV.Password = dec
		}
		// 解密失败保留原值（兼容迁移期明文）
	}
}

// saveSyncConfig 加密 WebDAV 密码后写入统一的 sync_config 键。
func (a *AppService) saveSyncConfig(cfg *sync.Config) error {
	stored := *cfg
	if stored.WebDAV.Password != "" {
		enc, err := platform.EncryptSecret(stored.WebDAV.Password)
		if err != nil {
			return fmt.Errorf("密码加密失败: %w", err)
		}
		stored.WebDAV.Password = enc
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	return a.DB.SetValue(syncConfigKey, string(b))
}

// GetSyncConfig 获取统一同步配置（密码为明文，供 UI 回填）。
func (a *AppService) GetSyncConfig() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, err := a.getSyncConfig()
	if err != nil {
		return Fail(err)
	}
	return Ok(cfg)
}

// SetSyncConfig 保存统一同步配置。
func (a *AppService) SetSyncConfig(cfg *sync.Config) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if cfg == nil {
		return Fail(fmt.Errorf("配置不能为空"))
	}
	if cfg.Type == "" {
		cfg.Type = "webdav"
	}
	if err := a.saveSyncConfig(cfg); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// SyncListBackends 返回所有可用的同步后端类型（供 UI 渲染选择器）。
func (a *AppService) SyncListBackends() *ApiResult {
	return Ok(sync.AvailableBackends())
}

// buildBackend 根据当前配置构造后端实现。
func (a *AppService) buildSyncBackend() (sync.Backend, error) {
	cfg, err := a.getSyncConfig()
	if err != nil {
		return nil, fmt.Errorf("获取同步配置失败: %w", err)
	}
	return sync.NewBackend(*cfg)
}

// SyncTestConnection 测试当前同步后端连接。
func (a *AppService) SyncTestConnection() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	b, err := a.buildSyncBackend()
	if err != nil {
		return Fail(err)
	}
	if err := b.Test(context.Background()); err != nil {
		return Fail(err)
	}
	return OkMsg(true, "连接成功")
}

// SyncExportBackup 导出当前数据并上传到当前同步后端。
func (a *AppService) SyncExportBackup() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	b, err := a.buildSyncBackend()
	if err != nil {
		return Fail(err)
	}
	jsonData, err := a.DB.ExportFullDataAsJSON()
	if err != nil {
		return Fail(fmt.Errorf("导出数据失败: %w", err))
	}
	name, err := b.Upload(context.Background(), []byte(jsonData))
	return wrap(name, err)
}

// SyncListBackups 列出当前同步后端上的备份文件。
func (a *AppService) SyncListBackups() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	b, err := a.buildSyncBackend()
	if err != nil {
		return Fail(err)
	}
	files, err := b.List(context.Background())
	if err != nil {
		return Fail(err)
	}
	return Ok(files)
}

// SyncDownloadAndRestore 从当前同步后端下载指定备份并恢复到数据库。
func (a *AppService) SyncDownloadAndRestore(filename string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	b, err := a.buildSyncBackend()
	if err != nil {
		return Fail(err)
	}
	data, err := b.Download(context.Background(), filename)
	if err != nil {
		return Fail(err)
	}
	if err := a.DB.RestoreFromJSON(string(data)); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// SyncDeleteBackup 删除当前同步后端上的指定备份文件。
func (a *AppService) SyncDeleteBackup(filename string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	b, err := a.buildSyncBackend()
	if err != nil {
		return Fail(err)
	}
	if err := b.Delete(context.Background(), filename); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
