package webdav

import (
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// Config WebDAV 服务器配置
type Config struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// BackupFile WebDAV 上的备份文件信息
type BackupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"`
}

// ---- PROPFIND response XML structures ----

type multistatus struct {
	XMLName  xml.Name   `xml:"DAV: multistatus"`
	Response []response `xml:"response"`
}

type response struct {
	Href    string   `xml:"href"`
	Propstat propstat `xml:"propstat"`
}

type propstat struct {
	Prop   prop   `xml:"prop"`
	Status string `xml:"status"`
}

type prop struct {
	DisplayName     string        `xml:"displayname"`
	GetContentLength string       `xml:"getcontentlength"`
	GetLastModified  string       `xml:"getlastmodified"`
	ResourceType    *resourceType `xml:"resourcetype"`
}

type resourceType struct {
	Collection string `xml:"collection"`
}

// HTTP 客户端创建与请求构建

func newClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
		},
		Timeout: 30 * time.Second,
	}
}

func newRequest(cfg *Config, method, urlPath string, body io.Reader) (*http.Request, error) {
	baseURL := strings.TrimRight(cfg.URL, "/")
	fullURL := baseURL + "/" + strings.TrimLeft(urlPath, "/")
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, err
	}
	if cfg.Username != "" || cfg.Password != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
	return req, nil
}

// ---- 公开 API ----

// TestConnection 测试 WebDAV 连接，成功返回 nil，失败返回错误
func TestConnection(cfg *Config) error {
	if cfg.URL == "" {
		return fmt.Errorf("服务器地址不能为空")
	}
	client := newClient()
	req, err := newRequest(cfg, "PROPFIND", "/", nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("服务器返回状态码: %d", resp.StatusCode)
}

// ListBackups 列出 WebDAV 服务器上的备份文件
func ListBackups(cfg *Config) ([]BackupFile, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("服务器地址不能为空")
	}
	client := newClient()
	req, err := newRequest(cfg, "PROPFIND", "/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("列出文件失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("服务器返回状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var ms multistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	var files []BackupFile
	for _, r := range ms.Response {
		if r.Propstat.Prop.ResourceType != nil && r.Propstat.Prop.ResourceType.Collection != "" {
			continue
		}
		name := strings.TrimRight(r.Href, "/")
		name = path.Base(name)
		if !strings.HasPrefix(name, "quickdock-backup") {
			continue
		}
		var fileSize int64
		fmt.Sscanf(r.Propstat.Prop.GetContentLength, "%d", &fileSize)
		files = append(files, BackupFile{
			Name: name,
			Size: fileSize,
			Time: r.Propstat.Prop.GetLastModified,
		})
	}
	return files, nil
}

// UploadBackup 上传备份到 WebDAV，返回文件名
func UploadBackup(cfg *Config, jsonData string) (string, error) {
	if cfg.URL == "" {
		return "", fmt.Errorf("服务器地址不能为空")
	}
	client := newClient()

	filename := fmt.Sprintf("quickdock-backup-%s.json", time.Now().Format("20060102-150405"))
	req, err := newRequest(cfg, "PUT", "/"+filename, strings.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("上传失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("上传失败 (HTTP %d): %s", resp.StatusCode, string(body))
	}

	return filename, nil
}

// DownloadBackup 从 WebDAV 下载备份并返回 JSON 数据
func DownloadBackup(cfg *Config, filename string) (string, error) {
	if cfg.URL == "" {
		return "", fmt.Errorf("服务器地址不能为空")
	}
	client := newClient()
	req, err := newRequest(cfg, "GET", "/"+filename, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("下载失败 (HTTP %d)", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// DeleteBackup 删除 WebDAV 上的备份文件
func DeleteBackup(cfg *Config, filename string) error {
	client := newClient()
	req, err := newRequest(cfg, "DELETE", "/"+filename, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("删除失败 (HTTP %d)", resp.StatusCode)
	}
	return nil
}
