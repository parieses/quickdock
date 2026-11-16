package aiutil

import "strings"

// 命令族识别：把一条命令归到一个「用户能理解的授权单位」上。
//
// 这个文件从 services/ai 的审批代码里搬出来，是为了让**工具注册处**也能用它。
// 按工具名记住一个写工具，等于批准了一次就永久放行；而 run_command 的正确粒度是
// 「命令族」——批准 npm install 之后 npm 一族不再问，rm 换了首词照问。
// 逻辑只有一份，注册处与审批处读同一个判断。

// commandShellWords 「执行壳」：它们本身不是干活的那个程序，跳过再取首词。
//
// 必须跳过 sudo：否则 `sudo rm -rf /` 会被记成「sudo 一族」，之后任意一条
// sudo 命令都放行——那比不记更糟。
var commandShellWords = map[string]bool{
	"sudo": true, "doas": true, "command": true, "exec": true,
	"time": true, "nohup": true, "env": true, "nice": true, "start": true,
}

// commandInterpreterWords 能执行任意命令的解释器。
//
// 与 commandShellWords 的区别：那批是「一层壳」，跳过它取下一个词还是同一个程序
// （`sudo rm` 记成 rm 是对的）；而这批的下一词是它自己的开关（`cmd /c …`、`bash -lc …`），
// 取到的不是被执行的程序，按它记族毫无意义。
//
// 也不能像 sudo 那样跳过：跳过 `cmd` 会取到 `/c`，跳过 `bash` 会取到 `-lc`。
// 唯一安全的做法是命中就不给「记住」——批准一次 `cmd` 等于放行之后任意
// `cmd /c del /s /q …`，那比每次多点一次确认糟得多。
var commandInterpreterWords = map[string]bool{
	// shell
	"cmd": true, "powershell": true, "pwsh": true, "wsl": true,
	"bash": true, "sh": true, "zsh": true, "fish": true,
	"ksh": true, "csh": true, "tcsh": true, "dash": true,
	// 脚本宿主：参数就是代码
	"python": true, "python3": true, "py": true, "node": true, "deno": true,
	"bun": true, "perl": true, "ruby": true, "php": true, "lua": true,
	"osascript": true, "expect": true,
	// 任意 jar / 类
	"java": true, "javaw": true,
}

// CommandFamily 取一条命令的「首词」，用作命令族的标识。
//
// 返回空字符串表示「这条不该给记住按钮」——多段命令（含 && || ; | &）一律如此。
// 理由：`cd repo && rm -rf build` 若按首词记成 cd，下次 `cd x && rm -rf y`
// 就再也不问了，而用户当初批准的是「切个目录」。
func CommandFamily(cmd string) string {
	s := strings.TrimSpace(cmd)
	if s == "" {
		return ""
	}
	// 一次只批准一条命令：连接符说明这里其实有多条。
	if strings.ContainsAny(s, "&|;\n") {
		return ""
	}
	fields := strings.Fields(s)
	for len(fields) > 0 {
		w := fields[0]
		// 跳过前置环境变量赋值 FOO=bar（只认「前面不像路径」的形态，
		// 避免把 C:\a=b\npm 这种路径里的等号当成赋值）。
		if i := strings.IndexByte(w, '='); i > 0 && !strings.ContainsAny(w[:i], `/\`) {
			fields = fields[1:]
			continue
		}
		if commandShellWords[strings.ToLower(w)] {
			fields = fields[1:]
			continue
		}
		break
	}
	if len(fields) == 0 {
		return ""
	}
	// 去掉路径与扩展名：C:\Program Files\nodejs\npm.cmd → npm
	base := fields[0]
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1", ".com"} {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "" || commandInterpreterWords[base] {
		return ""
	}
	return base
}
