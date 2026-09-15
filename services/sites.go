package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	envmgr "quickdock/internal/env"
	"quickdock/internal/platform"
	"quickdock/internal/sites"
)

// siteMgr 站点管理器单例，与 httpServe 同构：随包初始化，配置从数据目录懒加载。
// 它只维护站点集合、签发证书、同步 hosts —— 不监听任何端口，对外服务由 nginx/caddy 承担。
var siteMgr = sites.New(filepath.Join(platform.DefaultDataDir(), "sites"))

// checkSites 站点能力前置检查：环境管理器未就绪时不可用（证书签发依赖它）。
func (a *AppService) checkSites() *ApiResult {
	if a.Env == nil {
		return FailMsg("环境管理器未初始化")
	}
	return nil
}

// siteBackends 探测 nginx / caddy 两个服务后端的可用性，供「生成配置」弹窗在打开时
// 自动选一个能用的后端、并把装不了的标灰。「点进去什么都没生成」几乎都是默认后端没装引起的，
// 与其让用户点了之后去猜 toast，不如把状态先摆出来。
//
// 这里的 available 同时是「能不能创建站点」的判据：站点由 nginx/caddy 提供服务，
// 两个都没装就没有服务者。
func (a *AppService) siteBackends() []map[string]any {
	out := make([]map[string]any, 0, 2)
	for _, id := range []string{string(sites.BackendNginx), string(sites.BackendCaddy)} {
		item := map[string]any{"id": id, "available": false, "version": "", "reason": ""}
		if a.Env == nil {
			item["reason"] = "环境管理器未初始化"
			out = append(out, item)
			continue
		}
		ver, err := a.Env.ResolveVersion(envmgr.Runtime(id), "")
		if err != nil {
			item["reason"] = err.Error()
			out = append(out, item)
			continue
		}
		item["available"] = true
		item["version"] = ver
		// 服务器当前是否在跑：生成的片段只是磁盘上的文件，不跑就没有人加载它。
		// 前端据此提示「配置已就绪，启动 nginx/caddy 后生效」。
		if st, err := a.Env.Status(envmgr.Runtime(id), ver); err == nil {
			item["running"] = st.Running
		}
		out = append(out, item)
	}
	return out
}

// siteServerAvailable 返回一个已安装的服务软件 id（nginx 优先）。
// 站点必须由 nginx 或 caddy 提供服务 —— QuickDock 自己不监听端口。
func (a *AppService) siteServerAvailable() (string, bool) {
	if a.Env == nil {
		return "", false
	}
	for _, id := range []string{string(sites.BackendNginx), string(sites.BackendCaddy)} {
		if _, err := a.Env.ResolveVersion(envmgr.Runtime(id), ""); err == nil {
			return id, true
		}
	}
	return "", false
}

// requireSiteServer 创建站点前的前置检查。
// 没装服务器软件就建站点，只会留下一份「生成好了但没人加载」的配置和一条解析不到的域名，
// 用户看到的症状是「站点建了但打不开」—— 不如在入口就拦住，把原因说清楚。
func (a *AppService) requireSiteServer() *ApiResult {
	if _, ok := a.siteServerAvailable(); !ok {
		return FailMsg("请先安装 nginx 或 caddy：站点由它们提供服务，QuickDock 只负责生成配置、证书与域名解析")
	}
	return nil
}

// hasPHP 是否装了可用的 PHP（决定生成器是否带 FastCGI 段）。
func (a *AppService) hasPHP() bool {
	if a.Env == nil {
		return false
	}
	_, err := a.Env.ResolveVersion(envmgr.RuntimePHP, "")
	return err == nil
}

// SitesList 返回全部站点与整体状态（证书是否就绪、hosts 是否同步），
// 外加配置生成所依赖的后端可用性与 PHP 是否存在。
func (a *AppService) SitesList() *ApiResult {
	return Ok(map[string]any{
		"sites":    siteMgr.List(),
		"status":   siteMgr.Status(),
		"backends": a.siteBackends(),
		"hasPHP":   a.hasPHP(),
	})
}

// SitesCreate 新增站点：域名 → 本地目录，由 nginx/caddy 以 https://<域名> 对外提供服务。
// docRoot 是对外服务根相对项目目录的子目录（Laravel=public、Yii2=web），空 = 直接用项目目录。
//
// 前置条件：已安装 nginx 或 caddy。没有服务者就建站点，用户只会得到一个打不开的域名。
func (a *AppService) SitesCreate(name, domain, dir, docRoot string) *ApiResult {
	if r := a.checkSites(); r != nil {
		return r
	}
	if r := a.requireSiteServer(); r != nil {
		return r
	}
	s, err := siteMgr.Create(name, domain, dir, docRoot)
	if err != nil {
		return Fail(err)
	}
	return Ok(s)
}

// SitesUpdate 更新站点（含启用/禁用）。禁用后该域名从证书与 hosts 中移除、不再分发。
func (a *AppService) SitesUpdate(id, name, domain, dir string, enabled bool, docRoot string) *ApiResult {
	s, err := siteMgr.Update(id, name, domain, dir, enabled, docRoot)
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

// SitesSyncHosts 重新把启用域名写进系统 hosts。
// 独立暴露是因为首次写入常因缺管理员权限失败，用户提权后需要一个明确的重试入口。
func (a *AppService) SitesSyncHosts() *ApiResult {
	if err := siteMgr.SyncHosts(); err != nil {
		return Fail(err)
	}
	return Ok(siteMgr.Status())
}

// SitesGenConfig 为某个站点生成 nginx server 块 / Caddyfile 站点块，或保存用户改过的片段。
//
// custom 非空 = 「保存」：内容作为该站点该后端的自定义片段持久化（下次打开弹窗回填），
// write=true 时按原样落盘。此时不走生成器 —— 生成器会覆盖掉用户刚改的东西。
// custom 为空 = 「生成/重新生成」：返回生成器的新结果。刻意不动站点上已保存的自定义内容，
// 免得一次预览就把用户存过的东西悄悄清掉；要覆盖自定义，前端把新内容当 custom 再保存一次即可。
//
// write=true 时落盘到「运行时配置文件同级的 quickdock-sites/」目录，并让运行中的运行时
// 重新加载配置；返回值里的 reloaded / reloadError / manualImport 说明这次写入到底生没生效。
//
// 不直接改写用户手写的 nginx.conf / Caddyfile：一次误改就是整个服务起不来。改由 QuickDock
// 生成的那份默认主配置负责 include（首次安装、以及内容仍是历史默认模板时自动就位），
// 用户自己编辑过的主配置只提示 includeLine，由用户决定是否加。
func (a *AppService) SitesGenConfig(id, backend string, write bool, custom string) *ApiResult {
	if r := a.checkSites(); r != nil {
		return r
	}
	be, err := sites.ParseBackend(backend)
	if err != nil {
		return Fail(err)
	}

	isCustom := strings.TrimSpace(custom) != ""
	var site sites.Site
	if isCustom {
		s, err := siteMgr.SetCustomConfig(id, backend, custom)
		if err != nil {
			return Fail(err)
		}
		site = *s
	} else {
		s, err := siteMgr.Get(id)
		if err != nil {
			return Fail(err)
		}
		site = s
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
	// 证书固定文件名 + 覆盖全部启用域名，任何站点都能引用到含自己的那份证书。
	certPath, keyPath := siteMgr.CertPaths()

	// 两件事同时成立才生成 FastCGI 段：本机装了 PHP，且这个站点确实是 PHP 站点。
	// 装了 PHP 不等于每个站点都要跑 PHP —— 给纯静态站点（前端构建产物目录）挂一段 fastcgi_pass
	// 是无用配置，还会让人以为站点依赖 php-fpm 的 9000 端口；反过来，PHP 站点在没装 PHP 的机器上
	// 生成 fastcgi_pass 也只会换来一堆 502。两条都不成立就按纯静态生成。
	fpmAddr := ""
	if a.hasPHP() && sites.SiteNeedsPHP(site) {
		fpmAddr = envmgr.FPMAddr()
	}

	res, err := sites.Generate(sites.GenInput{
		Site:     site,
		Backend:  be,
		CertPath: certPath,
		KeyPath:  keyPath,
		// 站点对外恒为 https 443 + http 80（301 跳转），不可配 —— 服务器软件监听哪个端口
		// 是「环境」页的事，站点页只负责生成「某域名 → 某目录」这一段配置。
		PHPFPMAddr: fpmAddr,
		// 模块选择是站点属性，读站点上存的那份——预览与落盘永远同源，不会出现
		// 「界面上勾了 A、写进磁盘的是 B」。
		Modules:   site.Modules,
		ProxyPort: site.ProxyPort,
	})
	if err != nil {
		return Fail(err)
	}
	// 保存自定义内容时，落盘的是用户那份；其余元信息（文件名、目录、include 行）仍由生成器给出，
	// 它们是纯路径计算，与内容无关。
	snippet := res.Snippet
	if isCustom {
		snippet = custom
	}
	targetDir := sites.SuggestedConfDir(confPath)
	out := map[string]any{
		"backend":          res.Backend,
		"snippet":          snippet,
		"custom":           isCustom,
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
		if err := os.WriteFile(path, []byte(snippet), 0o644); err != nil {
			return Fail(err)
		}
		out["writtenPath"] = path

		// 片段只是磁盘上的一个文件，必须让运行时真的读到它才算生效 —— 否则就是
		// 「配置写好了、访问却打不开」：Caddy/nginx 只认 --config 指定的那份主配置。
		if st, err := a.Env.Status(rt, ver); err == nil && st.Running {
			if err := a.Env.ReloadRuntime(rt, ver); err != nil {
				out["reloadError"] = err.Error()
			} else {
				out["reloaded"] = true
			}
		}
		// 用户自己手写过主配置时 QuickDock 不碰它，此时片段不会被加载，如实告知。
		if needs, err := a.Env.SitesConfigNeedsImport(rt, ver); err == nil && needs {
			out["manualImport"] = true
		}
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
