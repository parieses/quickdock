// Package fsguard 是「AI 能读写哪些本地路径」这条策略的唯一实现。
//
// 为什么单独成包：读白名单与写白名单分属两个工具集——读文件是宿主能力（file_read 对外
// 也开放），改文件/跑命令是 agent 能力（只在 services/ai/tools 里）。但它们判的是同一件事
// 「用户有没有授权这个位置」，各写一份的结果是两边迟早给出不同的根目录，于是出现
// 「能读却改不了同一个文件」这种最难向用户解释的状态。
//
// 两套白名单的差别是刻意设计的，不要为了「一致」把它们合并：
//
//   - 读（ReadRoots）：家目录 + 数据目录 + 快捷项里登记的目录/工作目录。
//     用户让 AI 看一眼某个文件是很自然的诉求，拦掉会逼他去复制粘贴。
//   - 写（WriteRoots）：只有用户显式登记过的位置（快捷项目录、工作目录），再加
//     本次对话绑定的工作目录，家目录被故意排除。否则一句话就能改掉
//     ~/Documents 里的任何东西，而用户从未做过「允许写这里」的动作。
package fsguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/platform"
)

// skipDirs 遍历时默认跳过的目录名：这些目录里的内容几乎全是噪音，
// 体量通常是其余目录的几十倍。file_grep 与 dir_list 共用这一份。
//
// 不导出：判定必须走 SkipDir。直接查这张表的人会踩两个坑——表里混着 Pods 这种
// 首字母大写的键，而用户目录里写成 pods 的同样存在（真踩过：iOS 工程一列目录
// 就被 Pods 淹掉）。表只描述「哪些目录」，不描述「怎么比对」。
var skipDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true,
	"node_modules": true, "bower_components": true,
	"dist": true, "build": true, "out": true, "target": true,
	".next": true, ".nuxt": true, ".cache": true, ".turbo": true,
	"vendor": true, "Pods": true, "__pycache__": true, ".venv": true, "venv": true,
	".idea": true, ".vs": true, ".gradle": true,
}

// skipDirLower 是小写化后的 skipDirs，启动时建一次。
//
// 统一小写比对而不是「先原样、后小写」：后者只能匹配表里那种大小写混写的键，
// 用户目录写成全小写时仍然漏判（pods / PODS）。目录名在这些构建产物上
// 不存在「大小写不同但是另一个目录」的情况，统一小写是净收益。
var skipDirLower = func() map[string]bool {
	m := make(map[string]bool, len(skipDirs))
	for k := range skipDirs {
		m[strings.ToLower(k)] = true
	}
	return m
}()

// SkipDir 判断目录名是否属于「默认不看」的那批。
func SkipDir(name string) bool {
	return skipDirLower[strings.ToLower(name)]
}

// secretFileNames 私钥类文件名：模型永远不需要它们，一旦读进上下文就等于外泄
// （并且会被写进远端日志）。写同理——把密钥换成别的内容同样是事故。
var secretFileNames = map[string]bool{
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	".git-credentials": true, ".netrc": true, "credentials.json": true,
}

var secretFileExts = []string{".pem", ".key", ".pfx", ".p12", ".ppk", ".keystore"}

// SecretFile 判断一个文件名是否属于凭据/私钥类。
//
// 判据是文件名而不是内容：内容判据（-----BEGIN ... PRIVATE KEY-----）要读完才知道，
// 而读完就已经进了进程内存与日志；按名字拦是唯一能在读之前就生效的做法。
func SecretFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if secretFileNames[base] {
		return true
	}
	ext := strings.ToLower(filepath.Ext(base))
	for _, e := range secretFileExts {
		if ext == e {
			return true
		}
	}
	return false
}

// ResolveRead 把用户给的路径归一化并校验在可读范围内，返回绝对路径。
//
// cwd 是本次对话绑定的工作目录（可为空），既用于补全相对路径，也作为额外的可读根：
// 它是用户在界面上亲手选的，语义比「加了个快捷项」更强，所以不要求它同时出现在
// 快捷项里——否则会出现「选了目录却读不了里面任何文件」这种最让人费解的状态。
func ResolveRead(database *db.Database, raw, cwd string, mustBeFile bool) (string, error) {
	abs, err := clean(raw, cwd)
	if err != nil {
		return "", err
	}
	if mustBeFile {
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return "", fmt.Errorf("%s 是目录，不是文件；要列目录内容请用 dir_list", abs)
		}
	}
	roots := ReadRoots(database)
	if r := strings.TrimSpace(cwd); r != "" {
		roots = append(roots, r)
	}
	for _, root := range roots {
		if within(abs, root) {
			return abs, nil
		}
	}
	// 报错里必须列出可用根目录：模型据此就能自己改对，而不是反复猜路径。
	// 后半句给人看——授权目录的入口是「加个快捷项」，不另造一套路径授权设置页。
	return "", fmt.Errorf("路径 %s 不在可读范围内。AI 只能读取这些目录及其子目录：%s。需要读其他位置时，请用户在 QuickDock 里把该目录添加为快捷项，或直接把内容复制给 AI",
		abs, strings.Join(roots, "、"))
}

// ResolveWrite 解析并校验一个可写的路径。raw 为空时回落到对话工作目录。
func ResolveWrite(database *db.Database, raw, cwd string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = cwd
	}
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("不知道要操作哪个目录：当前对话没有绑定工作目录，" +
			"请让用户先在对话里选一个文件夹，或把项目添加为快捷项")
	}
	abs, err := clean(raw, cwd)
	if err != nil {
		return "", err
	}
	roots := WriteRoots(database, cwd)
	for _, root := range roots {
		if within(abs, root) {
			return abs, nil
		}
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("当前没有可写的目录。写操作只允许用户显式授权的位置："+
			"在对话里选一个工作目录，或把项目目录添加为快捷项。请求的路径是 %s", abs)
	}
	return "", fmt.Errorf("路径 %s 不在可写范围内。可写目录只有：%s。"+
		"需要改其他位置时，请让用户把它添加为快捷项或在对话里设为工作目录", abs, strings.Join(roots, "、"))
}

// clean 展开 ~、补全相对路径、转绝对、清理并解引用符号链接。
//
// 解引用必须在判白名单之前：反过来，家目录里一个指向系统目录的软链就能绕过整个白名单。
func clean(raw, cwd string) (string, error) {
	p := strings.Trim(strings.TrimSpace(raw), `"`) // 从资源管理器复制来的路径常带引号
	if p == "" {
		return "", errors.New("路径不能为空")
	}
	home, _ := os.UserHomeDir()
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`)) {
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), string(os.PathSeparator)))
	} else if !filepath.IsAbs(p) {
		if wd := strings.TrimSpace(cwd); wd != "" {
			p = filepath.Join(wd, p)
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("无法解析路径 %q：%w", raw, err)
	}
	abs = filepath.Clean(abs)
	// 解引用到真实路径，再判白名单——顺序反过来，家目录里一个指向系统目录的软链
	// 就能绕过整个白名单。
	return expandExisting(abs), nil
}

// expandExisting 把路径里已存在的那一段解引用成真实路径，再把还不存在的尾段拼回去。
//
// 为什么不能只对整条路径 EvalSymlinks：目标文件常常还不存在（新建文件、写入前），
// 一次解引用失败就退回原字符串，而白名单里的根目录都是解引用过的——
// Windows 短名（8.3 形式，形如 `MR12C5~1.LIA` 与展开后的 `Mr.liang`）于是永远对不上，
// 「写一个还不存在的文件」被判成越界。这一条是踩出来的，不是预防性代码。
func expandExisting(abs string) string {
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	var tail []string
	p := abs
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return abs // 一路到根都不存在，原样返回（交给调用方报错）
		}
		tail = append([]string{filepath.Base(p)}, tail...)
		p = parent
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{real}, tail...)...)
		}
	}
}

// ReadRoots 返回当前允许读取的根目录：家目录、QuickDock 数据目录，
// 以及用户在快捷项里登记过的目录与工作目录。
//
// 为什么要带上快捷项的目录：AI 需要读的几乎都是「用户正在做的项目」，
// 而这些路径本来就以快捷项/工作目录的形式存在库里——直接复用，等于让用户
// 用已有的界面动作（加个快捷项）来授权目录，不必再造一套「路径授权」设置页。
func ReadRoots(database *db.Database) []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	if dd := platform.DefaultDataDir(); dd != "" {
		roots = append(roots, dd)
	}
	roots = append(roots, authorizedDirs(database)...)
	out := normalizeRoots(roots, false)
	sort.Strings(out)
	return out
}

// WriteRoots 返回允许写的根目录：对话工作目录 + 快捷项登记的目录/工作目录。
//
// 与 ReadRoots 刻意不同（见包注释）：家目录与数据目录不在其中。
func WriteRoots(database *db.Database, cwd string) []string {
	var roots []string
	if c := strings.TrimSpace(cwd); c != "" {
		roots = append(roots, c)
	}
	roots = append(roots, authorizedDirs(database)...)
	return normalizeRoots(roots, true)
}

// authorizedDirs 从快捷项里取出用户显式登记过的目录。
func authorizedDirs(database *db.Database) []string {
	if database == nil {
		return nil
	}
	items, err := database.ListAllItems()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(items)*2)
	for _, it := range items {
		if strings.EqualFold(strings.TrimSpace(it.Type), "dir") && strings.TrimSpace(it.Value) != "" {
			out = append(out, it.Value)
		}
		if wd := strings.TrimSpace(it.WorkingDirectory); wd != "" {
			out = append(out, wd)
		}
	}
	return out
}

// normalizeRoots 去空、去非绝对路径、解引用软链、去重。
//
// deref=true 用于写白名单：写的时候路径一定要落到真实位置，否则「软链指向哪」
// 就成了绕过白名单的后门。
func normalizeRoots(roots []string, deref bool) []string {
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, r := range roots {
		r = filepath.Clean(strings.Trim(strings.TrimSpace(r), `"`))
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		if deref {
			if real, err := filepath.EvalSymlinks(r); err == nil {
				r = real
			}
		}
		key := r
		if runtime.GOOS == "windows" {
			key = strings.ToLower(r)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// within 判断 p 是否等于 root 或在其子目录下。
//
// 不能用 strings.HasPrefix(p, root)：那会把 C:\Users\ab 当成 C:\Users\a 的子目录，
// 也会把 .../proj2 当成 .../proj 的子目录。这里用 filepath.Rel 做目录语义比较，
// Windows 下先小写化（路径大小写不敏感，Rel 是字面比较）。
func within(p, root string) bool {
	if root == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
