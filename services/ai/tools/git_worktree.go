package aitools

// git worktree 能力：给 AI 任务开一个「隔离工作区」。
//
// 与 git.go 的只读工具分开：那个文件的立场是「模型不许替用户做版本控制决定」，
// 这个文件的服务对象不同——工作区是**宿主**为了「别动原工程目录」而建的基础设施，
// 由用户在输入框里勾选、由用户点移除，不是模型自己乱按的按钮。因此这里只提供
// 三个足够窄的动作：建、列、删；不提供 commit / merge / reset。
//
// 命名约定：每个任务建一个**新分支** quickdock/<任务短id>，基于用户选的分支。
// 不直接在某条已有分支上开工作区，有两个理由：
//  1. git 不允许同一个分支被两个工作区同时检出（原目录已经占着）；
//  2. 用户的 main 不该被 AI 的改动直接污染，事后要合并应当是显式动作。

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// gitBranchMax 分支列表上限：大仓库上千条分支，下拉框塞不下也用不着。
	gitBranchMax = 200
	// GitWorktreeBranchPrefix 任务工作区分支的前缀，便于在 git branch 里一眼认出。
	GitWorktreeBranchPrefix = "quickdock/"
)

// GitRepoRoot 返回 dir 所在仓库的根目录；不在仓库里返回 ("", false, nil)。
//
// 不报错是刻意的：调用方（输入框、发送前检查）经常拿到一个普通文件夹，
// 「不是仓库」是常态而不是异常。
func GitRepoRoot(d Deps, dir string) (string, bool) {
	if strings.TrimSpace(dir) == "" {
		return "", false
	}
	out, _, err := runGit(d, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", false
	}
	return filepath.Clean(root), true
}

// GitBranches 列出仓库的本地分支（远程分支单独用 remote=true 取）。
//
// 为什么分别列：下拉里混着 origin/main 与 main 会让用户分不清选哪条，
// 而「基于远程分支」是常见需求（刚 clone 只有 main）。
func GitBranches(d Deps, dir string) (local []string, current string, isRepo bool, err error) {
	root, ok := GitRepoRoot(d, dir)
	if !ok {
		return nil, "", false, nil
	}
	out, errOut, e := runGit(d, root, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if e != nil {
		return nil, "", true, fmt.Errorf("git for-each-ref 失败：%s", strings.TrimSpace(errOut))
	}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		local = append(local, name)
		if len(local) >= gitBranchMax {
			break
		}
	}
	sort.Strings(local)

	// 当前分支：detached 时给空（不是 "HEAD"），前端据此提示「当前不在任何分支上」。
	if b, _, be := runGit(d, root, "rev-parse", "--abbrev-ref", "HEAD"); be == nil {
		b = strings.TrimSpace(b)
		if b != "HEAD" {
			current = b
		}
	}
	return local, current, true, nil
}

// GitRemoteBranches 列出远程跟踪分支（去掉表头 origin/HEAD）。
func GitRemoteBranches(d Deps, dir string) []string {
	root, ok := GitRepoRoot(d, dir)
	if !ok {
		return nil
	}
	out, _, err := runGit(d, root, "for-each-ref", "--format=%(refname:short)", "refs/remotes")
	if err != nil {
		return nil
	}
	var res []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasSuffix(name, "/HEAD") {
			continue
		}
		res = append(res, name)
		if len(res) >= gitBranchMax {
			break
		}
	}
	sort.Strings(res)
	return res
}

// GitWorktreePaths 列出仓库已登记的工作区目录（主工作区在第一个）。
func GitWorktreePaths(d Deps, repo string) ([]string, error) {
	out, errOut, err := runGit(d, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list 失败：%s", strings.TrimSpace(errOut))
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		paths = append(paths, filepath.Clean(strings.TrimPrefix(line, "worktree ")))
	}
	return paths, nil
}

// GitWorktreeAdd 在 repo 下从 base 分支新建 newBranch，并检出到 path。
//
// base 可以是本地分支，也可以是 origin/xxx（git 会建本地跟踪分支的场景我们不用，
// 这里显式 -b 建新分支，因此远程 base 需要已是本地可解析的引用）。
func GitWorktreeAdd(d Deps, repo, path, newBranch, base string) error {
	args := []string{"worktree", "add", "-b", newBranch, path}
	if strings.TrimSpace(base) != "" {
		args = append(args, base)
	}
	_, errOut, err := runGit(d, repo, args...)
	if err != nil {
		msg := strings.TrimSpace(errOut)
		return fmt.Errorf("创建 git worktree 失败：%s", truncateMsg(msg, 400))
	}
	return nil
}

// GitWorktreeRemove 移除一个工作区目录（登记项与目录一起清掉）。
//
// force=false 时 git 会拒绝删除有未提交改动的工作区，这正是我们要的行为：
// 由调用方先检查、再决定要不要强制。
func GitWorktreeRemove(d Deps, repo, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, errOut, err := runGit(d, repo, args...)
	if err != nil {
		return fmt.Errorf("移除工作区失败：%s", truncateMsg(strings.TrimSpace(errOut), 400))
	}
	// prune 清掉「目录已被用户手工删掉」留下的登记项，否则下次同名路径会被拒。
	_, _, _ = runGit(d, repo, "worktree", "prune")
	return nil
}

// GitDirtySummary 返回工作区的改动摘要（空串 = 干净）。
//
// 用 porcelain 而不是 `diff --quiet`：这里要的是「有多少改动、大概是什么」，
// 移除工作区前能告诉用户「有 3 个文件没提交」，而不是一句干巴巴的「有改动」。
func GitDirtySummary(d Deps, dir string) (string, int) {
	out, _, err := runGit(d, dir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", 0
	}
	lines := 0
	var samples []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		if len(samples) < 3 && len(line) > 3 {
			samples = append(samples, strings.TrimSpace(line[3:]))
		}
	}
	if lines == 0 {
		return "", 0
	}
	return strings.Join(samples, "、"), lines
}

// GitBranchExists 判断分支是否已存在（本地）。
func GitBranchExists(d Deps, repo, branch string) bool {
	if strings.TrimSpace(branch) == "" {
		return false
	}
	_, _, err := runGit(d, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// truncateMsg 截断 git 的错误输出（多行 fatal 会淹没真正的那一句）。
func truncateMsg(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
