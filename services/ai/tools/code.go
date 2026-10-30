package aitools

// 代码工具：改文件、跑命令、探测工作目录。
//
// 这是全项目**唯一**具备「改用户磁盘」能力的地方，因此几条约束写在这里，改动前请先读完：
//
//  1. **写白名单与读白名单是两套，实现都在 internal/fsguard。** 读允许家目录
//     （用户让 AI 看个文件很自然），写只允许「用户显式登记为项目的目录」：快捷项里的
//     目录、项目的工作目录、以及对话绑定的工作目录。家目录不在其中——否则一句话
//     就能改掉 ~/Documents 里任何东西。
//
//  2. **file_edit 是主力，不是 file_write。** 整文件重写会覆盖用户同时进行的编辑，
//     token 也贵十倍；精确替换让「改了什么」在参数里就看得见。
//
//  3. **高危命令拒绝执行，而不是「执行了再警告」。** 这里没有弹窗确认的通道，
//     所以最保守的做法是把命令原样交还给用户，让他自己在终端里判断。
//     规则表复用的是 aiutil 的本机风险表（日志/环境页同一份），另加几条「在项目目录里做
//     等于毁项目」的 git 级操作——那些在通用表里只是 medium，在这里是致命的。
//
//  4. **这批能力不进 MCP。** 外部客户端有自己的编辑器和终端，再暴露一遍只会让模型
//     在两套同名能力之间反复挑；而且「任意路径写」是最不该外包出去的那类权限。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"quickdock/internal/aiutil"
	envengine "quickdock/internal/env"
	"quickdock/internal/fsguard"
	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/internal/sysutil"
)

const (
	fileEditMaxBytes  = 8 << 20   // file_edit 能处理的文件上限（再大就该用命令行工具）
	fileEditMaxRunes  = 4000      // 一次替换涉及的新旧文本长度上限，防模型整文件重写
	fileWriteMaxBytes = 2 << 20   // file_write 单次写入上限（模型一次能写的有量级上限）
	runCmdDefaultSec  = 60        // run_command 默认超时
	runCmdMaxSec      = 600       // run_command 超时上限
	runCmdMaxOutput   = 200 << 10 // 单路输出（stdout/stderr 各自）保留的字节数
	runCmdKillGrace   = 3 * time.Second
)

// codeAgentDangerRules 是「在项目目录里跑就等于毁掉用户工作」的补充规则。
//
// 为什么不复用 aiutil 那张表：那张表判的是「这台电脑安不安全」，
// 所以 `git reset --hard` 在它眼里只是「会丢未提交改动」（medium），
// 而对一个 code agent 来说，这句话意味着「用户的活儿没了」——必须直接拒。
var codeAgentDangerRules = []struct {
	re   *regexp.Regexp
	note string
}{
	{regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b`), "git reset --hard 会丢弃所有未提交改动，且无法恢复"},
	{regexp.MustCompile(`(?i)\bgit\s+clean\s+-[a-z]*[fd]`), "git clean -fd 会删除所有未跟踪文件（常常是用户新写的代码）"},
	{regexp.MustCompile(`(?i)\bgit\s+push\b[^\n]*\s(-f|--force)\b`), "强推会覆盖远端历史，可能影响其他人"},
	{regexp.MustCompile(`(?i)\bgit\s+checkout\s+(--\s+)?\.`), "git checkout . 会丢弃工作区全部修改"},
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*\s+(\*|\.|/|~)\s*($|;|&|\|)`), "递归删除当前/根目录"},
	{regexp.MustCompile(`(?i)\b(npm\s+publish|yarn\s+publish|cargo\s+publish|twine\s+upload)\b`), "发布会把代码推到公共仓库，撤不回"},
}

func registerCode(d Deps) {
	r := registrar{d: d, category: mcpsrv.CategoryCode}
	str := mcpsrv.Str
	intp := mcpsrv.Int
	boolp := mcpsrv.Bool

	r.add(fileEdit, mcpsrv.LevelWrite, "file_edit",
		"精确替换文件里的一段内容（old_string → new_string）。改代码请用这个，不要整文件重写："+
			"old_string 必须在文件里**唯一出现**（多处就报错并列出位置），并且要带足够上下文（含缩进与前后行）避免误匹配。"+
			"只支持用户显式授权的项目目录。改完建议用 git_diff 确认",
		[]string{"path", "old_string", "new_string"},
		map[string]any{
			"path":        str("文件路径：绝对路径，或相对当前工作目录"),
			"old_string":  str("要被替换的原文，必须与文件内容逐字符一致（含缩进）。建议带 2-3 行上下文保证唯一"),
			"new_string":  str("替换成的新内容；要删除这段就传空字符串"),
			"replace_all": boolp("可选。old_string 出现多次时是否全部替换，默认 false（此时多处会报错）"),
		})

	r.add(fileWrite, mcpsrv.LevelWrite, "file_write",
		"创建新文件并写入完整内容——**新建文件一律用它**。"+
			"不要在 run_command 里用 echo / Set-Content / node -e 造文件：引号、换行、编码都容易出错，"+
			"而且用户看不出到底写了什么。父目录不存在会自动创建。"+
			"文件已存在时默认拒绝覆盖（保护用户已有内容）：改已有文件请先 file_read 看原文，再用 file_edit 精确替换；"+
			"确实要整份替换请显式传 overwrite=true。只允许写用户授权的目录",
		[]string{"path", "content"},
		map[string]any{
			"path":      str("文件路径：绝对路径，或相对当前工作目录"),
			"content":   str("要写入的完整文本内容（含换行）。写空文件也要显式传空字符串"),
			"overwrite": boolp("可选。文件已存在时是否整份覆盖，默认 false（此时报错并提示改用 file_edit）"),
		})

	r.add(runCommand, mcpsrv.LevelWrite, "run_command",
		"在当前项目目录里执行一条 shell 命令并返回输出（stdout/stderr/退出码），用于装依赖、跑测试、构建、"+
			"查看 git log 这类必须在命令行做的事。有超时与输出上限，超时会连同子进程一起结束。"+
			"**高危命令会被拒绝执行**（删除根目录、git reset --hard、强推、发版等），此时请把命令交给用户自己运行",
		nil,
		map[string]any{
			"command":     str("要执行的完整命令，如 npm test / go build ./... / git log --oneline -10"),
			"cwd":         str("可选。在哪个目录执行，默认用当前工作目录"),
			"timeout_sec": intp("可选。超时秒数，默认 60，最大 600"),
		})

	r.add(checkWorkdir, mcpsrv.LevelRead, "check_workdir",
		"探测一个目录是什么项目：识别语言与包管理器、依赖装了没、有哪些可运行脚本、是不是 git 仓库、"+
			"建议用哪条命令做类型检查/构建。**动手之前先调它**，能省掉「猜 npm 还是 pnpm」「猜要不要先装依赖」那几轮",
		nil,
		map[string]any{
			"path": str("目录路径：绝对路径，或相对当前工作目录；不传则用当前工作目录"),
		})
}

// -------- file_edit --------

func fileEdit(d Deps, args map[string]any) (any, error) {
	path, err := fsguard.ResolveWrite(d.DB, mcpsrv.PathArg(args, "path"), mcpsrv.Cwd(args))
	if err != nil {
		return nil, err
	}
	oldStr := mcpsrv.Arg(args, "old_string")
	if oldStr == "" {
		return nil, errors.New("缺少参数 old_string（要被替换的原文）")
	}
	// new_string 允许是空串（=删除这一段），所以不能只判空串，要看键在不在。
	rawNew, ok := args["new_string"]
	if !ok {
		return nil, errors.New("缺少参数 new_string；如果是想删除这段内容，请显式传空字符串")
	}
	newStr, _ := rawNew.(string)
	if oldStr == newStr {
		return nil, errors.New("old_string 与 new_string 完全相同，没有改动")
	}
	if len([]rune(oldStr)) > fileEditMaxRunes || len([]rune(newStr)) > fileEditMaxRunes {
		return nil, fmt.Errorf("单次替换的新旧文本超过 %d 字符。改大段内容请拆成多次小改动，或用 run_command 配合脚本", fileEditMaxRunes)
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("无法访问 %s：%v", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s 是目录，不是文件", path)
	}
	if info.Size() > fileEditMaxBytes {
		return nil, fmt.Errorf("%s 有 %d 字节，超过 %dMB 上限；这类文件应该用命令处理而不是读进内存改",
			path, info.Size(), fileEditMaxBytes>>20)
	}
	if err := guardWritableFile(path); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取失败：%v", err)
	}
	content := string(data)

	count := strings.Count(content, oldStr)
	if count == 0 {
		return nil, fmt.Errorf("没找到要替换的内容，文件未被修改。%s", suggestSimilarBlock(content, oldStr))
	}
	replaceAll := mcpsrv.ArgBool(args, "replace_all")
	if count > 1 && !replaceAll {
		return nil, fmt.Errorf("old_string 在文件里出现了 %d 次，无法确定改哪一处，文件未被修改：%s。"+
			"请把 old_string 扩大到能唯一定位（多带一两行上下文），或传 replace_all=true 全部替换",
			count, locateOccurrences(content, oldStr, 5))
	}

	var updated string
	idx := strings.Index(content, oldStr)
	if replaceAll {
		updated = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		updated = content[:idx] + newStr + content[idx+len(oldStr):]
	}

	// 写入前再确认一次文件没被别人改过：模型给的 old_string 已经能匹配，说明内容基本没变，
	// 但从读到写之间用户可能在编辑器里保存了一次——那一次保存会被我们覆盖掉。
	if cur, err := os.Stat(path); err == nil && !cur.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("文件在读取后被外部修改过（可能用户正在编辑器里改），为避免覆盖别人的改动，本次未写入；请重新 file_read 后再改")
	}

	startLine := 1 + strings.Count(content[:idx], "\n")
	if err := writeFileAtomic(path, []byte(updated), info.Mode()); err != nil {
		return nil, fmt.Errorf("写入失败：%v", err)
	}
	removed := strings.Count(oldStr, "\n") + 1
	added := strings.Count(newStr, "\n") + 1
	if newStr == "" {
		added = 0
	}

	logger.I("[mcp] file_edit %s 第 %d 行 -%d +%d", path, startLine, removed, added)
	return map[string]any{
		"path":         path,
		"changed":      true,
		"startLine":    startLine,
		"removedLines": removed,
		"addedLines":   added,
		"occurrences":  count,
		"replacedAll":  replaceAll,
		"tip":          "改动已落盘。要继续改同一文件请重新 file_read（行号可能已变）；确认整体改动用 git_diff",
	}, nil
}

// guardWritableFile 拒绝对几类文件写入。
//
// 私钥不写是防「模型把密钥换成别的内容」；.git 内部文件不写是防「手改 index/HEAD 把仓库搞坏」，
// 后者应该走 git 命令而不是直接改文件。
func guardWritableFile(path string) error {
	if fsguard.SecretFile(path) {
		return fmt.Errorf("%s 属于凭据/密钥文件，不允许由 AI 写入", filepath.Base(path))
	}
	if strings.Contains(strings.ToLower(path), string(os.PathSeparator)+".git"+string(os.PathSeparator)) {
		return errors.New("不允许直接修改 .git 目录内的文件；需要改仓库状态请用 git 命令")
	}
	return nil
}

// writeFileAtomic 先写临时文件再改名，避免写到一半失败留下半截文件。
//
// 直接 os.WriteFile 在进程被杀/磁盘满时会得到一个截断的文件，
// 而用户当时看到的是「AI 说改好了」——这种损坏最难排查。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".qd-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // 成功后这里会失败，无害
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Windows 上 Chmod 基本无效，失败不影响结果
	_ = os.Chmod(tmpName, mode.Perm())
	return os.Rename(tmpName, path)
}

// locateOccurrences 列出 old_string 出现的位置，给「多处匹配」的报错用。
func locateOccurrences(content, want string, max int) string {
	var parts []string
	from := 0
	for len(parts) < max {
		i := strings.Index(content[from:], want)
		if i < 0 {
			break
		}
		abs := from + i
		line := 1 + strings.Count(content[:abs], "\n")
		lineText := strings.TrimSpace(lineAt(content, line))
		parts = append(parts, fmt.Sprintf("第 %d 行「%s」", line, mcpsrv.TruncateRunes(lineText, 80)))
		from = abs + len(want)
	}
	return strings.Join(parts, "、")
}

// suggestSimilarBlock 在 old_string 匹配不上时，找出最接近的行，让模型自己修参数。
//
// 只回「没找到」是最浪费的一步：模型只能整份重读，然后再猜一次。
func suggestSimilarBlock(content, want string) string {
	var anchor string
	for _, ln := range strings.Split(want, "\n") {
		if t := strings.TrimSpace(ln); len([]rune(t)) > len([]rune(anchor)) {
			anchor = t
		}
	}
	if len([]rune(anchor)) < 6 {
		return "old_string 太短（少于 6 个字符），无法给出相似位置；请先 file_read 确认原文再改"
	}
	lines := strings.Split(content, "\n")
	collect := func(cmp func(string) bool) []string {
		var hits []string
		for i, ln := range lines {
			if cmp(ln) {
				hits = append(hits, fmt.Sprintf("第 %d 行：%s", i+1, mcpsrv.TruncateRunes(strings.TrimSpace(ln), 140)))
				if len(hits) >= 3 {
					break
				}
			}
		}
		return hits
	}
	hits := collect(func(ln string) bool { return strings.Contains(ln, anchor) })
	if len(hits) == 0 {
		// 缩进/空格不一致是最常见的原因，去掉空白再比一次
		compact := strings.Join(strings.Fields(anchor), "")
		hits = collect(func(ln string) bool {
			return strings.Contains(strings.Join(strings.Fields(ln), ""), compact)
		})
		if len(hits) > 0 {
			return "\n文件里这几行内容接近但空白不同，请按文件里的实际缩进重写 old_string：\n" + strings.Join(hits, "\n")
		}
		return "\n没有找到相近的行：可能文件已经变了，或路径不对。请重新 file_read 确认。"
	}
	return "\n文件里这几行最接近（检查缩进、空格、引号是否与原文完全一致）：\n" + strings.Join(hits, "\n")
}

// lineAt 取第 n 行（1 起）。
func lineAt(content string, n int) string {
	if n < 1 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// -------- file_write --------

// fileWrite 创建（或整份覆盖）一个文本文件。
//
// 为什么必须有它：file_edit 要求「文件已存在且 old_string 能匹配」，
// 于是「帮我写一份 xxx.md」这类最常用的请求根本没有对应工具——模型只能去
// run_command 里 echo / Set-Content / node -e 造文件。那条路在 Windows 上
// 极易出错（cmd 的引号与 ^ 转义、PowerShell 的 UTF-16 默认编码、中文乱码），
// 失败后模型还会改着法子重试，用户看到的是一串莫名其妙的命令。
//
// 两条保护刻意保留：
//   - 已存在时默认不覆盖（overwrite=false）。整份覆盖会静默吃掉用户手改的内容，
//     而「新建文件」这个诉求本身不需要覆盖。
//   - 内容与现有文件完全一致时直接返回「无改动」，不发一次无意义的写盘
//     （模型的重复调用会被上层拦截，但换个顺序/空格再发一次仍会走到这里）。
func fileWrite(d Deps, args map[string]any) (any, error) {
	path, err := fsguard.ResolveWrite(d.DB, mcpsrv.PathArg(args, "path"), mcpsrv.Cwd(args))
	if err != nil {
		return nil, err
	}
	raw, ok := args["content"]
	if !ok {
		return nil, errors.New("缺少参数 content（要写入的完整内容）；想写空文件请显式传空字符串")
	}
	content, _ := raw.(string)
	if len(content) > fileWriteMaxBytes {
		return nil, fmt.Errorf("内容有 %d 字节，超过 %dMB 上限；大文件请用 run_command 配合脚本生成，或拆到多个文件",
			len(content), fileWriteMaxBytes>>20)
	}
	if err := guardWritableFile(path); err != nil {
		return nil, err
	}

	existed := false
	mode := os.FileMode(0o644)
	if fi, serr := os.Stat(path); serr == nil {
		if fi.IsDir() {
			return nil, fmt.Errorf("%s 是目录，不是文件；要往目录里加文件请把文件名写在路径里", path)
		}
		existed = true
		mode = fi.Mode()
		if fi.Size() > fileWriteMaxBytes {
			return nil, fmt.Errorf("%s 有 %d 字节，超过 %dMB 上限；这类文件应该用命令处理",
				path, fi.Size(), fileWriteMaxBytes>>20)
		}
		if old, rerr := os.ReadFile(path); rerr == nil && string(old) == content {
			return map[string]any{
				"path":    path,
				"created": false,
				"changed": false,
				"bytes":   len(content),
				"note":    "文件内容与要写入的完全一致，没有重复写盘",
			}, nil
		}
		if !mcpsrv.ArgBool(args, "overwrite") {
			return nil, fmt.Errorf("%s 已存在（%d 字节），本次未覆盖。要修改它请先 file_read 看原文，再用 file_edit 精确替换；"+
				"确实要整份替换请传 overwrite=true", path, fi.Size())
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建目录 %s 失败：%v", dir, err)
	}
	if err := writeFileAtomic(path, []byte(content), mode); err != nil {
		return nil, fmt.Errorf("写入失败：%v", err)
	}

	added := 0
	if content != "" {
		added = strings.Count(content, "\n") + 1
	}
	verb := "新建"
	if existed {
		verb = "覆盖"
	}
	logger.I("[mcp] file_write %s %s %d 字节 +%d 行", path, verb, len(content), added)
	return map[string]any{
		"path":       path,
		"created":    !existed,
		"changed":    true,
		"bytes":      len(content),
		"addedLines": added,
		"tip":        "已写入磁盘。要再改它请 file_read 后用 file_edit 精确替换，不要整份重写",
	}, nil
}

// -------- run_command --------

func runCommand(d Deps, args map[string]any) (any, error) {
	command := strings.TrimSpace(mcpsrv.Arg(args, "command"))
	if command == "" {
		return nil, errors.New("缺少参数 command（要执行的命令）")
	}
	dir, err := fsguard.ResolveWrite(d.DB, mcpsrv.Arg(args, "cwd"), mcpsrv.Cwd(args))
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("工作目录不可用：%v", err)
	} else if !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	// 风险判定：high 一律不执行，把命令交还给人。
	if level, note := aiutil.LocalCmdDangerRules.Eval(command); level == "high" {
		return nil, fmt.Errorf("这条命令被判定为高危，未执行：%s\n命令：%s\n"+
			"请把命令原样告诉用户，让他在终端里自己确认后运行", note, command)
	}
	for _, rule := range codeAgentDangerRules {
		if rule.re.MatchString(command) {
			return nil, fmt.Errorf("这条命令会破坏未提交的工作，未执行：%s\n命令：%s\n"+
				"如果确实要做，请让用户自己在终端里执行", rule.note, command)
		}
	}

	timeoutSec := mcpsrv.ArgInt(args, "timeout_sec", runCmdDefaultSec)
	if timeoutSec <= 0 {
		timeoutSec = runCmdDefaultSec
	}
	if timeoutSec > runCmdMaxSec {
		timeoutSec = runCmdMaxSec
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		shell := os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd.exe"
		}
		// /d 关掉 AutoRun：注册表里配了 autorun 时，别的软件可能往每条命令前塞东西。
		cmd = sysutil.CommandContext(ctx, shell, "/d", "/s", "/c", command)
	} else {
		cmd = sysutil.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Env = commandEnv(d)
	// 独立进程组：超时时能整组杀掉，而不是留下 npm→node 这样的孙子进程继续占端口。
	setProcessGroup(cmd)

	outBuf := &cappedBuffer{max: runCmdMaxOutput}
	errBuf := &cappedBuffer{max: runCmdMaxOutput}
	cmd.Stdout, cmd.Stderr = outBuf, errBuf

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动命令：%v", err)
	}
	waitErr := cmd.Wait()
	duration := time.Since(started)

	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if timedOut {
		killCommandTree(cmd, dir)
		// 再等一小会儿让输出落盘，否则「超时」那条结果里常常啥都没有。
		deadline := time.Now().Add(runCmdKillGrace)
		for time.Now().Before(deadline) && errBuf.buf.Len() == 0 && outBuf.buf.Len() == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else if !timedOut {
			return nil, fmt.Errorf("命令执行失败：%v", waitErr)
		}
	}

	logger.I("[mcp] run_command %s → exit=%d 耗时=%v 输出=%d+%d 字节",
		dir, exitCode, duration.Round(time.Millisecond), outBuf.buf.Len(), errBuf.buf.Len())

	res := map[string]any{
		"cwd":        dir,
		"command":    command,
		"exitCode":   exitCode,
		"ok":         exitCode == 0 && !timedOut,
		"stdout":     outBuf.String(),
		"stderr":     errBuf.String(),
		"durationMs": duration.Milliseconds(),
		"timeout":    timedOut,
	}
	if timedOut {
		res["note"] = fmt.Sprintf("命令超过 %d 秒未结束，已连同子进程一起终止。需要更久请调大 timeout_sec（上限 %d）", timeoutSec, runCmdMaxSec)
	}
	if outBuf.truncated || errBuf.truncated {
		res["outputTruncated"] = true
		res["outputNote"] = fmt.Sprintf("输出超过 %dKB，只保留了前段；要完整结果请把输出重定向到文件再用 file_read", runCmdMaxOutput>>10)
	}
	if level, note := aiutil.LocalCmdDangerRules.Eval(command); level == "medium" {
		res["warning"] = note
	}
	return res, nil
}

// commandEnv 构造命令的环境变量：在继承当前环境的基础上，把 QuickDock 托管的
// 运行时 bin 目录前插到 PATH。
//
// 为什么必须做：GUI 进程读到的是注册表里的 PATH，与用户终端里的经常不一样。
// 不补这一步，「npm 找不到」「go 不是内部或外部命令」会变成这个工具最常见的失败，
// 而用户明明在终端里跑得好好的。
func commandEnv(d Deps) []string {
	env := os.Environ()
	if d.Env == nil {
		return env
	}
	var extra []string
	for _, rt := range []envengine.Runtime{
		envengine.RuntimeNode, envengine.RuntimeGo, envengine.RuntimeGit,
		envengine.RuntimePython, envengine.RuntimeBun, envengine.RuntimePHP,
	} {
		if dir := d.Env.BinDirFor(rt, ""); dir != "" {
			extra = append(extra, dir)
		}
	}
	if len(extra) == 0 {
		return env
	}
	// 只在 PATH 里查一次，其它环境变量原样保留。
	for i, kv := range env {
		if len(kv) > 5 && strings.EqualFold(kv[:5], "PATH=") {
			env[i] = "PATH=" + strings.Join(extra, string(os.PathListSeparator)) + string(os.PathListSeparator) + kv[5:]
			return env
		}
	}
	return append(env, "PATH="+strings.Join(extra, string(os.PathListSeparator)))
}

// killCommandTree 连同子进程一起终止。
//
// Go 的 CommandContext 只会 kill 直接子进程：npm 自己退了，它拉起的 node 还活着并占着端口，
// 用户下次启动就报「端口被占用」——这正是 dsh 那边踩过的坑（见 internal/dsh 的 killProcessTree）。
// 平台相关部分在 tools_code_windows.go / tools_code_unix.go。
func killCommandTree(cmd *exec.Cmd, dir string) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	killProcessGroup(cmd)
}

// cappedBuffer 限制写入量的缓冲区。
//
// 必须「假装写成功」返回 len(p)：返回短写会让 os/exec 认为管道出错并提前中断命令，
// 表现为「命令莫名其妙失败」，而真实原因只是我们不想要那么多输出。
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len() >= c.max {
		c.truncated = true
		return len(p), nil
	}
	remain := c.max - c.buf.Len()
	if len(p) > remain {
		c.buf.Write(p[:remain])
		c.truncated = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	return c.buf.String()
}

// -------- check_workdir --------

// checkWorkdir 探测一个目录是什么项目、该怎么跑。
//
// 存在的理由：模型在一个陌生目录里做的第一件事往往是猜——猜 npm 还是 pnpm、
// 猜有没有装依赖、猜类型检查跑什么。每猜错一次就是一轮 token。
// 这里把「看文件就能知道」的部分一次性告诉它。
func checkWorkdir(d Deps, args map[string]any) (any, error) {
	raw := mcpsrv.PathArg(args, "path")
	if raw == "" {
		raw = mcpsrv.Cwd(args)
	}
	if raw == "" {
		return nil, errors.New("不知道要探测哪个目录：当前对话没有绑定工作目录，请传 path")
	}
	dir, err := fsguard.ResolveRead(d.DB, raw, mcpsrv.Cwd(args), false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("无法访问 %s：%v", dir, err)
	}
	if !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	entries, _ := os.ReadDir(dir)
	names := make(map[string]bool, len(entries))
	top := make([]string, 0, 20)
	for _, e := range entries {
		names[e.Name()] = true
		if len(top) < 20 {
			top = append(top, e.Name())
		}
	}
	has := func(list ...string) bool {
		for _, n := range list {
			if names[n] {
				return true
			}
		}
		return false
	}

	out := map[string]any{"path": dir, "topEntries": top}
	kinds := []string{}
	suggest := []string{}
	gitIgnored := []string{"node_modules", "dist", "build", ".venv", "target", "vendor"}

	// ---- Node / 前端 ----
	if names["package.json"] {
		kinds = append(kinds, "node")
		pkg := map[string]any{}
		if data, rerr := os.ReadFile(filepath.Join(dir, "package.json")); rerr == nil {
			var parsed struct {
				Name            string            `json:"name"`
				Version         string            `json:"version"`
				PackageManager  string            `json:"packageManager"`
				Scripts         map[string]string `json:"scripts"`
				Dependencies    map[string]string `json:"dependencies"`
				DevDependencies map[string]string `json:"devDependencies"`
			}
			if json.Unmarshal(data, &parsed) == nil {
				if parsed.Name != "" {
					pkg["name"] = parsed.Name
					out["name"] = parsed.Name
				}
				if parsed.Version != "" {
					pkg["version"] = parsed.Version
				}
				if len(parsed.Scripts) > 0 {
					pkg["scripts"] = parsed.Scripts
				}
				pm := detectNodePM(dir, names, parsed.PackageManager)
				pkg["packageManager"] = pm
				pkg["depsInstalled"] = names["node_modules"]
				if fw := detectFramework(parsed.Dependencies, parsed.DevDependencies); fw != "" {
					pkg["framework"] = fw
				}
				if !names["node_modules"] {
					suggest = append(suggest, pm+" install")
				}
				for _, s := range []string{"dev", "build", "test", "lint", "typecheck", "check"} {
					if _, ok := parsed.Scripts[s]; ok {
						suggest = append(suggest, pm+" run "+s)
					}
				}
				if names["tsconfig.json"] {
					suggest = append(suggest, "npx tsc --noEmit")
				}
			}
		}
		if pkg["packageManager"] == nil {
			pkg["packageManager"] = detectNodePM(dir, names, "")
		}
		if _, ok := pkg["depsInstalled"]; !ok {
			pkg["depsInstalled"] = names["node_modules"]
		}
		out["node"] = pkg
	}

	// ---- Go ----
	if names["go.mod"] {
		kinds = append(kinds, "go")
		g := map[string]any{}
		if data, rerr := os.ReadFile(filepath.Join(dir, "go.mod")); rerr == nil {
			for _, ln := range strings.Split(string(data), "\n") {
				t := strings.TrimSpace(ln)
				if strings.HasPrefix(t, "module ") {
					g["module"] = strings.TrimSpace(strings.TrimPrefix(t, "module "))
				}
				if strings.HasPrefix(t, "go ") {
					g["goVersion"] = strings.TrimSpace(strings.TrimPrefix(t, "go "))
				}
			}
		}
		g["vendored"] = names["vendor"]
		out["go"] = g
		suggest = append(suggest, "go build ./...", "go vet ./...")
	}

	// ---- 其它语言：只报类型与建议命令，不深入解析 ----
	if has("Cargo.toml") {
		kinds = append(kinds, "rust")
		suggest = append(suggest, "cargo check", "cargo test")
	}
	if has("pyproject.toml", "requirements.txt", "setup.py", "Pipfile") {
		kinds = append(kinds, "python")
		out["python"] = map[string]any{
			"venvPresent": has(".venv", "venv"),
			"manager":     pyManager(names),
		}
		if !has(".venv", "venv") {
			suggest = append(suggest, "python -m venv .venv")
		}
	}
	if has("pom.xml", "build.gradle", "build.gradle.kts") {
		kinds = append(kinds, "java")
		suggest = append(suggest, "mvn -q -DskipTests compile")
	}
	if has("composer.json") {
		kinds = append(kinds, "php")
		if !names["vendor"] {
			suggest = append(suggest, "composer install")
		}
	}
	if has("Gemfile") {
		kinds = append(kinds, "ruby")
		if !names["Gemfile.lock"] || !names["vendor"] {
			suggest = append(suggest, "bundle install")
		}
	}
	if hasDotNetProject(entries) {
		kinds = append(kinds, "dotnet")
		suggest = append(suggest, "dotnet build")
	}
	if len(kinds) == 0 {
		kinds = append(kinds, "unknown")
	}
	out["kinds"] = kinds

	// ---- git ----
	if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		g := map[string]any{"isRepo": true, "isWorktreeFile": info.Mode().IsRegular()}
		if top, _, e := runGit(d, dir, "rev-parse", "--show-toplevel"); e == nil {
			g["root"] = strings.TrimSpace(top)
		}
		if b, _, e := runGit(d, dir, "rev-parse", "--abbrev-ref", "HEAD"); e == nil {
			g["branch"] = strings.TrimSpace(b)
		}
		if st, _, e := runGit(d, dir, "status", "--porcelain=v1"); e == nil {
			n := 0
			for _, ln := range strings.Split(st, "\n") {
				if strings.TrimSpace(ln) != "" {
					n++
				}
			}
			g["changedFiles"] = n
			g["clean"] = n == 0
		}
		out["git"] = g
	} else {
		out["git"] = map[string]any{"isRepo": false}
		suggest = append(suggest, "git init")
	}

	out["suggestedCommands"] = suggest
	out["ignoredDirs"] = gitIgnored
	out["tip"] = "suggestedCommands 只是根据文件推断的候选命令，执行前先看 dependencies/scripts 是否真的匹配；" +
		"跑之前可用 dir_list 或 file_read 再确认"
	return out, nil
}

// detectNodePM 判断这个项目用哪个包管理器。
//
// 判据顺序是刻意的：锁文件是「事实」（谁生成的锁就是谁在用），
// package.json 的 packageManager 字段是「声明」，两者冲突时以锁文件为准——
// 用错包管理器会产生第二个锁文件，把项目搞脏。
func detectNodePM(dir string, names map[string]bool, declared string) string {
	switch {
	case names["pnpm-lock.yaml"]:
		return "pnpm"
	case names["yarn.lock"]:
		return "yarn"
	case names["bun.lockb"], names["bun.lock"]:
		return "bun"
	case names["package-lock.json"]:
		return "npm"
	}
	if declared != "" {
		if i := strings.Index(declared, "@"); i > 0 {
			return declared[:i]
		}
		return declared
	}
	return "npm"
}

// detectFramework 从依赖里认出常见框架，让模型知道「这是个 Vue 项目」而不是只能看到一堆包名。
func detectFramework(deps, devDeps map[string]string) string {
	known := []struct{ pkg, label string }{
		{"next", "next"}, {"nuxt", "nuxt"}, {"react", "react"}, {"vue", "vue"},
		{"svelte", "svelte"}, {"@angular/core", "angular"}, {"vite", "vite"},
		{"express", "express"}, {"fastify", "fastify"}, {"nest", "nest"},
	}
	for _, k := range known {
		if _, ok := deps[k.pkg]; ok {
			return k.label
		}
		if _, ok := devDeps[k.pkg]; ok {
			return k.label
		}
	}
	return ""
}

func pyManager(names map[string]bool) string {
	switch {
	case names["poetry.lock"]:
		return "poetry"
	case names["Pipfile.lock"]:
		return "pipenv"
	case names["uv.lock"]:
		return "uv"
	case names["pyproject.toml"]:
		return "pip (pyproject)"
	}
	return "pip"
}

func hasDotNetProject(entries []os.DirEntry) bool {
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if strings.HasSuffix(n, ".sln") || strings.HasSuffix(n, ".csproj") {
			return true
		}
	}
	return false
}
