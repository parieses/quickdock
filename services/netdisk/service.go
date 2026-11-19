// Package netdisk 网盘页门面：把本机 OpenList 网关暴露成「列目录 + 下载」。
//
// 职责边界：
//   - 网关本体（安装/启停/状态/日志/管理员密码托管）归 internal/env 的
//     OpenListRuntime，本包只读它的连接信息；
//   - WebDAV 协议细节归 internal/netdisk（纯客户端），本包只做参数校验、
//     目标路径兜底与进度事件推送。
//
// 为什么不做「多网盘源」表：这一版只接本机 OpenList 网关，地址与账号都由
// 环境管理侧托管，多一张表就等于多一份会漂移的真相。以后要接远程网关时，
// 再在 db 层加源表并把 clientConfig 换成读表即可，本包的对外方法签名不用动。
package netdisk

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/netdisk"
	"quickdock/services"
)

// Downloader 把远端文件交给下载器建任务。
//
// 用接口而不是具体类型：本包不该编译期依赖下载器实现（反过来下载器以后想复用
// 网盘能力就会成环）。main.go 赋值时天然做一次签名检查。
type Downloader interface {
	DlAddNamed(uri, dir, name, optsJSON string) *services.ApiResult
}

// NetdiskService 网盘页门面。
type NetdiskService struct {
	App *services.AppService

	// Downloader 由 main 注入下载器服务；nil 表示下载器未接入（页面给提示）。
	Downloader Downloader

	// proxy 远端文件的本地预览通道（懒启动，见 ensureProxy）。
	mu    sync.Mutex
	proxy *netdisk.ProxyServer
}

// NewNetdiskService 创建服务实例。
func NewNetdiskService(app *services.AppService) *NetdiskService {
	return &NetdiskService{App: app}
}

// GatewayInfo 网关连接概览，前端据此决定是「引导安装」还是「直接列目录」。
type GatewayInfo struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Port      int    `json:"port"`
	AdminURL  string `json:"adminUrl"` // 网页后台（配百度/夸克账号只能在这里做）
	DavURL    string `json:"davUrl"`   // WebDAV 根地址（本页取数用）
	Username  string `json:"username"`
	Version   string `json:"version"`
	// PreviewBase 预览地址前缀，形如 http://127.0.0.1:PORT/<token>/f?p=
	// 前端拼上 base64url(远端路径) 即可给 <video>/<img> 直接使用；文本预览再追加 &text=1。
	// 空串表示代理没起来，页面应退化成「只能下载」。
	PreviewBase string `json:"previewBase"`
	// NeedSetup 网关已运行，但根目录一个存储都没有（或根本列不出来）。
	// 前端据此显示「去管理后台添加网盘」的引导页，而不是把网关的 HTTP 500
	// 原样弹成一条没有下一步动作的 toast。
	NeedSetup bool `json:"needSetup"`
}

// downloadProgress 下载进度事件载荷（quickdock:netdisk:download）。
type downloadProgress struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"` // 服务器未给 Content-Length 时为 -1
	Percent int    `json:"percent"`
	Phase   string `json:"phase"` // downloading | done | error
	Err     string `json:"err"`
}

// NetdiskStatus 返回网关状态。前端首屏调用：未装 → 引导安装；装了没跑 → 给启动按钮。
func (s *NetdiskService) NetdiskStatus() *services.ApiResult {
	if s.App == nil || s.App.Env == nil {
		return services.FailMsg("环境管理未初始化")
	}
	rt := s.App.Env.OpenListRuntime()
	if rt == nil {
		return services.FailMsg("网盘网关未注册")
	}
	info := GatewayInfo{
		Port: rt.Port(),
		// 直达后台登录页（OpenList 前端路由为 /@manage）。指向根路径会停在文件
		// 浏览页，而用户点这个按钮的目的几乎总是「去加网盘账号」。
		AdminURL: rt.BaseURL() + "/@manage",
		DavURL:   rt.BaseURL() + "/dav",
		Username: rt.AdminUser(),
	}
	for _, ins := range rt.InstalledVersions() {
		info.Installed = true
		info.Version = ins.Version
		break
	}
	st := rt.Status("")
	info.Running = st.Running
	if st.Version != "" {
		info.Version = st.Version
	}
	if info.Running {
		info.NeedSetup = s.gatewayNeedSetup()
	}
	// 预览代理按需启动：只有页面真的打开才占一个本地端口。
	if p := s.ensureProxy(); p != nil {
		info.PreviewBase = p.Base() + "/f?p="
	}
	return services.Ok(info)
}

// Startup 应用启动时预热预览代理。
//
// aria2 会按 aria2.session 恢复网盘任务，恢复瞬间就来拉流；如果代理等到「用户
// 打开网盘页」才监听，这批任务会先撞 502，等 aria2 把重试次数耗尽就变成错误。
// 启动即监听的成本只是一个回环端口（与 fsview 的媒体服务同量级），换来的是
// 重启后网盘任务能直接续传。
func (s *NetdiskService) Startup() {
	s.ensureProxy()
}

// ensureProxy 懒启动远端文件的本地预览通道。
//
// 失败不阻断页面：返回 nil，前端看到 previewBase 为空就只显示「下载」，
// 而不是让整页因为「起不来一个端口」而不可用。
func (s *NetdiskService) ensureProxy() *netdisk.ProxyServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxy != nil {
		return s.proxy
	}
	p, err := netdisk.StartProxy(func() (*netdisk.Client, error) {
		c, r := s.client()
		if r != nil {
			return nil, errors.New(r.Msg)
		}
		return c, nil
	})
	if err != nil {
		logger.W("[netdisk] 启动预览代理失败: %v", err)
		return nil
	}
	s.proxy = p
	return p
}

// gatewayNeedSetup 判断网关是否还停在「没添加存储 / 根目录列不出来」的初始态。
//
// 为什么在 Status 里探测：没有存储时 OpenList 的 WebDAV 根目录会直接失败
// （细节只在它自己的日志里 "storage not found; please add a storage first"），
// 首次装完点进网盘页只会看到一条 HTTP 500 toast，用户不知道下一步该做什么。
// 这里用一次短超时的根目录 PROPFIND 把「500」变成可引导的状态：
//   - 列得出且非空 → 已配置好；
//   - 列得出但为空 → 根目录没有任何挂载点，就是没添加存储；
//   - 列不出来     → 网关没准备好（无存储 / 驱动未登录 / 刚启动），显示引导页
//     让用户去后台确认，回来点「刷新」。
//
// 401、连不上、SQLITE_BUSY 三种例外一律返回 false：前两者是瞬时状态，写锁死了
// hint() 另有「重启网关」的专门指引，都不该被误读成「没加存储」。
//
// 探测本身失败也返回 false：Status 是状态概览，不能因为一次探测把正常页锁死。
func (s *NetdiskService) gatewayNeedSetup() bool {
	c, r := s.client()
	if r != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entries, err := c.List(ctx, "/")
	if err != nil {
		if errors.Is(err, netdisk.ErrUnauthorized) ||
			strings.Contains(err.Error(), "无法连接") ||
			s.gatewayDatabaseLocked() {
			return false
		}
		return true
	}
	return len(entries) == 0
}

// NetdiskListDir 列出网关上的目录。path 为空表示根（各网盘的挂载点都在这层）。
func (s *NetdiskService) NetdiskListDir(path string) *services.ApiResult {
	c, r := s.client()
	if r != nil {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	entries, err := c.List(ctx, path)
	if err != nil {
		return services.FailMsg(s.hint(err))
	}
	return services.Ok(entries)
}

// NetdiskTest 测试网关 WebDAV 是否可用（前端「重试连接」用）。
func (s *NetdiskService) NetdiskTest() *services.ApiResult {
	c, r := s.client()
	if r != nil {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Test(ctx); err != nil {
		return services.FailMsg(s.hint(err))
	}
	return services.Ok(nil)
}

// hint 把网关的典型失败翻译成「用户能照着做」的一句话。
//
// 三种已知成因：
//  1. 401：密码库里的值与网关库不一致（data/ 被重建、手工改过密码、上次 set 失败）。
//     重启动网关会让 Start 里的 ensureAdminPassword 用库里的值重新 set 一遍。
//  2. 500 + 网关日志出现 `database is locked`：OpenList 在一次写事务失败后留下永久
//     写锁，自身无法自愈。WebDAV 那一侧只有 "Internal Server Error"，不读它的日志
//     就永远查不出原因，只能重启进程。
//  3. 其它 500：多半是网关还没配置好（一个存储都没加、驱动未登录、刚启动还没
//     加载完）。首屏已有引导页，这里再兜一句可操作的指引，别只甩一个状态码。
func (s *NetdiskService) hint(err error) string {
	if errors.Is(err, netdisk.ErrUnauthorized) {
		return "网关拒绝认证：QuickDock 记的管理员密码与网关库不一致。到「环境管理 → OpenList」点停止再启动，会自动同步密码"
	}
	if strings.Contains(err.Error(), "HTTP 500") && s.gatewayDatabaseLocked() {
		return "网关数据库被锁死（SQLITE_BUSY）：OpenList 在一次写库失败后留下了永久写锁，只能重启解除。点右上角「重启网关」或到「环境管理 → OpenList」停止再启动。"
	}
	if strings.Contains(err.Error(), "HTTP 500") {
		return "网关返回 500：多半是还没添加存储或网盘账号未登录。先点「管理后台」添加百度/夸克等网盘并确认能打开目录，回来点「刷新」；仍失败就点「重启网关」。"
	}
	return err.Error()
}

// gatewayDatabaseLocked 读网关日志判断是不是撞上了 sqlite 死锁。
// 取不到适配器时一律当 false：不要把一个不确定的推断塞进提示里。
func (s *NetdiskService) gatewayDatabaseLocked() bool {
	if s.App == nil || s.App.Env == nil {
		return false
	}
	rt := s.App.Env.OpenListRuntime()
	if rt == nil {
		return false
	}
	return rt.DatabaseLocked()
}

// NetdiskDownload 把网盘文件下载到本地路径（异步，进度走事件）。
// destPath 由前端经保存对话框传入，这里只做绝对路径校验与目录兜底。
func (s *NetdiskService) NetdiskDownload(remotePath, destPath string) *services.ApiResult {
	remotePath = strings.TrimSpace(remotePath)
	destPath = strings.TrimSpace(destPath)
	if remotePath == "" || destPath == "" {
		return services.FailMsg("下载路径不能为空")
	}
	if !filepath.IsAbs(destPath) {
		return services.FailMsg("保存路径必须是绝对路径")
	}
	c, r := s.client()
	if r != nil {
		return r
	}
	go s.runDownload(c, remotePath, destPath)
	return services.Ok(nil)
}

// runDownload 实际下载协程：所有失败都以事件收尾，不留悬挂进度。
func (s *NetdiskService) runDownload(c *netdisk.Client, remotePath, destPath string) {
	emit := func(p downloadProgress) {
		if s.App == nil || s.App.App() == nil {
			return
		}
		s.App.App().Event.Emit("quickdock:netdisk:download", p)
	}
	name := baseName(remotePath)
	base := downloadProgress{Name: name, Path: remotePath}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		base.Phase, base.Err = "error", "创建本地目录失败："+err.Error()
		emit(base)
		return
	}
	// 原子落盘：先写同目录下的 <dest>.part，成功后再 rename 覆盖目标文件。
	// 直接写 destPath 的话，中途断网/杀掉进程会把用户已有的同名文件截成半截，
	// 而覆盖语义只应在「下载完整成功」时生效。
	partPath := destPath + ".part"
	f, err := os.Create(partPath)
	if err != nil {
		base.Phase, base.Err = "error", "无法创建本地文件："+err.Error()
		emit(base)
		return
	}
	// 失败收尾：只删半成品，目标文件（若已存在）保持原样。
	fail := func(msg string) {
		_ = f.Close()
		_ = os.Remove(partPath)
		base.Phase, base.Err = "error", msg
		emit(base)
	}

	// 百分比节流：Content-Length 未知（total<=0）时按每 1MB 推一次。
	lastPercent, lastMB := -1, int64(-1)
	onProgress := func(done, total int64) {
		if total > 0 {
			if pct := int(done * 100 / total); pct != lastPercent {
				lastPercent = pct
				emit(downloadProgress{Name: name, Path: remotePath,
					Done: done, Total: total, Percent: pct, Phase: "downloading"})
			}
			return
		}
		if mb := done >> 20; mb != lastMB {
			lastMB = mb
			emit(downloadProgress{Name: name, Path: remotePath,
				Done: done, Total: -1, Phase: "downloading"})
		}
	}

	total, err := c.Download(context.Background(), remotePath, f, onProgress)
	if err != nil {
		fail(err.Error()) // 只删 .part，用户已有的同名文件保持原样
		return
	}
	// 落盘完成才 rename：写 .part 只保证「不破坏目标」，rename 才是原子替换。
	if err := f.Close(); err != nil {
		fail("写入本地文件失败：" + err.Error())
		return
	}
	if err := os.Rename(partPath, destPath); err != nil {
		fail("替换目标文件失败：" + err.Error())
		return
	}
	base.Phase, base.Done, base.Total, base.Percent = "done", total, total, 100
	emit(base)
}

// client 组装 WebDAV 客户端：地址与账号都取自网关适配器。
func (s *NetdiskService) client() (*netdisk.Client, *services.ApiResult) {
	if s.App == nil || s.App.Env == nil {
		return nil, services.FailMsg("环境管理未初始化")
	}
	rt := s.App.Env.OpenListRuntime()
	if rt == nil {
		return nil, services.FailMsg("网盘网关未注册")
	}
	if st := rt.Status(""); !st.Running {
		return nil, services.FailMsg("网盘网关未运行，请先启动 OpenList")
	}
	pwd := rt.AdminPassword()
	if pwd == "" {
		// 密码未托管（例如整库锁定，或用户手工安装的网关）：照常尝试匿名/已存
		// 凭据以外的方式只会得到 401，这里提前给出可操作的提示。
		return nil, services.FailMsg("未取到网关管理员密码，请在 OpenList 后台登录后到「密码库」补一条账号，或重启网关重新托管")
	}
	c, err := netdisk.New(netdisk.Config{
		Endpoint: rt.BaseURL() + "/dav",
		Username: rt.AdminUser(),
		Password: pwd,
	})
	if err != nil {
		return nil, services.FailMsg(err.Error())
	}
	return c, nil
}

// ---- 文件管理 ----

// NetdiskMkdir 在 parent 目录下新建一级目录。
func (s *NetdiskService) NetdiskMkdir(parent, name string) *services.ApiResult {
	name, err := validName(name)
	if err != nil {
		return services.FailMsg(err.Error())
	}
	c, r := s.client()
	if r != nil {
		return r
	}
	target := joinRemote(parent, name)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Mkdir(ctx, target); err != nil {
		return services.FailMsg(s.hint(err))
	}
	return services.Ok(target)
}

// NetdiskRename 重命名（同目录改名）。newName 只含名字，不含路径。
func (s *NetdiskService) NetdiskRename(remotePath, newName string) *services.ApiResult {
	// 先剪掉尾斜杠：path.Dir("/a/b/") 会得到 "/a/b"，拼出来的新路径就多了一层。
	remotePath = strings.TrimRight(strings.TrimSpace(remotePath), "/")
	if remotePath == "" {
		return services.FailMsg("路径不能为空")
	}
	newName, err := validName(newName)
	if err != nil {
		return services.FailMsg(err.Error())
	}
	target := joinRemote(path.Dir(remotePath), newName)
	if target == remotePath {
		return services.Ok(target)
	}
	c, r := s.client()
	if r != nil {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Move(ctx, remotePath, target); err != nil {
		return services.FailMsg(s.hint(err))
	}
	return services.Ok(target)
}

// NetdiskDelete 批量删除。逐个删，失败的收集起来一起回报——
// 一个文件删不掉（被占用/权限）不该让其余选中项也跟着失败。
func (s *NetdiskService) NetdiskDelete(pathsJSON string) *services.ApiResult {
	paths, err := decodePaths(pathsJSON)
	if err != nil {
		return services.FailMsg("路径列表格式不正确")
	}
	if len(paths) == 0 {
		return services.FailMsg("没有选中任何项目")
	}
	c, r := s.client()
	if r != nil {
		return r
	}
	deleted, failed := 0, make([]string, 0, len(paths))
	for _, p := range paths {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		derr := c.Delete(ctx, p)
		cancel()
		if derr != nil {
			if errors.Is(derr, netdisk.ErrUnauthorized) {
				return services.FailMsg(s.hint(derr))
			}
			failed = append(failed, baseName(p)+"："+derr.Error())
			continue
		}
		deleted++
	}
	if deleted == 0 {
		return services.FailMsg(strings.Join(failed, "；"))
	}
	return services.OkMsg(map[string]any{"deleted": deleted, "failed": failed}, "已删除")
}

// NetdiskSearch 在指定目录下按名称递归搜索（护栏见 netdisk.SearchOptions）。
//
// 超时但已找到一部分时返回那一部分而不是报错：网盘搜索本来就是「尽力而为」，
// 让用户看到部分结果比看到一个红色错误有用。
func (s *NetdiskService) NetdiskSearch(remotePath, keyword string) *services.ApiResult {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return services.Ok([]netdisk.Entry{})
	}
	c, r := s.client()
	if r != nil {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	items, err := c.Search(ctx, remotePath, keyword, netdisk.SearchOptions{})
	if err != nil && len(items) == 0 {
		return services.FailMsg(s.hint(err))
	}
	return services.Ok(items)
}

// validName 校验用户输入的单段名字（新建目录 / 重命名）。
//
// 名字里出现分隔符等于让用户从 UI 拼远端路径，会绕过「只改这一段」的预期，
// 所以一律拒绝；".." 也必须挡掉。
func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("名称不能为空")
	case name == "." || name == "..":
		return "", errors.New("名称不合法")
	case strings.ContainsAny(name, `/\`):
		return "", errors.New("名称不能包含 / 或 \\")
	case len([]rune(name)) > 120:
		return "", errors.New("名称不能超过 120 个字符")
	}
	return name, nil
}

// joinRemote 拼远端路径。不直接用 path.Join：它会清洗 ".."，而合法文件名里
// 本不该出现 ".."，清洗只会把一个非法输入悄悄变成另一个路径。
func joinRemote(dir, name string) string {
	dir = strings.TrimRight(strings.TrimSpace(dir), "/")
	if dir == "" {
		return "/" + name
	}
	return dir + "/" + name
}

// decodePaths 解析前端传来的 JSON 路径数组（空项已剔除）。
func decodePaths(raw string) ([]string, error) {
	var in []string
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// ---- 上传 / 批量下载 / 交给下载器 ----

// uploadProgress 上传进度事件载荷（quickdock:netdisk:upload）。
// 字段名与下载事件对齐，前端可以共用一套进度行渲染。
type uploadProgress struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Percent int    `json:"percent"`
	Phase   string `json:"phase"` // uploading | done | error
	Err     string `json:"err"`
	Index   int    `json:"index"` // 第几个 / 共几个，批量上传时显示 "2/5"
	Count   int    `json:"count"`
}

// NetdiskUpload 把本机选中的文件上传到 remoteDir（异步，进度走事件）。
func (s *NetdiskService) NetdiskUpload(localPathsJSON, remoteDir string) *services.ApiResult {
	locals, err := decodePaths(localPathsJSON)
	if err != nil {
		return services.FailMsg("文件列表格式不正确")
	}
	if len(locals) == 0 {
		return services.FailMsg("没有选择文件")
	}
	c, r := s.client()
	if r != nil {
		return r
	}
	go s.runUploads(c, locals, remoteDir)
	return services.Ok(len(locals))
}

// runUploads 逐个上传。串行而不是并发：网盘上传本就是带宽瓶颈，并发只会让
// 每个文件都变慢，还会触发网关的连接数限制。
func (s *NetdiskService) runUploads(c *netdisk.Client, locals []string, remoteDir string) {
	emit := func(p uploadProgress) {
		if s.App == nil || s.App.App() == nil {
			return
		}
		s.App.App().Event.Emit("quickdock:netdisk:upload", p)
	}
	count := len(locals)
	for i, lp := range locals {
		name := filepath.Base(lp)
		row := uploadProgress{
			Name: name, Path: joinRemote(remoteDir, name),
			Index: i + 1, Count: count, Phase: "uploading", Total: -1,
		}
		st, err := os.Stat(lp)
		if err != nil {
			row.Phase, row.Err = "error", "找不到本地文件："+err.Error()
			emit(row)
			continue
		}
		if st.IsDir() {
			row.Phase, row.Err = "error", "暂不支持上传文件夹"
			emit(row)
			continue
		}
		f, err := os.Open(lp)
		if err != nil {
			row.Phase, row.Err = "error", "无法读取本地文件："+err.Error()
			emit(row)
			continue
		}
		row.Total = st.Size()
		emit(row)
		uerr := c.Upload(context.Background(), row.Path, f, st.Size(), func(done, total int64) {
			pct := 0
			if total > 0 {
				pct = int(done * 100 / total)
			}
			emit(uploadProgress{
				Name: name, Path: row.Path, Done: done, Total: total, Percent: pct,
				Phase: "uploading", Index: i + 1, Count: count,
			})
		})
		f.Close()
		if uerr != nil {
			row.Phase, row.Err = "error", s.hint(uerr)
			emit(row)
			continue
		}
		row.Done, row.Percent, row.Phase = st.Size(), 100, "done"
		emit(row)
	}
}

// NetdiskDownloadBatch 把选中的文件下载到同一个本地目录（每个文件一条进度事件）。
func (s *NetdiskService) NetdiskDownloadBatch(pathsJSON, destDir string) *services.ApiResult {
	paths, err := decodePaths(pathsJSON)
	if err != nil {
		return services.FailMsg("路径列表格式不正确")
	}
	if len(paths) == 0 {
		return services.FailMsg("没有选中任何文件")
	}
	destDir = strings.TrimSpace(destDir)
	if !filepath.IsAbs(destDir) {
		return services.FailMsg("保存目录必须是绝对路径")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return services.FailMsg("创建保存目录失败：" + err.Error())
	}
	c, r := s.client()
	if r != nil {
		return r
	}
	// used 记下本批已分配的路径：两个同名的远端文件（不同目录）在同一批里
	// 都会 Stat 到「目标不存在」，只看磁盘就会出现两个任务写同一个文件。
	used := make(map[string]bool, len(paths))
	for _, p := range paths {
		dest := uniqueDest(destDir, baseName(p), used)
		used[dest] = true
		go s.runDownload(c, p, dest)
	}
	return services.Ok(len(paths))
}

// uniqueDest 同名文件不覆盖：依次试 "名字 (1).ext"。
// used 是本批已分配的目标（磁盘上还不存在），只有真的撞名才进循环。
func uniqueDest(dir, name string, used map[string]bool) string {
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); err != nil && !used[dest] {
		return dest
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, stem+" ("+strconv.Itoa(i)+")"+ext)
		if _, err := os.Stat(cand); err != nil && !used[cand] {
			return cand
		}
	}
	return dest
}

// NetdiskSendToDownloader 把选中的文件交给 aria2 下载（下载器页可见进度）。
//
// 为什么不是把网关地址直接给 aria2：网关要 Basic Auth，而 aria2 的 http-user /
// http-pass 会明文进任务记录；而且网关地址一旦换了端口就全失效。走本机代理
// （带随机令牌、Range 透传）既免了凭据外泄，也让 aria2 的分段请求能落到网关上。
func (s *NetdiskService) NetdiskSendToDownloader(pathsJSON string) *services.ApiResult {
	paths, err := decodePaths(pathsJSON)
	if err != nil {
		return services.FailMsg("路径列表格式不正确")
	}
	if len(paths) == 0 {
		return services.FailMsg("没有选中任何文件")
	}
	if s.Downloader == nil {
		return services.FailMsg("下载器未接入，请重启 QuickDock")
	}
	p := s.ensureProxy()
	if p == nil {
		return services.FailMsg("本地代理通道未启动，无法把网盘文件交给下载器")
	}
	added, failed := 0, make([]string, 0, len(paths))
	for _, rp := range paths {
		name := baseName(rp)
		opts, _ := json.Marshal(map[string]string{
			"out": name,
			// 单连接：网关与各家网盘对多段 Range 的支持参差，夸克多连接还容易
			// 触发限流。网盘任务宁可慢一点，也不要下一半出错。
			"split":                     "1",
			"max-connection-per-server": "1",
		})
		res := s.Downloader.DlAddNamed(p.URL(rp, false), "", name, string(opts))
		if res == nil || res.Code != 0 {
			msg := "未知错误"
			if res != nil {
				msg = res.Msg
			}
			failed = append(failed, name+"："+msg)
			continue
		}
		added++
	}
	if added == 0 {
		return services.FailMsg(strings.Join(failed, "；"))
	}
	return services.OkMsg(map[string]any{"added": added, "failed": failed}, "已交给下载器")
}

// baseName 取远端路径最后一段作为展示名（不引 path 包只为这一个用途）。
func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
