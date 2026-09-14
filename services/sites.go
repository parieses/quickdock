package services

import (
	"fmt"
	"os"
	"path/filepath"

	envmgr "quickdock/internal/env"
	"quickdock/internal/platform"
	"quickdock/internal/sites"
)

// siteMgr 站点管理器单例，与 httpServe 同构：随包初始化，配置从数据目录懒加载。
// port 传 0 让 sites 用默认 443；用户在配置里改过端口则以 sites.json 为准。
var siteMgr = sites.New(filepath.Join(platform.DefaultDataDir(), "sites"), 0)

// checkSites 站点能力前置检查：环境管理器未就绪时不可用（证书签发依赖它）。
func (a *AppService) checkSites() *ApiResult {
	if a.Env == nil {
		return FailMsg("环境管理器未初始化")
	}
	return nil
}

// SitesList 返回全部站点及服务整体状态（是否运行、端口、证书就绪、hosts 是否同步）。
func (a *AppService) SitesList() *ApiResult {
	return Ok(map[string]any{
		"sites":  siteMgr.List(),
		"status": siteMgr.Status(),
	})
}

// SitesCreate 新增站点：域名 → 本地目录，经 https://<域名> 访问。
func (a *AppService) SitesCreate(name, domain, dir string) *ApiResult {
	s, err := siteMgr.Create(name, domain, dir)
	if err != nil {
		return Fail(err)
	}
	return Ok(s)
}

// SitesUpdate 更新站点（含启用/禁用）。禁用后该域名从证书与 hosts 中移除、不再分发。
func (a *AppService) SitesUpdate(id, name, domain, dir string, enabled bool) *ApiResult {
	s, err := siteMgr.Update(id, name, domain, dir, enabled)
	if err != nil {
		return Fail(err)
	}
	return Ok(s)
}

// SitesDelete 删除站点。
func (a *AppService) SitesDelete(id string) *ApiResult {
	if err := siteMgr.Delete(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// SitesStart 启动内置 HTTPS 站点服务（按域名分发；证书由 mkcert 自动签发）。
func (a *AppService) SitesStart() *ApiResult {
	if r := a.checkSites(); r != nil {
		return r
	}
	if err := siteMgr.Start(); err != nil {
		return Fail(err)
	}
	return Ok(siteMgr.Status())
}

// SitesStop 停止站点服务，并清除 hosts 里 QuickDock 维护的解析区块，
// 避免留下「域名能解析但没有服务在听」的悬空状态。
func (a *AppService) SitesStop() *ApiResult {
	if err := siteMgr.Stop(); err != nil {
		return Fail(err)
	}
	return Ok(siteMgr.Status())
}

// SitesSyncHosts 重新把启用域名写进系统 hosts。
// 独立暴露是因为首次写入常因缺管理员权限失败，用户提权后需要一个明确的重试入口。
func (a *AppService) SitesSyncHosts() *ApiResult {
	if err := siteMgr.SyncHosts(); err != nil {
		return Fail(err)
	}
	return Ok(siteMgr.Status())
}

// SitesSetPort 修改监听端口（443 被占用或无权限时改用 8443 等）。服务运行中不允许改。
func (a *AppService) SitesSetPort(port int) *ApiResult {
	if err := siteMgr.SetPort(port); err != nil {
		return Fail(err)
	}
	return Ok(siteMgr.Status())
}

// SitesGenConfig 为某个站点生成 nginx server 块 / Caddyfile 站点块。
//
// write=true 时同时落盘到「运行时配置文件同级的 quickdock-sites/」目录。
// 刻意不自动改写 nginx.conf / Caddyfile：那是用户自己的主配置，一次误改就是整个
// nginx 起不来；改为让用户手动加一行 include（返回值 IncludeLine 给出确切内容），
// 之后每个站点的片段由 QuickDock 管理、可重复生成。
func (a *AppService) SitesGenConfig(id, backend string, write bool) *ApiResult {
	if r := a.checkSites(); r != nil {
		return r
	}
	be, err := sites.ParseBackend(backend)
	if err != nil {
		return Fail(err)
	}
	if be == sites.BackendBuiltin {
		return FailMsg("内置监听器不需要生成配置")
	}
	site, err := siteMgr.Get(id)
	if err != nil {
		return Fail(err)
	}

	rt := envmgr.Runtime(backend)
	ver, err := a.Env.ResolveVersion(rt, "")
	if err != nil {
		return FailMsg(fmt.Sprintf("请先安装 %s：%v", backend, err))
	}
	confPath, err := a.Env.ConfigPathFor(rt, ver)
	if err != nil {
		return Fail(err)
	}
	// 证书与内置站点服务共用一份：固定文件名 + 覆盖全部启用域名，
	// 因此不依赖内置服务是否在运行也能拿到含本站点的证书。
	certPath, keyPath := siteMgr.CertPaths()

	// 只有装了 PHP 才生成 FastCGI 段：没装就是纯静态站点，多一段无效配置只会误导。
	fpmAddr := ""
	if _, err := a.Env.ResolveVersion(envmgr.RuntimePHP, ""); err == nil {
		fpmAddr = envmgr.FPMAddr()
	}

	res, err := sites.Generate(sites.GenInput{
		Site:     site,
		Backend:  be,
		CertPath: certPath,
		KeyPath:  keyPath,
		// ListenPort 传 0（用 443/80 默认值）：这是给 nginx/caddy 自己监听用的端口，
		// 与内置监听器的端口无关。若用户的 nginx 配了别的 listen，需自行调整这段。
		ListenPort: 0,
		PHPFPMAddr: fpmAddr,
		// 模块选择是站点属性，读站点上存的那份——预览与落盘永远同源，不会出现
		// 「界面上勾了 A、写进磁盘的是 B」。
		Modules:   site.Modules,
		ProxyPort: site.ProxyPort,
	})
	if err != nil {
		return Fail(err)
	}
	targetDir := sites.SuggestedConfDir(confPath)
	out := map[string]any{
		"backend":          res.Backend,
		"snippet":          res.Snippet,
		"fileName":         res.FileName,
		"includeLine":      res.IncludeLine,
		"needsPhpFpm":      res.NeedsPHPFPM,
		"modules":          res.Modules,
		"availableModules": sites.AllModules(),
		"confPath":         confPath,
		"targetDir":        targetDir,
	}
	if write {
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return Fail(err)
		}
		path := filepath.Join(targetDir, res.FileName)
		if err := os.WriteFile(path, []byte(res.Snippet), 0o644); err != nil {
			return Fail(err)
		}
		out["writtenPath"] = path
	}
	return Ok(out)
}

// SitesSetModules 保存站点在「生成配置」里勾选的常用模块（含反向代理上游端口）。
// 模块是站点属性：存下来后下次打开弹窗自动回填，重新生成也能拿到同一份片段。
// 未知 id 会被后端过滤掉，前端版本比后端新时不会写进一份无效配置。
func (a *AppService) SitesSetModules(id string, modules []string, proxyPort int) *ApiResult {
	s, err := siteMgr.SetModules(id, modules, proxyPort)
	if err != nil {
		return Fail(err)
	}
	return Ok(s)
}
