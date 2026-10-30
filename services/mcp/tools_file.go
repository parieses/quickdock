package mcp

// 文件域工具：检索（Everything）、读取内容、按正则在目录里搜。
//
// 为什么从 env 域拆出来单独成域：file_search 原先是挂在环境管理下的（Everything 是受管运行时），
// 但「文件」是模型最常打交道的对象，和运行时状态混在一组既不好找，也没法按域整体开关。
//
// 「能读文件」是这个域里最敏感的一条能力，因此四道闸是同时上的，缺一不可：
//   - 路径白名单：只能读家目录、数据目录、以及用户自己在快捷项里登记过的目录/工作目录；
//   - 符号链接先解引用再判白名单（否则家目录里一个指向 C:\Windows 的软链就绕过了）；
//   - 私钥类文件直接拒（id_rsa / *.pem / *.key 等）；
//   - 单次调用有行数、字节与时间预算，结果给模型的是「够判断」的量，不是整份文件。
//
// 本文件只放读工具（file_search / file_read / file_grep），且都是对外 MCP 工具：
// 「让 AI 看一个文件」是用户对这类软件的正常期待，读本身不改任何东西。
//
// 写入与执行不在这个包里：改文件、跑命令、列目录、看 git 差异都在
// services/ai/tools（agent 专用，不进 MCP）。路径策略则由 internal/fsguard 统一提供，
// 读白名单（含家目录）与写白名单（不含家目录）是两套，见那边的包注释。

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	envengine "quickdock/internal/env"
	"quickdock/internal/fsguard"
	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
)

const (
	readDefaultLines = 400     // file_read 默认返回行数
	readMaxLines     = 2000    // file_read 单次上限
	readLineMaxRune  = 2000    // 单行超过此长度截断（压缩过的 JS/一行 JSON 会把上下文吃光）
	readSkipBytes    = 8 << 20 // 跳过 offset 时最多扫描的字节数，防止 offset 巨大时扫穿整个大文件
	readSniffBytes   = 8192    // 二进制 / UTF-16 嗅探窗口

	grepDefaultLimit = 50              // file_grep 默认命中上限
	grepMaxLimit     = 200             // file_grep 命中上限
	grepMaxFileBytes = 1 << 20         // 单文件超过 1MB 不扫（日志与打包产物为主）
	grepMaxFiles     = 4000            // 单次最多打开的文件数
	grepMaxDepth     = 24              // 目录深度上限
	grepBudget       = 6 * time.Second // 总体时间预算：MCP 请求是同步的，不能让客户端等到超时
)

func registerFileTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	// ---- Everything 检索（只读）----
	// 不做「启动期绝对可用性门禁」：handler 内动态校验 EverythingRuntime().IsAvailable()，
	// 这样即使用户在 QuickDock 启动后才装好/启动 Everything，也无需重启 MCP 即可用，
	// 且未就绪时给出明确可执行的错误，而不是默默返回一个永远为空的工具。
	r.add(func(args map[string]any) (any, error) {
		q := strings.TrimSpace(mcpsrv.Arg(args, "query"))
		if q == "" {
			return nil, errors.New("缺少参数 query（检索关键词）")
		}
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		ev := s.App.Env.EverythingRuntime()
		if ev == nil || !ev.IsAvailable() {
			return nil, errors.New("文件搜索不可用：请确认已安装 Everything、es.exe 在位且 Everything 正在运行")
		}
		limit := mcpsrv.ArgInt(args, "limit", 30)
		if limit > 200 {
			limit = 200
		}
		sortBy := mcpsrv.Arg(args, "sort")
		results, err := ev.Search(context.Background(), q, limit, sortBy)
		if err != nil {
			return nil, err
		}
		if results == nil {
			results = []envengine.FileSearchResult{}
		}
		files := make([]map[string]any, 0, len(results))
		for _, res := range results {
			files = append(files, map[string]any{
				"path":     res.Path,
				"name":     res.Name,
				"dir":      res.Dir,
				"modified": res.Modified,
			})
		}
		return map[string]any{"query": q, "count": len(files), "files": files}, nil
	}, mcpsrv.LevelRead, "file_search", "通过 Everything (es.exe) 在本地全盘检索文件名，返回结构化文件列表（path/name/dir/modified）。只按名字匹配、不读内容——要读内容用 file_read，要在目录里按内容找用 file_grep。仅在 Everything 已安装、es.exe 在位且正在运行时可用；支持 Everything 检索语法（空格为与、| 为或、*.ext 按扩展名）", []string{"query"},
		map[string]any{
			"query": str("检索关键词，支持 Everything 语法（空格为与、| 为或、*.ext 按扩展名等）"),
			"limit": intp("返回条数，默认 30，最大 200"),
			"sort":  str("排序字段：name/path/size/date_modified/date_created，默认 name"),
		})

	r.add(func(args map[string]any) (any, error) {
		return readFileText(s, args)
	}, mcpsrv.LevelRead, "file_read", "读取一个文本文件的内容。**返回的 content 每行前面是行号**（形如 `  42→代码`），行号不是文件内容，"+
		"引用/修改时用这个行号才和后续工具对得上。path 可以传绝对路径，也可以传相对当前工作目录的路径。"+
		"只能读用户已授权的目录：家目录、数据目录、以及快捷项里登记过的目录/工作目录；路径不在范围内会报错并列出可用目录。"+
		"私钥类文件、二进制文件（含 NUL 字节）与 UTF-16 编码文件会被拒绝。大文件请配合 offset/limit 分段读", []string{"path"},
		map[string]any{
			"path":   str("文件路径：绝对路径，或相对当前工作目录的路径（也支持 ~/ 开头）"),
			"offset": intp("从第几行开始，1 起，默认 1"),
			"limit":  intp("最多返回多少行，默认 400，最大 2000"),
		})

	r.add(func(args map[string]any) (any, error) {
		res, err := grepFiles(s, mcpsrv.PathArg(args, "path"), mcpsrv.Arg(args, "pattern"),
			mcpsrv.Arg(args, "glob"), mcpsrv.ArgBool(args, "ignoreCase"), mcpsrv.ArgInt(args, "limit", grepDefaultLimit),
			mcpsrv.Cwd(args))
		if err != nil {
			return nil, err
		}
		return res, nil
	}, mcpsrv.LevelRead, "file_grep", "在某个目录（白名单内的）里按正则逐行搜索文本内容，返回 path:line 与命中行。自动跳过 .git/node_modules/dist 等目录、跳过二进制与超过 1MB 的文件，单次有命中数/文件数/时间预算（触发上限时会说明 truncated 原因）。用来在项目里定位「某个函数在哪定义」「哪份配置引用了这个 key」", []string{"path", "pattern"},
		map[string]any{
			"path":       str("要搜索的目录（或单个文件）：绝对路径，或相对当前工作目录的路径"),
			"pattern":    str("正则表达式（Go 语法）。字面量特殊字符记得转义，如 \\bfunc main\\b"),
			"glob":       str("可选。只搜匹配该通配符的文件，如 *.go / *.ts / package.json"),
			"ignoreCase": mcpsrv.Bool("可选。忽略大小写，默认 false"),
			"limit":      intp("最多返回多少条命中，默认 50，最大 200"),
		})
}

// -------- file_read --------

// dbOf 取库句柄；服务未初始化时返回 nil（fsguard 会按「没有任何快捷项」处理，
// 于是只剩家目录与数据目录可读，而不是 panic）。
func dbOf(s *MCPService) *db.Database {
	if s == nil || s.App == nil {
		return nil
	}
	return s.App.DB
}

func readFileText(s *MCPService, args map[string]any) (any, error) {
	rawPath := mcpsrv.Arg(args, "path")
	offset := mcpsrv.ArgInt(args, "offset", 1)
	limit := mcpsrv.ArgInt(args, "limit", readDefaultLines)
	if strings.TrimSpace(rawPath) == "" {
		return nil, errors.New("缺少参数 path（文件路径）")
	}
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = readDefaultLines
	}
	if limit > readMaxLines {
		limit = readMaxLines
	}
	path, err := fsguard.ResolveRead(dbOf(s), mcpsrv.PathArg(args, "path"), mcpsrv.Cwd(args), true)
	if err != nil {
		return nil, err
	}
	if err := guardReadableFile(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 只说「打开失败」会让模型换个写法再读一遍同一个不存在的路径
			// （实测：它甚至会用 run_command echo 出一个空文件再回来读）。
			// 这里必须区分「路径写错」与「本来就要新建」两种真实意图。
			return nil, fmt.Errorf("文件不存在：%s。若是路径写错了，先用 dir_list 看目录或 file_search 找文件；"+
				"若本来就要新建这个文件，直接用 file_write 一步创建（会自动建父目录）", path)
		}
		return nil, fmt.Errorf("打开文件失败：%w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取文件信息失败：%w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s 是目录：读目录请用 file_grep，或先用 file_search 找到具体文件", path)
	}

	// 嗅探编码/二进制：这两类读出来是一屏乱码，既浪费上下文又会让模型据此瞎猜。
	head := make([]byte, readSniffBytes)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if err := sniffTextFile(path, head); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("定位文件失败：%w", err)
	}

	lines, _, hitEOF, truncatedLine, err := scanLines(f, offset, limit)
	if err != nil {
		return nil, err
	}
	// 读文件是审计刚需（「AI 看过什么」），但这类调用频率高，落库会像 -status 轮询一样刷表，
	// 故只写一行应用日志：需要追溯时用 log_read 查得到。
	logger.I("[mcp] file_read %s 行 %d-%d（%d 行）", path, offset, offset+len(lines)-1, len(lines))

	// 空结果必须给原因：content 为空且什么都不解释，模型只能猜（是空文件？offset 太大？
	// 路径错了？）——这正是这套工具最不该出现的失败形状。
	note := truncateNote(len(lines), limit, hitEOF, truncatedLine)
	if len(lines) == 0 && note == "" {
		note = "该区间没有内容：文件为空，或 offset 超出了文件末尾；请用更小的 offset 重试"
	}

	return map[string]any{
		"path":      path,
		"sizeBytes": info.Size(),
		"from":      offset,
		"to":        offset + len(lines) - 1,
		"lines":     len(lines),
		"content":   numberLines(lines, offset),
		// truncated 只在「还有内容没返回」时为 true，并说明原因：
		// 模型据此决定要不要继续读，而不是以为已经看到全文。
		"truncated":     !hitEOF || truncatedLine,
		"truncatedNote": note,
	}, nil
}

// numberLines 给每行前面加上行号，形如 `  42→内容`。
//
// 加行号不是为了好看，是为了让「模型说的第 N 行」和文件里真实的第 N 行是同一个数：
// 不带行号时，模型只能靠数换行猜位置，一旦它要在别处引用或精确替换，位置就全错了。
// 分隔符用「→」而不是 tab：tab 在等宽显示里对不齐，而 → 无论是纯文本还是日志里都读得出来。
func numberLines(lines []string, start int) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(lines) * 12)
	for i, ln := range lines {
		fmt.Fprintf(&b, "%5d→%s", start+i, ln)
		if i != len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// scanLines 从文件的第 offset 行起取最多 limit 行。
//
// 用 bufio.Reader 而不是 Scanner：Scanner 默认 64KB 的单行上限，压缩过的前端产物
// 或一行 JSON 会直接把它顶爆并报 "token too long"，而那类文件恰恰是最需要被截断展示的。
func scanLines(f *os.File, offset, limit int) (lines []string, scanned int64, hitEOF, truncatedLine bool, err error) {
	br := bufio.NewReaderSize(f, 64*1024)
	lineNo := 0
	for {
		chunk, readErr := br.ReadString('\n')
		scanned += int64(len(chunk))
		if chunk != "" {
			lineNo++
			if lineNo >= offset && len(lines) < limit {
				text := strings.TrimRight(chunk, "\r\n")
				if utf8.RuneCountInString(text) > readLineMaxRune {
					text = string([]rune(text)[:readLineMaxRune]) + " …（本行过长，已截断）"
					truncatedLine = true
				}
				lines = append(lines, text)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				hitEOF = true
				break
			}
			return nil, scanned, false, false, fmt.Errorf("读取文件失败：%w", readErr)
		}
		// 三种收尾：已取够行数 / 跳过阶段扫太多字节 / 已提前越过目标区间且取够
		if len(lines) >= limit {
			return lines, scanned, false, truncatedLine, nil
		}
		if scanned >= readSkipBytes && len(lines) == 0 {
			return lines, scanned, false, truncatedLine, nil
		}
	}
	return lines, scanned, hitEOF, truncatedLine, nil
}

// truncateNote 说明「为什么没读完」，三种原因对应三种不同的下一步动作：
// 行数上限 → 加 offset 接着读；超长行 → 换 file_grep 按内容找；扫描上限 → offset 太大。
func truncateNote(got, limit int, hitEOF, truncatedLine bool) string {
	switch {
	case truncatedLine:
		return fmt.Sprintf("存在超过 %d 字符的超长行，已按字符截断（行结构与内容不受影响）", readLineMaxRune)
	case !hitEOF && got < limit:
		return fmt.Sprintf("为定位到指定行已扫描 %dMB 仍未到目标区间，offset 很可能超出文件行数", readSkipBytes>>20)
	case !hitEOF:
		return fmt.Sprintf("已达本次 %d 行上限，继续读请把 offset 设为上次返回的 to + 1", limit)
	}
	return ""
}

// sniffTextFile 拒绝二进制与 UTF-16：前者是乱码，后者按字节切行会切出残缺的中文。
func sniffTextFile(path string, head []byte) error {
	if len(head) == 0 {
		return nil
	}
	if bytes.HasPrefix(head, []byte{0xFF, 0xFE}) || bytes.HasPrefix(head, []byte{0xFE, 0xFF}) {
		return fmt.Errorf("%s 是 UTF-16 编码（带 BOM），按行读取会得到乱码；请让用户用编辑器另存为 UTF-8 后再读", path)
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return fmt.Errorf("%s 看起来是二进制文件（含 NUL 字节），不返回内容；文本内容请用 file_grep 或让用户导出后再读", path)
	}
	return nil
}

// guardReadableFile 挡住私钥类文件。
//
// 判据是文件名而不是内容：内容判据（-----BEGIN ... PRIVATE KEY-----）要在读完之后才能判，
// 而读完就已经进了进程内存与日志；按名字拦是唯一能在读之前就生效的做法。
func guardReadableFile(path string) error {
	if fsguard.SecretFile(path) {
		return fmt.Errorf("%s 是凭据/私钥类文件，不向 AI 开放读取；需要时请用户在本地自行查看", filepath.Base(path))
	}
	return nil
}

// -------- file_grep --------

func grepFiles(s *MCPService, rawPath, pattern, glob string, ignoreCase bool, limit int, cwd string) (any, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, errors.New("缺少参数 pattern（正则表达式）")
	}
	if limit <= 0 {
		limit = grepDefaultLimit
	}
	if limit > grepMaxLimit {
		limit = grepMaxLimit
	}
	root, err := fsguard.ResolveRead(dbOf(s), rawPath, cwd, false)
	if err != nil {
		return nil, err
	}
	expr := pattern
	if ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("pattern 不是合法的正则表达式：%w", err)
	}

	deadline := time.Now().Add(grepBudget)
	matches := make([]map[string]any, 0, limit)
	scanned, skippedBinary := 0, 0
	stopped := ""

	visit := func(path string, d os.DirEntry) error {
		if len(matches) >= limit {
			stopped = "已到命中上限（" + fmt.Sprint(limit) + " 条），可加 glob 或换更精确的 pattern 缩小范围"
			return filepath.SkipAll
		}
		if time.Now().After(deadline) {
			stopped = "已达时间预算（" + fmt.Sprint(int(grepBudget.Seconds())) + " 秒），结果不完整；请缩小目录或用更精确的 pattern"
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if glob != "" {
			ok, _ := filepath.Match(glob, filepath.Base(path))
			if !ok {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil || info.Size() > grepMaxFileBytes {
			return nil
		}
		if scanned >= grepMaxFiles {
			stopped = "已到文件数上限（" + fmt.Sprint(grepMaxFiles) + " 个），结果不完整；请缩小目录范围"
			return filepath.SkipAll
		}
		scanned++
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			skippedBinary++
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if len(matches) >= limit {
				break
			}
			line = strings.TrimRight(line, "\r")
			if !re.MatchString(line) {
				continue
			}
			matches = append(matches, map[string]any{
				"path": path,
				"line": i + 1,
				"text": mcpsrv.TruncateRunes(strings.TrimSpace(line), 300),
			})
		}
		// 命中上限可能是在这个文件内部被顶满的：此时必须就地收尾，
		// 否则「只搜单个文件」的场景走不到下一次 visit，truncated 会错报成 false。
		if len(matches) >= limit {
			stopped = "已到命中上限（" + fmt.Sprint(limit) + " 条），结果不完整；可加 glob 或换更精确的 pattern 缩小范围"
			return filepath.SkipAll
		}
		return nil
	}

	info, statErr := os.Stat(root)
	switch {
	case statErr != nil:
		return nil, fmt.Errorf("路径不可用：%w", statErr)
	case info.IsDir():
		walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // 单个目录/文件不可读（权限）时继续，别让整次搜索失败
			}
			if d.IsDir() && path != root {
				// 精确名匹配而非小写化：目录名基本是固定写法，小写化会把 Linux 上的
				// Out/ 与 Build/ 这类正常源码目录也吞掉。
				if fsguard.SkipDir(d.Name()) {
					return filepath.SkipDir
				}
				// 深度按分隔符数量估算：比每层传参省事，且这里只需要粗略上限
				if strings.Count(strings.TrimPrefix(path, root), string(os.PathSeparator)) >= grepMaxDepth {
					return filepath.SkipDir
				}
			}
			return visit(path, d)
		})
		if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
			return nil, fmt.Errorf("遍历目录失败：%w", walkErr)
		}
	default:
		// 传的是单个文件：直接按文件处理，省得模型为了搜一个文件再造一个目录参数
		entry := fileEntry{info: info, name: filepath.Base(root)}
		if err := visit(root, entry); err != nil && !errors.Is(err, filepath.SkipAll) {
			return nil, err
		}
	}

	logger.I("[mcp] file_grep %q in %s（扫描 %d 个文件，命中 %d）", pattern, root, scanned, len(matches))
	return map[string]any{
		"path":          root,
		"pattern":       pattern,
		"glob":          glob,
		"matches":       matches,
		"count":         len(matches),
		"scannedFiles":  scanned,
		"skippedBinary": skippedBinary,
		"truncated":     stopped != "",
		"truncatedNote": stopped,
	}, nil
}

// fileEntry 把单个文件包装成 os.DirEntry，让「搜目录」与「搜单个文件」复用同一段匹配逻辑。
type fileEntry struct {
	info os.FileInfo
	name string
}

func (e fileEntry) Name() string               { return e.name }
func (e fileEntry) IsDir() bool                { return false }
func (e fileEntry) Type() os.FileMode          { return e.info.Mode().Type() }
func (e fileEntry) Info() (os.FileInfo, error) { return e.info, nil }
