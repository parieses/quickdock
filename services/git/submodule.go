// 子模块（submodule）：列表、初始化/更新、同步 URL、新增。
//
// 子模块最容易踩的两个坑都体现在状态首字符上：
//   - '-' 未初始化：仓库里只有 gitlink，子模块目录是空的（clone 后没 --recursive）；
//   - '+' 与索引记录不一致：子模块当前 HEAD 不是父仓库记录的那个提交。
//
// 所以列表必须把首字符原样带给前端，而不是归一成「已装/未装」——'+' 时用户
// 需要的动作是「提交子模块的指针变更」，不是「更新」。
//
// 不用 `git submodule foreach`：那会在每个子模块里跑一次 shell，Windows 上
// 引号与路径空格都容易出问题；这里一律单层 `submodule <cmd> [-- <path>]`。
package git

import (
	"regexp"
	"strings"

	"quickdock/services"
)

// Submodule 一条子模块记录。
type Submodule struct {
	Path     string `json:"path"`
	SHA      string `json:"sha"`      // 父仓库记录的提交
	State    string `json:"state"`    // ' ' 正常 / '-' 未初始化 / '+' 与索引不一致 / 'U' 冲突
	Describe string `json:"describe"` // 括号里的描述（通常是 tag）
}

// submoduleLine 解析 `git submodule status --recursive` 的一行。
// 路径可能带空格，描述是行尾括号，因此用正则而不是按空格切。
var submoduleLine = regexp.MustCompile(`^([ +\-U])([0-9a-f]{7,64}) (.+?)(?: \(([^)]*)\))?$`)

// GitSubmodules 列出子模块（含未初始化）。
func (s *GitService) GitSubmodules(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	out, errOut, err := run(bin, path, cmdTimeout, "submodule", "status", "--recursive")
	if err != nil {
		return services.FailMsg("读取子模块失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	list := []Submodule{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := submoduleLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		list = append(list, Submodule{State: m[1], SHA: m[2], Path: m[3], Describe: m[4]})
	}
	return services.Ok(list)
}

// GitSubmoduleUpdate 初始化并更新子模块。sub 为空表示全部；remote 为真时
// 拉取子模块远端的最新提交（--remote），否则对齐父仓库记录的那个提交。
func (s *GitService) GitSubmoduleUpdate(path, sub string, remote bool) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	args := []string{"submodule", "update", "--init", "--recursive"}
	if remote {
		args = append(args, "--remote")
	}
	if p := strings.TrimSpace(sub); p != "" {
		args = append(args, "--", p)
	}
	if _, errOut, e := run(bin, path, networkTimeout, args...); e != nil {
		return services.FailMsg("更新子模块失败：" + clip(strings.TrimSpace(errOut), 400))
	}
	return services.OkMsg(nil, "子模块已更新")
}

// GitSubmoduleSync 把 .gitmodules 里的新 URL 同步进 .git/config。
// 改了远端地址却只改 .gitmodules 时，fetch 仍走旧地址，靠它修。
func (s *GitService) GitSubmoduleSync(path string) *services.ApiResult {
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "submodule", "sync", "--recursive"); e != nil {
		return services.FailMsg("同步子模块地址失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return services.OkMsg(nil, "子模块地址已同步")
}

// GitSubmoduleAdd 新增子模块。url 与 sub 都必填，sub 用相对路径（git 也接受绝对路径，
// 但绝对路径会把本机目录写进 .gitmodules，提交后别人 clone 就废了——这里挡掉）。
func (s *GitService) GitSubmoduleAdd(path, url, sub string) *services.ApiResult {
	url = strings.TrimSpace(url)
	sub = strings.TrimSpace(sub)
	if url == "" || sub == "" {
		return services.FailMsg("子模块地址与目录都不能为空")
	}
	if strings.HasPrefix(sub, "/") || (len(sub) > 1 && sub[1] == ':') {
		return services.FailMsg("子模块目录请用仓库内相对路径")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, e := run(bin, path, networkTimeout, "submodule", "add", "--", url, sub); e != nil {
		return services.FailMsg("添加子模块失败：" + clip(strings.TrimSpace(errOut), 400))
	}
	return services.OkMsg(nil, "子模块已添加，记得连同 .gitmodules 一起提交")
}
