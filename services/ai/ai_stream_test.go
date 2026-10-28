package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"quickdock/internal/db"
)

// sseRecorder 记录所有写入的 SSE 帧，供断言使用。
// 实现完整的 http.ResponseWriter，以便直接注入 flusher。
type sseRecorder struct {
	mu     sync.Mutex
	parts  []string
	header http.Header
}

func (r *sseRecorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *sseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parts = append(r.parts, string(p))
	return len(p), nil
}

func (r *sseRecorder) WriteHeader(int) {}

func (r *sseRecorder) Flush() {}

func (r *sseRecorder) frames() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, p := range r.parts {
		for _, block := range strings.Split(p, "\n\n") {
			block = strings.TrimSpace(block)
			if block == "" {
				continue
			}
			var event, data string
			for _, line := range strings.Split(block, "\n") {
				if strings.HasPrefix(line, "event: ") {
					event = strings.TrimSpace(line[7:])
				} else if strings.HasPrefix(line, "data: ") {
					data += line[6:]
				}
			}
			if event == "" || data == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(data), &m); err != nil {
				continue
			}
			m["__event"] = event
			out = append(out, m)
		}
	}
	return out
}

// TestSSEFlusherMergesTokens 验证：连续 token 被合并成少量帧，且文本内容不丢失。
func TestSSEFlusherMergesTokens(t *testing.T) {
	rec := &sseRecorder{}
	fl := newSSEFlusher(rec, rec, "conv-1")

	const total = 200
	var sb strings.Builder
	for i := 0; i < total; i++ {
		txt := fmt.Sprintf("t%d ", i)
		sb.WriteString(txt)
		fl.push("token", txt)
	}
	// done 事件应在所有 token 之后
	if err := fl.writeEvent("done", doneEvent("conv-1", 120*time.Millisecond, db.TokenUsage{Prompt: 100, Completion: 200})); err != nil {
		t.Fatalf("writeEvent: %v", err)
	}
	fl.close()

	frames := rec.frames()
	if len(frames) < 2 {
		t.Fatalf("期望至少 2 帧（token + done），实际 %d", len(frames))
	}
	// 最后一帧必须是 done
	last := frames[len(frames)-1]
	if last["__event"] != "done" {
		t.Fatalf("最后一帧应为 done，实际 %v", last["__event"])
	}
	// token 帧数应显著少于 200（合并生效）
	tokenFrames := 0
	for _, f := range frames {
		if f["__event"] == "token" {
			tokenFrames++
		}
	}
	if tokenFrames >= total {
		t.Errorf("token 帧未合并：%d 帧 / %d token", tokenFrames, total)
	}
	// 合并后文本必须完整无损
	var got strings.Builder
	for _, f := range frames {
		if f["__event"] == "token" {
			got.WriteString(fmt.Sprint(f["text"]))
		}
	}
	if got.String() != sb.String() {
		t.Errorf("token 文本丢失：\n期望 %q\n实际 %q", sb.String(), got.String())
	}
}

// TestSSEFlusherDoneEventFields 验证 done 事件字段名与前端读取一致，且为数值类型。
func TestSSEFlusherDoneEventFields(t *testing.T) {
	rec := &sseRecorder{}
	fl := newSSEFlusher(rec, rec, "conv-2")
	if err := fl.writeEvent("done", doneEvent("conv-2", 350*time.Millisecond, db.TokenUsage{Prompt: 1234, Completion: 567})); err != nil {
		t.Fatalf("writeEvent: %v", err)
	}
	fl.close()

	frames := rec.frames()
	if len(frames) != 1 {
		t.Fatalf("期望 1 帧，实际 %d", len(frames))
	}
	f := frames[0]
	if f["convId"] != "conv-2" {
		t.Errorf("convId = %v", f["convId"])
	}
	// 必须是数字而非字符串：前端用 Number(data.x) 读取，字符串也能转，
	// 但 JSON 数值更规范，也便于前端做类型判断
	if _, ok := f["firstTokenMs"].(float64); !ok {
		t.Errorf("firstTokenMs 应为数值，实际 %T (%v)", f["firstTokenMs"], f["firstTokenMs"])
	}
	if v, _ := f["firstTokenMs"].(float64); int(v) != 350 {
		t.Errorf("firstTokenMs = %v, 期望 350", v)
	}
	if v, _ := f["promptTokens"].(float64); int(v) != 1234 {
		t.Errorf("promptTokens = %v, 期望 1234", v)
	}
	if v, _ := f["completionTokens"].(float64); int(v) != 567 {
		t.Errorf("completionTokens = %v, 期望 567", v)
	}
}

// TestSSEFlusherTokenBeforeDone 验证帧序：所有 token 帧都在 done 帧之前。
func TestSSEFlusherTokenBeforeDone(t *testing.T) {
	rec := &sseRecorder{}
	fl := newSSEFlusher(rec, rec, "conv-3")
	for i := 0; i < 50; i++ {
		fl.push("token", "x")
	}
	if err := fl.writeEvent("done", doneEvent("conv-3", 0, db.TokenUsage{Prompt: 0, Completion: 0})); err != nil {
		t.Fatalf("writeEvent: %v", err)
	}
	fl.close()

	frames := rec.frames()
	doneIdx := -1
	for i, f := range frames {
		if f["__event"] == "done" {
			doneIdx = i
			break
		}
	}
	if doneIdx < 0 {
		t.Fatal("未找到 done 帧")
	}
	for i := 0; i < doneIdx; i++ {
		if frames[i]["__event"] != "token" {
			t.Fatalf("done 帧之前出现了非 token 帧: %v", frames[i]["__event"])
		}
	}
}

// TestSSEFlusherConcurrentPush 验证并发 push 不丢数据、不 panic。
// 竞态检测依赖 -race。
func TestSSEFlusherConcurrentPush(t *testing.T) {
	rec := &sseRecorder{}
	fl := newSSEFlusher(rec, rec, "conv-4")

	var wg sync.WaitGroup
	const workers = 8
	const perWorker = 100
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				fl.push("token", "z")
			}
		}(w)
	}
	wg.Wait()
	_ = fl.writeEvent("done", doneEvent("conv-4", 0, db.TokenUsage{Prompt: 0, Completion: 0}))
	fl.close()

	var count int
	for _, f := range rec.frames() {
		if f["__event"] == "token" {
			count += len(fmt.Sprint(f["text"]))
		}
	}
	if count != workers*perWorker {
		t.Errorf("并发 push 丢数据：期望 %d 字符，实际 %d", workers*perWorker, count)
	}
}

// TestSSEFrameFormat 验证输出符合 SSE 帧格式（event:/data:/空行分隔）。
func TestSSEFrameFormat(t *testing.T) {
	rec := &sseRecorder{}
	fl := newSSEFlusher(rec, rec, "conv-5")
	fl.push("token", "hello")
	if err := fl.writeEvent("done", doneEvent("conv-5", 0, db.TokenUsage{Prompt: 0, Completion: 0})); err != nil {
		t.Fatalf("writeEvent: %v", err)
	}
	fl.close()

	rec.mu.Lock()
	raw := strings.Join(rec.parts, "")
	rec.mu.Unlock()

	if !regexp.MustCompile(`event: token\ndata: \{.*\}\n\n`).MatchString(raw) {
		t.Errorf("SSE 帧格式不符: %q", raw)
	}
	if !strings.Contains(raw, "event: done\n") {
		t.Errorf("缺少 done 事件: %q", raw)
	}
	// 校验 data 是合法 JSON
	for _, m := range regexp.MustCompile(`data: (.*)`).FindAllStringSubmatch(raw, -1) {
		var v map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v); err != nil {
			t.Errorf("data 不是合法 JSON: %q", m[1])
		}
	}
}

// TestDoneEventFirstTokenZero 验证首字延迟为 0 时（中断场景）字段仍存在。
func TestDoneEventFirstTokenZero(t *testing.T) {
	ev := doneEvent("c", 0, db.TokenUsage{Prompt: 0, Completion: 0})
	if ev["firstTokenMs"] != int64(0) {
		t.Errorf("firstTokenMs = %v, 期望 0", ev["firstTokenMs"])
	}
	// 确认数值能被 JSON 正常序列化
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"firstTokenMs":0`) {
		t.Errorf("序列化结果异常: %s", string(b))
	}
	_ = strconv.Itoa(0)
	_ = httptest.NewRecorder
}
