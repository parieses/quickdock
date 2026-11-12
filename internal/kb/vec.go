package kb

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

// 向量序列化与暴力检索。
//
// 为什么没有向量数据库：本项目是纯 Go + modernc.org/sqlite（无 CGO），
// sqlite-vec 那类扩展需要加载 C 动态库，这条路径不存在；Qdrant/Milvus
// 要用户先起一个服务，桌面应用没法这么发。而个人知识库的量级（几千到
// 几万块）根本用不上 ANN——全量点积比任何索引都简单，且召回率 100%。
//
// 代价与边界：1 万块 × 1024 维 float32 = 40MB，单次检索 8~12ms。
// 到 20 万块（800MB / 200ms）才值得考虑 HNSW，那时换的只是
// VectorIndex 的实现，BuildIndex / TopK 的签名不用动。

// ErrDimMismatch 维度不一致。换 embedding 模型后最容易踩到，
// 而且不校验的话点积会算出无意义的小数——结果"看起来正常，只是不准"。
var ErrDimMismatch = errors.New("向量维度不一致")

// EncodeVec 归一化后的 float32 → 小端字节串（dim*4 字节）。
//
// 存归一化后的原因：查询向量也归一化后，余弦相似度就退化成点积，
// 每次检索省掉 n 次开方。丢失的模长信息对「只要方向」的检索毫无用处。
func EncodeVec(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DecodeVec 字节串 → float32。
func DecodeVec(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, errors.New("向量字节长度不是 4 的倍数")
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

// Normalize 原地 L2 归一化。零向量原样返回（不会除零）。
func Normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

// Dot 点积。两边都已归一化时即为余弦相似度。
func Dot(a, b []float32) float32 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var s float32
	for i := 0; i < n; i++ {
		s += a[i] * b[i]
	}
	return s
}

// VectorEntry 一条待入索引的向量（由调用方从库里读出后传入）。
type VectorEntry struct {
	RowID int64
	Vec   []byte
	Dim   int
	// Tag 归属标签（知识库 id）。检索时可按标签过滤，空串表示不限。
	Tag string
	// SourceID 切片所属的源文件。只用于「限定在某一份文档内提问」，
	// 与 Tag 一样在扫描期过滤。
	SourceID string
}

// VectorFilter 检索时的范围限定。两个字段互不冲突：
// 都为空 = 全库；只给 LibraryID = 单库；再给 SourceID = 单文件。
type VectorFilter struct {
	LibraryID string
	SourceID  string
}

// VectorIndex 连续内存的行优先矩阵，便于顺序扫描。
//
// 用一块大 []float32 而不是 [][]float32：后者每行一次指针跳转，
// 几万行下来缓存命中率差一个档；连续内存可以被 CPU 预取器吃到饱。
type VectorIndex struct {
	RowIDs []int64
	// Tags 与 RowIDs 一一对应，值已去重（整库共用同一个字符串实例）。
	// 不存一份 map[rowid]tag：矩阵扫描是顺序的，按下标直取比哈希命中快，
	// 也不用为每次检索重建映射。
	Tags []string
	// SourceIDs 与 RowIDs 一一对应，同样去重。
	SourceIDs []string
	Matrix    []float32
	Dim       int
	// Skipped 维度与主流不一致而被跳过的条数（换模型过渡期会出现）。
	Skipped int
}

// BuildIndex 构建矩阵。维度取「出现最多的那个」，零散异维度直接跳过
// ——它们与新模型不可比，算进去就是往结果里掺噪声。
func BuildIndex(entries []VectorEntry) *VectorIndex {
	if len(entries) == 0 {
		return &VectorIndex{}
	}
	// 先数维度，选出主流维度。
	counts := make(map[int]int, 4)
	for _, e := range entries {
		if e.Dim > 0 && len(e.Vec) == e.Dim*4 {
			counts[e.Dim]++
		}
	}
	dim, best := 0, 0
	for d, c := range counts {
		if c > best || (c == best && d > dim) {
			dim, best = d, c
		}
	}
	if dim == 0 {
		return &VectorIndex{}
	}

	idx := &VectorIndex{Dim: dim}
	idx.RowIDs = make([]int64, 0, best)
	idx.Tags = make([]string, 0, best)
	idx.SourceIDs = make([]string, 0, best)
	idx.Matrix = make([]float32, 0, best*dim)
	// 标签去重：整库几千块共用同一个库 id，不去重就是几千份同内容字符串头。
	interned := make(map[string]string, 8)
	internedSrc := make(map[string]string, 64)
	for _, e := range entries {
		if e.Dim != dim || len(e.Vec) != dim*4 {
			idx.Skipped++
			continue
		}
		v, err := DecodeVec(e.Vec)
		if err != nil {
			idx.Skipped++
			continue
		}
		tag, ok := interned[e.Tag]
		if !ok {
			tag = e.Tag
			interned[e.Tag] = tag
		}
		src, ok := internedSrc[e.SourceID]
		if !ok {
			src = e.SourceID
			internedSrc[e.SourceID] = src
		}
		idx.RowIDs = append(idx.RowIDs, e.RowID)
		idx.Tags = append(idx.Tags, tag)
		idx.SourceIDs = append(idx.SourceIDs, src)
		idx.Matrix = append(idx.Matrix, v...)
	}
	return idx
}

// Len 索引里的向量条数。
func (x *VectorIndex) Len() int {
	if x == nil {
		return 0
	}
	return len(x.RowIDs)
}

// ScoredRow 一个命中及其相似度。
type ScoredRow struct {
	RowID int64
	Score float32
}

// TopK 暴力扫描取前 k 个。
//
// 过滤在扫描时做，不是先取 TopK 再筛：先筛后取的差别在「目标范围只占
// 全库的 5%」时非常明显——那时 Top30 里可能一条都不属于目标，检索直接空手而归。
//
// 全排序而不是堆：k 通常 30、n 通常几万，全排序 n log n 在这个量级
// 比维护小顶堆更快（没有堆操作的常数开销），代码也短。
func (x *VectorIndex) TopK(query []float32, k int, f VectorFilter) []ScoredRow {
	if x == nil || len(x.RowIDs) == 0 || k <= 0 || len(query) != x.Dim {
		return nil
	}
	n := len(x.RowIDs)
	scored := make([]ScoredRow, 0, n)
	dim := x.Dim
	for i := 0; i < n; i++ {
		// Tags/SourceIDs 可能与 RowIDs 不等长（老版本构造的索引），越界时按「不过滤」处理。
		if f.LibraryID != "" && i < len(x.Tags) && x.Tags[i] != f.LibraryID {
			continue
		}
		if f.SourceID != "" && i < len(x.SourceIDs) && x.SourceIDs[i] != f.SourceID {
			continue
		}
		off := i * dim
		scored = append(scored, ScoredRow{RowID: x.RowIDs[i], Score: Dot(x.Matrix[off:off+dim], query)})
	}
	if len(scored) == 0 {
		return nil
	}
	sort.Slice(scored, func(a, b int) bool { return scored[a].Score > scored[b].Score })
	if k > len(scored) {
		k = len(scored)
	}
	return scored[:k]
}

// RRF 融合两路召回（Reciprocal Rank Fusion）。
//
// 为什么用 RRF 而不是加权求和：BM25 是负分（越小越相关）、余弦是 [-1,1]，
// 两者量纲完全不同，归一化之后还要调权重。RRF 只看名次，零调参，
// 而且对「某一路完全没召回」是天然免疫的。
//
// k=60 是原论文的常数，用来压住「第 1 名与第 2 名差距过大」的情况。
func RRF(lists ...[]int64) map[int64]float64 {
	const k = 60.0
	scores := make(map[int64]float64)
	for _, list := range lists {
		for rank, id := range list {
			scores[id] += 1.0 / (k + float64(rank+1))
		}
	}
	return scores
}
