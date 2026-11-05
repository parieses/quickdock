// 贮藏（stash）：把没做完的改动收起来，腾出干净的工作区切分支/拉代码，之后再拿出来。
//
// 为什么要做成一组可视化记录而不是一个按钮：stash 是**栈**，反复 stash 之后
// 「哪一条是我的」全靠 list 里的文案辨认；面板必须把每条的时间、分支、信息都摆出来，
// 否则用户只能凭 stash@{1} 这种序号瞎猜，一 apply 就 apply 到别人的改动上。
//
// 全部动作都按 ref 精确指定（stash@{n}），不用默认的 stash@{0}：
// 列表是用户点的那一行，操作对象必须就是它，不能因为期间又 stash 了一次就错位。
package git

import (
	"strconv"
	"strings"

	"quickdock/services"
)

// StashEntry 一条贮藏记录。
type StashEntry struct {
	Ref     string `json:"ref"` // stash@{0}
	Index   int    `json:"index"`
	Branch  string `json:"branch"`  // 贮藏时所在分支
	Message string `json:"message"` // 用户写的说明（自动生成的 WIP 会带提交摘要）
	Auto    bool   `json:"auto"`    // 没有写说明、由 git 自动生成的那条
	Ts      int64  `json:"ts"`
}

// GitStashList 逆序列出全部贮藏（最新的在最上面，与 git stash list 一致）。
func (s *GitService) GitStashList(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, e := run(bin, path, cmdTimeout, "stash", "list", "--pretty=format:%gd%x09%ct%x09%gs")
	if e != nil {
		return services.FailMsg("读取贮藏失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	entries := []StashEntry{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		e := StashEntry{Ref: strings.TrimSpace(parts[0])}
		e.Index, _ = strconv.Atoi(strings.Trim(e.Ref, "stash@{}"))
		e.Ts, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		e.Branch, e.Message, e.Auto = parseStashSubject(parts[2])
		entries = append(entries, e)
	}
	return services.Ok(entries)
}

// parseStashSubject 拆 %gs 的两类文案：
//
//	WIP on dev: a1b2c3d 上次提交的标题   ← git 自动生成（没写说明）
//	On dev: 我写的说明                    ← 写了 -m
//
// 分支名里可能有 `/`，所以只按第一个 ": " 切分；提交摘要里带冒号也不会被切坏。
func parseStashSubject(s string) (branch, msg string, auto bool) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "WIP on "); ok {
		auto = true
		if i := strings.Index(rest, ": "); i >= 0 {
			return rest[:i], strings.TrimSpace(rest[i+2:]), true
		}
		return rest, "", true
	}
	if rest, ok := strings.CutPrefix(s, "On "); ok {
		if i := strings.Index(rest, ": "); i >= 0 {
			return rest[:i], strings.TrimSpace(rest[i+2:]), false
		}
		return rest, "", false
	}
	return "", s, false
}

// GitStashSave 贮藏当前改动；includeUntracked 决定是否连未跟踪文件一起收走。
//
// 默认**不含**未跟踪文件（git 默认如此）：那些文件往往是被 .gitignore 漏掉的
// 构建产物或本地配置，莫名其妙消失比留在原地更让人困惑。前端在勾选时才加 -u。
func (s *GitService) GitStashSave(path, message string, includeUntracked bool) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	// 不加 -q：干净工作区时 git 的唯一提示就是 stdout 里那句 "No local changes to save"，
	// 压掉它就只剩退出码 0，没法区分「贮藏成功」和「根本没东西可贮藏」
	args := []string{"stash", "push"}
	if m := strings.TrimSpace(message); m != "" {
		args = append(args, "-m", m)
	}
	if includeUntracked {
		args = append(args, "-u")
	}
	out, errOut, e := run(bin, path, cmdTimeout, args...)
	text := strings.TrimSpace(out + "\n" + errOut)
	if e != nil {
		return services.FailMsg("贮藏失败：" + clip(text, 300))
	}
	// 没有改动时 git 退出码是 0，只在文案里说，所以这里靠文案判断
	if strings.Contains(text, "No local changes to save") {
		return services.FailMsg("工作区是干净的，没有可贮藏的改动")
	}
	return services.OkMsg(nil, "已贮藏当前改动")
}

// GitStashApply 把某条贮藏应用到工作区，**记录保留**（可以再应用到别的分支）。
func (s *GitService) GitStashApply(path, ref string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, err := s.stashRef(bin, path, ref); err != nil {
		return services.Fail(err)
	}
	out, errOut, e := run(bin, path, cmdTimeout, "stash", "apply", ref)
	text := strings.TrimSpace(out + "\n" + errOut)
	if e != nil {
		if strings.Contains(text, "CONFLICT") || strings.Contains(text, "conflict") {
			return services.FailMsg("应用时有冲突：改动已落到工作区，需要手动解决（冲突文件会标成 U）；想放弃就 git checkout -- .")
		}
		return services.FailMsg("应用贮藏失败：" + clip(text, 400))
	}
	return services.OkMsg(nil, "已应用 "+ref+"（记录仍在列表里）")
}

// GitStashPop 应用并删除记录（最常用的一条路径）。
func (s *GitService) GitStashPop(path, ref string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, err := s.stashRef(bin, path, ref); err != nil {
		return services.Fail(err)
	}
	out, errOut, e := run(bin, path, cmdTimeout, "stash", "pop", ref)
	text := strings.TrimSpace(out + "\n" + errOut)
	if e != nil {
		// 冲突时 pop **不会**删记录（git 有意如此），这句要讲清楚，否则用户以为丢了
		if strings.Contains(text, "CONFLICT") || strings.Contains(text, "conflict") {
			return services.FailMsg("有冲突：改动已落到工作区，贮藏记录还留着；解决冲突后再手动删除记录")
		}
		return services.FailMsg("弹出贮藏失败：" + clip(text, 400))
	}
	return services.OkMsg(nil, "已弹出 "+ref)
}

// GitStashDrop 删除一条贮藏记录（不可恢复，前端先确认）。
func (s *GitService) GitStashDrop(path, ref string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, err := s.stashRef(bin, path, ref); err != nil {
		return services.Fail(err)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "stash", "drop", ref); e != nil {
		return services.FailMsg("删除贮藏失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(nil, "已删除 "+ref)
}

// GitStashShow 取一条贮藏的预览：改了哪些文件 + 文本差异，与提交详情（CommitDetail）
// 同构，前端直接复用那套文件树与 diff 渲染。git 面板点贮藏条目时调用——之前点了没反应。
//
// 只取已跟踪文件的改动（git stash show -p 的默认范围）；贮藏若含未跟踪文件，它们
// 不在 diff 里，与「应用」的主流场景一致，且未跟踪文件本就不在版本控制中。
func (s *GitService) GitStashShow(path, ref string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, err := s.stashRef(bin, path, ref); err != nil {
		return services.Fail(err)
	}
	detail := CommitDetail{Hash: ref, Files: []FileStat{}}
	out, errOut, e := run(bin, path, cmdTimeout, "stash", "show", "--name-status", "--no-color", ref)
	if e != nil {
		return services.FailMsg("读取贮藏失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	stats := stashNumstat(bin, path, ref)
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
	if d, _, derr := run(bin, path, cmdTimeout, "stash", "show", "-p", "--unified=3", "--no-color", ref); derr == nil {
		detail.Diff = clip(d, 60000)
	}
	return services.Ok(detail)
}

// stashNumstat 取一条贮藏的逐文件增删行数（与 commitNumstat 同构，只是 rev 是 stash@{n}）。
// 单独一条命令，失败了就当没统计：它只是装饰，不该让它把文件清单也拖挂。
func stashNumstat(bin, dir, ref string) numstat {
	m := numstat{}
	out, _, err := run(bin, dir, cmdTimeout, "stash", "show", "--numstat", "--no-color", ref)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		add, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
		del, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
		m[fields[2]] = [2]int{add, del}
	}
	return m
}

// stashRef 校验 ref 仍存在。
//
// 为什么每次都查：列表是几秒前的快照，期间可能在终端里 stash pop 过；
// 直接跑 apply 会得到 "is not a valid reference"，用户看到这句只会以为面板坏了。
func (s *GitService) stashRef(bin, dir, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errMsg("缺少贮藏记录")
	}
	if !strings.HasPrefix(ref, "stash@{") {
		return "", errMsg("贮藏记录格式不对：" + ref)
	}
	out, _, e := run(bin, dir, cmdTimeout, "stash", "list", "--pretty=format:%gd")
	if e != nil {
		return "", errMsg("读取贮藏列表失败")
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == ref {
			return ref, nil
		}
	}
	return "", errMsg("这条贮藏记录已经不存在了（可能在别处 pop 过），刷新一下列表")
}

// errMsg 一小段文本错误，避免为每个校验点 import errors 造一个 error。
type errMsg string

func (e errMsg) Error() string { return string(e) }
