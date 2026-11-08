package kb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"quickdock/internal/db"
	ikb "quickdock/internal/kb"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 索引流水线。
//
// 单 goroutine 串行执行，不并发。理由不是"简单"：embedding 接口有速率限制，
// 并发只会一起撞 429，然后一起退避，总耗时比串行还长；而串行至少能让
// 用户看到「第 3/12 个文件」这种有意义的进度。
//
// 流水线内部的三段式是这套设计的核心约束（详见 internal/db/kb.go 的
// UpdateChunkVecs 注释）：写文本用事务、调接口不持锁、回填向量每批一个短事务。
// 任何把网络调用包进 Transaction 的写法，都会让整个应用在导入知识库时卡死。

const (
	// batchMaxTokens 单批 embedding 的 token 预算。
	// 多数接口单请求上限 8191 token，留 2000 余量给特殊文本。
	batchMaxTokens = 6000
	// batchMaxItems 单批条数上限，防止一堆极短块把请求条数塞满。
	batchMaxItems = 16
	// hashMaxSize 超过这个大小就不算 sha256（几十 GB 的文件也没人往知识库里放）。
	hashMaxSize = 200 << 20
)

// ServiceStartup 启动索引 worker，并把上次没跑完的源重新排队。
func (s *KBService) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	s.startWorker()
	go s.recoverUnfinished()
	return nil
}

// ServiceShutdown 停止 worker。
func (s *KBService) ServiceShutdown() error {
	s.stopOnce.Do(func() { close(s.quit) })
	return nil
}

// recoverUnfinished 把处于中间态（解析/切分/建向量）的源重新排队。
//
// 这些状态只可能来自「上次进程被杀」，正常流程里它们存在的时间以秒计。
// 不恢复的话，这些源会永远卡在 embedding，用户看到转圈的图标但什么也不会发生。
func (s *KBService) recoverUnfinished() {
	if s.App.DB == nil {
		return
	}
	list, err := s.App.DB.ListKBSources("")
	if err != nil {
		return
	}
	for _, src := range list {
		switch src.Status {
		case db.KBSourceParsing, db.KBSourceChunking, db.KBSourceEmbedding:
			s.enqueue(src.ID)
		}
	}
}

func (s *KBService) startWorker() {
	s.startOnce.Do(func() { go s.worker() })
}

func (s *KBService) enqueue(id string) {
	if id == "" {
		return
	}
	select {
	case s.queue <- id:
	default:
		// 队列满（>1024）：不改状态，下次扫描会再排一遍。
	}
}

func (s *KBService) worker() {
	for {
		select {
		case <-s.quit:
			return
		case id := <-s.queue:
			// 用户点过「停止」后不再处理队列里剩余项。
			// 状态保持不变（pending），下次扫描会重新排队。
			if atomic.LoadInt32(&s.cancelFlag) == 1 {
				continue
			}
			s.processSource(id)
		}
	}
}

// setRun 更新运行时状态。
func (s *KBService) setRun(running bool, stage, id, name string, done, total int) {
	s.runMu.Lock()
	s.running = running
	s.stage = stage
	s.current = id
	s.curName = name
	s.done = done
	s.total = total
	s.runMu.Unlock()
	if running {
		s.emit("kb:index-progress", map[string]interface{}{
			"sourceId": id, "name": name, "stage": stage, "done": done, "total": total,
			"queue": len(s.queue),
		})
	}
}

// processSource 处理一个源：解析 → 切片 → 写库 → 补向量。
func (s *KBService) processSource(id string) {
	src, err := s.App.DB.GetKBSource(id)
	if err != nil || src == nil {
		return
	}

	st, statErr := os.Stat(src.Path)
	if statErr != nil {
		_ = s.App.DB.UpdateKBSourceStatus(id, db.KBSourceMissing, "文件不存在")
		s.emit("kb:index-progress", map[string]interface{}{
			"sourceId": id, "name": src.Name, "stage": "missing", "done": 0, "total": 0,
		})
		return
	}

	size, mtime := st.Size(), st.ModTime().Unix()
	hash, hashErr := fileHash(src.Path)
	if hashErr != nil {
		hash = ""
	}

	// 内容确实没变（hash 相同）且已有切片 → 跳过重建，只补向量。
	// hash 而不是 size+mtime：用户「另存为」一遍文件，时间戳变了内容没变，
	// 这种情况按时间戳判会做一次完全多余的重建（几十次 embedding 调用）。
	needRebuild := hash == "" || hash != src.Hash || src.ChunkCount == 0 ||
		src.Status != db.KBSourceReady

	var chunkCount, pageCount int
	var ocrHint bool

	if needRebuild {
		s.setRun(true, "parsing", id, src.Name, 0, 0)
		doc, perr := ikb.ParseFile(src.Path)
		if perr != nil {
			_ = s.App.DB.UpdateKBSourceStatus(id, db.KBSourceFailed, describeParseErr(perr))
			s.setRun(false, "", "", "", 0, 0)
			s.emit("kb:index-progress", map[string]interface{}{
				"sourceId": id, "name": src.Name, "stage": "failed", "error": describeParseErr(perr),
			})
			return
		}
		ocrHint = doc.OCRHint

		s.setRun(true, "chunking", id, src.Name, 0, 0)
		chunks := ikb.Split(doc)
		if len(chunks) == 0 {
			_ = s.App.DB.UpdateKBSourceStatus(id, db.KBSourceFailed, "切不出有效文本")
			s.setRun(false, "", "", "", 0, 0)
			return
		}

		rows := make([]db.KBChunk, 0, len(chunks))
		for _, c := range chunks {
			rows = append(rows, db.KBChunk{
				Idx:       c.Idx,
				Heading:   c.Heading,
				Text:      c.Text,
				CharCount: len([]rune(c.Text)),
				Page:      c.Page,
			})
		}
		if err := s.App.DB.ReplaceChunks(id, rows); err != nil {
			_ = s.App.DB.UpdateKBSourceStatus(id, db.KBSourceFailed, "写入索引失败："+err.Error())
			s.setRun(false, "", "", "", 0, 0)
			return
		}
		chunkCount, pageCount = len(chunks), doc.Pages
		if err := s.App.DB.UpdateKBSourceMeta(id, hash, size, mtime, chunkCount, pageCount); err != nil {
			_ = s.App.DB.UpdateKBSourceStatus(id, db.KBSourceFailed, "写入索引失败："+err.Error())
			s.setRun(false, "", "", "", 0, 0)
			return
		}
	} else {
		chunkCount, pageCount = src.ChunkCount, src.PageCount
		// 指纹可能变了（例如只 touch 过），顺手更新，避免下次扫描又白跑一遍。
		if src.Size != size || src.Mtime != mtime {
			_ = s.App.DB.UpdateKBSourceMeta(id, hash, size, mtime, chunkCount, pageCount)
		}
	}

	// 到这里文本已可检索。向量是增量能力：失败不影响源可用，
	// 只是检索降级为纯关键词——所以不把它算进源状态。
	_ = s.App.DB.UpdateKBSourceStatus(id, db.KBSourceEmbedding, "")
	s.embedSource(id, src.Name, ocrHint)

	_ = s.App.DB.UpdateKBSourceStatus(id, db.KBSourceReady, "")
	s.invalidateVecCache()
	s.setRun(false, "", "", "", 0, 0)
	s.emit("kb:index-progress", map[string]interface{}{
		"sourceId": id, "name": src.Name, "stage": "ready", "done": chunkCount, "total": chunkCount,
	})
}

// embedSource 给还没建立向量的切片补向量（断点续做的入口）。
func (s *KBService) embedSource(sourceID, name string, ocrHint bool) {
	cfg := s.settings()
	if !cfg.VectorOn || cfg.EmbedModel == "" {
		return
	}
	baseURL, apiKey, ok := s.embedConfig()
	if !ok {
		return
	}

	pending, err := s.App.DB.KBPendingVecChunks(sourceID, cfg.EmbedModel)
	if err != nil || len(pending) == 0 {
		return
	}

	emb := ikb.NewEmbedder(ikb.EmbedConfig{BaseURL: baseURL, APIKey: apiKey, Model: cfg.EmbedModel})
	total := len(pending)
	done := 0

	for i := 0; i < len(pending); {
		batch, next := nextBatch(pending, i)
		if len(batch) == 0 {
			break
		}
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = embedInput(c)
		}

		s.setRun(true, "embedding", sourceID, name, done, total)
		ctx, cancel := contextWithTimeout(0)
		vecs, embErr := emb.EmbedBatch(ctx, texts)
		cancel()

		if embErr != nil {
			// 整批失败不中断：可能只是这一批里有超长文本。
			// 剩下的批继续试，最后没建上的靠下次扫描补。
			done += len(batch)
			i = next
			continue
		}

		updates := make([]db.KBVecUpdate, 0, len(batch))
		for j := range batch {
			vec := vecs[j]
			ikb.Normalize(vec)
			updates = append(updates, db.KBVecUpdate{
				ChunkID: batch[j].ID,
				Vec:     ikb.EncodeVec(vec),
				Dim:     len(vec),
			})
		}
		if err := s.App.DB.UpdateChunkVecs(cfg.EmbedModel, updates); err != nil {
			done += len(batch)
			i = next
			continue
		}
		done += len(batch)
		i = next
		s.setRun(true, "embedding", sourceID, name, done, total)
	}
}

// nextBatch 按 token 预算切一批。
//
// 不能固定「每批 N 条」：中文 800 字的块约 800~1200 token，16 条就是
// 1.3~1.9 万 token，直接超过单请求上限，整批 400 报错重试。
func nextBatch(chunks []db.KBChunk, start int) ([]db.KBChunk, int) {
	i := start
	tokens := 0
	for i < len(chunks) && len(chunks[start:i]) < batchMaxItems {
		t := ikb.EstimateTokens(embedInput(chunks[i]))
		if i > start && tokens+t > batchMaxTokens {
			break
		}
		tokens += t
		i++
	}
	return chunks[start:i], i
}

// embedInput 送模型时的文本：标题路径 + 正文。
//
// 带 heading 是因为同一段正文挂在「安装」和「故障排查」下语义不同。
// 只在送模型时拼，存库的 text 保持原样——拼进去会让 FTS 反复命中标题词。
func embedInput(c db.KBChunk) string {
	if c.Heading == "" {
		return c.Text
	}
	return c.Heading + "\n" + c.Text
}

// fileHash 文件内容 sha256。超过阈值退化为「大小+时间」，够判断变化。
func fileHash(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Size() > hashMaxSize {
		return fmt.Sprintf("big:%d:%d", st.Size(), st.ModTime().Unix()), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// describeParseErr 把解析错误翻成用户能看懂的一句话。
//
// 直接把 err.Error() 摆到界面上，用户看到的是
// "zip: not a valid zip file"，除了让人困惑没有任何帮助。
func describeParseErr(err error) string {
	switch {
	case errors.Is(err, ikb.ErrEncrypted):
		return "文件已加密，请先解除密码保护"
	case errors.Is(err, ikb.ErrUnsupported):
		return "不支持的文件格式"
	case errors.Is(err, ikb.ErrCorrupted):
		return "文件已损坏或格式不标准"
	case errors.Is(err, ikb.ErrNoText):
		return "文件里没有可提取的文本（若为扫描件请用 OCR 插件先转成文字）"
	default:
		return err.Error()
	}
}
