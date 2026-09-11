package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// ---- 文件系统权限与路径 scope ----
//
// permissions.filesystem 支持两种写法（向后兼容旧清单）：
//
//	"filesystem": true                                  仅允许弹文件/保存对话框
//	"filesystem": { "read": [...], "write": [...] }     对话框 + 白名单内的读写
//
// ⚠️ `true` 的语义**不包含**读写。现有插件里的 filesystem:true 一律是「能弹框」，
// 沿用该语义可保证升级后零权限扩张；要读写必须显式声明 scope 对象。
//
// scope 条目支持：
//   - `~` 展开为用户主目录
//   - `**` 跨任意多级路径段
//   - `*` / `?` / `[...]` 单段内通配
//   - 不含通配符的条目 = 「该路径及其整个子树」（如 ~/Documents 等价于 ~/Documents/**）
//
// 匹配前**必须先把目标路径解析为绝对真实路径**（见 ResolveRealPath），
// 否则白名单内的 ~/Documents/link（→ /etc）能让读写落到任意位置。

// 文件系统动作（scope 分组名）
const (
	FSActionRead  = "read"
	FSActionWrite = "write"
)

// FilesystemPerm 文件系统权限
type FilesystemPerm struct {
	Dialog bool     // host.dialog.open / host.dialog.save
	Read   []string // host.fs.read / host.fs.list / host.fs.stat / host.fs.exists
	Write  []string // host.fs.write / host.fs.mkdir / host.fs.remove；host.fs.move 的 from 与 to 两侧都要命中
}

// UnmarshalJSON 兼容 bool 与对象两种写法。
// 对象形式隐含 Dialog——能浏览目录的插件本就该能用文件对话框，且对话框返回的
// 路径仍受 scope 约束，不构成额外越权。
func (f *FilesystemPerm) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "", "null", "false":
		*f = FilesystemPerm{}
		return nil
	case "true":
		*f = FilesystemPerm{Dialog: true}
		return nil
	}

	var raw struct {
		Read  []string `json:"read"`
		Write []string `json:"write"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf(`permissions.filesystem 必须是 true/false 或 {"read":[...],"write":[...]}: %w`, err)
	}
	*f = FilesystemPerm{Dialog: true, Read: raw.Read, Write: raw.Write}
	return nil
}

// MarshalJSON 与输入对称：仅对话框权限仍写回 true，
// 使老插件写库 / 进市场索引的形状保持不变。
func (f FilesystemPerm) MarshalJSON() ([]byte, error) {
	if len(f.Read) == 0 && len(f.Write) == 0 {
		return json.Marshal(f.Dialog)
	}
	obj := make(map[string]interface{}, 2)
	if len(f.Read) > 0 {
		obj["read"] = f.Read
	}
	if len(f.Write) > 0 {
		obj["write"] = f.Write
	}
	return json.Marshal(obj)
}

// Granted 是否声明了任何文件系统能力
func (f FilesystemPerm) Granted() bool {
	return f.Dialog || len(f.Read) > 0 || len(f.Write) > 0
}

// Allows 判断已解析的绝对真实路径是否落在 action 对应的白名单内。
// 未知 action 一律拒绝（fail-closed），避免将来新增动作时忘了配白名单却默认放行。
func (f FilesystemPerm) Allows(action, realPath string) bool {
	var patterns []string
	switch action {
	case FSActionRead:
		patterns = f.Read
	case FSActionWrite:
		patterns = f.Write
	default:
		return false
	}
	for _, p := range patterns {
		if matchScope(p, realPath) {
			return true
		}
	}
	return false
}

// Validate 清单加载时的形式校验：scope 条目必须能展开为绝对路径。
// 写错的条目只拒绝该字段，不影响插件加载（fail-closed：非法条目永不匹配）。
func (f FilesystemPerm) Validate() error {
	groups := []struct {
		action string
		items  []string
	}{{FSActionRead, f.Read}, {FSActionWrite, f.Write}}

	for _, g := range groups {
		for _, item := range g.items {
			if strings.TrimSpace(item) == "" {
				return fmt.Errorf("%w: permissions.filesystem.%s 存在空条目", ErrInvalidManifest, g.action)
			}
			expanded, err := expandHome(item)
			if err != nil {
				return fmt.Errorf("%w: permissions.filesystem.%s 条目 %q 无法展开: %v", ErrInvalidManifest, g.action, item, err)
			}
			if !looksAbsolute(expanded) {
				return fmt.Errorf("%w: permissions.filesystem.%s 条目 %q 必须是绝对路径或以 ~ 开头", ErrInvalidManifest, g.action, item)
			}
		}
	}
	return nil
}

// ---- 路径解析 ----

// ResolveRealPath 把插件给出的路径规范化为「可安全比对与 I/O 的绝对真实路径」：
//  1. 展开开头的 ~ 为用户主目录
//  2. 转绝对路径并 Clean（消除 . 与 ..）
//  3. 展开路径上的所有链接（符号链接 + Windows 目录联接）
//
// 第 3 步是 scope 校验的前提：不做的话，白名单内的 ~/Documents/link（→ /etc）
// 能把读写引到任意位置——文件系统对链接是透明穿透的，只比字符串等于没校验。
//
// 目标是待创建的新文件时，解析其父目录后拼回自身；悬空链接会解析到它指向的
// 真实位置（该位置可能尚不存在），因此「写穿悬空链接」也落在正确的 scope 判定下。
func ResolveRealPath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return "", fmt.Errorf("路径不能为空")
	}
	expanded, err := expandHome(raw)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("路径 %q 无效: %w", p, err)
	}
	return resolveLinks(filepath.Clean(abs))
}

// maxLinkHops 链接展开步数上限，防自引用链接把解析拖成死循环
const maxLinkHops = 40

// resolveLinks 逐级展开路径上的链接，得到真实位置。
//
// 为什么不直接用 filepath.EvalSymlinks：**Go 在 Windows 上只把
// IO_REPARSE_TAG_SYMLINK 标记为符号链接，目录联接（junction，
// IO_REPARSE_TAG_MOUNT_POINT）会被它当成普通目录而不解析**，但文件系统
// 对 junction 是透明穿透的——只靠 EvalSymlinks 就等于给白名单留了个洞，
// 而 junction 普通用户就能建（用户目录里还有系统自建的）。
// os.Readlink 在 Windows 上两种 reparse tag 都支持，所以自己走一遍。
//
// 算法：从卷根开始逐段推进，维护「已确认存在的真实前缀」。
//   - 某段不存在 → 其后所有段必然也不存在（对「待创建的新文件」是正常情形），
//     直接拼回并结束
//   - 某段是链接 → 把路径换成「链接目标 + 剩余段」从头再走
//   - 全程无链接 → 当前路径即真实位置
//
// 这样悬空链接也会解析到它指向的真实位置，写操作的 scope 判定因此落在正确对象上。
func resolveLinks(abs string) (string, error) {
	for hop := 0; hop < maxLinkHops; hop++ {
		vol := filepath.VolumeName(abs)
		cur := vol + string(filepath.Separator)
		segs := splitSegmentsOf(abs[len(vol):])

		linked := false
		for i, seg := range segs {
			cur = filepath.Join(cur, seg)
			if _, err := os.Lstat(cur); err != nil {
				if !isMissing(err) {
					return "", fmt.Errorf("访问路径 %q 失败: %w", cur, err)
				}
				return joinTail(cur, segs[i+1:]), nil
			}
			target, err := os.Readlink(cur)
			if err != nil {
				continue // 不是链接（Windows 上非 reparse point 同样报错）
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			abs = joinTail(target, segs[i+1:])
			linked = true
			break
		}
		if !linked {
			return cur, nil
		}
	}
	return "", fmt.Errorf("路径 %q 的链接嵌套超过 %d 层（疑似自引用）", abs, maxLinkHops)
}

// splitSegmentsOf 把路径切成段并丢弃空段。
// 只按当前平台的分隔符切：Unix 下 \ 可以是文件名字面量，不能当分隔符。
func splitSegmentsOf(p string) []string {
	var out []string
	for _, s := range strings.Split(p, string(filepath.Separator)) {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// joinTail 把 base 与后续路径段拼起来
func joinTail(base string, rest []string) string {
	if len(rest) == 0 {
		return base
	}
	return filepath.Join(append([]string{base}, rest...)...)
}

// isMissing 判断错误是否表示「路径不存在」。
// 不能只用 errors.Is(err, fs.ErrNotExist)：Windows 上中间目录缺失返回
// ERROR_PATH_NOT_FOUND(3)，该判定不覆盖它，会把「新建文件的正常路径」
// 误判成硬失败。
func isMissing(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == 3 // ERROR_PATH_NOT_FOUND
}

// expandHome 展开开头的 ~（仅支持 ~ 与 ~/、~\ 前缀）
func expandHome(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户主目录（~ 展开失败）: %w", err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// looksAbsolute 判断是否可视为绝对路径。
//
// 除当前平台的 IsAbs 外还接受两类写法，避免跨平台插件在「错误的」操作系统上
// 直接加载失败（匹配不到真实路径时自然 fail-closed，不会因此放行）：
//   - Windows 盘符形式：声明 platforms:["windows"] 的插件在 mac 上不应校验失败
//   - 以 / 开头：`platforms:["darwin","linux"]` 的插件在 Windows 上同理
func looksAbsolute(p string) bool {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && isASCIIAlpha(p[0]) && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

func isASCIIAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ---- 通配匹配 ----

// matchScope 判断已解析的真实路径是否命中某个 scope 条目
func matchScope(pattern, realPath string) bool {
	patSegs, ok := compilePattern(pattern)
	if !ok {
		return false
	}
	return matchSegments(patSegs, splitPath(realPath))
}

// compilePattern 把 scope 条目编译为段序列：
// 展开 ~、Clean、把无通配符的条目补成 <path>/**（授予整个子树），
// 并解析静态前缀中的符号链接——否则 scope 自身是链接时会与真实路径对不上。
func compilePattern(pattern string) ([]string, bool) {
	p, err := expandHome(pattern)
	if err != nil || p == "" || !looksAbsolute(p) {
		return nil, false
	}
	p = filepath.Clean(p)
	if !strings.ContainsAny(p, "*?[") {
		p = strings.TrimRight(p, `/\`) + string(filepath.Separator) + "**"
	}
	// 静态前缀（第一个通配段之前）展开链接：否则 scope 自身是链接（或经由
	// junction 抵达）时会与已解析的真实路径对不上，白名单形同虚设。
	if idx := strings.IndexAny(p, "*?["); idx > 0 {
		if cut := strings.LastIndexAny(p[:idx], `/\`); cut > 0 {
			if real, lerr := resolveLinks(p[:cut]); lerr == nil {
				p = filepath.Join(real, p[cut:])
			}
		}
	}
	return splitPath(p), true
}

// splitPath 按路径分隔符切段并丢弃空段（Windows 上统一把 \ 视为分隔符）。
// 注意：仅在 Windows 上做 \ → / 转换——Unix 下 \ 可以是文件名字面量。
func splitPath(p string) []string {
	if runtime.GOOS == "windows" {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// matchSegments 段序列递归匹配，** 匹配零到多段
func matchSegments(pattern, target []string) bool {
	if len(pattern) == 0 {
		return len(target) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(target); i++ {
			if matchSegments(pattern[1:], target[i:]) {
				return true
			}
		}
		return false
	}
	if len(target) == 0 {
		return false
	}
	if !segmentMatch(pattern[0], target[0]) {
		return false
	}
	return matchSegments(pattern[1:], target[1:])
}

// segmentMatch 单段通配匹配。Windows 文件系统大小写不敏感，比对前统一折叠小写。
func segmentMatch(pattern, seg string) bool {
	if runtime.GOOS == "windows" {
		pattern = strings.ToLower(pattern)
		seg = strings.ToLower(seg)
	}
	ok, err := path.Match(pattern, seg)
	return err == nil && ok
}
