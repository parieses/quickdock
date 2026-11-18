package aitools

// WP3：工具结果外置的存储层。
//
// 失效方式两种，都很安静：
//   - id 校验松动 → 模型传 "../.." 就变成任意文件读取（这是安全边界，不是 bug）；
//   - offset/limit 边界错 → 中文内容从半个字中间切开，模型拿到乱码却看不出来。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 把落点指到临时目录，避免污染真实数据目录。
func withTempRoot(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := toolResultRoot
	SetToolResultRoot(func() string { return dir })
	t.Cleanup(func() { toolResultRoot = old })
}

func TestSaveAndReadToolResult(t *testing.T) {
	withTempRoot(t)

	body := "第一段：这是一个比较长的工具结果。\n第二段：还有更多内容。\n"
	id, err := SaveToolResult("conv-1", "call-abc", "file_read", body)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !validToolResultID(id) {
		t.Fatalf("id 形状不对: %q", id)
	}
	// 同一次调用重复保存要得到同一个 id（重试/去重路径会重复落到这里）。
	id2, _ := SaveToolResult("conv-1", "call-abc", "file_read", body)
	if id2 != id {
		t.Errorf("同一 callID 的 id 应稳定: %q != %q", id, id2)
	}

	chunk, total, next, err := ReadToolResult("conv-1", id, 0, 10)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if total <= 10 {
		t.Fatalf("total 应大于一次读取量: %d", total)
	}
	if len(chunk) != 10 || next != 10 {
		t.Errorf("首段读取不对: len=%d next=%d", len(chunk), next)
	}
	// 从 next 接着读，拼起来必须等于原文件（含头部说明行）。
	rest, _, next2, err := ReadToolResult("conv-1", id, next, 200000)
	if err != nil {
		t.Fatalf("续读失败: %v", err)
	}
	if next2 != total {
		t.Errorf("读到末尾时 next 应等于 total: next=%d total=%d", next2, total)
	}
	full := chunk + rest
	if !strings.Contains(full, "第二段") {
		t.Errorf("续读内容不完整: %q", full)
	}
}

// id 是唯一由模型控制的输入，它最终会拼进路径——必须挡住穿越。
func TestToolResultRejectsBadID(t *testing.T) {
	withTempRoot(t)
	bad := []string{
		"", "r-", "r-1234567", "r-123456789", "r-ZZZZZZZZ",
		"../../etc/passwd", "r-../../x", "r-1234567/../../x",
	}
	for _, id := range bad {
		if _, _, _, err := ReadToolResult("conv-1", id, 0, 10); err == nil {
			t.Errorf("非法 id %q 应当被拒绝", id)
		}
	}
}

// 会话隔离：A 会话读不到 B 会话的结果。
func TestToolResultScopedToConv(t *testing.T) {
	withTempRoot(t)
	id, err := SaveToolResult("conv-a", "call-1", "file_read", "A 的内容")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, _, _, err := ReadToolResult("conv-b", id, 0, 100); err == nil {
		t.Error("跨会话读取必须失败")
	}
}

// 边界：offset 越界不 panic，limit 被夹到上限。
func TestToolResultBoundaries(t *testing.T) {
	withTempRoot(t)
	id, _ := SaveToolResult("conv-1", "call-2", "file_read", "0123456789")

	if _, total, next, err := ReadToolResult("conv-1", id, 9999, 10); err != nil {
		t.Fatalf("越界 offset 不应报错: %v", err)
	} else if next != total {
		t.Errorf("越界 offset 的 next 应等于 total: next=%d total=%d", next, total)
	}
	// 负数 offset 归零，limit<=0 走默认值。
	if chunk, _, _, err := ReadToolResult("conv-1", id, -5, 0); err != nil || chunk == "" {
		t.Errorf("负数 offset/零 limit 应走默认: err=%v len=%d", err, len(chunk))
	}
	// limit 超过上限时不应一次读回超过上限的字节。
	chunk, _, _, _ := ReadToolResult("conv-1", id, 0, toolResultReadMax*10)
	if len(chunk) > toolResultReadMax+64 {
		t.Errorf("limit 未被夹到上限: %d", len(chunk))
	}
}

// 超量淘汰：文件数超过上限后，最旧的先被删。
func TestToolResultPruneByCount(t *testing.T) {
	withTempRoot(t)

	// 造 205 个结果（上限 200）。逐个写入，mtime 递增天然有顺序。
	ids := make([]string, 0, toolResultMaxFilesPerConv+5)
	for i := 0; i < toolResultMaxFilesPerConv+5; i++ {
		id, err := SaveToolResult("conv-prune", "call-"+itoa(i), "file_read", "x")
		if err != nil {
			t.Fatalf("第 %d 次保存失败: %v", i, err)
		}
		ids = append(ids, id)
	}
	dir := toolResultDir("conv-prune")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录失败: %v", err)
	}
	if len(entries) > toolResultMaxFilesPerConv {
		t.Errorf("淘汰后仍有 %d 个文件，上限 %d", len(entries), toolResultMaxFilesPerConv)
	}
	// 最后写入的那个必须还在（淘汰的是最旧的）。
	if _, err := os.Stat(filepath.Join(dir, ids[len(ids)-1]+".txt")); err != nil {
		t.Errorf("最新文件被误删: %v", err)
	}
}

// 保留期清理：只有过老的文件被删，会话目录清空后一并移除。
func TestPurgeToolResultsBefore(t *testing.T) {
	withTempRoot(t)

	id, err := SaveToolResult("conv-old", "call-old", "file_read", "旧内容")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := SaveToolResult("conv-new", "call-new", "file_read", "新内容"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	// 把「旧」那个的 mtime 回拨 40 天。
	old := filepath.Join(toolResultDir("conv-old"), id+".txt")
	oldTime := time.Now().AddDate(0, 0, -40)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatalf("回拨 mtime 失败: %v", err)
	}

	removed := PurgeToolResultsBefore(time.Now().AddDate(0, 0, -ToolResultKeepDays).UnixNano())
	if removed != 1 {
		t.Errorf("应删掉 1 个过期文件，实际 %d", removed)
	}
	if _, err := os.Stat(filepath.Join(toolResultDir("conv-old"))); !os.IsNotExist(err) {
		t.Error("清空后的会话目录应被移除")
	}
	// 新会话不受影响。
	if entries, err := os.ReadDir(toolResultDir("conv-new")); err != nil || len(entries) != 1 {
		t.Errorf("未过期文件被误删: err=%v n=%d", err, len(entries))
	}
}

// tool_result_read 的正常读取路径（走 ctx 里的会话 id）。
func TestReadToolResultTool(t *testing.T) {
	withTempRoot(t)

	id, err := SaveToolResult("conv-x", "call-x", "file_read", strings.Repeat("内容", 100))
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	ctx := WithConvID(context.Background(), "conv-x")
	val, err := readToolResult(ctx, Deps{}, map[string]any{"id": id, "limit": float64(20)})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	res, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("返回类型不对: %T", val)
	}
	if res["id"] != id {
		t.Errorf("id 回显不对: %v", res["id"])
	}
	if res["hasMore"] != true {
		t.Errorf("还有剩余时应标 hasMore: %v", res)
	}
	// 没有会话 id 时必须给出可读错误，而不是读任意路径。
	if _, err := readToolResult(context.Background(), Deps{}, map[string]any{"id": id}); err == nil {
		t.Error("缺会话 id 时应报错")
	}
}

// itoa 本地小工具：本包没有现成的整数转字符串。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
