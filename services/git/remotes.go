package git

import (
	"errors"
	"regexp"
	"strings"

	"quickdock/services"
)

// RemoteEntry 一条远端配置。
//
// PushURL 只在「推送地址和拉取地址不一样」时才有值（比如 fork 的 origin 拉自己的、
// push 到 upstream）：这种仓库两行都要显示，否则用户会以为推到自己 fork 去了。
type RemoteEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	PushURL string `json:"pushUrl"`
	IsSSH   bool   `json:"isSsh"`
}

// remoteNameRe 远端名的合法字符集，与 git 自身规则（不能有空格、不能以 - 开头）对齐。
var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// parseRemotes 解析 `git remote -v`。
//
// 输出形如：
//
//	origin\thttps://github.com/o/r.git (fetch)
//	origin\thttps://github.com/o/r.git (push)
//
// 保留首次出现的顺序：列表跟着 `git remote` 的字母序走即可，但必须稳定，
// 否则每次刷新行序都在跳。
func parseRemotes(out string) []RemoteEntry {
	list := []RemoteEntry{}
	idx := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name, url := fields[0], fields[1]
		kind := ""
		if len(fields) >= 3 {
			kind = strings.Trim(fields[2], "()")
		}
		i, ok := idx[name]
		if !ok {
			idx[name] = len(list)
			list = append(list, RemoteEntry{Name: name, URL: url, IsSSH: isSSHURL(url)})
			continue
		}
		if kind == "push" && list[i].URL != url {
			list[i].PushURL = url
		}
	}
	return list
}

// loadRemotes 内部共用：取远端清单。
func (s *GitService) loadRemotes(path string) ([]RemoteEntry, error) {
	bin, err := findGit()
	if err != nil {
		return nil, err
	}
	out, errOut, e := run(bin, path, cmdTimeout, "remote", "-v")
	if e != nil {
		return nil, errors.New("读取远端列表失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	return parseRemotes(out), nil
}

// GitRemotes 列出全部远端（origin / upstream …）。
func (s *GitService) GitRemotes(path string) *services.ApiResult {
	list, err := s.loadRemotes(path)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(list)
}

// GitRemoteSet 新增或修改一条远端地址。
//
// 同名已存在时用 set-url 改地址而不是先删再加：删了再加会丢掉 fetch refspec
// （自定义过多 remote 有这种情况），改地址则原样保留。
func (s *GitService) GitRemoteSet(path, name, url string) *services.ApiResult {
	name = strings.TrimSpace(name)
	url = strings.TrimSpace(url)
	if !remoteNameRe.MatchString(name) {
		return services.FailMsg("远端名不合法：只能包含字母、数字、点、下划线、短横线，且不能以短横线开头")
	}
	if url == "" {
		return services.FailMsg("远端地址不能为空")
	}
	if strings.HasPrefix(url, "-") {
		// 传给 git 的参数以 - 开头会被当成选项，这里挡掉而不是加 --（git remote add 不吃 --）
		return services.FailMsg("远端地址不能以短横线开头")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	existed := false
	if _, _, e := run(bin, path, cmdTimeout, "remote", "get-url", name); e == nil {
		existed = true
	}
	var errOut string
	if existed {
		_, errOut, err = run(bin, path, cmdTimeout, "remote", "set-url", name, url)
	} else {
		_, errOut, err = run(bin, path, cmdTimeout, "remote", "add", name, url)
	}
	if err != nil {
		return services.FailMsg(clip(strings.TrimSpace(errOut), 300))
	}
	list, _ := s.loadRemotes(path)
	msg := "已添加远端 " + name
	if existed {
		msg = "已更新远端 " + name + " 的地址"
	}
	return services.OkMsg(list, msg)
}

// GitRemoteRemove 删除一条远端（连同它的远程分支引用）。
func (s *GitService) GitRemoteRemove(path, name string) *services.ApiResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return services.FailMsg("远端名不能为空")
	}
	bin, err := findGit()
	if err != nil {
		return services.Fail(err)
	}
	if _, errOut, e := run(bin, path, cmdTimeout, "remote", "remove", name); e != nil {
		return services.FailMsg("删除远端失败：" + clip(strings.TrimSpace(errOut), 300))
	}
	list, _ := s.loadRemotes(path)
	msg := "已删除远端 " + name
	if name == "origin" {
		msg = "已删除远端 origin：现在没有推送目标了，需要重新添加一个远端"
	}
	return services.OkMsg(list, msg)
}
