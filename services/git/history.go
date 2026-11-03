// 单个文件的考古：它改过几次（--follow）、每一行是谁写的（blame）、
// 它在某次提交里长什么样（show rev:path）。外加两条历史操作：cherry-pick 与存档导出。
//
// 为什么「文件历史」必须带 --follow：改名是 git 里最常见的操作，
// 不带 --follow 时历史会在改名那一刻断掉——用户看到的是「这个文件是上周才新建的」，
// 而它其实有三年的提交。代价是 --follow 只接受单个路径，所以这个入口一次只能问一个文件。
package git

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quickdock/services"
)

const (
	maxBlameLines  = 2000      // 单次 blame 最多渲染多少行（超大文件只截前 2000 行）
	maxFileBytes   = 400 << 10 // 历史版本内容上限：再大就不是「看一眼」的场景了
	blameTimeout   = 40 * time.Second
	archiveTimeout = 180 * time.Second
)

// BlameLine 一行代码的归属。
type BlameLine struct {
	Line   int    `json:"line"`
	Hash   string `json:"hash"`
	Short  string `json:"short"`
	Author string `json:"author"`
	Ts     int64  `json:"ts"`
	Text   string `json:"text"`
}

// BlameResult blame 结果 + 截断标记。
type BlameResult struct {
	File      string      `json:"file"`
	Lines     []BlameLine `json:"lines"`
	Truncated bool        `json:"truncated"`
}

// GitFileHistory 某个文件的提交历史（跟随重命名）。rev 为空表示从 HEAD 开始。
func (s *GitService) GitFileHistory(path, file, rev string, limit int) *services.ApiResult {
	file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
	if file == "" {
		return services.FailMsg("缺少文件路径")
	}
	if limit <= 0 || limit > maxLogEntries {
		limit = 100
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	args := []string{"log", "--follow", "--date-order", "-n", strconv.Itoa(limit), commitFormat, "--no-color"}
	if r := strings.TrimSpace(rev); r != "" {
		args = append(args, r)
	}
	args = append(args, "--", file)
	out, errOut, e := run(bin, path, cmdTimeout, args...)
	if e != nil {
		if strings.Contains(errOut, "does not have any commits") || strings.Contains(errOut, "bad default revision") {
			return services.Ok([]Commit{})
		}
		return services.FailMsg("读取文件历史失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	commits := []Commit{}
	for _, line := range strings.Split(out, "\n") {
		if c, ok := parseCommitLine(strings.TrimRight(line, "\r")); ok {
			commits = append(commits, c)
		}
	}
	return services.Ok(commits)
}

// GitFileBlame 逐行归属。参数只给文件路径，不改工作区、不写任何东西。
func (s *GitService) GitFileBlame(path, file, rev string) *services.ApiResult {
	file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
	if file == "" {
		return services.FailMsg("缺少文件路径")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	args := []string{"blame", "--line-porcelain", "--no-color"}
	if r := strings.TrimSpace(rev); r != "" {
		args = append(args, r)
	}
	args = append(args, "--", file)
	out, errOut, e := run(bin, path, blameTimeout, args...)
	if e != nil {
		msg := strings.TrimSpace(errOut)
		switch {
		case strings.Contains(msg, "no such path"):
			return services.FailMsg("这次提交里没有这个文件：" + file)
		case strings.Contains(msg, "not a git repository"):
			return services.FailMsg("不是 git 仓库")
		case strings.Contains(msg, "has only"):
			// 二进制文件 git 会直接拒绝，没有「行」可归属
			return services.FailMsg("这是二进制文件或没有可归属的行")
		}
		return services.FailMsg("读取 blame 失败：" + clip(msg, 300))
	}

	res := BlameResult{File: file, Lines: []BlameLine{}}
	cur := BlameLine{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "\t") {
			cur.Text = line[1:]
			cur.Short = cur.Hash
			if len(cur.Short) > 8 {
				cur.Short = cur.Short[:8]
			}
			res.Lines = append(res.Lines, cur)
			if len(res.Lines) >= maxBlameLines {
				res.Truncated = true
				break
			}
			cur = BlameLine{Hash: cur.Hash, Author: cur.Author, Ts: cur.Ts}
			continue
		}
		fields := strings.Fields(line)
		// 头行：<sha> <orig-line> <final-line> [<group-size>]，四段且首段是 40 位十六进制
		if len(fields) >= 3 && len(fields[0]) >= 8 && isHex(fields[0]) {
			cur.Hash = fields[0]
			if n, err := strconv.Atoi(fields[2]); err == nil {
				cur.Line = n
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "author "); ok {
			cur.Author = strings.TrimSpace(v)
			continue
		}
		if v, ok := strings.CutPrefix(line, "author-time "); ok {
			cur.Ts, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			continue
		}
		// author-mail / summary / filename / previous / boundary 等都不需要
	}
	return services.Ok(res)
}

// ShowFileResult 某个提交里的文件内容。
type ShowFileResult struct {
	Rev       string `json:"rev"`
	File      string `json:"file"`
	Content   string `json:"content"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

// GitShowFile 取「某次提交里的这个文件」（git show rev:path），只读。
//
// 用来看「改之前是什么样」。不做另存为：面板里另存一份到哪都是猜的，
// 真想保存的用户会自己去 git show > file。
func (s *GitService) GitShowFile(path, rev, file string) *services.ApiResult {
	file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
	rev = strings.TrimSpace(rev)
	if file == "" || rev == "" {
		return services.FailMsg("缺少提交或文件路径")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, e := run(bin, path, cmdTimeout, "show", rev+":"+file)
	if e != nil {
		msg := strings.TrimSpace(errOut)
		if strings.Contains(msg, "exists on disk, but not in") || strings.Contains(msg, "does not exist") {
			return services.FailMsg("这次提交里没有这个路径（可能是改名前的旧路径）")
		}
		return services.FailMsg("读取文件内容失败：" + clip(msg, 300))
	}
	res := ShowFileResult{Rev: rev, File: file}
	if len(out) > maxFileBytes {
		out = out[:maxFileBytes]
		res.Truncated = true
	}
	if strings.IndexByte(out, 0) >= 0 {
		res.Binary = true
		return services.Ok(res)
	}
	res.Content = strings.ReplaceAll(out, "\r\n", "\n")
	return services.Ok(res)
}

// GitCherryPick 把指定提交摘到当前分支（新哈希，不改原分支的历史）。
func (s *GitService) GitCherryPick(path, hash string) *services.ApiResult {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return services.FailMsg("缺少提交 hash")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, e := run(bin, path, commitTimeout, "cherry-pick", "--no-edit", hash)
	if e != nil {
		text := strings.TrimSpace(out + "\n" + errOut)
		if strings.Contains(text, "CONFLICT") || strings.Contains(text, "could not apply") {
			return services.FailMsg("摘取有冲突：解决后提交即可（git cherry-pick --continue）；想放弃就 git cherry-pick --abort")
		}
		if strings.Contains(text, "empty") {
			return services.FailMsg("这次提交的改动在当前分支上已经存在，摘取后没有内容可提交")
		}
		return services.FailMsg("cherry-pick 失败：" + clip(text, 400))
	}
	return services.OkMsg(nil, "已摘取 "+hash[:min(7, len(hash))]+" 到当前分支")
}

// GitArchive 把某个提交（默认 HEAD）打包成 zip 到用户选定的路径。
//
// 为什么用 git archive 而不是自己压缩工作区：archive 只打包**已提交**的内容，
// 不含 .git、不含未跟踪的临时文件——这正是「发一个版本给对方」需要的语义。
func (s *GitService) GitArchive(path, rev, dest string) *services.ApiResult {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return services.FailMsg("没有选择保存位置")
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".zip") {
		dest += ".zip" // 另存为对话框可能把后缀吃掉，这里补齐，否则用户得到一个没后缀的文件
	}
	if dir := filepath.Dir(dest); dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return services.FailMsg("保存目录不存在：" + dir)
		}
	}
	rev = strings.TrimSpace(rev)
	if rev == "" {
		rev = "HEAD"
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, e := run(bin, path, archiveTimeout, "archive", "--format=zip", "--output", dest, rev); e != nil {
		return services.FailMsg("导出失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(map[string]any{"dest": dest}, "已导出到 "+dest)
}

// GitSearchLog 全历史搜索：在**所有分支**的提交信息与作者里找关键词。
//
// 面板里已载入的 200 条只够浏览，找「半年前那次改登录态的提交」必须问 git 本身。
// --grep 与 --author 各给一次：git 对它们的默认关系是「或」——摘要命中或作者命中
// 都算，正合「我只记得个大概」的使用方式。不查补丁内容（-S/-G 那种全仓扫描
// 在大仓库上要分钟级，不适合面板的同步请求）。
func (s *GitService) GitSearchLog(path, query string, limit int) *services.ApiResult {
	q := strings.TrimSpace(query)
	if q == "" {
		return services.FailMsg("请输入要搜索的关键词")
	}
	if limit <= 0 || limit > maxLogEntries {
		limit = 100
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, e := run(bin, path, cmdTimeout, "log", "--all", "--date-order", "-i",
		"--grep", q, "--author", q, "-n", strconv.Itoa(limit), commitFormat, "--no-color")
	if e != nil {
		if strings.Contains(errOut, "does not have any commits") || strings.Contains(errOut, "bad default revision") {
			return services.Ok([]Commit{})
		}
		return services.FailMsg("搜索提交失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	commits := []Commit{}
	for _, line := range strings.Split(out, "\n") {
		if c, ok := parseCommitLine(strings.TrimRight(line, "\r")); ok {
			commits = append(commits, c)
		}
	}
	return services.Ok(commits)
}

// GitRestoreFile 把某个提交里的文件内容恢复到工作区（checkout rev -- file）。
//
// git 的这条命令会**同时写工作区与暂存区**：恢复完用户只需确认提交，
// 不用再手动 add 一次。代价是该文件未提交的改动会被覆盖——这个风险
// 由前端在弹确认框时说清楚，后端只负责执行与报错。
func (s *GitService) GitRestoreFile(path, rev, file string) *services.ApiResult {
	rev = strings.TrimSpace(rev)
	file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
	if rev == "" || file == "" {
		return services.FailMsg("缺少提交或文件路径")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "checkout", rev, "--", file); e != nil {
		msg := strings.TrimSpace(errOut)
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "invalid path") {
			return services.FailMsg("这次提交里没有这个路径（可能是改名前的旧路径）")
		}
		return services.FailMsg("恢复失败：" + clip(msg, 300))
	}
	return services.OkMsg(nil, "已恢复 "+file+"（在暂存区，确认提交后生效）")
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}
