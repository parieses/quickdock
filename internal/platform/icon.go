package platform

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/logger"
)

// IconMIME 根据扩展名返回图标 data URI 的 MIME 类型。
// 支持 svg/png/ico/jpg/jpeg，未知扩展名默认 image/svg+xml（内置插件图标均为 SVG）。
func IconMIME(ext string) string {
	switch strings.ToLower(ext) {
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return "image/svg+xml"
	}
}

// iconCacheDir 返回图标缓存目录
func iconCacheDir() string {
	dir := filepath.Join(DefaultDataDir(), "icons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.W("QuickDock: 图标缓存目录创建失败: %v", err)
	}
	return dir
}

// iconMemEntry 是进程内图标缓存的一格。
//
// 磁盘缓存（iconCacheDir 里的 .png）解决了「不要重复调 Shell API」，但没解决
// 「每次查询都要为每个条目重跑一遍 2×stat + ReadFile + base64」。实测单条
// p50=530µs、p99=4ms：条目一多（命令面板全量池每次唤起都要扫一遍），光读图标
// 就能吃掉几十毫秒，而这些字节在进程生命周期内根本不会变。
type iconMemEntry struct {
	modTime time.Time // 源文件 mtime；与 size 一起作为缓存有效性判据
	size    int64
	dataURL string    // 提取结果，空串 = 负缓存（提取失败）
	at      time.Time // 写入时刻，仅负缓存用（见 iconNegativeTTL）
}

// iconNegativeTTL 提取失败结果的缓存时长。
//
// 失败可能是瞬时的（Shell 还没就绪、文件被独占、网络盘暂时不可达），若永久记住
// 就会出现「图标永远补不上，只有重启才恢复」。而失败重试的代价很高（一次 Shell API），
// 所以既不能不复用、也不能一直复用：给一个短 TTL，这段时间内同一路径只试一次。
const iconNegativeTTL = 5 * time.Minute

// iconMemMaxEntries 进程内缓存条目上限。
//
// 一格约 33KB（base64 后的 32×32 PNG），1024 条约 33MB，是「几千个快捷方式」
// 这种极端库也不会失控的上限。超出后按 Range 顺序丢弃一半——这里不追求 LRU 精度，
// 只是防止无界增长；被误丢的条目下次会走磁盘缓存重建，代价可接受。
const iconMemMaxEntries = 1024

var (
	iconMemCache sync.Map // map[string]iconMemEntry，key 为原始路径
	iconMemCount atomic.Int64
)

// sanitizeIconName 将路径转为安全的缓存文件名。
// 同时纳入目录归一化哈希，避免不同目录下同名 exe（如两个 notepad.exe）共用同一缓存而串图。
func sanitizeIconName(path string) string {
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return dirHash(dir) + "_" + safeBaseName(base)
}

// safeBaseName 仅保留文件名词中的安全字符
func safeBaseName(base string) string {
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, base)
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

// dirHash 对目录做 FNV-1a 哈希，得到稳定的短标识，用于区分同名文件的不同来源目录
func dirHash(dir string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(filepath.Clean(dir))))
	return fmt.Sprintf("%08x", h.Sum32())
}

// ExtractIconBase64 从文件提取图标，返回 base64 data URL。
// 优先读进程内缓存，其次磁盘缓存；若源文件比缓存更新则重新提取（避免图标永不刷新）。
// 失败时返回空字符串（且把失败也缓存起来，避免坏路径每次都去撞 Shell API）。
// 实际提取由平台相关实现 extractIconRaw 完成（Windows=Shell API，其他平台暂无实现）。
func ExtractIconBase64(filePath string) string {
	// 源文件的 mtime/size 同时是「内存缓存是否有效」和「磁盘缓存是否过期」的判据，
	// 所以这一次 Stat 本来就要做，不算额外开销。
	var (
		srcMod  time.Time
		srcSize int64
		srcOK   bool
	)
	if srcInfo, err := os.Stat(filePath); err == nil {
		srcMod, srcSize, srcOK = srcInfo.ModTime(), srcInfo.Size(), true
	}

	if v, ok := iconMemCache.Load(filePath); ok {
		if e := v.(iconMemEntry); e.modTime.Equal(srcMod) && e.size == srcSize {
			// 负缓存要带 TTL：失败可能是瞬时的，不能永久封死。
			if e.dataURL != "" || time.Since(e.at) < iconNegativeTTL {
				return e.dataURL
			}
		}
	}

	dataURL := loadIconDiskCached(filePath, srcMod, srcOK)
	storeIconMem(filePath, srcMod, srcSize, dataURL)
	return dataURL
}

// loadIconDiskCached 走磁盘缓存 / 真实提取，是 ExtractIconBase64 未命中内存缓存时的慢路径。
func loadIconDiskCached(filePath string, srcMod time.Time, srcOK bool) string {
	cacheKey := sanitizeIconName(filePath)
	cachePath := filepath.Join(iconCacheDir(), cacheKey+".png")

	// 源文件比缓存新 → 跳过缓存，重新提取
	if srcOK {
		if cacheInfo, err := os.Stat(cachePath); err == nil && srcMod.After(cacheInfo.ModTime()) {
			return extractAndCacheIcon(filePath, cachePath)
		}
	}

	// 1. 尝试读磁盘缓存
	if data, err := os.ReadFile(cachePath); err == nil {
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	}

	// 2. 没有缓存 → 真实提取
	return extractAndCacheIcon(filePath, cachePath)
}

// extractAndCacheIcon 调平台实现提取图标并落磁盘缓存，失败返回空串。
func extractAndCacheIcon(filePath, cachePath string) string {
	dataURL := extractIconRaw(filePath)
	if dataURL == "" {
		return ""
	}
	if idx := bytes.IndexByte([]byte(dataURL), ','); idx >= 0 {
		if pngData, err := base64.StdEncoding.DecodeString(dataURL[idx+1:]); err == nil {
			_ = os.WriteFile(cachePath, pngData, 0o644)
		}
	}
	return dataURL
}

// storeIconMem 写入进程内缓存（含提取失败的负缓存），并在超过上限时丢弃一半。
func storeIconMem(filePath string, mod time.Time, size int64, dataURL string) {
	e := iconMemEntry{modTime: mod, size: size, dataURL: dataURL, at: time.Now()}
	// Swap 而不是 Store：只有「新增一格」才需要动计数，覆盖已有 key 不该重复计数。
	if _, loaded := iconMemCache.Swap(filePath, e); !loaded {
		if iconMemCount.Add(1) > iconMemMaxEntries {
			trimIconMem()
		}
	}
}

// trimIconMem 把缓存砍到大约一半。
//
// 不引第三方 LRU：这里的 key 是「用户装了哪些 exe/lnk」，稳态下几乎不新增；
// 只有在批量导入/清理时才可能触发，用「随机丢一半」就足够，且不会引入新的锁竞争。
func trimIconMem() {
	drop := iconMemMaxEntries / 2
	n := 0
	iconMemCache.Range(func(k, _ any) bool {
		if n >= drop {
			return false
		}
		iconMemCache.Delete(k)
		n++
		return true
	})
	iconMemCount.Add(-int64(n))
}
