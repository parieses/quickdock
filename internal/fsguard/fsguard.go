// Package fsguard 是「AI 能读写哪些本地路径」这条策略的唯一实现。
//
// 为什么单独成包：读白名单与写白名单分属两个工具集——读文件是宿主能力（file_read 对外
// 也开放），改文件/跑命令是 agent 能力（只在 services/ai/tools 里）。但它们判的是同一件事
// 「用户有没有授权这个位置」，各写一份的结果是两边迟早给出不同的根目录，于是出现
// 「能读却改不了同一个文件」这种最难向用户解释的状态。
//
// 两套白名单的差别是刻意设计的，不要为了「一致」把它们合并：
//
//   - 读（ReadRoots）：家目录 + 数据目录 + 快捷项里登记的目录/工作目录 + AI 空间与
//     会话绑定过的目录（同样是在界面上亲手选的）。用户让 AI 看一眼某个文件是很自然的
//     诉求，拦掉会逼他去复制粘贴。但这只是「这次对话没声明范围」时的兜底：
//     绑定了用户选的项目目录时，实际判据换成 ResolveReadScoped（只许该目录内），
//     否则模型会顺着报错文本里的路径去读另一个项目的源码。
//   - 写（WriteRoots）：只有用户显式登记过的位置（快捷项目录、工作目录、AI 空间/会话
//     目录），再加本次对话绑定的工作目录；家目录被故意排除。否则一句话就能改掉
//     ~/Documents 里的任何东西，而用户从未做过「允许写这里」的动作。
//     这层是「没绑定工作目录」时的兜底：一旦绑了，ResolveWrite 只允许写在该目录内，
//     与读取的默认边界一致。
package fsguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

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
	if err := checkFileTarget(abs, mustBeFile); err != nil {
		return "", err
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

// ResolveReadScoped 是「本次对话绑定了用户选的项目目录」时的读校验：只允许该目录及其子目录。
//
// 与 ResolveRead 的唯一差别在有 scope 时——不再回退到全局读白名单（家目录、别的快捷项）。
// 起因是一个真实越界：用户把本地项目的报错贴进来，报错文本里带着另一个项目的绝对路径，
// 模型顺着那个路径就把源码读了。从白名单看它没犯规（都在家目录/快捷项里），
// 但用户的心智是「这次对话是这个项目」，读到别处就是越权。
//
// scope 为空时完全退回 ResolveRead：没声明范围的对话仍然要能读用户随手提到的一个文件，
// 否则每个任务都得先选目录才能开口。注意「没有项目目录」与「cwd 为空」不是一回事——
// 无目录任务会被分配 ai_tasks/<id>，那种空壳由调用方判掉、不当作 scope 传进来。
//
// 为什么不给 ResolveRead 加一个 bool：那个布尔会散布到 8 个调用点，而 @ 引用、
// 前端预览、规则文件这三处必须保持宽松（它们不是模型自主发起的读取），
// 统一改签名等于让每个调用点都得回答一个它不关心的问题。
func ResolveReadScoped(database *db.Database, raw, scope string, mustBeFile bool) (string, error) {
	return ResolveReadScopedExtra(database, raw, scope, nil, mustBeFile)
}

// ResolveReadScopedExtra 在 Scoped 的基础上额外允许 extra 里的目录。
//
// extra 来自「用户当场点了允许」的那一次读取：模型想读当前目录之外的文件时，
// 宿主先弹一张确认卡，用户同意后把该文件的目录塞进参数（键 _allow）。
// 只对这一次调用有效——用户同意「看一眼这个报错文件」不等于永久开放那个项目，
// 要长期读就应该把目录设为工作目录，那是界面上一个明确动作。
func ResolveReadScopedExtra(database *db.Database, raw, scope string, extra []string, mustBeFile bool) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return ResolveRead(database, raw, "", mustBeFile)
	}
	// 范围目录本身也要解引用：Windows 短名（8.3 形式）与真实路径不一致时，
	// 直接拿原始字符串做 within 比较会把目录内的文件判成越界。
	root, err := clean(scope, "")
	if err != nil {
		return "", fmt.Errorf("工作目录不可用（%s）：%w", scope, err)
	}
	abs, err := clean(raw, root)
	if err != nil {
		return "", err
	}
	if err := checkFileTarget(abs, mustBeFile); err != nil {
		return "", err
	}
	if within(abs, root) {
		return abs, nil
	}
	for _, e := range extra {
		if strings.TrimSpace(e) == "" {
			continue
		}
		if r, cerr := clean(e, ""); cerr == nil && within(abs, r) {
			return abs, nil
		}
	}
	// 报错要同时服务两方：模型据此知道「换个地方/换个办法」，用户据此知道
	// 「要读别处得先切工作目录」。只说「无权限」会让模型换个写法再试一次。
	return "", fmt.Errorf("路径 %s 不在本次对话的工作目录 %s 之内，已拒绝读取。"+
		"AI 只在这个目录里干活；要读别的位置，请用户先把那个目录设为工作目录，或把内容直接发给 AI", abs, root)
}

// Outside 判断一个路径是否在 scope 之外，用于「先问用户再读」那条路。
//
// 与 ResolveReadScoped 的分工：这个只做判断、不报错，供宿主在调用工具之前
// 决定要不要弹确认卡；真正的放行仍由 ResolveReadScopedExtra 完成。
// scope 为空表示这次对话没声明范围（未绑定目录），一律不算越界。
func Outside(raw, scope string) (abs string, outside bool) {
	scope = strings.TrimSpace(scope)
	if scope == "" || strings.TrimSpace(raw) == "" {
		return "", false
	}
	root, err := clean(scope, "")
	if err != nil {
		return "", false
	}
	abs, err = clean(raw, root)
	if err != nil {
		return "", false
	}
	return abs, !within(abs, root)
}

// checkFileTarget 对「必须是个文件」的调用做个早判。
//
// 抽出来是因为两条读路径（宽松 / 限定工作目录）都要判，而文案分叉过一次就会
// 让模型对同一个错误给出两种反应。
func checkFileTarget(abs string, mustBeFile bool) error {
	if !mustBeFile {
		return nil
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return fmt.Errorf("%s 是目录，不是文件；要列目录内容请用 dir_list", abs)
	}
	return nil
}

// ResolveWrite 解析并校验一个可写的路径。raw 为空时回落到对话工作目录。
//
// 有工作目录时只允许写在其中；cwd 为空才退回 WriteRoots 那套「快捷项授权目录」。
// 写与读用同一套边界：只收窄读会让模型改得动一个它读不到的项目。
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
	// 绑定工作目录时写只允许在其中（与读取同一套「默认只操作当前目录」的语义）。
	// 不加这一层，写白名单里的「所有快捷项目录」就会让模型在一个对话里改到另一个项目去——
	// 读收窄了、写没收，等于边界只挡住看、挡不住改。
	if wd := strings.TrimSpace(cwd); wd != "" {
		root, rerr := clean(wd, "")
		if rerr != nil {
			return "", fmt.Errorf("工作目录不可用（%s）：%w", wd, rerr)
		}
		if within(abs, root) {
			return abs, nil
		}
		return "", fmt.Errorf("路径 %s 不在本次对话的工作目录 %s 之内，不能写入。"+
			"要改其他项目，请先把那个目录设为工作目录——AI 不会跨目录改文件", abs, root)
	}
	roots := WriteRoots(database, "")
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

// rootsCacheTTL 授权目录的缓存时长。
//
// 为什么需要缓存：ReadRoots / WriteRoots 在每一次工具调用前都会被问一遍
// （file_read、file_edit、dir_list、file_grep、cmd_run …全部要过闸）。原来每次都
// 查一次 items 全表（含一次大 UPDATE）加两次 ai_spaces/ai_conversations 的 DISTINCT，
// 20 轮工具调用就是 20 次全表读 + 20 次写事务，与流式落库抢写锁 ——
// 用户看到的是「回答中途卡一下」。
//
// 为什么不做成「一次 run 内快照」：fsguard 是包级函数，拿不到 ctx；而带 ctx 的
// 那套签名要改遍所有工具。TTL 足够覆盖同一次 run 的连发调用，代价只剩一个极短窗口。
//
// 3 秒是权衡：用户「加个快捷项 → 立刻让 AI 读那个目录」中间几乎必然超过 3 秒
// （要先切窗口、打字），而单次 run 里连续调工具通常远小于 3 秒。
const rootsCacheTTL = 3 * time.Second

var (
	rootsMu    sync.Mutex
	rootsOwner *db.Database
	rootsDirs  []string
	rootsAt    time.Time
)

// InvalidateRoots 丢掉授权目录缓存。条目/空间/会话目录发生写入时调用，
// 让下一次工具调用的判断立刻反映新状态（否则最多滞后 rootsCacheTTL）。
func InvalidateRoots() {
	rootsMu.Lock()
	rootsDirs = nil
	rootsAt = time.Time{}
	rootsMu.Unlock()
}

// authorizedDirs 从快捷项里取出用户显式登记过的目录（带短时缓存）。
func authorizedDirs(database *db.Database) []string {
	if database == nil {
		return nil
	}
	rootsMu.Lock()
	// 用时间戳而不是「切片非空」判断命中：目录可能真的是空的（新库、没加任何快捷项），
	// 那时缓存结果就是 nil，用非空判断会每次重新查库。
	if rootsOwner == database && !rootsAt.IsZero() && time.Since(rootsAt) < rootsCacheTTL {
		out := rootsDirs
		rootsMu.Unlock()
		return out
	}
	rootsMu.Unlock()

	// 查库在锁外做：ListAllItems 自己会拿 db 的读锁，若把网络/IO 也塞进 rootsMu，
	// 并发的工具调用会在这里串行排队（原来就是这样，只是没人注意）。
	items, err := database.ItemAuthDirs()
	if err != nil {
		return nil
	}
	// AI 空间 / 会话绑定的工作目录：用户是在新建任务时亲手选的目录，
	// 授权强度与「加个快捷项」相同，只是没在 items 表里留痕。
	//
	// 不加这一段会留下一个自相矛盾的状态——AI 能把文件写进空间目录
	//（写权限会把会话 cwd 拼进白名单），却预览不了自己刚写的文件
	//（预览走的是这个函数，此前只看快捷项）。用户看到的就是「没有权限」。
	out := append(items, database.AIWorkDirs()...)

	rootsMu.Lock()
	rootsOwner, rootsDirs, rootsAt = database, out, time.Now()
	rootsMu.Unlock()
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
