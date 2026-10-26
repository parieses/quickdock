// Package logtail 实现「尾随一个不断增长的文本文件」的最小内核。
//
// 为什么不用 fsnotify：项目 go.mod 里没有它，为一个刷新频率以秒计的功能引入一个
// 需要额外处理平台差异（Windows 上 ReadDirectoryChangesW 的缓冲区溢出会静默丢事件）
// 的通知库不划算。轮询 + 偏移的代价是一次 stat + 一次按需读，远小于一层抽象。
//
// 包刻意不碰 Wails、不碰 DB、不起 goroutine：所有 IO 都是调用方触发的同步方法，
// 这样行为可预测、单测不需要等定时器。
package logtail

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
)

// 单次轮询最多读多少字节。日志爆发（比如一个程序刚崩、倾泻几十 MB 堆栈）时
// 不能一次把它全读进内存；读不完的下次继续，只是延迟一轮显示。
const MaxPollBytes = 4 << 20 // 4MB

// FingerprintBytes 轮转检测用的内容指纹长度。
//
// 为什么需要它：size 变小能发现「截断」，SameFile 判否能发现「改名后新建」，
// 但「原地把整个文件重写一遍、且新内容更长」这两种信号都抓不到——size 变大了、
// 文件身份也没变。Windows 上 O_TRUNC 覆盖写走的正是这条路。
// 记下已消费的最后若干字节，下次轮询比对，不匹配即为被重写。
const FingerprintBytes = 512

// MaxBufferedLines 首读（Tail）默认给多少行。
// 用户打开一个 2GB 的日志时，只关心最近发生的事，从头灌等于卡死界面。
const MaxBufferedLines = 500

// Snapshot 一次读取的结果。
type Snapshot struct {
	Lines   []string // 本次新增的完整行；可能为 nil
	Offset  int64    // 已经消费到的字节位置（下次从这里继续）
	Rotated bool     // 本次检测到文件被截断或替换（日志轮转），已从头/末尾重读
}

// Tailer 维护单个文件的追加读位置与身份指纹。
//
// 它不是并发安全的：一个 Tailer 只由一个轮询 goroutine 持有。
// 需要并发访问时由调用方（services/logtail）加锁。
type Tailer struct {
	path   string
	offset int64
	stat   os.FileInfo
	fp     []byte // 已消费的最后 FingerprintBytes 个字节，用于识别原地重写
}

// NewTailer 创建一个 Tailer。文件不存在时返回错误：打开文件对人的心智而言是
// 「选错了」，应当立刻报错，而不是起一个每 500ms stat 一次的僵尸轮询。
func NewTailer(path string) (*Tailer, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrInvalid}
	}
	return &Tailer{path: path, stat: st}, nil
}

// Path 返回被尾随的路径，供会话表与前端展示。
func (t *Tailer) Path() string { return t.path }

// Size 返回上次 stat 到的文件大小。
func (t *Tailer) Size() int64 {
	if t.stat == nil {
		return 0
	}
	return t.stat.Size()
}

// ModTime 返回上次 stat 到的修改时间。
func (t *Tailer) ModTime() (sec int64, nsec int64) {
	if t.stat == nil {
		return 0, 0
	}
	return t.stat.ModTime().Unix(), int64(t.stat.ModTime().Nanosecond())
}

// Tail 首读：取文件末尾最多 n 行，并把读位置推到文件末尾
// （这样紧接着的 Poll 只会拿到真正新增的东西，不会重放）。
//
// n <= 0 时用 MaxBufferedLines。
func (t *Tailer) Tail(n int) (Snapshot, error) {
	if n <= 0 {
		n = MaxBufferedLines
	}
	st, err := os.Stat(t.path)
	if err != nil {
		return Snapshot{}, err
	}
	t.stat = st
	if st.Size() == 0 {
		t.offset = 0
		return Snapshot{}, nil
	}
	lines, raw, err := ReadLastLines(t.path, st.Size(), n)
	if err != nil {
		return Snapshot{}, err
	}
	t.offset = st.Size()
	t.setFingerprint(raw)
	return Snapshot{Lines: lines, Offset: t.offset}, nil
}

// setFingerprint 记下 data 的最后 FingerprintBytes 个字节。
// 约定：data 必须是紧贴在 offset 之前的那些字节，这样下次才能在
// [offset-len(fp), offset) 处读回来比对。
func (t *Tailer) setFingerprint(data []byte) {
	if len(data) == 0 {
		t.fp = nil
		return
	}
	n := FingerprintBytes
	if len(data) < n {
		n = len(data)
	}
	t.fp = []byte(data[len(data)-n:])
}

// Poll 读取自上次消费位置之后新增的完整行。
//
// 半行处理：只消费到最后一个 '\n' 为止，剩余部分留在下一次。
// 写日志的程序常常一次 write 一半（尤其跨平台没有行缓冲时），
// 急着把半行吐出去会让用户看到一条被截断的错误信息。
//
// 轮转处理：size 变小（truncate）或 SameFile 判否（rename + 新建）都算轮转，
// 此时重新取新文件的末尾 n 行，而不是 myriads of lines from 0 —— 轮转后的新文件
// 通常很小，但轮转也可能只是「另一个程序往同路径写了别的东西」，按 tail 语义处理最稳。
func (t *Tailer) Poll(maxLines int) (Snapshot, error) {
	if maxLines <= 0 {
		maxLines = MaxBufferedLines
	}
	st, err := os.Stat(t.path)
	if err != nil {
		return Snapshot{}, err
	}
	rotated := st.Size() < t.offset || (t.stat != nil && !os.SameFile(t.stat, st))
	if !rotated && st.Size() > t.offset && len(t.fp) > 0 {
		// 只有在要读新数据时才花这一次 ReadAt：空闲文件里省掉它，
		// 而有新内容的时候一次 512 字节的读取完全淹没在不对称开销里。
		r, e := t.fingerprintMatches(int64(len(t.fp)))
		rotated = e == nil && !r
	}
	t.stat = st
	if rotated {
		t.offset = 0
		if st.Size() == 0 {
			t.fp = nil
			return Snapshot{Rotated: true}, nil
		}
		lines, raw, err := ReadLastLines(t.path, st.Size(), maxLines)
		if err != nil {
			return Snapshot{}, err
		}
		t.offset = st.Size()
		t.setFingerprint(raw)
		return Snapshot{Lines: lines, Offset: t.offset, Rotated: true}, nil
	}
	if st.Size() == t.offset {
		return Snapshot{Offset: t.offset}, nil
	}
	lines, err := t.readSince(st.Size())
	if err != nil {
		return Snapshot{}, err
	}
	if len(lines) == 0 {
		return Snapshot{Offset: t.offset}, nil
	}
	return Snapshot{Lines: lines, Offset: t.offset}, nil
}

// fingerprintMatches 比对 [offset-n, offset) 这段字节是否与上次消费时一致。
// n 超出文件长度（文件被删/重写得很短）时按匹配处理 —— 那种情况由 size 比较已经判为轮转。
func (t *Tailer) fingerprintMatches(n int64) (bool, error) {
	start := t.offset - n
	if start < 0 {
		return true, nil
	}
	f, err := os.Open(t.path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	cur := make([]byte, n)
	if _, err := f.ReadAt(cur, start); err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.Equal(cur, t.fp), nil
}

// readSince 读取 [offset, size) 之间新增的完整行，并推进 offset 与指纹。
func (t *Tailer) readSince(size int64) ([]string, error) {
	f, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, MaxPollBytes)
	// Read 在读满 buf 之外还可能返回 (0, io.EOF)（正好读到末尾）或 (n, nil)；
	// (n, io.EOF) 也可能出现。只有非 EOF 的错误才是真失败。
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	buf = buf[:n]
	// 只消费到最后一个换行为止：写日志的程序常常一次 write 一半，
	// 急着把半行吐出去会让用户看到一条被截断的错误信息。
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return nil, nil
	}
	consumed := buf[:end+1]
	t.offset += int64(len(consumed))
	t.setFingerprint(consumed)
	return SplitLines(string(buf[:end])), nil
}

// ReadLastLines 读 path 末尾最多 n 行，不修改任何 Tailer 状态（纯函数式辅助）。
//
// 第二个返回值是被读到的原始字节（紧贴文件末尾的那一段），调用方用它建立轮转指纹。
func ReadLastLines(path string, size int64, n int) ([]string, []byte, error) {
	if n <= 0 || size <= 0 {
		return nil, nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	const chunk = 8192
	var (
		buf   []byte
		lines int
		pos   = size
	)
	// 倒着读，直到累计换行数超过 n —— 多读一个换行才能确定倒数第 n 行是完整的。
	// 中途撞到文件头说明整个文件都不够 n 行，那时 buf 就是全文。
	for pos > 0 && lines <= n {
		want := int64(chunk)
		if pos < want {
			want = pos
		}
		pos -= want
		b := make([]byte, want)
		if _, err := f.ReadAt(b, pos); err != nil && !errors.Is(err, io.EOF) {
			return nil, nil, err
		}
		buf = append(b, buf...)
		lines += bytes.Count(b, []byte{'\n'})
	}
	all := SplitLines(string(buf))
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, buf, nil
}

// SplitLines 按 '\n' 切行：去掉行尾 '\r'（Windows 日志常见），丢弃最后那次换行产生的空串。
//
// 不丢弃中间的空行：日志里的空行往往是有意的视觉分隔。
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i := range parts {
		parts[i] = strings.TrimSuffix(parts[i], "\r")
	}
	return parts
}
