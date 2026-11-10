package download

// 换电脑续传的导入门面。
//
// aria2 的续传凭据不在 QuickDock 数据库里，而在磁盘上这两样东西：
//   1. <名字>.aria2  控制文件（分片位图 / piece hash，记录哪些块已完成）
//   2. <名字>        已下载的数据文件
// 加上 aria2.session（--save-session 每 60s 写一次）里的原下载链接与选项。
// 会话里的 dir 属于旧电脑，导入时绝不能照抄：按新机器上实际扫描到的
// 控制文件所在目录重写 dir，再用 addUri + out + continue=true 重新提交，
// aria2 就会命中已有的 .aria2 从原进度继续。
//
// 没有 .aria2 但有数据文件时也能续：continue=true 让 aria2 从已有文件体积
// 发 HTTP Range（RequestGroup::adjustFilename 会据此跳过「文件已存在」保护）；
// auto-file-renaming=false 防止它把已有文件改名成 xxx.1 后重头下。
// allow-overwrite 刻意保持默认 false：续传失败时宁可报错，也别把几百 GB
// 的半成品截断到 0。
//
// 远程引擎特殊：这里扫描到的是「本机」的拷贝，dir 若要用必须是远端路径；
// 后端不会在本机 mkdir，也不会拿本机文件存在与否去推断远端状态。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"quickdock/internal/aria2"
	"quickdock/internal/logger"
	"quickdock/services"
)

const (
	importMaxDepth    = 6
	importMaxControls = 500
	importMaxItems    = 500
)

var errImportScanFull = errors.New("import scan full")

// DlImportItem 是预览表里的一行，也是提交时回传的一行。
// 前端可以改 name / uris / dir 后再整体回传。
type DlImportItem struct {
	Name       string            `json:"name"`
	Uris       []string          `json:"uris"`
	Dir        string            `json:"dir"`
	Size       int64             `json:"size"`
	HasControl bool              `json:"hasControl"`
	HasData    bool              `json:"hasData"`
	State      string            `json:"state"` // resume=有控制文件 / partial=只有数据文件 / fresh=都没有
	Reason     string            `json:"reason"`
	Gid        string            `json:"gid"`
	Options    map[string]string `json:"options"`
	Checked    bool              `json:"checked"`
}

type DlImportPreview struct {
	Items        []DlImportItem `json:"items"`
	RootDir      string         `json:"rootDir"`
	SessionPath  string         `json:"sessionPath"`
	LocalSession string         `json:"localSession"`
	Remote       bool           `json:"remote"`
	Controls     int            `json:"controls"`
	SessionTasks int            `json:"sessionTasks"`
}

type DlImportAction struct {
	Name  string `json:"name"`
	GID   string `json:"gid"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

type DlImportResult struct {
	Added   int              `json:"added"`
	Skipped int              `json:"skipped"`
	Actions []DlImportAction `json:"actions"`
}

type importPayload struct {
	Items          []DlImportItem `json:"items"`
	Paused         bool           `json:"paused"`
	CheckIntegrity bool           `json:"checkIntegrity"`
}

type controlInfo struct {
	name    string
	dir     string
	size    int64
	hasData bool
	used    bool
}

// ===== 扫描 =====

// DlImportScan 把旧机器的 aria2.session 与新机器上拷贝过来的下载目录对上号。
// rootDir 为空时用默认下载目录；sessionPath 为空表示「不用会话」，只列控制文件。
func (s *DownloadService) DlImportScan(rootDir, sessionPath string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	root := strings.TrimSpace(rootDir)
	if root == "" {
		root = cfg.DownloadDir
	}
	if root == "" {
		return services.FailMsg("请先选择拷贝过来的下载目录")
	}
	root = filepath.Clean(root)
	if st, e := os.Stat(root); e != nil || !st.IsDir() {
		return services.FailMsg("下载目录不存在或不可访问，请重新选择")
	}

	// 刻意不自动使用本机会话：新电脑上可能已经有正在下载的任务，
	// 静默把本机 session 混进预览会诱导用户重复提交同一批任务。
	// 用户要么显式选旧电脑拷来的文件，要么点「用本机会话」。
	sess := strings.TrimSpace(sessionPath)
	var sessionTasks []aria2.SessionTask
	if sess != "" {
		sessionTasks, err = aria2.ReadSession(sess)
		if err != nil {
			return services.Fail(err)
		}
	}
	controls, err := scanControlFiles(root)
	if err != nil {
		return services.Fail(err)
	}

	byName := make(map[string]int, len(controls))
	byStem := make(map[string][]int, len(controls))
	for i, c := range controls {
		byName[strings.ToLower(c.name)] = i
		stem := importStem(c.name)
		byStem[stem] = append(byStem[stem], i)
	}

	items := make([]DlImportItem, 0, len(sessionTasks)+len(controls))
	find := func(name string) int {
		if name == "" {
			return -1
		}
		if i, ok := byName[strings.ToLower(name)]; ok && !controls[i].used {
			return i
		}
		// Content-Disposition 或改名会让 out 与 URL 里的文件名不同；
		// 同名主干唯一时才敢猜，避免把两个不同任务错配。
		if cand := byStem[importStem(name)]; len(cand) == 1 && !controls[cand[0]].used {
			return cand[0]
		}
		return -1
	}

	for _, t := range sessionTasks {
		if len(items) >= importMaxItems {
			break
		}
		name := importSessionFileName(t)
		uris := importUris(t.URIs)
		it := DlImportItem{
			Name:    name,
			Uris:    uris,
			Dir:     root,
			Gid:     t.GID(),
			Options: importOptions(t.Options),
		}
		if idx := find(name); idx >= 0 {
			c := &controls[idx]
			c.used = true
			it.Dir = c.dir
			it.Size = c.size
			it.HasControl = true
			it.HasData = c.hasData
		} else if name != "" {
			if st, e := os.Stat(filepath.Join(root, name)); e == nil && !st.IsDir() {
				it.HasData = true
				it.Size = st.Size()
			}
		}
		switch {
		case it.HasControl:
			it.State = "resume"
		case it.HasData:
			it.State = "partial"
		default:
			it.State = "fresh"
		}
		it.Reason = importReason(it)
		it.Checked = len(uris) > 0 && (it.HasControl || it.HasData)
		items = append(items, it)
	}

	// 目录里有控制文件、会话里却没有对应链接：仍列出来让用户手填 URL，
	// 否则最常见「只拷了下载文件夹、没拷 session」的场景会直接丢任务。
	var leftover []DlImportItem
	for _, c := range controls {
		if c.used || len(items)+len(leftover) >= importMaxItems {
			continue
		}
		leftover = append(leftover, DlImportItem{
			Name:       c.name,
			Uris:       []string{},
			Dir:        c.dir,
			Size:       c.size,
			HasControl: true,
			HasData:    c.hasData,
			State:      "resume",
			Reason:     "dlImportReasonNeedUri",
			Checked:    false,
		})
	}
	sort.Slice(leftover, func(i, j int) bool { return leftover[i].Name < leftover[j].Name })
	items = append(items, leftover...)

	return services.Ok(DlImportPreview{
		Items:        items,
		RootDir:      root,
		SessionPath:  sess,
		LocalSession: aria2.SessionPath(),
		Remote:       cfg.IsRemote(),
		Controls:     len(controls),
		SessionTasks: len(sessionTasks),
	})
}

// DlImportCommit 把勾选的行重新提交给 aria2。前端已经过滤了勾选项，
// 这里不再看 Checked（旧前端不带这个字段时全量跳过会很难排查）。
func (s *DownloadService) DlImportCommit(payloadJSON string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	var p importPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(payloadJSON)), &p); err != nil {
		return services.Fail(fmt.Errorf("导入数据格式不正确: %w", err))
	}
	if len(p.Items) == 0 {
		return services.FailMsg("没有需要导入的任务")
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	c := aria2.NewClient(cfg)
	if _, err := c.GetVersion(); err != nil {
		return services.Fail(fmt.Errorf("aria2 未运行，请先启用下载器"))
	}

	out := DlImportResult{Actions: make([]DlImportAction, 0, len(p.Items))}
	// 同一个目录下同名文件只能提交一次：两个任务同时写同一个 .aria2 会互相踩进度。
	seen := make(map[string]bool, len(p.Items))
	for _, it := range p.Items {
		if len(out.Actions) >= importMaxItems {
			break
		}
		name, err := cleanImportName(it.Name)
		if err != nil {
			out.Skipped++
			out.Actions = append(out.Actions, DlImportAction{Name: it.Name, Error: err.Error()})
			continue
		}
		uris := importUris(it.Uris)
		if len(uris) == 0 {
			out.Skipped++
			out.Actions = append(out.Actions, DlImportAction{Name: name, Error: "缺少有效的下载链接"})
			continue
		}
		dir := strings.TrimSpace(it.Dir)
		if dir == "" {
			out.Skipped++
			out.Actions = append(out.Actions, DlImportAction{Name: name, Error: "保存目录为空"})
			continue
		}
		if !cfg.IsRemote() {
			if st, e := os.Stat(dir); e != nil || !st.IsDir() {
				out.Skipped++
				out.Actions = append(out.Actions, DlImportAction{Name: name, Error: "保存目录不存在，请选择本机实际存放数据文件的目录"})
				continue
			}
		}
		key := strings.ToLower(dir) + "|" + strings.ToLower(name)
		if seen[key] {
			out.Skipped++
			out.Actions = append(out.Actions, DlImportAction{Name: name, Error: "同名任务重复，已跳过"})
			continue
		}
		seen[key] = true
		hasControl := it.HasControl
		if !cfg.IsRemote() {
			_, e := os.Stat(filepath.Join(dir, name+".aria2"))
			hasControl = e == nil
		}

		opts, _, err := baseOptions(cfg, dir, "")
		if err != nil {
			out.Skipped++
			out.Actions = append(out.Actions, DlImportAction{Name: name, Error: err.Error()})
			continue
		}
		preserved := importOptions(it.Options)
		for k, v := range preserved {
			opts[k] = v
		}
		isMagnet := strings.HasPrefix(strings.ToLower(uris[0]), "magnet:")
		opts["dir"] = dir
		if !isMagnet {
			opts["out"] = name
		} else {
			// 磁力 / BT 的文件名由元数据决定，硬塞 out 会把多文件种子压成单文件。
			delete(opts, "out")
		}
		opts["continue"] = "true"
		opts["auto-file-renaming"] = "false"
		if p.CheckIntegrity && (hasControl || isMagnet) {
			opts["check-integrity"] = "true"
		}
		if p.Paused {
			opts["pause"] = "true"
		}

		gid, err := c.AddURIAt(uris, opts, 0)
		if err != nil && isImportOptionError(err) {
			// 老版本 aria2 不认识某些选项（pause / always-resume / 自定义 header 等）：
			// 去掉保留选项与校验项，只留续传必需的几个再试一次，别整批失败。
			minimal := map[string]string{
				"dir": dir, "continue": "true", "auto-file-renaming": "false",
			}
			if !isMagnet {
				minimal["out"] = name
			}
			gid, err = c.AddURIAt(uris, minimal, 0)
			if err == nil && p.Paused {
				if pe := c.ForcePause(gid); pe != nil {
					logger.W("[download] 导入后暂停失败 gid=%s: %v", gid, pe)
				}
			}
		}
		if err != nil {
			out.Skipped++
			logger.W("[download] 导入任务失败 name=%s: %v", name, err)
			out.Actions = append(out.Actions, DlImportAction{Name: name, Error: err.Error()})
			continue
		}
		s.insertMeta(gid, uris[0], name, dir, "import")
		out.Added++
		out.Actions = append(out.Actions, DlImportAction{Name: name, GID: gid, OK: true})
	}
	s.invalidate()
	s.startPoller()
	if out.Added > 0 {
		if err := c.SaveSession(); err != nil {
			logger.W("[download] 导入后保存会话失败: %v", err)
		}
	}
	return services.Ok(out)
}

// ===== 辅助 =====

func isImportOptionError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "option") || strings.Contains(s, "not allowed") || strings.Contains(s, "unrecognized")
}

// importOptions 只保留能安全下发给 addUri 的「网络相关」选项。
// 会话里还混着 gid / pause / dir / file-allocation / 一堆 BT 启动参数，
// 这些要么不是任务级选项、要么属于旧机器，全量透传会让 aria2 直接拒收。
func importOptions(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.ToLower(strings.TrimSpace(k))
		if !importKeepOptions[k] || strings.TrimSpace(v) == "" {
			continue
		}
		if len(v) > 4096 {
			v = v[:4096]
		}
		out[k] = v
	}
	return out
}

var importKeepOptions = map[string]bool{
	"header": true, "referer": true, "http-user": true, "http-passwd": true,
	"user-agent": true, "ftp-user": true, "ftp-passwd": true,
	"http-proxy": true, "https-proxy": true, "ftp-proxy": true,
	"all-proxy": true, "all-proxy-user": true, "all-proxy-passwd": true, "no-proxy": true,
	"proxy-method": true, "checksum": true,
	"split": true, "max-connection-per-server": true, "min-split-size": true,
	"max-tries": true, "retry-wait": true, "timeout": true, "connect-timeout": true,
	"max-download-limit": true, "max-upload-limit": true,
	"remote-time": true, "conditional-get": true, "use-head": true,
	"seed-ratio": true, "seed-time": true, "select-file": true,
	"bt-tracker": true, "bt-max-peers": true, "bt-require-crypto": true,
	"bt-force-encryption": true, "bt-exclude-tracker": true,
	"content-disposition-default-utf8": true,
}

// importUris 过滤掉本地种子路径等无法重新 addUri 的条目，同时去重。
func importUris(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] || validateURI(u) != nil {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// importSessionFileName 还原会话任务在磁盘上的文件名：优先 out 选项，
// 否则从第一条 URI 推。out 可能是旧机器的绝对路径，只取末段。
func importSessionFileName(t aria2.SessionTask) string {
	if v := strings.TrimSpace(t.Options["out"]); v != "" {
		return importBasename(v)
	}
	if len(t.URIs) == 0 {
		return ""
	}
	n := nameFromURL(t.URIs[0])
	return importBasename(n)
}

func importBasename(v string) string {
	v = strings.ReplaceAll(v, "\\", "/")
	n := path.Base(v)
	if n == "." || n == "/" || n == "" || strings.Contains(n, "://") {
		return ""
	}
	return n
}

func importStem(name string) string {
	lower := strings.ToLower(name)
	ext := path.Ext(lower)
	return strings.TrimSuffix(lower, ext)
}

// cleanImportName 把用户在预览表里改过的文件名收敛成「纯文件名」。
// out 会直接拼进 dir，允许 / 或 .. 就等于允许写任意路径。
func cleanImportName(v string) (string, error) {
	name := strings.TrimSpace(v)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("文件名为空或不合法")
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return "", fmt.Errorf("文件名不能包含路径分隔符")
	}
	if rs := []rune(name); len(rs) > 200 {
		name = strings.Trim(string(rs[:200]), " .")
	}
	if name == "" {
		return "", fmt.Errorf("文件名为空或不合法")
	}
	return name, nil
}

func importReason(it DlImportItem) string {
	if len(it.Uris) == 0 {
		return "dlImportReasonNoUri"
	}
	if it.HasControl && !it.HasData {
		return "dlImportReasonNoData"
	}
	if !it.HasControl && it.HasData {
		return "dlImportReasonNoControl"
	}
	if !it.HasControl {
		return "dlImportReasonFresh"
	}
	return ""
}

// scanControlFiles 在下载目录里找 *.aria2，并顺手 stat 同名数据文件。
// 目录层级与数量都设上限：用户可能误选整个 D 盘，不能让 WalkDir 跑几分钟。
func scanControlFiles(root string) ([]controlInfo, error) {
	var out []controlInfo
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单个目录没权限就跳过，不中断整次扫描
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return nil
		}
		depth := strings.Count(rel, string(os.PathSeparator)) + 1
		if d.IsDir() {
			if p != root && (depth > importMaxDepth || importSkipDir(d.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if depth > importMaxDepth {
			return nil
		}
		ext := filepath.Ext(d.Name())
		if !strings.EqualFold(ext, ".aria2") {
			return nil
		}
		name := d.Name()[:len(d.Name())-len(ext)]
		if name == "" {
			return nil
		}
		ci := controlInfo{name: name, dir: filepath.Dir(p)}
		dataPath := filepath.Join(ci.dir, name)
		if st, se := os.Stat(dataPath); se == nil && !st.IsDir() {
			ci.hasData = true
			ci.size = st.Size()
		}
		out = append(out, ci)
		if len(out) >= importMaxControls {
			return errImportScanFull
		}
		return nil
	})
	if err != nil && !errors.Is(err, errImportScanFull) {
		return nil, fmt.Errorf("扫描下载目录失败: %w", err)
	}
	return out, nil
}

func importSkipDir(name string) bool {
	if strings.HasPrefix(name, "$") || name == ".git" || name == "node_modules" || name == "__MACOSX" {
		return true
	}
	return strings.EqualFold(name, "System Volume Information")
}
