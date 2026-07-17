package services

import (
	"fmt"

	"quickdock/internal/webdav"
)

// ===== WebDAV 同步 =====
// 分层: services(服务层) → internal/webdav(HTTP客户端层)

// getWebdavCfg 从数据库读取配置并转为 webdav.Config
func (a *AppService) getWebdavCfg() (*webdav.Config, error) {
	val, err := a.DB.GetValue("webdav_config")
	if err != nil {
		return &webdav.Config{}, nil
	}
	return webdav.UnmarshalConfig(val), nil
}

// GetWebDAVConfig 获取 WebDAV 同步配置
func (a *AppService) GetWebDAVConfig() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, err := a.getWebdavCfg()
	if err != nil {
		return Fail(err)
	}
	return Ok(&WebDAVConfig{
		URL:      cfg.URL,
		Username: cfg.Username,
		Password: cfg.Password,
	})
}

// SetWebDAVConfig 保存 WebDAV 同步配置
func (a *AppService) SetWebDAVConfig(config *WebDAVConfig) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if config == nil {
		return Fail(fmt.Errorf("配置不能为空"))
	}
	inner := &webdav.Config{
		URL:      config.URL,
		Username: config.Username,
		Password: config.Password,
	}
	jsonStr, err := webdav.MarshalConfig(inner)
	if err != nil {
		return Fail(err)
	}
	if err := a.DB.SetValue("webdav_config", jsonStr); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// WebDAVTestConnection 测试 WebDAV 连接
func (a *AppService) WebDAVTestConnection() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, err := a.getWebdavCfg()
	if err != nil {
		return Fail(fmt.Errorf("获取配置失败: %w", err))
	}
	if err := webdav.TestConnection(cfg); err != nil {
		return Fail(err)
	}
	return OkMsg(true, "连接成功")
}

// WebDAVExportBackup 导出当前数据并上传到 WebDAV
func (a *AppService) WebDAVExportBackup() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	jsonData, err := a.DB.ExportFullDataAsJSON()
	if err != nil {
		return Fail(fmt.Errorf("导出数据失败: %w", err))
	}
	cfg, err := a.getWebdavCfg()
	if err != nil {
		return Fail(fmt.Errorf("获取 WebDAV 配置失败: %w", err))
	}
	name, err := webdav.UploadBackup(cfg, jsonData)
	return wrap(name, err)
}

// WebDAVListBackups 列出 WebDAV 服务器上的备份文件
func (a *AppService) WebDAVListBackups() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, err := a.getWebdavCfg()
	if err != nil {
		return Fail(fmt.Errorf("获取 WebDAV 配置失败: %w", err))
	}
	backups, err := webdav.ListBackups(cfg)
	if err != nil {
		return Fail(err)
	}
	result := make([]BackupFile, len(backups))
	for i, b := range backups {
		result[i] = BackupFile{
			Name: b.Name,
			Size: b.Size,
			Time: b.Time,
		}
	}
	return Ok(result)
}

// WebDAVDownaloadAndRestore 从 WebDAV 下载备份并恢复到数据库
func (a *AppService) WebDAVDownaloadAndRestore(filename string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, err := a.getWebdavCfg()
	if err != nil {
		return Fail(fmt.Errorf("获取 WebDAV 配置失败: %w", err))
	}
	jsonData, err := webdav.DownloadBackup(cfg, filename)
	if err != nil {
		return Fail(err)
	}
	if err := a.DB.RestoreFromJSON(jsonData); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// WebDAVDeleteBackup 删除 WebDAV 上的备份文件
func (a *AppService) WebDAVDeleteBackup(filename string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, err := a.getWebdavCfg()
	if err != nil {
		return Fail(fmt.Errorf("获取 WebDAV 配置失败: %w", err))
	}
	if err := webdav.DeleteBackup(cfg, filename); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
