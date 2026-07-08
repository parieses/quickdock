package webdav

import (
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
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

// ---- HTTP 客户端单例（复用 TCP 连接，减少 TIME_WAIT）----

var (
	httpClientOnce sync.Once
	httpClient     *http.Client
)

func newClient() *http.Client {
	httpClientOnce.Do(func() {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			},
			Timeout: 30 * time.Second,
		}
	})
	return httpClient
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

// maxResponseSize 最大响应体大小：10MB
const maxResponseSize int64 = 10 * 1024 * 1024

// sanitizeFilename 校验备份文件名，防止路径穿越
func sanitizeFilename(name string) error {
	if name == "" {
		return fmt.Errorf("文件名不能为空")
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("非法的文件名: %s", name)
	}
	if len(name) > 255 {
		return fmt.Errorf("文件名过长: %d", len(name))
	}
	return nil
}

// readLimited 限制读取大小
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	reader := io.LimitReader(r, limit+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("响应体过大 (超过 %d bytes)", limit)
	}
	return body, nil
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

	body, err := readLimited(resp.Body, maxResponseSize)
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
		body, _ := readLimited(resp.Body, maxResponseSize)
		return "", fmt.Errorf("上传失败 (HTTP %d): %s", resp.StatusCode, string(body))
	}

	return filename, nil
}

// DownloadBackup 从 WebDAV 下载备份并返回 JSON 数据
func DownloadBackup(cfg *Config, filename string) (string, error) {
	if cfg.URL == "" {
		return "", fmt.Errorf("服务器地址不能为空")
	}
	if err := sanitizeFilename(filename); err != nil {
		return "", err
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

	body, err := readLimited(resp.Body, maxResponseSize)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// DeleteBackup 删除 WebDAV 上的备份文件
func DeleteBackup(cfg *Config, filename string) error {
	if err := sanitizeFilename(filename); err != nil {
		return err
	}
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
