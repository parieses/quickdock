package aitools

// git 只读工具：看改动、看差异。
//
// 为什么值得单独立一个文件：改代码这件事，模型可以写完就算，但**用户不敢**——
// 没有「改完能看差异、能回滚」这条链，任何人都不敢把一个能写文件的助手用在真项目上。
// 所以 file_edit 上线的前提是这两个只读工具先到位。
//
// 只做读：status / diff 不会改变任何东西，也不会碰网络（git fetch 属于写，不做）。
// 提交、切分支、reset 这类一律不给——那些是用户自己的决定，模型最多把命令写出来让用户执行。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/fsguard"
	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/internal/sysutil"
)

const (
	gitCmdTimeout  = 20 * time.Second // 单条 git 命令超时；大仓库的 diff 也可能要几秒
	gitDiffMaxChar = 20000            // 返回给模型的 diff 字符上限
	gitStatusMax   = 500              // 改动条目上限
)

func registerGit(d Deps) {
	r := registrar{d: d, category: mcpsrv.CategoryCode}
	str := mcpsrv.Str
	intp := mcpsrv.Int
	boolp := mcpsrv.Bool

	r.add(func(_ Deps, args map[string]any) (any, error) {
		return gitStatus(d, args)
	}, mcpsrv.LevelRead, "git_status",
		"查看一个仓库的当前状态：所在分支、与远端的领先/落后提交数、每个改动文件的状态码（M 改 / A 新增 / D 删除 / ?? 未跟踪）。"+
			"改完代码后用它确认自己动了哪些文件。不传 path 时用当前工作目录",
		nil,
		map[string]any{
			"path": str("仓库目录（或其子目录）。相对路径按当前工作目录解析；不传则用当前工作目录"),
		})

	r.add(func(_ Deps, args map[string]any) (any, error) {
		return gitDiff(d, args)
	}, mcpsrv.LevelRead, "git_diff",
		"查看未提交改动的内容差异（diff 文本）。默认看工作区相对暂存区的改动；staged=true 看已暂存的部分。"+
			"用 stat=true 只取文件级增删行数汇总（改动很大时先看这个，再按 file 精确看某个文件）",
		nil,
		map[string]any{
			"path":     str("仓库目录（或其子目录）。不传则用当前工作目录"),
			"file":     str("可选。只看某个文件的差异（相对仓库根的路径）"),
			"staged":   boolp("可选。看已暂存（git add 过）的改动，默认 false 看工作区"),
			"stat":     boolp("可选。只返回文件级增删行数汇总，不返回具体 diff，默认 false"),
			"maxChars": intp("最多返回多少字符的 diff 文本，默认 20000"),
		})
}

// gitBin 找到可用的 git 可执行文件。
//
// 先看系统 PATH，再回落到 QuickDock 托管的便携版：GUI 进程读的是注册表 PATH，
// 与用户终端里的 PATH 经常不一样（用户刚装完 git、终端里能用而这里找不到）。
func gitBin(d Deps) (string, error) {
	if p, err := exec.LookPath("git"); err == nil {
		return p, nil
	}
	if d.Env != nil {
		if st := d.Env.GitStatus(); strings.TrimSpace(st.Path) != "" {
			p := strings.TrimSpace(st.Path)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p, nil
			}
			// Path 有时候给的是安装目录，拼出可执行文件
			cands := []string{
				filepath.Join(p, "cmd", "git.exe"),
				filepath.Join(p, "bin", "git"),
				filepath.Join(p, "bin", "git.exe"),
			}
			for _, c := range cands {
				if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
					return c, nil
				}
			}
		}
	}
	return "", errors.New("找不到 git：系统 PATH 里没有，QuickDock 里也没安装。" +
		"请先在「环境管理」里安装 git（或让用户确认终端里 git 可用后重启 QuickDock）")
}

// runGit 在 dir 下执行一条 git 子命令，返回 stdout。
//
// 用 exec 直接传参数而不是走 shell：参数里可能有空格、中文、引号，
// 走 shell 拼字符串既容易出错，也把「命令注入」这条风险凭空引进来。
//
// -c core.quotepath=false：默认 git 会把中文路径转义成 \344\270\255 这种八进制，
// 模型看到一堆转义序列既读不懂，也没法直接拿去当 file 参数。
func runGit(d Deps, dir string, args ...string) (string, string, error) {
	bin, err := gitBin(d)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	full := append([]string{"-C", dir, "-c", "core.quotepath=false"}, args...)
	cmd := sysutil.CommandContext(ctx, bin, full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), errb.String(), fmt.Errorf("git 命令超时（%s）", gitCmdTimeout)
	}
	return out.String(), errb.String(), err
}

// gitDir 解析目标目录，并确认它确实在某个 git 仓库里。
//
// 提前判一次仓库，是为了把 "fatal: not a git repository" 这种原始输出
// 换成人能看懂的话——模型看到 fatal 会去猜参数，而真实原因往往是「这个目录不是仓库」。
func gitDir(d Deps, args map[string]any) (string, error) {
	raw := mcpsrv.PathArg(args, "path")
	if raw == "" {
		raw = mcpsrv.Cwd(args)
	}
	if raw == "" {
		return "", errors.New("不知道在哪个目录执行：当前对话没有绑定工作目录，请传 path（仓库目录）")
	}
	dir, err := fsguard.ResolveRead(d.DB, raw, mcpsrv.Cwd(args), false)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("无法访问 %s：%v", dir, err)
	}
	if !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	out, errOut, err := runGit(d, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(out) != "true" {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return "", fmt.Errorf("%s 不是一个 git 仓库（git 返回：%s）。"+
			"如果这是新项目还没初始化，可以告诉用户先执行 git init", dir, mcpsrv.TruncateRunes(msg, 200))
	}
	// 归一化到仓库根：子目录里看 status/diff 得到的结果一样，但 file 参数要相对根才准。
	top, _, terr := runGit(d, dir, "rev-parse", "--show-toplevel")
	if terr == nil && strings.TrimSpace(top) != "" {
		dir = strings.TrimSpace(top)
	}
	return dir, nil
}

func gitStatus(d Deps, args map[string]any) (any, error) {
	dir, err := gitDir(d, args)
	if err != nil {
		return nil, err
	}
	// --untracked-files=all 让新建目录里的每个文件都单独列出来；
	// 不给这个参数时 git 只报目录名，模型会以为只有一个文件。
	out, errOut, err := runGit(d, dir, "status", "--porcelain=v1", "-b", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("git status 失败：%s", mcpsrv.TruncateRunes(strings.TrimSpace(errOut), 300))
	}

	res := map[string]any{"path": dir}
	var changes []map[string]any
	truncated := false
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			parseGitBranchLine(line[3:], res)
			continue
		}
		if len(line) < 3 {
			continue
		}
		if len(changes) >= gitStatusMax {
			truncated = true
			continue
		}
		code := strings.TrimSpace(line[:2])
		changes = append(changes, map[string]any{
			"status": code,
			"path":   strings.TrimSpace(line[3:]),
			"desc":   gitStatusDesc(code),
		})
	}
	if changes == nil {
		changes = []map[string]any{}
	}
	res["count"] = len(changes)
	res["changes"] = changes
	res["clean"] = len(changes) == 0
	if truncated {
		res["truncated"] = true
		res["truncatedNote"] = fmt.Sprintf("改动超过 %d 条，只列了前 %d 条", gitStatusMax, gitStatusMax)
	}
	logger.I("[mcp] git_status %s 改动 %d 条", dir, len(changes))
	return res, nil
}

// parseGitBranchLine 解析 `main...origin/main [ahead 1, behind 2]`。
func parseGitBranchLine(s string, res map[string]any) {
	// 没有上游时是 `main`，初始化仓库是 `No commits yet on main`
	if strings.HasPrefix(s, "No commits yet on ") {
		res["branch"] = strings.TrimSpace(strings.TrimPrefix(s, "No commits yet on "))
		res["noCommits"] = true
		return
	}
	rest := s
	if i := strings.Index(s, "..."); i >= 0 {
		res["branch"] = s[:i]
		rest = s[i+3:]
		if j := strings.Index(rest, " "); j >= 0 {
			res["upstream"] = rest[:j]
			rest = rest[j:]
		} else {
			res["upstream"] = rest
			rest = ""
		}
	} else if i := strings.Index(s, " "); i >= 0 {
		res["branch"] = s[:i]
		rest = s[i:]
	} else {
		res["branch"] = s
		rest = ""
	}
	if strings.Contains(rest, "ahead ") {
		res["ahead"] = leadingInt(rest, "ahead ")
	}
	if strings.Contains(rest, "behind ") {
		res["behind"] = leadingInt(rest, "behind ")
	}
	if strings.Contains(rest, "gone") {
		res["upstreamGone"] = true
	}
}

// leadingInt 从 `... ahead 3...` 里取出 3。
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

func gitStatusDesc(code string) string {
	switch {
	case code == "??":
		return "未跟踪的新文件"
	case strings.ContainsAny(code, "M"):
		return "内容已修改"
	case strings.ContainsAny(code, "A"):
		return "已暂存的新增"
	case strings.ContainsAny(code, "D"):
		return "已删除"
	case strings.ContainsAny(code, "R"):
		return "重命名"
	case strings.ContainsAny(code, "C"):
		return "复制"
	case strings.ContainsAny(code, "U"):
		return "冲突未解决"
	}
	return "有改动"
}

func gitDiff(d Deps, args map[string]any) (any, error) {
	dir, err := gitDir(d, args)
	if err != nil {
		return nil, err
	}
	maxChars := mcpsrv.ArgInt(args, "maxChars", gitDiffMaxChar)
	if maxChars <= 0 || maxChars > gitDiffMaxChar {
		maxChars = gitDiffMaxChar
	}

	cmdArgs := []string{"diff", "--no-color", "--no-ext-diff"}
	if mcpsrv.ArgBool(args, "stat") {
		cmdArgs = append(cmdArgs, "--stat")
	} else {
		cmdArgs = append(cmdArgs, "--unified=3")
		if n := mcpsrv.ArgInt(args, "context", 3); n > 0 && n <= 20 {
			cmdArgs = append(cmdArgs, fmt.Sprintf("--unified=%d", n))
		}
	}
	if mcpsrv.ArgBool(args, "staged") {
		cmdArgs = append(cmdArgs, "--cached")
	} else if !mcpsrv.ArgBool(args, "staged") {
		// 不带 --cached 时默认就是工作区，显式写出来只是为了读代码时清楚。
	}
	if f := strings.TrimSpace(mcpsrv.Arg(args, "file")); f != "" {
		cmdArgs = append(cmdArgs, "--", f)
	}

	out, errOut, err := runGit(d, dir, cmdArgs...)
	if err != nil {
		return nil, fmt.Errorf("git diff 失败：%s", mcpsrv.TruncateRunes(strings.TrimSpace(errOut), 300))
	}
	text := strings.TrimSpace(out)
	if text == "" {
		return map[string]any{
			"path":  dir,
			"empty": true,
			"text":  "没有差异：工作区与 " + ternaryStr(mcpsrv.ArgBool(args, "staged"), "暂存区", "HEAD") + " 一致，或者这些改动全是未跟踪的新文件（未跟踪文件不在 diff 里，用 git_status 确认）",
		}, nil
	}

	truncated := false
	runes := []rune(text)
	if len(runes) > maxChars {
		text = string(runes[:maxChars])
		truncated = true
	}
	res := map[string]any{
		"path":      dir,
		"staged":    mcpsrv.ArgBool(args, "staged"),
		"stat":      mcpsrv.ArgBool(args, "stat"),
		"text":      text,
		"chars":     len(runes),
		"truncated": truncated,
	}
	if truncated {
		res["truncatedNote"] = fmt.Sprintf("diff 超过 %d 字符，只给了前 %d；建议加 file 参数只看某个文件，或加 stat=true 先看汇总", maxChars, maxChars)
	}
	logger.I("[mcp] git_diff %s %d 字符", dir, len(runes))
	return res, nil
}

func ternaryStr(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
