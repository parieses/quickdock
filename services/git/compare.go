// 两个分支（或任意 rev）之间的对比：谁多谁少、差在哪些提交、合起来改了什么。
//
// 为什么用三点 base...head 取差异：两点 diff 是「两个快照的差别」，当 head 落后于
// base 的共同祖先时，会把 head 上从没见过的改动反向显示成「你删掉了它」；
// 三点从 merge-base 起算，得到的正是「head 相对分叉点多了什么」，与 SourceTree /
// GitHub 的 Compare 一致。
//
// Ahead/Behind 都以 Head 视角：Ahead = Head 独有，Behind = Base 独有。
package git

import (
	"strconv"
	"strings"

	"quickdock/services"
)

// BranchCompare 对比结果。两个提交清单各带上限，超出只说明「还有很多」，
// 面板本来也只展示前若干条。
type BranchCompare struct {
	Base     string     `json:"base"`
	Head     string     `json:"head"`
	Ahead    int        `json:"ahead"`  // head 独有
	Behind   int        `json:"behind"` // base 独有
	HeadOnly []Commit   `json:"headOnly"`
	BaseOnly []Commit   `json:"baseOnly"`
	Files    []FileStat `json:"files"`
	Diff     string     `json:"diff"`
}

// GitCompare 对比 base 与 head。head 传空表示当前分支。
func (s *GitService) GitCompare(path, base, head string) *services.ApiResult {
	base = strings.TrimSpace(base)
	head = strings.TrimSpace(head)
	if base == "" {
		return services.FailMsg("请选择要比对的分支")
	}
	if head == "" {
		head = "HEAD"
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if base == head {
		return services.FailMsg("两个分支相同，没有可比的内容")
	}
	spec := base + "..." + head
	out, errOut, err := run(bin, path, cmdTimeout, "rev-list", "--left-right", "--count", spec)
	if err != nil {
		return services.FailMsg("读取分支差异失败：" + clip(strings.TrimSpace(errOut), 300))
	}

	cmp := BranchCompare{Base: base, Head: head, HeadOnly: []Commit{}, BaseOnly: []Commit{}, Files: []FileStat{}}
	// 输出两列：左 = base 独有（behind），右 = head 独有（ahead）
	if f := strings.Fields(out); len(f) >= 2 {
		cmp.Behind, _ = strconv.Atoi(f[0])
		cmp.Ahead, _ = strconv.Atoi(f[1])
	}

	cmp.HeadOnly = logRange(bin, path, base+".."+head, 200)
	cmp.BaseOnly = logRange(bin, path, head+".."+base, 200)

	stats := diffNumstat(bin, path, spec)
	cmp.Files = diffFiles(bin, path, spec, stats)
	if d, _, derr := run(bin, path, cmdTimeout, "diff", "--no-color", "--no-ext-diff", spec); derr == nil {
		cmp.Diff = clip(d, 60000)
	}
	return services.Ok(cmp)
}

// logRange 取一个区间内的提交（不带 --graph：对比视图要的是干净的两列清单）。
func logRange(bin, dir, spec string, limit int) []Commit {
	out, _, err := run(bin, dir, cmdTimeout, "log", "-n", strconv.Itoa(limit),
		"--date-order", commitFormat, "--no-color", spec)
	if err != nil {
		return []Commit{}
	}
	list := []Commit{}
	for _, line := range strings.Split(out, "\n") {
		if c, ok := parseCommitLine(strings.TrimRight(line, "\r")); ok {
			list = append(list, c)
		}
	}
	return list
}

// diffFiles 把 `git diff --name-status` 的每一行转成 FileStat，行数从 numstat 补。
func diffFiles(bin, dir, spec string, stats numstat) []FileStat {
	out, _, err := run(bin, dir, cmdTimeout, "diff", "--name-status", "--no-color", spec)
	if err != nil {
		return []FileStat{}
	}
	files := []FileStat{}
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
			p = fields[1] + " → " + fields[2]
		}
		f := FileStat{Status: code, Path: p, Desc: statusDesc(code)}
		f.Add, f.Del = stats.lookup(p)
		files = append(files, f)
	}
	return files
}
