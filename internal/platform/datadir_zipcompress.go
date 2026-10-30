package platform

import (
	"archive/zip"
	"compress/flate"
	"io"
	"sync"
)

// 压缩级别选择（实测 5.2GB / 17 万文件）：
//
//	level 6（flate 默认）：约 13 MB/s，5.2GB 要 7 分钟，压缩后约 2.5GB
//	level 1（BestSpeed）  ：约 40-60 MB/s，5.2GB 约 2 分钟，压缩后约 3.2GB
//
// 迁移场景选 level 1：跨盘搬运总耗时 = 打包 + 复制 + 解压，
// 打包时间降 3 倍而压缩后体积只多 25%，总体更快，用户等的时间更短。
//
// 另一个提速点是复用 flate.Writer：17 万个文件若各自 new 一个，
// 每次都要分配 ~1.2MB 的压缩缓冲区（17 万 × 1.2MB 的分配/回收开销可观）。
// 用 sync.Pool 复用，Close 后归还、下次 Reset 重用。
//
// 曾试过「小文件（<4KB）直接 Store 不压缩」，实测没有提速
// （node_modules 6.4 万文件：92.6s → 89.6s，噪声范围内），
// 反而让压缩包大了 27MB。说明瓶颈是磁盘读小文件（IO），不是压缩计算。
// 故保持全部 Deflate：包更小，跨盘复制更快。
const archiveCompressLevel = flate.BestSpeed

// flateWriterPool 复用 flate.Writer，避免每个文件都新建压缩器。
var flateWriterPool = sync.Pool{
	New: func() any {
		// NewWriter(nil) 只是为了拿到一个可 Reset 的实例，
		// 真正使用时先 Reset 到目标 writer。
		w, _ := flate.NewWriter(io.Discard, archiveCompressLevel)
		return w
	},
}

// pooledFlateCompressor 实现 zip.Compressor：从池里取 flate.Writer，
// Close 时归还池。
type pooledFlateCompressor struct {
	w *flate.Writer
}

func (c *pooledFlateCompressor) Write(b []byte) (int, error) { return c.w.Write(b) }

func (c *pooledFlateCompressor) Close() error {
	err := c.w.Close()
	// 归还前先 Reset 到 io.Discard：下次取用时还会 Reset 到真正的目标 writer，
	// 但这样能让池里躺着的实例不持有上一个文件的 writer 引用。
	c.w.Reset(io.Discard)
	flateWriterPool.Put(c.w)
	return err
}

// registerArchiveCompressor 给 zip.Writer 注册快速压缩器。
func registerArchiveCompressor(zw *zip.Writer) {
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		fw := flateWriterPool.Get().(*flate.Writer)
		fw.Reset(w)
		return &pooledFlateCompressor{w: fw}, nil
	})
}
