package ai

// 隔离工作区（git worktree）：让一条任务在独立副本里改代码，原工程目录不动。
//
// 为什么值得做：file_edit / run_command 已经能改真代码，用户每次都得先自己
// git stash 或复制一份目录才敢让 AI 动手。worktree 把这件事变成输入框里一个开关：
// 勾上之后 cwd 落到 <数据目录>/ai_worktrees/wt_<任务短id>，模型看到的是同一个仓库
// （同一份 .git、同样的历史），改的却是另一份工作区文件。
//
// 几个刻意的取舍：
//   - **每条任务一个工作区，分支叫 quickdock/<短id>**。不直接检出用户的分支：
//     git 不允许同一分支被两个工作区同时检出（原目录已经占着），而且用户的 main
//     不该被 AI 的改动直接污染——要合并应当是用户显式的动作。
//   - **建在数据目录下而不是仓库旁边**：仓库根目录是用户的地盘，不能因为开了一次
//     AI 任务就往里塞目录（还会被 git status 当成未跟踪改动）。
//   - **工作区不做自动删除**：里面有未提交改动时静默删掉是不可接受的损失。
//     移除走 AIWorktreeRemove，先查 dirty，有改动时要求前端二次确认。
//   - **fsguard 不用改**：写白名单本来就包含「会话 cwd」，worktree 路径一旦落库成
//     cwd，写入授权自动覆盖它（见 internal/fsguard 的 WriteRoots）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
	aitools "quickdock/services/ai/tools"
)

const (
	// aiWorktreeDirName 工作区目录名（数据目录下）。
	aiWorktreeDirName = "ai_worktrees"
	// aiWorktreeMaxBranchTry 分支重名时最多试几个后缀。
	aiWorktreeMaxBranchTry = 20
)

// aiGitDeps 构造 git 工具需要的依赖（DB 让 fsguard 生效，Env 让 gitBin 能回落到便携版）。
func (a *AIService) aiGitDeps() aitools.Deps {
	d := aitools.Deps{}
	if a.App != nil {
		d.DB = a.App.DB
		d.Env = a.App.Env
	}
	return d
}

// aiWorktreeRoot 工作区根目录（数据目录下）。
func aiWorktreeRoot() string {
	dir, _ := platform.DataDirInfo()
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, aiWorktreeDirName)
}

// aiWorktreeShortID 会话 id 的短片段（目录名与分支名都用它）。
func aiWorktreeShortID(convID string) string {
	id := strings.Map(func(r rune) rune {
		if r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return -1
	}, convID)
	id = strings.ToLower(id)
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		id = "task"
	}
	return id
}

// aiWorktreePath 这条任务的工作区目录。
func aiWorktreePath(convID string) string {
	root := aiWorktreeRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "wt_"+aiWorktreeShortID(convID))
}

// worktreeUsable 判断会话记着的工作区是否真的还在（目录在 + git 认它）。
func (a *AIService) worktreeUsable(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return false
	}
	paths, err := aitools.GitWorktreePaths(a.aiGitDeps(), path)
	if err != nil {
		return false
	}
	want := filepath.Clean(path)
	for _, p := range paths {
		if strings.EqualFold(filepath.Clean(p), want) {
			return true
		}
	}
	// git 自己列不出来（.git/worktrees 元数据被清过、或路径被搬走），说明这个目录
	// 已经不再是一个可用的工作区：宁可当作「没开」让它重建，也不要让模型在一个
	// 半坏的工作区里改文件——那些改动 git 自己都不认得。
	return false
}

// applyWorktree 决定本轮 cwd 要不要落到隔离工作区。
//
// 入参 want 来自前端这次的勾选；conv 上记着的是「这条任务已经有工作区」。
// 返回本轮的最终 cwd（没开工作区时原样返回 cwd）。
//
// 复用的是**会话级**状态而不是请求级：模型跑了十轮，每一轮都必须是同一个工作区，
// 否则上一轮改的文件下一轮就看不见了。前端后续轮次即便不再传 worktree 字段，
// 这里也会按会话上记录的值继续用。
func (a *AIService) applyWorktree(conv *db.AIConversation, cwd string, want bool, branch string) string {
	if a.App == nil || a.App.DB == nil || conv == nil {
		return cwd
	}
	if strings.TrimSpace(cwd) == "" {
		return cwd
	}

	// 已有工作区：优先续用（哪怕这次没勾——关开关不等于拆工作区）。
	if strings.TrimSpace(conv.WorktreePath) != "" {
		if a.worktreeUsable(conv.WorktreePath) {
			return conv.WorktreePath
		}
		logger.W("[ai-worktree] 会话 %s 的工作区已不可用，清除记录 path=%s", conv.ID, conv.WorktreePath)
		if err := a.App.DB.SetAIConversationWorktree(conv.ID, "", ""); err != nil {
			logger.W("[ai-worktree] 清除工作区记录失败 conv=%s: %v", conv.ID, err)
		}
		conv.WorktreePath = ""
		conv.WorktreeBranch = ""
	}

	if !want {
		return cwd
	}

	// 新建：先确认 cwd 真在 git 仓库里（前端会按 isRepo 控制开关显隐，这里是兜底）。
	root, ok := aitools.GitRepoRoot(a.aiGitDeps(), cwd)
	if !ok {
		logger.W("[ai-worktree] %s 不是 git 仓库，本次不建工作区", cwd)
		return cwd
	}
	path := aiWorktreePath(conv.ID)
	if path == "" {
		return cwd
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.W("[ai-worktree] 准备工作区目录失败: %v", err)
		return cwd
	}
	// 目录残留（上次建到一半 / 用户手工删了登记项）：先按强制移除清干净。
	if _, err := os.Stat(path); err == nil {
		_ = aitools.GitWorktreeRemove(a.aiGitDeps(), root, path, true)
		_ = os.RemoveAll(path)
	}

	cur := ""
	if b, isRepo, _ := aitools.GitBranch(a.aiGitDeps(), cwd); isRepo && b != "" && !strings.HasPrefix(b, "detached") {
		cur = b
	}
	base := strings.TrimSpace(branch)
	if base == "" {
		base = cur
	} else if !aitools.GitBranchExists(a.aiGitDeps(), root, base) {
		// 用户选的分支可能刚被删掉（下拉是上一次拉的列表）：远程分支仍可用，
		// 其它情况回落到当前分支。宁可基于错一点的分支也比「静默不开工作区」好——
		// 后者会让用户以为隔离生效了，实际模型在改原目录。
		remote := false
		for _, rb := range aitools.GitRemoteBranches(a.aiGitDeps(), root) {
			if rb == base {
				remote = true
				break
			}
		}
		if !remote {
			logger.W("[ai-worktree] 分支 %s 已不存在，回落到当前分支 %s", base, cur)
			base = cur
		}
	}
	if base == "" {
		base = "HEAD"
	}
	newBranch := a.pickWorktreeBranch(root, conv.ID)
	if err := aitools.GitWorktreeAdd(a.aiGitDeps(), root, path, newBranch, base); err != nil {
		logger.W("[ai-worktree] 创建失败 conv=%s base=%s: %v", conv.ID, base, err)
		return cwd
	}
	if err := a.App.DB.SetAIConversationWorktree(conv.ID, path, base); err != nil {
		logger.W("[ai-worktree] 保存工作区记录失败 conv=%s: %v", conv.ID, err)
	}
	conv.WorktreePath = path
	conv.WorktreeBranch = base
	logger.I("[ai-worktree] 会话 %s 工作区就绪 path=%s base=%s branch=%s", conv.ID, path, base, newBranch)
	return path
}

// pickWorktreeBranch 选一个没被占用的工作区分支名（quickdock/<短id>，重名加后缀）。
func (a *AIService) pickWorktreeBranch(repo, convID string) string {
	deps := a.aiGitDeps()
	base := aitools.GitWorktreeBranchPrefix + aiWorktreeShortID(convID)
	name := base
	for i := 2; i <= aiWorktreeMaxBranchTry; i++ {
		if !aitools.GitBranchExists(deps, repo, name) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// ---- bound：分支列表 / 工作区状态 / 移除 ----

// AIGitBranches 列出工作目录所在仓库的分支（bound）。前端「Worktree + 分支下拉」用它。
//
// 与 AIGetGitBranch 的分工：那个只回答「当前在哪条分支」（输入框下方那行小字），
// 这个回答「有哪些分支可选」，别为了省一个接口把两者揉在一起——前者每切目录都调，
// 后者只在用户打开下拉时调。
func (a *AIService) AIGitBranches(cwd string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	local, current, isRepo, err := aitools.GitBranches(a.aiGitDeps(), cwd)
	if err != nil {
		return services.Fail(err)
	}
	if local == nil {
		local = []string{}
	}
	res := map[string]any{
		"repo":           isRepo,
		"current":        current,
		"branches":       local,
		"remoteBranches": []string{},
	}
	if isRepo {
		if root, ok := aitools.GitRepoRoot(a.aiGitDeps(), cwd); ok {
			res["root"] = root
		}
		if remote := aitools.GitRemoteBranches(a.aiGitDeps(), cwd); len(remote) > 0 {
			res["remoteBranches"] = remote
		}
	}
	return services.Ok(res)
}

// AIWorktreeRemove 移除任务的工作区并清掉会话上的记录（bound）。
//
// 有未提交改动时默认拒绝（force=false）：「移除」不是「丢弃」，
// 让用户先看一眼改了什么，再决定是不是真要连改动一起扔。
func (a *AIService) AIWorktreeRemove(convID string, force bool) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	id := strings.TrimSpace(convID)
	conv, err := a.App.DB.GetAIConversation(id)
	if err != nil {
		return services.Fail(err)
	}
	path := strings.TrimSpace(conv.WorktreePath)
	if path == "" {
		return services.OkMsg(nil, "这条任务没有工作区")
	}

	// 移除前先把「原工程目录」记下来：移除之后工作区里就查不到共享 .git 了。
	mainRepo := ""
	if paths, perr := aitools.GitWorktreePaths(a.aiGitDeps(), path); perr == nil && len(paths) > 0 {
		mainRepo = paths[0]
	}
	if mainRepo == "" || strings.EqualFold(filepath.Clean(mainRepo), filepath.Clean(path)) {
		// 工作区已损坏（目录没了 / 元数据丢了）：退回会话 cwd 的仓库根当落点。
		if root, ok := aitools.GitRepoRoot(a.aiGitDeps(), conv.Cwd); ok {
			mainRepo = root
		}
	}

	if !force {
		if _, n := aitools.GitDirtySummary(a.aiGitDeps(), path); n > 0 {
			return services.FailMsg(fmt.Sprintf("工作区里还有 %d 个文件没提交。确认不再需要这些改动的话，可以强制移除", n))
		}
	}
	if _, serr := os.Stat(path); serr == nil {
		// 从主工作区发起：在「要被删的那个工作区」里执行 git worktree remove 会被
		// git 以 "cannot remove working tree" 拒绝（当前目录就在里面）。
		from := mainRepo
		if from == "" {
			from = path
		}
		if rerr := aitools.GitWorktreeRemove(a.aiGitDeps(), from, path, force); rerr != nil {
			return services.Fail(rerr)
		}
	}
	// 走到这里目录应该已经没了（git 会连目录一起删）；兜底再来一次。
	_ = os.RemoveAll(path)

	if err := a.App.DB.SetAIConversationWorktree(id, "", ""); err != nil {
		return services.Fail(err)
	}
	// cwd 回到原工程目录，否则下一轮工具还在往一个不存在的目录里写。
	if mainRepo != "" && !strings.EqualFold(filepath.Clean(mainRepo), filepath.Clean(path)) {
		if st, serr := os.Stat(mainRepo); serr == nil && st.IsDir() {
			if err := a.App.DB.SetAIConversationCwd(id, mainRepo); err != nil {
				logger.W("[ai-worktree] 恢复会话工作目录失败 conv=%s: %v", id, err)
			}
		}
	}
	logger.I("[ai-worktree] 会话 %s 工作区已移除 path=%s force=%v", id, path, force)
	return services.OkMsg(map[string]any{"cwd": mainRepo}, "工作区已移除")
}
