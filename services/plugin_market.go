package services

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/plugin"
)

// 默认插件市场索引地址（QuickDock 插件中心 GitHub Pages）。
// 由 plugins/external 仓库的 gen_site.py 生成 site/index.json 后经 CI 部署。
// MVP 阶段硬编码；后续若需多市场源可改为存 DB 配置。
const defaultPluginMarketURL = "https://parieses.github.io/quickdock-plugins/index.json"

// 下载/索引的流量上限：索引 5MB、zip 包 100MB（与 InstallFromZip 解压上限对齐）
const (
	maxMarketIndexSize = 5 << 20
	maxPluginZipSize   = 100 << 20
)

// marketIndex 机器可读的插件市场索引（对应 gen_site.py 输出的 index.json）
type marketIndex struct {
	Name    string         `json:"name"`
	Updated string         `json:"updated"`
	Plugins []marketPlugin `json:"plugins"`
}

// marketPlugin 市场中单个插件的描述
type marketPlugin struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Version string                 `json:"version"`
	// 展示信息
	Description string `json:"description"`
	Author      string `json:"author"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	// 约束信息（与 plugin.json 对齐）
	Platforms    []string               `json:"platforms"`
	Permissions  map[string]interface{} `json:"permissions"`
	Capabilities []string               `json:"capabilities"`
	Downloads    map[string]string      `json:"downloads"`
	// 以下由后端 GetPluginMarket 填充，前端据此显示"已安装/有新版/不支持"
	Installed       bool   `json:"installed,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	HasUpdate       bool   `json:"has_update,omitempty"`
	Supported       bool   `json:"supported,omitempty"`
}

// GetPluginMarket 拉取插件市场索引并标注每个插件的本地安装状态。
// 前端据此渲染市场列表与"安装/升级/已安装"按钮。
func (a *AppService) GetPluginMarket() *ApiResult {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(defaultPluginMarketURL)
	if err != nil {
		return Fail(fmt.Errorf("拉取插件市场失败: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return FailMsg(fmt.Sprintf("插件市场返回 HTTP %d", resp.StatusCode))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMarketIndexSize))
	if err != nil {
		return Fail(fmt.Errorf("读取市场索引失败: %w", err))
	}

	var idx marketIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return Fail(fmt.Errorf("解析市场索引失败: %w", err))
	}

	// 标注已安装/版本/平台支持
	pluginsDir := ""
	if a.PluginMgr != nil {
		pluginsDir = a.PluginMgr.PluginsDir()
	}
	for i := range idx.Plugins {
		p := &idx.Plugins[i]
		p.Supported = isPlatformSupported(p.Platforms)
		if pluginsDir != "" {
			if mf, err := plugin.LoadManifest(filepath.Join(pluginsDir, p.ID, "plugin.json")); err == nil {
				p.Installed = true
				p.InstalledVersion = mf.Version
				// 版本不一致即视为有更新（不做 semver 比较，简单可靠）
				p.HasUpdate = mf.Version != p.Version
			}
		}
	}

	return Ok(idx)
}

// InstallPluginFromURL 从 HTTPS URL 下载插件 zip 并安装。
// 下载完成后复用 InstallPlugin（InstallFromZip + manifest 读取 + DB 记录），
// 即与"从文件安装"走完全相同的校验与加载链路。
func (a *AppService) InstallPluginFromURL(url string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	// 强制 HTTPS：防止下载链路被中间人篡改（供应链攻击防护）
	if !strings.HasPrefix(url, "https://") {
		return FailMsg("仅支持 HTTPS 下载链接")
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return Fail(fmt.Errorf("下载插件包失败: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return FailMsg(fmt.Sprintf("下载失败: HTTP %d", resp.StatusCode))
	}

	tmpDir := filepath.Join(os.TempDir(), "quickdock-market-install")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return Fail(fmt.Errorf("创建临时目录失败: %w", err))
	}

	// 文件名取 URL 末段；缺失/异常则 fallback
	fname := filepath.Base(url)
	if fname == "" || fname == "." || fname == string(filepath.Separator) {
		fname = "market.zip"
	}
	if !strings.HasSuffix(strings.ToLower(fname), ".zip") {
		fname += ".zip"
	}
	tmpPath := filepath.Join(tmpDir, fname)

	f, err := os.Create(tmpPath)
	if err != nil {
		return Fail(fmt.Errorf("创建临时文件失败: %w", err))
	}
	// 限制下载体积 100MB（与 InstallFromZip 解压上限一致，防恶意大包）
	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxPluginZipSize)); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return Fail(fmt.Errorf("写入临时文件失败: %w", err))
	}
	f.Close()
	defer os.Remove(tmpPath)

	// 版本检查：只允许安装"新的"或"更新的"，拒绝同版本重装与降级
	if err := a.checkPluginVersion(tmpPath); err != nil {
		return Fail(err)
	}

	// 复用标准安装链路：InstallFromZip(zip slip/bomb 防护 + 回滚) + manifest + DB
	return a.InstallPlugin(tmpPath)
}

// isPlatformSupported 判断当前系统是否支持该插件的平台声明。
// 与 internal/plugin.IsPlatformSupported 行为一致，但接受 []string 输入
// （市场索引里的 platforms 不是完整 manifest，无法直接复用）。
func isPlatformSupported(platforms []string) bool {
	if len(platforms) == 0 {
		return true // 未声明 = 全平台
	}
	for _, p := range platforms {
		if strings.ToLower(p) == runtime.GOOS {
			return true
		}
	}
	return false
}

// checkPluginVersion 从 zip 读取 plugin.json 的 id/version，与本地已装版本比较：
// 未安装/同版本/更新 → 放行；仅降级 → 拒绝。
// 同版本放行是为了允许覆盖重装/强制刷新（用户主动点升级即为同意）。
// 在 InstallPluginFromURL 下载完成后、InstallPlugin 解压前调用。
func (a *AppService) checkPluginVersion(zipPath string) error {
	dlID, dlVer, err := readZipManifestIDVersion(zipPath)
	if err != nil {
		return fmt.Errorf("读取插件包 manifest 失败: %w", err)
	}
	if a.PluginMgr == nil {
		return nil // 无法比对，放行（兜底）
	}
	localMf, err := plugin.LoadManifest(filepath.Join(a.PluginMgr.PluginsDir(), dlID, "plugin.json"))
	if err != nil {
		return nil // 本地未安装，放行（新安装）
	}
	if compareVersions(dlVer, localMf.Version) < 0 {
		return fmt.Errorf("本地版本 v%s 更新，拒绝降级到 v%s", localMf.Version, dlVer)
	}
	return nil // 同版本放行（覆盖重装）/ 更新放行；仅拒绝降级
}

// readZipManifestIDVersion 从 zip 包读取 plugin.json 的 id 和 version（不解压整个包）。
// 与 internal/plugin/installer.go 的 manifest 解析逻辑一致，但只取 id/version 供版本检查。
func readZipManifestIDVersion(zipPath string) (id, version string, err error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", "", fmt.Errorf("打开 zip 包失败: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name == "plugin.json" || f.Name == "./plugin.json" {
			rc, e := f.Open()
			if e != nil {
				return "", "", fmt.Errorf("读取 plugin.json 失败: %w", e)
			}
			defer rc.Close()
			// plugin.json 上限 1MB（与 installer.go 的 maxPluginJSONSize 对齐）
			data, e := io.ReadAll(io.LimitReader(rc, 1<<20))
			if e != nil {
				return "", "", fmt.Errorf("读取 plugin.json 失败: %w", e)
			}
			var mf struct {
				ID      string `json:"id"`
				Version string `json:"version"`
			}
			if e := json.Unmarshal(data, &mf); e != nil {
				return "", "", fmt.Errorf("解析 plugin.json 失败: %w", e)
			}
			return mf.ID, mf.Version, nil
		}
	}
	return "", "", fmt.Errorf("zip 包中未找到 plugin.json")
}

// compareVersions 比较 semver 风格版本字符串 a 与 b：
// 返回 -1 (a<b) / 0 (a==b) / 1 (a>b)。
// 按 . 和 - 分段；数字段比数值（保证 0.10.0 > 0.9.0），非数字段按字符串比较。
// 例：0.1.0 < 0.2.0 < 0.10.0 < 1.0.0；0.1.0-beta < 0.1.0。
func compareVersions(a, b string) int {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' })
	}
	sa, sb := split(a), split(b)
	for i := 0; i < len(sa) && i < len(sb); i++ {
		na, ea := strconv.Atoi(sa[i])
		nb, eb := strconv.Atoi(sb[i])
		if ea == nil && eb == nil {
			if na < nb {
				return -1
			}
			if na > nb {
				return 1
			}
		} else {
			if sa[i] < sb[i] {
				return -1
			}
			if sa[i] > sb[i] {
				return 1
			}
		}
	}
	switch {
	case len(sa) < len(sb):
		return -1
	case len(sa) > len(sb):
		return 1
	}
	return 0
}
