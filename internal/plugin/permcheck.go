package plugin

// permcheck.go —— native 插件的「权限自证」静态审计器
//
// 背景：宿主对插件的能力管控有一条明确的分界线——凡是走 host.* 桥接的调用，
// 都会经过 checkPermission（见 host.go），受 permissions 声明严格约束。
// 但 native 插件是独立子进程，它在自己的 Go 代码里可以直接调用标准库
// （os.Remove / exec.Command / net.Dial …），这些调用不经过任何 Go 接口，
// 宿主完全无法在运行期拦截。
//
// 于是出现一种现实：插件清单上写着 "filesystem": true，
// 代码里却直接 os.RemoveAll 了整个用户目录；声明了 "network": false，
// 却用 net.Dial 硬连了任意地址。permissions 沦为一张对用户可见的授权契约，
// 而契约与实现不符时，这份授权对用户就毫无意义。
//
// 本文件不改变运行期行为，只做一件事：读源码，得出该插件实际用到了哪些
// 危险能力，再与 manifest 声明比对，把"清单撒谎"的插件揪出来。
//
// 阶段一（当前）：只审计、只告警，输出名单供人工修清单。
// 阶段二起：可切换为安装期拦截（见 CheckAgainst 的调用方）。
//
// 设计取舍：
//   - 用 go/ast 而非正则：正则分不清 os.Remove 与 mypkg.Remove，
//     也分不清字符串字面量里的路径与注释里的路径。
//   - 静态分析必然漏判（路径动态拼接、网络地址来自配置），
//     因此提供 permissions.selfAudit="skip" 逃生舱，但强制要求填写 skipReason，
//     让绕过行为可见、可审计。
//   - 只审计 native 插件：goja 插件跑在宿主进程内的沙箱里，
//     它的每次 host.* 调用都过 checkPermission，天然受控。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ---- 审计结果 ----

// AuditFinding 一条审计发现
type AuditFinding struct {
	// Kind 危险能力类别：fs_write / fs_read / exec / raw_net
	Kind string
	// API 命中的 API，如 "os.Remove"
	API string
	// Location 源码位置 "file.go:123"
	Location string
	// PathLiteral 路径字面量（仅当参数是字符串常量时有值），用于与 filesystem scope 比对
	PathLiteral string
}

// PermissionAudit 某个插件的权限审计结果
type PermissionAudit struct {
	// Findings 全部发现
	Findings []AuditFinding
	// NeedsWrite 是否直接写/删文件
	NeedsWrite bool
	// NeedsExec 是否启动子进程
	NeedsExec bool
	// ExecPrograms 源码中出现过的可执行程序名（去重，仅取字符串字面量参数）
	ExecPrograms []string
	// NeedsRawNet 是否绕过 host.http 直连网络
	NeedsRawNet bool
	// ReadPaths 源码中出现的读路径字面量（去重）
	ReadPaths []string
	// WritePaths 源码中出现的写路径字面量（去重）
	WritePaths []string
}

// 危险能力类别常量
const (
	AuditKindFSWrite = "fs_write"
	AuditKindFSRead  = "fs_read"
	AuditKindExec    = "exec"
	AuditKindRawNet  = "raw_net"
)

// ---- 危险 API 规则表 ----
//
// 键为「包路径.函数名」，值为能力类别。
// 注意 os/exec.LookPath 只做程序探测不执行，不算 exec；
// os.Stat / os.Open 只读不算写。

var auditRules = map[string]string{
	// 写 / 删 / 改文件
	"os.WriteFile":     AuditKindFSWrite,
	"os.Remove":        AuditKindFSWrite,
	"os.RemoveAll":     AuditKindFSWrite,
	"os.Rename":        AuditKindFSWrite,
	"os.Mkdir":         AuditKindFSWrite,
	"os.MkdirAll":      AuditKindFSWrite,
	"os.Create":        AuditKindFSWrite,
	"os.OpenFile":      AuditKindFSWrite,
	"os.Truncate":      AuditKindFSWrite,
	"os.Chmod":         AuditKindFSWrite,
	"os.Chown":         AuditKindFSWrite,
	"os.Link":          AuditKindFSWrite,
	"os.Symlink":       AuditKindFSWrite,
	"os.Chtimes":       AuditKindFSWrite,
	"ioutil.WriteFile": AuditKindFSWrite,

	// 读文件 / 遍历目录
	"os.ReadFile":      AuditKindFSRead,
	"os.Open":          AuditKindFSRead,
	"os.ReadDir":       AuditKindFSRead,
	"os.Stat":          AuditKindFSRead,
	"os.Lstat":         AuditKindFSRead,
	"ioutil.ReadFile":  AuditKindFSRead,
	"filepath.Walk":    AuditKindFSRead,
	"filepath.WalkDir": AuditKindFSRead,

	// 启动子进程
	"os/exec.Command":        AuditKindExec,
	"os/exec.CommandContext": AuditKindExec,
	"os/exec.LookPath":       "", // 仅探测，不执行

	// 绕过 host.http 的直连网络
	"net.Dial":            AuditKindRawNet,
	"net.DialTCP":         AuditKindRawNet,
	"net.DialUDP":         AuditKindRawNet,
	"net.Listen":          AuditKindRawNet,
	"net.ListenTCP":       AuditKindRawNet,
	"net/http.Get":        AuditKindRawNet,
	"net/http.Post":       AuditKindRawNet,
	"net/http.Head":       AuditKindRawNet,
	"net/http.Do":         AuditKindRawNet,
	"net/http.NewRequest": AuditKindRawNet,
}

// auditSkipFiles 审计时跳过的文件（样板/日志/测试）
var auditSkipFiles = map[string]bool{
	"qdlog.go": true,
}

// AuditPermissions 静态分析 native 插件源码，得出其实际需要的权限下限。
// 只读不写，解析失败的文件会被跳过并记录在 Result.Skipped 中（不阻断）。
func AuditPermissions(pluginDir string) (*PermissionAudit, error) {
	a := &PermissionAudit{}
	fset := token.NewFileSet()

	err := filepath.Walk(pluginDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 读不到就跳过，不阻断
		}
		if info.IsDir() {
			// 只审计插件根目录下的 Go 文件，不进子目录（子目录多是 tools/ 第三方源码）
			if info.Name() != "." && path != pluginDir {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasSuffix(base, ".go") {
			return nil
		}
		if strings.HasSuffix(base, "_test.go") || auditSkipFiles[base] {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil // 语法错误的文件跳过，交给编译期报错
		}

		rel, _ := filepath.Rel(pluginDir, path)
		ast.Inspect(f, func(n ast.Node) bool {
			// 只在调用表达式上处理，才能同时拿到被调用的 API 与实参
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			// 还原导入别名：源码里可能是 exec.Command，也可能别名成 ec.Command
			realPkg := resolveImportAlias(f, pkg.Name)
			if realPkg == "" {
				return true
			}
			key := realPkg + "." + sel.Sel.Name
			kind, known := auditRules[key]
			if !known {
				return true
			}
			if kind == "" {
				return true // 规则表里显式标为不计入
			}

			loc := fset.Position(sel.Pos())
			fd := AuditFinding{
				Kind:     kind,
				API:      key,
				Location: fmt.Sprintf("%s:%d", filepath.ToSlash(rel), loc.Line),
			}
			// 提取字符串字面量参数（路径/URL）
			fd.PathLiteral = firstStringArg(call)
			a.Findings = append(a.Findings, fd)

			switch kind {
			case AuditKindFSWrite:
				a.NeedsWrite = true
				if fd.PathLiteral != "" {
					a.WritePaths = append(a.WritePaths, fd.PathLiteral)
				}
			case AuditKindExec:
				a.NeedsExec = true
				if fd.PathLiteral != "" {
					a.ExecPrograms = append(a.ExecPrograms, fd.PathLiteral)
				}
			case AuditKindRawNet:
				a.NeedsRawNet = true
			case AuditKindFSRead:
				if fd.PathLiteral != "" {
					a.ReadPaths = append(a.ReadPaths, fd.PathLiteral)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// ReadPaths 去重并排序，保证输出稳定
	sort.Strings(a.ReadPaths)
	a.ReadPaths = dedupStrings(a.ReadPaths)
	sort.Strings(a.WritePaths)
	a.WritePaths = dedupStrings(a.WritePaths)
	sort.Strings(a.ExecPrograms)
	a.ExecPrograms = dedupStrings(a.ExecPrograms)
	return a, nil
}

// resolveImportAlias 把源码里的选择器前缀解析为真实包路径。
// 例如 `import ec "os/exec"` 后源码里的 ec.Command 应识别为 os/exec.Command。
// 未被识别为导入包的标识符返回空串（避免把本地变量 os 当成标准库 os）。
func resolveImportAlias(f *ast.File, name string) string {
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		local := path
		if idx := strings.LastIndex(path, "/"); idx >= 0 {
			local = path[idx+1:]
		}
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local == name {
			return path
		}
	}
	return ""
}

// firstStringArg 取函数调用的第一个字符串字面量参数。
// 用于把 os.WriteFile("/etc/hosts", ...) 里的路径抽出来与 filesystem scope 比对。
// 参数是变量或拼接表达式时返回空串（静态分析能力有限，如实返回）。
//
// 实现方式：ast.Inspect 里拿到 SelectorExpr 时无法直接看到父节点，
// 因此这里改为在 AuditPermissions 里对 CallExpr 单独处理，
// 判定其 Fun 是否为 SelectorExpr，再取其第一个字符串字面量实参。
func firstStringArg(call *ast.CallExpr) string {
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			// 只取第一个位置为字符串字面量的参数；遇到非字面量即停止
			return ""
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return ""
		}
		return s
	}
	return ""
}

// dedupStrings 去重（保持顺序）
func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := in[:0]
	seen := map[string]bool{}
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ---- 与 manifest 声明比对 ----

// ErrPermissionMismatch 清单声明与源码实现不符
var ErrPermissionMismatch = fmt.Errorf("插件权限声明与实现不符")

// CheckAgainst 把审计结果与 manifest 的权限声明比对，返回缺失项描述。
// 阶段一仅用于生成告警文案，阶段二可作为拒绝安装的依据。
//
// 比对规则刻意保守——宁可漏报也不误报：
//   - filesystem 声明为 bool（true/false）时无法做路径级比对，只做能力级判断；
//   - 只有声明了 scope 对象时才逐路径比对，且只比对源码里出现的字面量路径。
//
// 关于「启动子进程」：对应 permissions.process（详见 procperm.go）。
// 比对分两层：
//   - 能力级：插件启动了子进程，就必须声明 process，否则声明与实现不符；
//   - 程序级：声明了白名单时，源码里出现的可执行程序名必须在白名单内。
//
// 注意 native 插件是独立子进程，它在自己的代码里调 exec.Command，
// 宿主在运行期无法拦截——本比对是**安装期的静态核对**，
// 用来发现「清单漏声明」和「白名单没覆盖实际执行的程序」。
// 真正的运行期强制要走 host.process.exec（宿主代执行）这条路。
func (p *Permissions) CheckAgainst(a *PermissionAudit) error {
	if a == nil {
		return nil
	}
	var missing []string

	// ---- 能力级比对 ----
	// 直接写/删文件：需要 filesystem 具备任一授权（弹框或 scope 读写皆可，
	// 因为弹框返回的路径同样会被 scope 约束，写入行为并未被放宽）。
	if a.NeedsWrite && !p.Filesystem.Granted() {
		missing = append(missing, "filesystem（插件直接写/删文件）")
	}

	// 启动子进程：需要声明 process
	if a.NeedsExec && !p.Process.Granted() {
		missing = append(missing, "process（插件直接启动子进程）")
	}

	// 直连网络：需要 network 授权
	if a.NeedsRawNet && !p.Network.Granted() {
		missing = append(missing, "network（插件直连网络，绕过 host.http 白名单）")
	}

	// ---- 程序级比对 ----
	// 只有声明了具体程序白名单才有意义；process:true（全放行）不做程序级比对。
	if len(p.Process.Programs) > 0 {
		for _, prog := range a.ExecPrograms {
			if !p.Process.AllowsProgram(prog) {
				missing = append(missing, fmt.Sprintf(
					"process 白名单未覆盖程序 %s（源码中出现该程序名）", prog))
			}
		}
	}

	// ---- 路径级比对 ----
	// 只有声明了 scope 对象（Read/Write 非空）才有意义。
	// bool 形式的 filesystem 只表示"能弹文件对话框"，不含读写，
	// 拿它去比对路径会全部落空，反而淹没真正的告警，故此处只在有 scope 时比对。
	if len(p.Filesystem.Read) > 0 {
		for _, path := range a.ReadPaths {
			if !pathAllowed(p.Filesystem.Read, path) {
				missing = append(missing, fmt.Sprintf(
					"filesystem.read 未覆盖 %s（源码中出现该路径字面量）", path))
			}
		}
	}
	if len(p.Filesystem.Write) > 0 {
		for _, path := range a.WritePaths {
			if !pathAllowed(p.Filesystem.Write, path) {
				missing = append(missing, fmt.Sprintf(
					"filesystem.write 未覆盖 %s（源码中出现该路径字面量）", path))
			}
		}
	}

	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrPermissionMismatch, strings.Join(missing, "、"))
}

// pathAllowed 判断一个字面量路径是否落在 scope 列表内。
//
// 与运行期的 FilesystemPerm.Allows 保持一致的口径：先把路径解析为绝对真实路径
// （含 ~ 展开与符号链接解析），再逐条 scope 匹配。解析失败时保守放行——
// 静态审计只是辅助提示，不应因为路径解析不了就断言插件越权。
func pathAllowed(scope []string, literal string) bool {
	if literal == "" {
		return true
	}
	abs := literal
	if !looksAbsolute(abs) {
		// 相对路径：相对插件工作目录，插件后端进程的 cmd.Dir 就是插件目录
		abs = filepath.Join(".", abs)
	}
	real, err := ResolveRealPath(abs)
	if err != nil {
		return true // 解析不了 → 不判定越权
	}
	for _, s := range scope {
		if matchScope(s, real) {
			return true
		}
	}
	return false
}

// DescribeAudit 生成人类可读的审计摘要，用于日志输出。
func DescribeAudit(pluginID string, a *PermissionAudit) string {
	if a == nil || len(a.Findings) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[plugin-perm] %s 发现 %d 处危险 API 使用", pluginID, len(a.Findings))
	if a.NeedsWrite {
		sb.WriteString(" [直接写/删文件]")
	}
	if a.NeedsExec {
		sb.WriteString(" [启动子进程]")
	}
	if a.NeedsRawNet {
		sb.WriteString(" [直连网络]")
	}
	sb.WriteString("\n")
	for _, f := range a.Findings {
		fmt.Fprintf(&sb, "    %-10s %-24s %s\n", f.Kind, f.API, f.Location)
	}
	return sb.String()
}

// quotePath 便于日志输出路径字面量
func quotePath(p string) string {
	if p == "" {
		return ""
	}
	return strconv.Quote(p)
}
