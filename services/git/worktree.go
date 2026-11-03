// 工作区（未提交改动）的查看、暂存与提交。
//
// 与 service.go 的分工：那边是「看清仓库」的只读视图（状态/分支/历史/某次提交），
// 这边是「把改动交上去」的那条链路：看 diff → 勾选暂存 → 写提交信息（可让 AI 起草）→ commit → push。
//
// 三条设计约束：
//  1. 一律用 `git add -A -- <paths>` / `git restore --staged -- <paths>`，不做「部分行暂存」
//     （行级暂存需要自己写 patch，出错时仓库会停在一个用户看不懂的中间态）。
//  2. commit 失败必须把 hook 的输出原样带回去：pre-commit 挂掉时唯一有用的信息就在那段文本里，
//     只说「提交失败」会让人以为面板坏了。
//  3. AI 只负责**起草**提交信息，不决定提交什么：喂给模型的 diff 只用于措辞。
package git

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"quickdock/services"
)

// WorkFile 工作区里的一条改动。
type WorkFile struct {
	Status string `json:"status"` // porcelain 两字母码：X=索引区、Y=工作区（?? = 未跟踪）
	Path   string `json:"path"`
	Orig   string `json:"orig,omitempty"` // 重命名/复制时的旧路径
	Desc   string `json:"desc"`
	Add    int    `json:"add"`
	Del    int    `json:"del"`
}

// WorkingTree 工作区快照：暂存区与未暂存各一份清单。
//
// 分两份而不是一份带 staged 布尔：界面就是分两组画的（SourceTree 的 Staged / Unstaged），
// 前端再自己 filter 一遍只是把同一件事做成两条路径。
type WorkingTree struct {
	Branch    string     `json:"branch"`
	Upstream  string     `json:"upstream"`
	Ahead     int        `json:"ahead"`
	Behind    int        `json:"behind"`
	NoCommits bool       `json:"noCommits"`
	Staged    []WorkFile `json:"staged"`
	Unstaged  []WorkFile `json:"unstaged"`
}

// GitWorkingTree 读取工作区：已暂存 / 未暂存两组改动。
func (s *GitService) GitWorkingTree(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	// -z：路径里带空格、中文、甚至 " -> " 时都不用转义解析；重命名的旧路径紧跟其后作为下一条。
	out, errOut, err := run(bin, path, cmdTimeout, "status", "--porcelain=v1", "-z", "-b", "--untracked-files=all")
	if err != nil {
		return services.FailMsg("读取工作区失败：" + clip(strings.TrimSpace(errOut), 300))
	}

	wt := WorkingTree{Staged: []WorkFile{}, Unstaged: []WorkFile{}}
	var repoStatus RepoStatus
	stagedStats := diffNumstat(bin, path, "--cached")
	workStats := diffNumstat(bin, path)

	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if e == "" {
			continue
		}
		if strings.HasPrefix(e, "## ") {
			parseBranchLine(e[3:], &repoStatus)
			continue
		}
		if len(e) < 4 {
			continue
		}
		x, y := e[0], e[1]
		f := WorkFile{Path: e[3:], Status: strings.TrimSpace(string([]byte{x, y}))}
		if f.Status == "" {
			f.Status = "M"
		}
		// 重命名/复制：-z 下新路径在前，旧路径是紧接着的独立条目
		if (x == 'R' || x == 'C') && i+1 < len(entries) {
			f.Orig = entries[i+1]
			i++
		}
		f.Desc = statusDesc(f.Status)

		if x != ' ' && x != '?' {
			add, del := stagedStats.lookup(f.Path)
			sf := f
			sf.Add, sf.Del = add, del
			wt.Staged = append(wt.Staged, sf)
		}
		// 未跟踪（??）只属于工作区；冲突标记（U*）两边都可能是空的，按工作区处理
		if y != ' ' || x == '?' {
			add, del := workStats.lookup(f.Path)
			f.Add, f.Del = add, del
			wt.Unstaged = append(wt.Unstaged, f)
		}
	}

	wt.Branch = repoStatus.Branch
	wt.Upstream = repoStatus.Upstream
	wt.Ahead = repoStatus.Ahead
	wt.Behind = repoStatus.Behind
	wt.NoCommits = repoStatus.NoCommits
	return services.Ok(wt)
}

// GitWorkingDiff 一次返回整个工作区的 diff 文本（暂存 + 未暂存 + 未跟踪）。
//
// 不为「单文件」再开一条接口：前端已有按 `diff --git` 切块并按路径筛选的逻辑（历史详情在用），
// 复用同一份数据比分两次取更不容易出现「列表说改了、diff 是空的」这种错位。
func (s *GitService) GitWorkingDiff(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	var b strings.Builder
	if out, _, e := run(bin, path, cmdTimeout, "diff", "--cached", "--no-color", "--unified=3"); e == nil {
		b.WriteString(out)
	}
	if out, _, e := run(bin, path, cmdTimeout, "diff", "--no-color", "--unified=3"); e == nil {
		b.WriteString(out)
	}
	b.WriteString(untrackedDiff(bin, path))
	return services.Ok(map[string]any{"diff": clip(b.String(), 240000)})
}

// GitStage 暂存指定文件（files 为空 = 全部）。
func (s *GitService) GitStage(path string, files []string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	// -A 让「删除」也能被暂存：只 add 已存在文件的话，删掉的文件永远提交不上去
	args := append([]string{"add", "-A", "--"}, files...)
	if _, errOut, e := run(bin, path, cmdTimeout, args...); e != nil {
		return services.FailMsg("暂存失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(nil, "已暂存")
}

// GitUnstage 取消暂存（把文件从索引区挪回工作区）。
//
// 三代命令按可用性降级：restore --staged（git 2.23+）→ reset HEAD（更老）→
// rm --cached（还没第一次提交的空仓库，此时没有 HEAD 可比）。
func (s *GitService) GitUnstage(path string, files []string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	targets := files
	if len(targets) == 0 {
		targets = []string{"."} // 空 = 全部：从仓库根重来一遍
	}
	args := append([]string{"restore", "--staged", "--"}, targets...)
	out, errOut, e := run(bin, path, cmdTimeout, args...)
	if e == nil {
		return services.OkMsg(nil, "已取消暂存")
	}
	msg := strings.TrimSpace(errOut) + strings.TrimSpace(out)
	// 空仓库的实测输出是 `fatal: could not resolve 'HEAD'`（带引号），只匹配前半句
	if strings.Contains(msg, "could not resolve") || strings.Contains(msg, "unknown revision") || strings.Contains(msg, "bad revision") {
		rmArgs := append([]string{"rm", "--cached", "-r", "-q", "--"}, targets...)
		if _, rmErr, e2 := run(bin, path, cmdTimeout, rmArgs...); e2 != nil {
			return services.FailMsg("取消暂存失败：" + clip(strings.TrimSpace(rmErr), 300))
		}
		return services.OkMsg(nil, "已取消暂存")
	}
	resetArgs := append([]string{"reset", "-q", "HEAD", "--"}, targets...)
	if _, _, e2 := run(bin, path, cmdTimeout, resetArgs...); e2 != nil {
		return services.FailMsg("取消暂存失败：" + clip(strings.TrimSpace(msg), 300))
	}
	return services.OkMsg(nil, "已取消暂存")
}

// GitCommit 提交暂存区；stageAll 为真时先把全部改动暂存（「全部提交」）。
func (s *GitService) GitCommit(path, message string, stageAll bool) *services.ApiResult {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return services.FailMsg("提交信息不能为空")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if stageAll {
		if _, errOut, e := run(bin, path, cmdTimeout, "add", "-A", "--"); e != nil {
			return services.FailMsg("暂存失败：" + clip(strings.TrimSpace(errOut), 300))
		}
	}
	out, errOut, err := run(bin, path, commitTimeout, "commit", "-m", msg)
	if err != nil {
		text := strings.TrimSpace(out)
		if e := strings.TrimSpace(errOut); e != "" {
			text = strings.TrimSpace(text + "\n" + e)
		}
		switch {
		case strings.Contains(text, "nothing to commit"), strings.Contains(text, "no changes added to commit"):
			return services.FailMsg("暂存区是空的：先勾选要提交的文件")
		case strings.Contains(text, "Please tell me who you are"), strings.Contains(text, "unable to auto-detect email"):
			return services.FailMsg("git 还没配置提交者身份：先在终端执行 git config --global user.name 与 user.email")
		}
		// hook 失败（pre-commit / commit-msg）的原样输出在这里，一定要透出
		return services.FailMsg("提交失败：" + clip(text, 800))
	}
	hash, _, _ := run(bin, path, cmdTimeout, "rev-parse", "--short", "HEAD")
	short := strings.TrimSpace(hash)
	return services.OkMsg(map[string]any{"hash": short}, "已提交 "+short)
}

// GitCommitAmend 把暂存区改动并入上一次提交（同时可改提交信息）。
//
// message 为空 = 保留原信息（--no-edit），只把新暂存的内容叠进去；
// 填了 = 整条信息换掉。改写的是历史（哈希会变），如果上次提交已推送到远端，
// 推上去就需要 force——前端在已推送时会先弹确认，这里只管执行。
func (s *GitService) GitCommitAmend(path, message string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	// 没有第一次提交时 amend 没有目标，git 的报错是一段英文长句；先探 HEAD 给人话
	if _, _, e := run(bin, path, cmdTimeout, "rev-parse", "-q", "--verify", "HEAD"); e != nil {
		return services.FailMsg("还没有任何提交，无从修改；先做一次普通提交")
	}
	var args []string
	if msg := strings.TrimSpace(message); msg != "" {
		args = []string{"commit", "--amend", "-m", msg}
	} else {
		args = []string{"commit", "--amend", "--no-edit"}
	}
	out, errOut, err := run(bin, path, commitTimeout, args...)
	if err != nil {
		text := strings.TrimSpace(out)
		if e := strings.TrimSpace(errOut); e != "" {
			text = strings.TrimSpace(text + "\n" + e)
		}
		if strings.Contains(text, "Please tell me who you are") || strings.Contains(text, "unable to auto-detect email") {
			return services.FailMsg("git 还没配置提交者身份：先在终端执行 git config --global user.name 与 user.email")
		}
		// merge 进行中时 amend 会被拒绝，这个中间态必须如实告知
		if strings.Contains(text, "MERGE_HEAD") || strings.Contains(text, "CHERRY_PICK_HEAD") {
			return services.FailMsg("合并/摘取还没完成，不能修改上次提交：先解决冲突并提交，或 git merge --abort 退出")
		}
		return services.FailMsg("修改上次提交失败：" + clip(text, 800))
	}
	hash, _, _ := run(bin, path, cmdTimeout, "rev-parse", "--short", "HEAD")
	short := strings.TrimSpace(hash)
	return services.OkMsg(map[string]any{"hash": short}, "已修改上次提交，新哈希 "+short)
}

// -------- 文件级操作：丢弃 / 忽略 / 停止跟踪 --------

// GitDiscardChanges 丢弃工作区改动（**不可恢复**，前端必须先确认）。
//
// includeStaged 的语义：
//   - false：只还原工作区，索引里已经暂存的内容保留（右键「未暂存」行时用）；
//   - true ：连索引一起回退到 HEAD（右键「已暂存」行时用，此时用户期望的是「这处改动不要了」）。
//
// 未跟踪文件走 `git clean`，已跟踪文件走 `git restore`：restore 对未跟踪文件会直接报错，
// 而 clean 又不会碰已跟踪文件——一批文件里两种都有时只能分开跑，不能图省事用一条命令。
func (s *GitService) GitDiscardChanges(path string, files []string, includeStaged bool) *services.ApiResult {
	files = cleanPaths(files)
	if len(files) == 0 {
		return services.FailMsg("请先选中要丢弃的文件")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}

	st := statusMap(bin, path)
	var untracked, tracked []string
	for _, f := range files {
		if e, ok := st[f]; ok && e.X == '?' {
			untracked = append(untracked, f)
			continue
		}
		tracked = append(tracked, f)
	}

	if len(untracked) > 0 {
		// -d：未跟踪的整目录也要删干净；-q 只保留错误输出
		args := append([]string{"clean", "-f", "-d", "-q", "--"}, untracked...)
		if _, errOut, e := run(bin, path, cmdTimeout, args...); e != nil {
			return services.FailMsg("丢弃未跟踪文件失败：" + clip(strings.TrimSpace(errOut), 300))
		}
	}
	if len(tracked) > 0 {
		args := []string{"restore"}
		if includeStaged {
			args = append(args, "--source=HEAD", "--staged")
		}
		args = append(args, "--worktree", "--")
		args = append(args, tracked...)
		if _, errOut, e := run(bin, path, cmdTimeout, args...); e != nil {
			// 空仓库（还没有第一次提交）没有 HEAD 可回退：只能把文件从索引摘掉再落盘删除
			if includeStaged && isEmptyHeadErr(errOut) {
				if e2 := dropFromIndexAndDisk(bin, path, tracked); e2 != nil {
					return services.FailMsg("丢弃改动失败：" + clip(e2.Error(), 300))
				}
				return services.OkMsg(nil, "已丢弃改动")
			}
			return services.FailMsg("丢弃改动失败：" + clip(strings.TrimSpace(errOut), 300))
		}
	}
	return services.OkMsg(nil, "已丢弃改动")
}

// GitAddIgnore 把选中文件写成 .gitignore 规则。
//
// mode 决定粒度（对应 SourceTree 右键里的三种「忽略」）：
//
//	file → /dir/name.ext   只忽略这一个文件
//	dir  → /dir/          忽略整个目录
//	ext  → *.ext          忽略所有同扩展名文件
//
// 已被跟踪的文件写规则不会生效（git 只对未跟踪文件应用 ignore），
// 这里检测出来并写进返回文案，免得用户手动执行「忽略」后仍看到文件在列表里而以为面板坏了。
func (s *GitService) GitAddIgnore(path string, files []string, mode string) *services.ApiResult {
	files = cleanPaths(files)
	if len(files) == 0 {
		return services.FailMsg("请先选中要忽略的文件")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	root := repoRoot(bin, path)
	ignorePath := filepath.Join(root, ".gitignore")

	existing := map[string]bool{}
	body := ""
	if data, e := os.ReadFile(ignorePath); e == nil {
		body = string(data)
		for _, line := range strings.Split(body, "\n") {
			existing[strings.TrimSpace(line)] = true
		}
	}

	added := []string{}
	for _, f := range files {
		rel := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(f), "./"))
		var rule string
		switch mode {
		case "dir":
			d := rel
			if !strings.HasSuffix(d, "/") {
				if i := strings.LastIndex(d, "/"); i >= 0 {
					d = d[:i+1]
				} else {
					d = "" // 根目录下的文件没有「所在目录」可忽略，退化为按文件忽略
				}
			}
			if d == "" {
				rule = "/" + rel
			} else {
				rule = "/" + strings.Trim(d, "/") + "/"
			}
		case "ext":
			if ext := filepath.Ext(rel); ext != "" {
				rule = "*" + ext
			} else {
				rule = "/" + rel // 没有扩展名，按扩展名忽略没有意义
			}
		default:
			rule = "/" + rel
		}
		if existing[rule] {
			continue
		}
		existing[rule] = true
		added = append(added, rule)
	}

	if len(added) > 0 {
		out := body
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += strings.Join(added, "\n") + "\n"
		if err := os.WriteFile(ignorePath, []byte(out), 0o644); err != nil {
			return services.FailMsg("写入 .gitignore 失败：" + clip(err.Error(), 200))
		}
	}

	msg := "已写入 .gitignore"
	if len(added) == 0 {
		msg = "这些规则已经在 .gitignore 里了"
	}
	if n := len(trackedSet(bin, path, files)); n > 0 {
		msg += "；其中 " + strconv.Itoa(n) + " 个文件已被跟踪，需先「停止跟踪」才生效"
	}
	return services.OkMsg(map[string]any{"added": added}, msg)
}

// GitUntrack 停止跟踪但保留磁盘文件（`git rm --cached`）。
// 这是「已提交过的文件想忽略」的前置动作：先停止跟踪 → 再写 .gitignore → 下次提交里它变成删除。
func (s *GitService) GitUntrack(path string, files []string) *services.ApiResult {
	files = cleanPaths(files)
	if len(files) == 0 {
		return services.FailMsg("请先选中要停止跟踪的文件")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	tracked := trackedPaths(bin, path, files)
	if len(tracked) == 0 {
		return services.FailMsg("这些文件没有被跟踪，无需停止跟踪")
	}
	args := append([]string{"rm", "--cached", "-r", "-q", "--"}, tracked...)
	if _, errOut, e := run(bin, path, cmdTimeout, args...); e != nil {
		return services.FailMsg("停止跟踪失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(nil, "已停止跟踪，文件仍保留在磁盘")
}

// workEntry porcelain 两个状态位：X=索引、Y=工作区。
type workEntry struct{ X, Y byte }

// statusMap 一次 status 调用得到「路径 → 状态位」。
//
// 丢弃要按文件判断「是不是未跟踪」——这决定了走 clean 还是 restore，
// 逐文件再问一次 git 会是 N 次进程启动，故整份取回来查。
func statusMap(bin, dir string) map[string]workEntry {
	m := map[string]workEntry{}
	out, _, err := run(bin, dir, cmdTimeout, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return m
	}
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 || strings.HasPrefix(e, "## ") {
			continue
		}
		m[e[3:]] = workEntry{X: e[0], Y: e[1]}
		// 重命名/复制：-z 下旧路径是紧跟着的独立条目，跳过它免得被当成一条改动
		if (e[0] == 'R' || e[0] == 'C') && i+1 < len(entries) {
			i++
		}
	}
	return m
}

func trackedSet(bin, dir string, files []string) map[string]bool {
	m := map[string]bool{}
	for _, p := range trackedPaths(bin, dir, files) {
		m[p] = true
	}
	return m
}

// trackedPaths 从给定路径里挑出「索引中已有」的那些。
func trackedPaths(bin, dir string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"ls-files", "-z", "--"}, files...)
	out, _, err := run(bin, dir, cmdTimeout, args...)
	if err != nil {
		return nil
	}
	res := []string{}
	for _, p := range strings.Split(out, "\x00") {
		if strings.TrimSpace(p) != "" {
			res = append(res, p)
		}
	}
	return res
}

// dropFromIndexAndDisk 空仓库里的「丢弃」：把文件从索引摘掉并删除落盘文件。
func dropFromIndexAndDisk(bin, dir string, files []string) error {
	if _, errOut, e := run(bin, dir, cmdTimeout, append([]string{"rm", "--cached", "-r", "-q", "--"}, files...)...); e != nil {
		return errors.New(clip(strings.TrimSpace(errOut), 300))
	}
	for _, f := range files {
		abs := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.RemoveAll(abs); err != nil {
			return err
		}
	}
	return nil
}

func isEmptyHeadErr(errOut string) bool {
	s := errOut
	// 空仓库的实测输出是 `fatal: could not resolve 'HEAD'`（带引号），
	// 所以这里只匹配前半句，不要写成 "could not resolve HEAD"。
	return strings.Contains(s, "could not resolve") ||
		strings.Contains(s, "unknown revision") ||
		strings.Contains(s, "bad revision") ||
		strings.Contains(s, "ambiguous argument")
}

// repoRoot 仓库根（gitignore 必须写在根上才会对全仓库生效）。
func repoRoot(bin, dir string) string {
	out, _, err := run(bin, dir, cmdTimeout, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(out) == "" {
		return dir
	}
	return filepath.Clean(strings.TrimSpace(out))
}

// cleanPaths 去空、去重、统一分隔符：前端可能把同一路径传两次（勾选 + 右键选中）。
func cleanPaths(files []string) []string {
	seen := map[string]bool{}
	res := []string{}
	for _, f := range files {
		f = strings.TrimSpace(strings.ReplaceAll(f, "\\", "/"))
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		res = append(res, f)
	}
	return res
}

// -------- AI 起草提交信息 --------

const commitMsgSystemPrompt = `你是 Git 提交信息生成器。根据用户给出的改动清单与 diff，输出一条可以直接使用的提交信息。
规则：
1. 首行格式为「类型(范围): 摘要」，类型从 feat/fix/refactor/perf/docs/style/test/chore/build/ci 中选；摘要用中文，不超过 50 字，结尾不加句号。
2. 改动涉及多个互不相关的点时，首行之后空一行，再写 1~3 条以「- 」开头的要点，每条不超过 40 字。
3. 只输出提交信息本身：不要解释、不要 Markdown 代码块、不要引号、不要复述 diff 内容。`

// aiDiffMaxRune 送进模型的 diff 上限。提交信息只需要「改了什么」，不需要每一行。
const aiDiffMaxRune = 12000

// GitCommitMessageAI 让 AI 根据当前改动起草一条提交信息。
//
// 优先喂暂存区：那才是「这一次要提交的东西」。没有暂存内容时退回全部改动（含未跟踪文件名），
// 这样用户刚改完还没勾选也能先起草。
func (s *GitService) GitCommitMessageAI(path string) *services.ApiResult {
	if s.App == nil || s.App.AskAIFn == nil {
		return services.FailMsg("AI 能力不可用：先在设置里配置一个 AI 档案")
	}
	if s.App.AIAvailableFn != nil {
		if ok, reason, _, _ := s.App.AIAvailableFn(); !ok {
			return services.FailMsg("AI 不可用：" + reason)
		}
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	staged, _, _ := run(bin, path, cmdTimeout, "diff", "--cached", "--no-color", "--unified=2")
	branch := ""
	if b, _, e := run(bin, path, cmdTimeout, "rev-parse", "--abbrev-ref", "HEAD"); e == nil {
		branch = strings.TrimSpace(b)
	}

	scope := "暂存区"
	diff := staged
	names := stagedPaths(bin, path)
	if strings.TrimSpace(diff) == "" {
		scope = "全部改动"
		diff, _, _ = run(bin, path, cmdTimeout, "diff", "--no-color", "--unified=2")
		names = workingPaths(bin, path)
		if len(names) == 0 {
			return services.FailMsg("没有可提交的改动")
		}
	}

	prompt := strings.Builder{}
	prompt.WriteString("分支：" + branch + "\n")
	prompt.WriteString("范围：" + scope + "\n")
	prompt.WriteString("改动文件：\n")
	for i, n := range names {
		if i >= 40 {
			prompt.WriteString("- …（更多文件已省略）\n")
			break
		}
		prompt.WriteString("- " + n + "\n")
	}
	if strings.TrimSpace(diff) != "" {
		prompt.WriteString("\ndiff（可能截断）：\n")
		prompt.WriteString(clip(diff, aiDiffMaxRune))
	}

	text, model, err := s.App.AskAIFn(commitMsgSystemPrompt, prompt.String(), 60)
	if err != nil {
		return services.Fail(err)
	}
	msg := cleanCommitMessage(text)
	if msg == "" {
		return services.FailMsg("AI 没有返回可用的提交信息，换一次试试")
	}
	return services.Ok(map[string]any{"message": msg, "model": model, "scope": scope})
}

// cleanCommitMessage 去掉模型偶尔多写的东西：代码围栏、包裹的引号、行首多余空行。
func cleanCommitMessage(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'“”")
	s = strings.TrimSpace(s)
	lines := make([]string, 0, 8)
	for _, l := range strings.Split(s, "\n") {
		// 模型有时会加「提交信息：」这样的前缀行，去掉
		if strings.HasPrefix(l, "提交信息：") || strings.HasPrefix(l, "提交信息:") {
			continue
		}
		lines = append(lines, strings.TrimRight(l, " "))
	}
	return clip(strings.TrimSpace(strings.Join(lines, "\n")), 800)
}

// stagedPaths / workingPaths 只是给 prompt 一份文件清单（模型看 diff 也要看全局）。
func stagedPaths(bin, dir string) []string {
	return porcelainPaths(bin, dir, "--porcelain=v1", "-z", "--untracked-files=no")
}

func workingPaths(bin, dir string) []string {
	return porcelainPaths(bin, dir, "--porcelain=v1", "-z", "--untracked-files=all")
}

func porcelainPaths(bin, dir string, args ...string) []string {
	out, _, err := run(bin, dir, cmdTimeout, append([]string{"status"}, args...)...)
	if err != nil {
		return nil
	}
	paths := []string{}
	for _, e := range strings.Split(out, "\x00") {
		if len(e) < 4 || strings.HasPrefix(e, "## ") {
			continue
		}
		paths = append(paths, strings.TrimSpace(e[:2])+" "+e[3:])
	}
	return paths
}

// -------- diff 统计 --------

// diffNumstat 工作区 / 暂存区的逐文件增删行数。
func diffNumstat(bin, dir string, extra ...string) numstat {
	m := numstat{}
	args := append([]string{"diff", "--numstat", "--no-color"}, extra...)
	out, _, err := run(bin, dir, cmdTimeout, args...)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
		if len(fields) < 3 {
			continue
		}
		add, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
		del, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
		m[fields[2]] = [2]int{add, del}
	}
	return m
}

// -------- 未跟踪文件的伪 diff --------

const (
	untrackedMaxFiles = 30   // 一次最多造多少个未跟踪文件的块
	untrackedMaxLines = 1200 // 单文件最多展示多少行
	untrackedMaxBytes = 256 << 10
)

// untrackedDiff 给未跟踪文件造 diff。
//
// 为什么自己做而不调 `git diff --no-index /dev/null <file>`：Windows 上 /dev/null
// 不是每个 git 版本都认，且二进制文件会直接报错退出。自己读文件还能顺手判断二进制与截断。
func untrackedDiff(bin, dir string) string {
	out, _, err := run(bin, dir, cmdTimeout, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, p := range strings.Split(out, "\x00") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if n >= untrackedMaxFiles {
			b.WriteString("\n# 未跟踪文件过多，仅列出前 " + strconv.Itoa(untrackedMaxFiles) + " 个\n")
			break
		}
		n++
		b.WriteString(untrackedFileBlock(filepath.Join(dir, p), p))
	}
	return b.String()
}

func untrackedFileBlock(abs, rel string) string {
	var b strings.Builder
	b.WriteString("diff --git a/" + rel + " b/" + rel + "\n")
	b.WriteString("new file mode 100644\n")
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		b.WriteString("Binary files /dev/null and b/" + rel + " differ\n")
		return b.String()
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		b.WriteString("Binary files /dev/null and b/" + rel + " differ\n")
		return b.String()
	}
	if len(data) > untrackedMaxBytes {
		data = data[:untrackedMaxBytes]
	}
	// NUL 字节 = 二进制（git 也是这么判的）
	if strings.IndexByte(string(data), 0) >= 0 {
		b.WriteString("Binary files /dev/null and b/" + rel + " differ\n")
		return b.String()
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	truncated := false
	if len(lines) > untrackedMaxLines {
		lines = lines[:untrackedMaxLines]
		truncated = true
	}
	b.WriteString("--- /dev/null\n")
	b.WriteString("+++ b/" + rel + "\n")
	b.WriteString("@@ -0,0 +1," + strconv.Itoa(len(lines)) + " @@\n")
	for _, l := range lines {
		b.WriteString("+" + l + "\n")
	}
	if truncated {
		b.WriteString("+…（文件过长，仅显示前 " + strconv.Itoa(untrackedMaxLines) + " 行）\n")
	}
	return b.String()
}
