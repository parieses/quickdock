// Package git 本地 Git 仓库的浏览与常用操作（参考 SourceTree 的仓库视角）。
//
// 定位：一个够用的 Git 可视化客户端——看清仓库状态（分支 / 历史 / 某次提交）、
// 处理工作区（暂存 / 丢弃 / 忽略 / 提交 / 推送）、做常见的分支与历史操作
// （切换 / 新建 / 重命名 / 删除 / 合并 / 打标签 / 回滚）。工作区细节在 worktree.go。
//
// 有意不做的：交互式 rebase、reset --hard、行级暂存、冲突解决界面。
// 这些要么需要用户看着完整上下文逐块决策，要么做半套就会把仓库留在
// 「界面显示成功、实际卡在中间态」的状态里——那种错误比没有功能贵得多。
//
// 所有命令一律 exec 传参数数组，不拼 shell：路径里可能有空格与中文，
// 拼字符串既容易错，也把命令注入引进来。
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/sysutil"
	"quickdock/services"
)

const (
	// repoStoreKey 仓库清单落在 app_state（KV）里，不新建表：
	// 它只是一串路径，没有关联查询，也不参与回收站/搜索。
	repoStoreKey = "git_repos"

	cmdTimeout     = 20 * time.Second
	networkTimeout = 90 * time.Second  // fetch/pull 可能很慢，但也不能无限挂着
	cloneTimeout   = 600 * time.Second // clone 的体量没有上限（小仓库秒级，大仓库十几分钟）
	pushTimeout    = 300 * time.Second // push 要等远端确认，首次推送还可能弹凭据窗口
	commitTimeout  = 120 * time.Second // pre-commit hook 可能跑 lint / 测试
	maxLogEntries  = 200
)

// GitService 承载 Git 面板的全部领域方法。
type GitService struct {
	App *services.AppService
}

// NewGitService 创建 Git 门面服务。
func NewGitService(app *services.AppService) *GitService {
	return &GitService{App: app}
}

// RepoRef 一个被加入面板的仓库（持久化的最小信息）。
type RepoRef struct {
	Path    string `json:"path"`    // 仓库根目录绝对路径（归一化后的 toplevel）
	Name    string `json:"name"`    // 目录名，仅用于展示
	AddedAt string `json:"addedAt"` // 加入时间，用于稳定排序
}

// RepoStatus 仓库当前状态（左侧列表与顶部栏都吃这一份）。
type RepoStatus struct {
	Path       string     `json:"path"`
	Branch     string     `json:"branch"`
	Upstream   string     `json:"upstream"`
	Ahead      int        `json:"ahead"`
	Behind     int        `json:"behind"`
	Changes    []FileStat `json:"changes"`
	Clean      bool       `json:"clean"`
	NoCommits  bool       `json:"noCommits"`
	GitMissing bool       `json:"gitMissing"` // 找不到 git 可执行文件
	Error      string     `json:"error"`
}

// FileStat 一条工作区改动。
type FileStat struct {
	Status string `json:"status"` // M / A / D / R / ?? ...
	Path   string `json:"path"`
	Desc   string `json:"desc"`
	// 增删行数：只有提交详情会填（工作区 status 拿不到，也不该为它多跑一条 numstat）。
	// 二进制文件 git 给的是 "-"，此处留 0，前端据此显示成「二进制」。
	Add int `json:"add"`
	Del int `json:"del"`
}

// BranchInfo 分支 / 标签清单。
type BranchInfo struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Remote  bool   `json:"remote"`
	Tag     bool   `json:"tag,omitempty"`
	Ahead   int    `json:"ahead,omitempty"`
	Behind  int    `json:"behind,omitempty"`
}

// Commit 一条提交历史。
type Commit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Author  string `json:"author"`
	Email   string `json:"email"`
	Ts      int64  `json:"ts"`
	Subject string `json:"subject"`
	Refs    string `json:"refs"`  // 分支/标签装饰（git 给的原始串）
	Graph   string `json:"graph"` // 图形前缀（--graph 的第一列字符），前端用等宽字体画线
}

// CommitDetail 提交详情：改了哪些文件 + 可选 diff 文本。
type CommitDetail struct {
	Hash  string     `json:"hash"`
	Files []FileStat `json:"files"`
	Diff  string     `json:"diff"`
}

// -------- 仓库清单 --------

// GitListRepos 返回已加入面板的仓库清单（只读 KV，不跑 git，快）。
func (s *GitService) GitListRepos() *services.ApiResult {
	repos, err := s.loadRepos()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(repos)
}

// GitAddRepo 把一个目录加入面板：先确认它确实在 git 仓库里，再把路径归一化到
// 仓库根（用户在子目录里选的目录，后续 status/log 都按根算更准）。
func (s *GitService) GitAddRepo(path string) *services.ApiResult {
	dir := strings.TrimSpace(path)
	if dir == "" {
		return services.FailMsg("缺少仓库路径")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return services.FailMsg("目录不存在：" + dir)
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, err := run(bin, dir, cmdTimeout, "rev-parse", "--show-toplevel")
	if err != nil {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return services.FailMsg("这个目录不是 git 仓库：" + clip(msg, 200))
	}
	root := strings.TrimSpace(out)
	if root == "" {
		root = dir
	}
	root = filepath.Clean(root)

	repos, err := s.loadRepos()
	if err != nil {
		return services.Fail(err)
	}
	for _, r := range repos {
		if strings.EqualFold(r.Path, root) {
			return services.FailMsg("这个仓库已经在列表里了")
		}
	}
	repos = append(repos, RepoRef{
		Path:    root,
		Name:    filepath.Base(root),
		AddedAt: time.Now().Format(time.RFC3339),
	})
	if err := s.saveRepos(repos); err != nil {
		return services.Fail(err)
	}
	return services.Ok(repos)
}

// GitRemoveRepo 从面板移除（只删记录，绝不动磁盘上的仓库）。
func (s *GitService) GitRemoveRepo(path string) *services.ApiResult {
	repos, err := s.loadRepos()
	if err != nil {
		return services.Fail(err)
	}
	next := make([]RepoRef, 0, len(repos))
	removed := false
	for _, r := range repos {
		if strings.EqualFold(r.Path, path) {
			removed = true
			continue
		}
		next = append(next, r)
	}
	if !removed {
		return services.FailMsg("列表里没有这个仓库")
	}
	if err := s.saveRepos(next); err != nil {
		return services.Fail(err)
	}
	return services.Ok(next)
}

// GitPickRepo 弹出目录选择框，返回选中的路径（由前端决定是否加入列表）。
func (s *GitService) GitPickRepo() *services.ApiResult {
	return s.App.SelectLocalDirectory("选择 Git 仓库目录")
}

// -------- 克隆远端仓库 --------

// GitPickCloneParent 弹出目录选择框，选克隆的**父目录**（仓库目录将创建在它下面）。
// 与 GitPickRepo 分开：两件事选的目录语义不同，混用一个方法日后改文案/校验就得拆。
func (s *GitService) GitPickCloneParent() *services.ApiResult {
	return s.App.SelectLocalDirectory("选择克隆目标目录（仓库将创建在它下面）")
}

// GitClone 把远端仓库克隆到 parent/<name> 并加入清单。name 从 URL 末段推导。
//
// 鉴权交给 git 自己：HTTPS 私有仓库由 Windows 的 Git Credential Manager 弹窗收凭据，
// SSH 走用户的 agent/key——面板不收集也不落库任何密码，这是有意的边界。
func (s *GitService) GitClone(url, parent string) *services.ApiResult {
	raw := strings.TrimSpace(url)
	if raw == "" {
		return services.FailMsg("请填写仓库地址")
	}
	if strings.ContainsAny(raw, " \t") {
		return services.FailMsg("仓库地址不能包含空格")
	}
	parent = strings.TrimSpace(parent)
	if fi, err := os.Stat(parent); err != nil || !fi.IsDir() {
		return services.FailMsg("目标目录不存在：" + parent)
	}

	name := repoNameFromURL(raw)
	if name == "" {
		return services.FailMsg("无法从地址推导仓库名，请检查地址格式（支持 https:// 与 git@host:path）")
	}
	target := filepath.Join(parent, name)
	if _, err := os.Stat(target); err == nil {
		return services.FailMsg("目录已存在：" + target + "，换个目标目录或先处理同名仓库")
	}

	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	// --progress 让凭据弹窗等交互信息走 stderr 而不是挂死；clone 只读远端，不动本地已有内容
	if _, errOut, err := run(bin, parent, cloneTimeout, "clone", "--progress", raw, target); err != nil {
		msg := clip(strings.TrimSpace(errOut), 400)
		if msg == "" {
			msg = err.Error()
		}
		return services.FailMsg("clone 失败：" + msg)
	}

	if err := s.appendRepo(target, name); err != nil {
		return services.Fail(err)
	}
	// data 带回目标路径：前端拿它直接切到新仓库，不用自己拼路径分隔符
	return services.OkMsg(target, "已克隆并加入列表")
}

// repoNameFromURL 从仓库地址推导目录名：取路径末段并剥掉 .git 后缀。
// 兼容 https://host/owner/repo.git 与 scp 风格 git@host:owner/repo.git。
func repoNameFromURL(raw string) string {
	u := strings.TrimRight(raw, "/")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return strings.TrimSuffix(strings.TrimSpace(u), ".git")
}

// appendRepo 把归一化后的路径加入仓库清单（去重）。
func (s *GitService) appendRepo(root, name string) error {
	root = filepath.Clean(root)
	repos, err := s.loadRepos()
	if err != nil {
		return err
	}
	for _, r := range repos {
		if strings.EqualFold(r.Path, root) {
			return nil // 已在列表里就不重复加，clone 本身已经成功
		}
	}
	repos = append(repos, RepoRef{
		Path:    root,
		Name:    name,
		AddedAt: time.Now().Format(time.RFC3339),
	})
	return s.saveRepos(repos)
}

// 仓库清单直接读写 app_state：走 GetSetting/SetSetting 而**不是** App.GetValue/SetValue。
//
// 区别很关键：SetValue 是给前端用的公开入口，前面挂着 appStateKeyValid 那张 key 白名单
// （只认 theme/locale/ai_* 等已知前缀），后端自己的配置项写进去会被当成非法 key 拒掉，
// 报出来的是一句「invalid app_state key」，从面板上看就是「保存仓库列表失败」。
// db.go 里写得很清楚：内部写入走 SetSetting，白名单只守前端入口——这里就该走内部通道。
func (s *GitService) loadRepos() ([]RepoRef, error) {
	if s.App == nil || s.App.DB == nil {
		return []RepoRef{}, nil // 数据库还没就绪：当空列表，别让面板打不开
	}
	raw, err := s.App.DB.GetSetting(repoStoreKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return []RepoRef{}, nil // 没存过就是空列表，不是错误
	}
	var repos []RepoRef
	if err := json.Unmarshal([]byte(raw), &repos); err != nil {
		// 存的内容坏了：当成空列表，而不是让整个面板打不开
		return []RepoRef{}, nil
	}
	return repos, nil
}

func (s *GitService) saveRepos(repos []RepoRef) error {
	if s.App == nil || s.App.DB == nil {
		return errors.New("数据库未就绪，仓库列表暂时存不下来")
	}
	b, err := json.Marshal(repos)
	if err != nil {
		return err
	}
	if err := s.App.DB.SetSetting(repoStoreKey, string(b)); err != nil {
		return fmt.Errorf("保存仓库列表失败：%w", err)
	}
	return nil
}

// -------- 状态 --------

// GitRepoStatus 汇总一个仓库的当前状态：分支、领先/落后、工作区改动。
//
// 拆成单仓库接口而不是「列表里带状态」：仓库数量少但每个都要跑 2~3 条 git 命令，
// 一次性串行做完会让面板首屏卡住；前端并发请求，谁先回来谁先显示。
func (s *GitService) GitRepoStatus(path string) *services.ApiResult {
	st := RepoStatus{Path: path, Changes: []FileStat{}}
	bin, err := findGit()
	if err != nil {
		st.GitMissing = true
		st.Error = err.Error()
		return services.Ok(st)
	}
	out, errOut, err := run(bin, path, cmdTimeout, "status", "--porcelain=v1", "-b", "--untracked-files=all")
	if err != nil {
		st.Error = clip(strings.TrimSpace(errOut), 300)
		if st.Error == "" {
			st.Error = err.Error()
		}
		return services.Ok(st)
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			parseBranchLine(line[3:], &st)
			continue
		}
		if len(line) < 3 {
			continue
		}
		code := strings.TrimSpace(line[:2])
		st.Changes = append(st.Changes, FileStat{
			Status: code,
			Path:   strings.TrimSpace(line[3:]),
			Desc:   statusDesc(code),
		})
	}
	st.Clean = len(st.Changes) == 0
	return services.Ok(st)
}

// GitBranches 本地 + 远程分支清单，当前分支标记出来（SourceTree 左侧那棵树）。
func (s *GitService) GitBranches(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	// -vv 会带上上游与 ahead/behind，切分支前能看出「切过去会不会落后」。
	out, errOut, err := run(bin, path, cmdTimeout, "branch", "-vv", "--no-color")
	if err != nil {
		return services.FailMsg("读取分支失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	local := []BranchInfo{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		current := strings.HasPrefix(line, "*")
		body := strings.TrimSpace(strings.TrimPrefix(line, "*"))
		fields := strings.Fields(body)
		if len(fields) == 0 {
			continue
		}
		b := BranchInfo{Name: fields[0], Current: current}
		for i, f := range fields {
			if f == "ahead" && i+1 < len(fields) {
				b.Ahead, _ = strconv.Atoi(strings.Trim(fields[i+1], ",]"))
			}
			if f == "behind" && i+1 < len(fields) {
				b.Behind, _ = strconv.Atoi(strings.Trim(fields[i+1], ",]"))
			}
		}
		local = append(local, b)
	}
	remote := []BranchInfo{}
	if outR, _, e := run(bin, path, cmdTimeout, "branch", "-r", "--no-color"); e == nil {
		for _, line := range strings.Split(outR, "\n") {
			name := strings.TrimSpace(strings.TrimPrefix(strings.TrimRight(line, "\r"), "*"))
			if name == "" || strings.HasSuffix(name, "/HEAD") {
				continue
			}
			remote = append(remote, BranchInfo{Name: name, Remote: true})
		}
	}
	// 标签：SourceTree 左侧把它单列一组。按创建时间倒序，最近打的版本 tag 在最上面。
	tags := []BranchInfo{}
	if outT, _, e := run(bin, path, cmdTimeout, "tag", "--sort=-creatordate", "--no-color"); e == nil {
		for _, line := range strings.Split(outT, "\n") {
			name := strings.TrimSpace(strings.TrimRight(line, "\r"))
			if name == "" {
				continue
			}
			tags = append(tags, BranchInfo{Name: name, Tag: true})
		}
	}
	return services.Ok(map[string]any{"local": local, "remote": remote, "tags": tags})
}

// GitLog 当前分支的提交历史（带 --graph，前端按等宽字体画线，贴近 SourceTree 的观感）。
func (s *GitService) GitLog(path string, limit int) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if limit <= 0 || limit > maxLogEntries {
		limit = 80
	}
	// %x09 是制表符：用不可见字符分隔，避免作者名里带 | 把解析搞乱。
	out, errOut, err := run(bin, path, cmdTimeout, "log", "--graph", "--date-order",
		"-n", strconv.Itoa(limit), commitFormat, "--all", "--no-color")
	if err != nil {
		// 空仓库（还没有第一次提交）不是错误，返回空列表让面板显示引导
		if strings.Contains(errOut, "does not have any commits") || strings.Contains(errOut, "bad default revision") {
			return services.Ok([]Commit{})
		}
		return services.FailMsg("读取提交历史失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	commits := []Commit{}
	for _, line := range strings.Split(out, "\n") {
		if c, ok := parseCommitLine(strings.TrimRight(line, "\r")); ok {
			commits = append(commits, c)
		}
	}
	return services.Ok(commits)
}

// commitFormat 提交行的字段：hash / 短 hash / 作者 / 邮箱 / 时间戳 / 标题 / 装饰。
// GitLog 与 GitFileHistory 共用，免得两处格式串改一处忘一处。
const commitFormat = "--pretty=format:%H%x09%h%x09%an%x09%ae%x09%at%x09%s%x09%D"

// parseCommitLine 解析一行提交。--graph 时行首会插 "* " / "| * " 这类前缀，
// 且它粘在第一个字段（hash）前面；不用 --graph 时照常按 tab 切。
func parseCommitLine(line string) (Commit, bool) {
	if strings.TrimSpace(line) == "" {
		return Commit{}, false
	}
	parts := strings.Split(line, "\t")
	if len(parts) < 7 {
		return Commit{}, false
	}
	head := parts[0]
	graph, hash := "", strings.TrimSpace(head)
	if i := strings.LastIndex(head, " "); i >= 0 {
		graph = head[:i+1]
		hash = strings.TrimSpace(head[i+1:])
	}
	c := Commit{
		Hash:    hash,
		Short:   parts[1],
		Author:  parts[2],
		Email:   parts[3],
		Subject: parts[5],
		Refs:    parts[6],
		Graph:   graph,
	}
	// subject 本身含 tab（少见）时会被多切几段：把中间并回 subject，末段才是 refs。
	if len(parts) > 7 {
		c.Subject = strings.Join(parts[5:len(parts)-1], " ")
		c.Refs = parts[len(parts)-1]
	}
	if ts, e := strconv.ParseInt(parts[4], 10, 64); e == nil {
		c.Ts = ts
	}
	return c, true
}

// GitCommitDetail 某次提交改了哪些文件（+ 可选 diff 文本）。
func (s *GitService) GitCommitDetail(path, hash string, withDiff bool) *services.ApiResult {
	if strings.TrimSpace(hash) == "" {
		return services.FailMsg("缺少提交 hash")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	detail := CommitDetail{Hash: hash, Files: []FileStat{}}
	out, errOut, err := run(bin, path, cmdTimeout, "show", "--name-status", "--pretty=format:", "--no-color", hash)
	if err != nil {
		return services.FailMsg("读取提交失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	stats := commitNumstat(bin, path, hash)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		code := strings.TrimSpace(fields[0])
		p := fields[1]
		if len(fields) == 3 {
			p = fields[1] + " → " + fields[2] // 重命名：旧 → 新
		}
		f := FileStat{Status: code, Path: p, Desc: statusDesc(code)}
		f.Add, f.Del = stats.lookup(p)
		detail.Files = append(detail.Files, f)
	}
	if withDiff {
		d, _, derr := run(bin, path, cmdTimeout, "show", "--no-color", "--unified=3", "--pretty=format:", hash)
		if derr == nil {
			detail.Diff = clip(d, 60000)
		}
	}
	return services.Ok(detail)
}

// -------- 写操作（都由用户点按钮触发） --------

// GitCheckout 切换分支。带未提交改动时 git 会自己拒绝，原样把原因给用户看。
func (s *GitService) GitCheckout(path, branch string) *services.ApiResult {
	if strings.TrimSpace(branch) == "" {
		return services.FailMsg("缺少分支名")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, err := run(bin, path, cmdTimeout, "checkout", branch); err != nil {
		return services.FailMsg("切换失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(nil, "已切换到 "+branch)
}

// GitFetch 拉取远端引用（不改工作区，安全）。
func (s *GitService) GitFetch(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, err := run(bin, path, networkTimeout, "fetch", "--all", "--prune"); err != nil {
		return services.FailMsg("fetch 失败：" + clip(strings.TrimSpace(errOut), 400))
	}
	return services.OkMsg(nil, "已获取远端更新")
}

// GitPull 只做快进合并：--ff-only 失败时给出明确提示，而不是悄悄造一个 merge commit。
// 面板不提供冲突解决界面，产生冲突的 pull 只会让用户在一个看不懂的状态里卡住。
func (s *GitService) GitPull(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, err := run(bin, path, networkTimeout, "pull", "--ff-only")
	if err != nil {
		msg := clip(strings.TrimSpace(errOut), 400)
		if strings.Contains(msg, "Not possible to fast-forward") || strings.Contains(msg, "divergent") {
			return services.FailMsg("无法快进合并：本地有分叉的提交，请在终端里手动处理（git pull --rebase 或 merge）")
		}
		return services.FailMsg("pull 失败：" + msg)
	}
	return services.OkMsg(nil, clip(strings.TrimSpace(out), 200))
}

// GitPush 推送当前分支。没有上游时自动补 -u origin <branch>：
// 「本地能提交但推不上去」是首次推送最常撞的一堵墙，让用户自己去敲 -u 没有意义。
//
// 成功信息 git 写在 stderr（进度与结果都在那），所以两路都要读。
func (s *GitService) GitPush(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	branchOut, _, berr := run(bin, path, cmdTimeout, "rev-parse", "--abbrev-ref", "HEAD")
	branch := strings.TrimSpace(branchOut)
	if berr != nil || branch == "" || branch == "HEAD" {
		return services.FailMsg("当前是游离 HEAD 或空仓库，无法推送")
	}
	args := []string{"push"}
	if _, _, e := run(bin, path, cmdTimeout, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); e != nil {
		args = []string{"push", "-u", "origin", branch}
	}
	out, errOut, err := run(bin, path, pushTimeout, args...)
	msg := strings.TrimSpace(errOut)
	if msg == "" {
		msg = strings.TrimSpace(out)
	}
	if err != nil {
		return services.FailMsg("推送失败：" + clip(msg, 400))
	}
	return services.OkMsg(nil, clip(msg, 200))
}

// -------- 分支 / 标签的远端动作 --------

// GitPushBranch 把指定分支推到 origin（不要求先切过去）。
//
// 没有上游时补 -u：否则「推上去了但下次 pull 不知道跟谁」——这一步用户自己做
// 要在终端里敲 -u origin xxx，面板顺手做掉更合理。
func (s *GitService) GitPushBranch(path, name string) *services.ApiResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return services.FailMsg("缺少分支名")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	args := []string{"push", "origin", name}
	if _, _, e := run(bin, path, cmdTimeout, "rev-parse", "--abbrev-ref", name+"@{u}"); e != nil {
		args = []string{"push", "-u", "origin", name}
	}
	out, errOut, e := run(bin, path, pushTimeout, args...)
	msg := strings.TrimSpace(errOut)
	if msg == "" {
		msg = strings.TrimSpace(out)
	}
	if e != nil {
		if strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") {
			return services.FailMsg("远端有本地没有的提交，先 pull 再推（面板不做强推，避免覆盖别人的提交）")
		}
		return services.FailMsg("推送分支失败：" + clip(msg, 400))
	}
	return services.OkMsg(nil, clip(msg, 200))
}

// GitPushTag 把某个标签推到 origin。
//
// 用完整 refspec（refs/tags/x）而不是裸名字：分支和标签同名时，
// `git push origin v1` 的行为取决于远端有没有同名分支，写全就没有歧义。
func (s *GitService) GitPushTag(path, name string) *services.ApiResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return services.FailMsg("缺少标签名")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	ref := "refs/tags/" + name
	out, errOut, e := run(bin, path, pushTimeout, "push", "origin", ref)
	msg := strings.TrimSpace(errOut)
	if msg == "" {
		msg = strings.TrimSpace(out)
	}
	if e != nil {
		return services.FailMsg("推送标签失败：" + clip(msg, 400))
	}
	return services.OkMsg(nil, "已推送标签 "+name)
}

// GitDeleteTag 删除标签：remote=false 删本地，true 删远端。
//
// 两步分开而不是「一键都删」：本地删错了 git 里还有（远端），远端删错了
// 影响的是所有协作者——前者可恢复，后者不可，得让用户自己选。
func (s *GitService) GitDeleteTag(path, name string, remote bool) *services.ApiResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return services.FailMsg("缺少标签名")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if remote {
		if _, errOut, e := run(bin, path, pushTimeout, "push", "origin", ":refs/tags/"+name); e != nil {
			return services.FailMsg("删除远端标签失败：" + clip(strings.TrimSpace(errOut), 400))
		}
		return services.OkMsg(nil, "已删除远端标签 "+name)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "tag", "-d", name); e != nil {
		return services.FailMsg("删除本地标签失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(nil, "已删除本地标签 "+name)
}

// -------- 分支 / 标签 / 历史操作（右键菜单触发） --------

// GitCreateBranch 从 from 创建并切换到新分支（等价 checkout -b）。
//
// 一步完成「创建 + 切换」：只建不切的话，用户还得回分支列表再点一次，那一步没有额外信息量。
// from 为空表示从当前 HEAD 拉出去。
func (s *GitService) GitCreateBranch(path, name, from string) *services.ApiResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return services.FailMsg("分支名不能为空")
	}
	// git 自己也会校验，但它的报错是英文长句；这几个字符是用户最常误输的
	if strings.ContainsAny(name, " \t~^:?*[\\") {
		return services.FailMsg("分支名不能包含空格或 ~ ^ : ? * [ \\ 等字符")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	args := []string{"checkout", "-b", name}
	if f := strings.TrimSpace(from); f != "" {
		args = append(args, f)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, args...); e != nil {
		return services.FailMsg("创建分支失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(nil, "已创建并切换到 "+name)
}

// GitDeleteBranch 删除本地分支。
//
// force=false 用 -d（未合并会拒绝），force=true 用 -D。分两步而不是直接 -D：
// 未合并分支里的提交删掉就真没了，必须让用户先看到「未合并」这句再决定。
func (s *GitService) GitDeleteBranch(path, name string, force bool) *services.ApiResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return services.FailMsg("缺少分支名")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if cur, _, e := run(bin, path, cmdTimeout, "rev-parse", "--abbrev-ref", "HEAD"); e == nil {
		if strings.TrimSpace(cur) == name {
			return services.FailMsg("不能删除当前所在的分支：先切到别的分支")
		}
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "branch", flag, name); e != nil {
		msg := strings.TrimSpace(errOut)
		if strings.Contains(msg, "not fully merged") {
			return services.FailMsg("分支还没合并：确认要丢掉它的提交才选「强制删除」")
		}
		return services.FailMsg("删除分支失败：" + clip(msg, 300))
	}
	return services.OkMsg(nil, "已删除分支 "+name)
}

// GitRenameBranch 重命名本地分支。
func (s *GitService) GitRenameBranch(path, oldName, newName string) *services.ApiResult {
	oldName, newName = strings.TrimSpace(oldName), strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return services.FailMsg("分支名不能为空")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "branch", "-m", oldName, newName); e != nil {
		return services.FailMsg("重命名失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(nil, "已重命名为 "+newName)
}

// GitMerge 把 branch 合并进当前分支。
//
// 冲突时 git 会停在「合并中」状态，面板不提供冲突解决界面——这里如实说明
// 怎么退出来（git merge --abort），比让用户对着一半的文件列表猜发生了什么强。
func (s *GitService) GitMerge(path, branch string) *services.ApiResult {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return services.FailMsg("缺少要合并的分支")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, e := run(bin, path, networkTimeout, "merge", "--no-edit", branch)
	if e != nil {
		text := strings.TrimSpace(out + "\n" + errOut)
		if strings.Contains(text, "CONFLICT") || strings.Contains(text, "Automatic merge failed") {
			return services.FailMsg("合并有冲突：在编辑器里解决后提交即可；想放弃这次合并就执行 git merge --abort")
		}
		return services.FailMsg("合并失败：" + clip(text, 400))
	}
	return services.OkMsg(nil, "已合并 "+branch)
}

// GitCreateTag 在 hash（为空取 HEAD）上打标签。
func (s *GitService) GitCreateTag(path, name, hash string) *services.ApiResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return services.FailMsg("标签名不能为空")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	args := []string{"tag", name}
	if h := strings.TrimSpace(hash); h != "" {
		args = append(args, h)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, args...); e != nil {
		return services.FailMsg("创建标签失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	// 标签默认只在本地：提醒一句，否则用户以为远端已经有了（右键标签可一键推送）
	return services.OkMsg(nil, "已创建标签 "+name+"（仅本地，右键标签可推送到远端）")
}

// GitRevertCommit 用一个新提交回滚指定提交（不改历史，已推送的提交也能安全撤销）。
func (s *GitService) GitRevertCommit(path, hash string) *services.ApiResult {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return services.FailMsg("缺少提交 hash")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, e := run(bin, path, commitTimeout, "revert", "--no-edit", hash)
	if e != nil {
		text := strings.TrimSpace(out + "\n" + errOut)
		if strings.Contains(text, "CONFLICT") || strings.Contains(text, "could not apply") {
			return services.FailMsg("回滚有冲突：解决冲突后提交即可；想放弃这次回滚就执行 git revert --abort")
		}
		if strings.Contains(text, "empty") {
			return services.FailMsg("这次提交的改动已经被后续提交抵消了，回滚后没有内容可提交")
		}
		return services.FailMsg("回滚失败：" + clip(text, 400))
	}
	return services.OkMsg(nil, "已回滚 "+hash[:min(7, len(hash))])
}

// GitCheckoutCommit 检出某次提交（detached HEAD）。
//
// 明确告知这是游离状态：用户在这里提交的改动不属于任何分支，切走就可能丢。
func (s *GitService) GitCheckoutCommit(path, hash string) *services.ApiResult {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return services.FailMsg("缺少提交 hash")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "checkout", hash); e != nil {
		return services.FailMsg("检出失败：" + clip(strings.TrimSpace(errOut), 400))
	}
	return services.OkMsg(nil, "已检出 "+hash[:min(7, len(hash))]+"（游离 HEAD：要在这里开发请先新建分支）")
}

// -------- 内部工具 --------

// findGit 定位 git 可执行文件：先 PATH，再几个常见安装位置。
// GUI 进程读的是注册表 PATH，与用户终端里的 PATH 经常不一样（刚装完 git、终端里能用
// 而这里找不到），所以不能只靠 exec.LookPath。
func findGit() (string, error) {
	if p, err := exec.LookPath("git"); err == nil {
		return p, nil
	}
	cands := []string{
		`C:\Program Files\Git\cmd\git.exe`,
		`C:\Program Files (x86)\Git\cmd\git.exe`,
		"/usr/bin/git",
		"/usr/local/bin/git",
		"/opt/homebrew/bin/git",
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("找不到 git：系统 PATH 与常见安装目录里都没有。请先在「环境管理」里安装 git")
}

// run 在 dir 下执行一条 git 命令，返回 stdout / stderr。
// -c core.quotepath=false：默认 git 会把中文路径转义成 \344\270\255，面板上就没法看了。
func run(bin, dir string, timeout time.Duration, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	full := append([]string{"-C", dir, "-c", "core.quotepath=false"}, args...)
	cmd := sysutil.CommandContext(ctx, bin, full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), errb.String(), fmt.Errorf("git 命令超时（%s）", timeout)
	}
	return out.String(), errb.String(), err
}

// runTool 执行一个**非 git** 的外部程序（ssh / ssh-keygen）。
//
// 不能复用 run()：那个会前置 `-C <dir>` 与 `-c core.quotepath=false`，那是 git 的参数，
// 传给 ssh-keygen 会被当成未知选项直接失败。
func runTool(bin string, timeout time.Duration, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := sysutil.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), errb.String(), fmt.Errorf("命令超时（%s）", timeout)
	}
	return out.String(), errb.String(), err
}

// numstat 每个文件的「新增/删除」行数，键是 git 给出的路径。
type numstat map[string][2]int

// commitNumstat 取一次提交的逐文件增删行数（SourceTree 文件列表里那对 +12 −3）。
// 单独一条命令，失败了就当没统计：它只是装饰，不该让它把文件清单也拖挂。
func commitNumstat(bin, dir, hash string) numstat {
	m := numstat{}
	out, _, err := run(bin, dir, cmdTimeout, "show", "--numstat", "--pretty=format:", "--no-color", hash)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		// 二进制文件 git 给的是 "-"，Atoi 失败留 0
		add, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
		del, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
		m[fields[2]] = [2]int{add, del}
	}
	return m
}

// lookup 按 name-status 的路径取统计值。重命名在这里是「旧 → 新」，
// 而 numstat 的路径可能被 git 压缩成 `dir/{a => b}`，故退一步用新路径、
// 再用文件名兜底；都匹配不上就返回 0，前端据此不显示角标。
func (m numstat) lookup(p string) (int, int) {
	if v, ok := m[p]; ok {
		return v[0], v[1]
	}
	cand := p
	if i := strings.LastIndex(cand, " → "); i >= 0 {
		cand = cand[i+len(" → "):]
	}
	if v, ok := m[cand]; ok {
		return v[0], v[1]
	}
	base := filepath.Base(cand)
	for k, v := range m {
		if filepath.Base(k) == base {
			return v[0], v[1]
		}
	}
	return 0, 0
}

// parseBranchLine 解析 `main...origin/main [ahead 1, behind 2]`。
func parseBranchLine(s string, st *RepoStatus) {
	if strings.HasPrefix(s, "No commits yet on ") {
		st.Branch = strings.TrimSpace(strings.TrimPrefix(s, "No commits yet on "))
		st.NoCommits = true
		return
	}
	rest := s
	if i := strings.Index(s, "..."); i >= 0 {
		st.Branch = s[:i]
		rest = s[i+3:]
		if j := strings.Index(rest, " "); j >= 0 {
			st.Upstream = rest[:j]
			rest = rest[j:]
		} else {
			st.Upstream = rest
			rest = ""
		}
	} else if i := strings.Index(s, " "); i >= 0 {
		st.Branch = s[:i]
		rest = s[i:]
	} else {
		st.Branch = s
		rest = ""
	}
	st.Ahead = leadingInt(rest, "ahead ")
	st.Behind = leadingInt(rest, "behind ")
}

func leadingInt(s, marker string) int {
	i := strings.Index(s, marker)
	if i < 0 {
		return 0
	}
	num := ""
	for _, r := range s[i+len(marker):] {
		if r < '0' || r > '9' {
			break
		}
		num += string(r)
	}
	n, _ := strconv.Atoi(num)
	return n
}

func statusDesc(code string) string {
	switch {
	case code == "??":
		return "未跟踪"
	case strings.ContainsAny(code, "M"):
		return "已修改"
	case strings.ContainsAny(code, "A"):
		return "新增"
	case strings.ContainsAny(code, "D"):
		return "删除"
	case strings.ContainsAny(code, "R"):
		return "重命名"
	case strings.ContainsAny(code, "C"):
		return "复制"
	case strings.ContainsAny(code, "U"):
		return "冲突"
	}
	return "改动"
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
