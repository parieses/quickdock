package aitools

// 工具结果外置（WP3）。
//
// 问题：一次 file_read 读 200KB，整个结果会塞进 messages，并且每一轮都重发——
// 一轮十几次工具调用之后，上下文里大部分是「已经用过、不会再看」的原文。
// 现在的做法是超限直接截断（`truncateToolResult`），模型拿不到后半段，
// 只能重读一次，等于又付一遍钱。
//
// 做法：超限时把**原文落盘**，喂给模型的是「摘要 + 一个 id」，需要细节时用
// `tool_result_read` 分段取。关键取舍在存储位置：
//
//	存在对话 cwd 下 → 污染用户项目、进 git status、被 git clean 删掉
//	给 fsguard 开数据目录例外 → 动安全边界，放宽整个 AI 的读取面
//	独立只读工具 + 数据目录（本实现）→ 不碰 fsguard；id 不透明，天然无路径穿越
//
// id 是「会话内的短哈希」而不是路径：模型拿不到也拼不出目录结构，
// 想读别人会话的结果也做不到——读取时只看当前会话自己的目录。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mcpsrv "quickdock/internal/mcp"
	"quickdock/internal/platform"
)

const (
	// 单会话最多保留多少个外置结果文件、总共多少字节。
	// 上限存在的理由与 WP1-c 一致：不设的话「一千次运行」会把磁盘吃掉，
	// 而这些文件的价值随时间衰减得比轨迹更快（原文你基本不会回头看第二遍）。
	toolResultMaxFilesPerConv = 200
	toolResultMaxBytesPerConv = 100 << 20 // 100 MB

	// 单次读取的默认与上限（字节）。给默认值是因为模型常常只传 id。
	toolResultReadDefault = 20000
	toolResultReadMax     = 200000

	// toolResultKeepDays 外置结果的保留天数。与运行轨迹同一趟清理。
	ToolResultKeepDays = 30
)

// ToolResultRoot 外置结果的根目录（测试可覆盖）。
var toolResultRoot = func() string {
	return filepath.Join(platform.DefaultDataDir(), "ai_tool_results")
}

// SetToolResultRoot 仅测试用：把落点指到临时目录，避免污染真实数据目录。
func SetToolResultRoot(fn func() string) { toolResultRoot = fn }

// toolResultShortID 由调用 id 派生的短标识（`r-xxxxxxxx`）。
//
// 为什么不直接用 callID：callID 来自模型（`call_abc123` 之类），格式不受控，
// 直接拼进路径就有穿越风险；而且把它原样回显给模型也没有额外价值。
// 哈希后取前 8 位既稳定（同一次调用重复保存得到同一个 id），又不透明。
func toolResultShortID(callID string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(callID)))
	return "r-" + hex.EncodeToString(sum[:])[:8]
}

// convDirName 会话目录名。
//
// 用会话 id 的哈希而不是 id 本身：会话 id 是 UUID（含 `-`，安全），
// 但把内部 id 暴露成目录名没有好处，统一走哈希更省心。
func convDirName(convID string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(convID)))
	return hex.EncodeToString(sum[:])[:16]
}

func toolResultDir(convID string) string {
	return filepath.Join(toolResultRoot(), convDirName(convID))
}

// validToolResultID 校验 id 形状。读取前必须过这一关：它是唯一由模型控制的输入，
// 而它最终会拼进文件路径。
func validToolResultID(id string) bool {
	if len(id) != len("r-")+8 || !strings.HasPrefix(id, "r-") {
		return false
	}
	for _, c := range id[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// SaveToolResult 把工具结果原文落盘，返回对模型可见的 id。
//
// 写失败返回 error，调用方应当退回「只截断」的老行为——外置是优化，
// 不是正确性的前提。
func SaveToolResult(convID, callID, tool, text string) (string, error) {
	convID = strings.TrimSpace(convID)
	if convID == "" || strings.TrimSpace(callID) == "" {
		return "", errors.New("缺少会话 id 或调用 id")
	}
	dir := toolResultDir(convID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	id := toolResultShortID(callID)
	path := filepath.Join(dir, id+".txt")
	// 头部带一行来源说明：直接打开这个文件的人（用户自己、或将来排查问题）
	// 得知道它是哪次调用产生的，否则一堆哈希文件名毫无意义。
	body := fmt.Sprintf("# tool=%s call=%s\n\n%s", strings.TrimSpace(tool), strings.TrimSpace(callID), text)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	pruneToolResults(dir)
	return id, nil
}

// pruneToolResults 按「条数」与「总字节」两个维度淘汰最旧的文件。
//
// 两个维度都要：一个大文件就能吃掉整个字节预算，而一万个小文件会让目录操作变慢。
func pruneToolResults(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileInfo struct {
		path  string
		size  int64
		mtime int64
	}
	files := make([]fileInfo, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, fileInfo{
			path:  filepath.Join(dir, e.Name()),
			size:  info.Size(),
			mtime: info.ModTime().UnixNano(),
		})
		total += info.Size()
	}
	if len(files) <= toolResultMaxFilesPerConv && total <= toolResultMaxBytesPerConv {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime < files[j].mtime })
	for _, f := range files {
		if len(files) <= toolResultMaxFilesPerConv && total <= toolResultMaxBytesPerConv {
			return
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		total -= f.size
		files = files[1:]
	}
}

// ReadToolResult 按字节区间读取外置结果。
//
// 返回的 next 是「下一次该从哪开始」，等于 offset+已读长度；读到末尾时等于 total。
func ReadToolResult(convID, id string, offset, limit int) (chunk string, total, next int, err error) {
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return "", 0, 0, errors.New("不知道当前会话，无法读取工具结果")
	}
	if !validToolResultID(id) {
		// 这个分支要能挡住 `../` 之类的输入：id 只允许 r- 加 8 位十六进制。
		return "", 0, 0, fmt.Errorf("结果 id 不合法：%q", id)
	}
	path := filepath.Join(toolResultDir(convID), id+".txt")
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "", 0, 0, fmt.Errorf("结果 %s 不存在（可能已被清理）", id)
		}
		return "", 0, 0, rerr
	}
	total = len(data)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	if limit <= 0 {
		limit = toolResultReadDefault
	}
	if limit > toolResultReadMax {
		limit = toolResultReadMax
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return string(data[offset:end]), total, end, nil
}

// PurgeToolResultsBefore 删掉超过保留期的外置结果，返回删掉的文件数。
func PurgeToolResultsBefore(cutoffUnixNano int64) int {
	root := toolResultRoot()
	dirs, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		full := filepath.Join(root, d.Name())
		files, ferr := os.ReadDir(full)
		if ferr != nil {
			continue
		}
		for _, f := range files {
			info, ierr := f.Info()
			if ierr != nil || info.ModTime().UnixNano() >= cutoffUnixNano {
				continue
			}
			if os.Remove(filepath.Join(full, f.Name())) == nil {
				removed++
			}
		}
		// 目录空了就一起删，避免留下几百个空壳。
		if rest, rerr := os.ReadDir(full); rerr == nil && len(rest) == 0 {
			_ = os.Remove(full)
		}
	}
	return removed
}

// PurgeToolResultsForConv 删掉某个会话的全部外置结果（删会话时调用）。
func PurgeToolResultsForConv(convID string) {
	if strings.TrimSpace(convID) == "" {
		return
	}
	_ = os.RemoveAll(toolResultDir(convID))
}

// registerToolResult 注册 tool_result_read。
func registerToolResult(d Deps) {
	r := registrar{d: d, domain: mcpsrv.CategoryCode}
	str := mcpsrv.Str
	intp := mcpsrv.Int

	r.addCtx(readToolResult, mcpsrv.LevelRead, "tool_result_read",
		"读取被外置的工具结果原文。工具结果过长时不会再整段塞进上下文，而是给一个 id，"+
			"用这个工具按区间取回。"+
			"offset 是字节偏移（从 0 开始），limit 是一次读多少字节（默认 20000，最大 200000）。"+
			"返回里带 total 与 next：next < total 说明还有后续，接着用 next 当 offset。"+
			"只在确实需要原文细节时才读——先看摘要能不能满足。",
		[]string{"id"},
		map[string]any{
			"id":     str("结果 id，形如 r-1a2b3c4d（在工具结果的提示里给出）"),
			"offset": intp("从第几个字节开始读，默认 0"),
			"limit":  intp("一次读多少字节，默认 20000，最大 200000"),
		})
}

// readToolResult 是 tool_result_read 的实现。
func readToolResult(ctx context.Context, d Deps, args map[string]any) (any, error) {
	convID := ConvIDFrom(ctx)
	id := strings.TrimSpace(mcpsrv.Arg(args, "id"))
	if id == "" {
		return nil, errors.New("缺少参数 id")
	}
	offset := mcpsrv.ArgInt(args, "offset", 0)
	limit := mcpsrv.ArgInt(args, "limit", toolResultReadDefault)

	chunk, total, next, err := ReadToolResult(convID, id, offset, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":      id,
		"offset":  offset,
		"total":   total,
		"next":    next,
		"hasMore": next < total,
		"content": chunk,
	}, nil
}
