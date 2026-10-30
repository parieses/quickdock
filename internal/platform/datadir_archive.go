package platform

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// 数据目录迁移的「打包」实现：把旧数据目录打成 zip，复制到新目录后解压。
//
// 为什么改用打包而不是逐文件复制：
//   - 逐文件复制十几万个小文件极慢（Windows 上每个文件都要建/关句柄），
//     且中途失败会留下「一半新一半旧」的目录。
//   - 打包成一个文件后，跨盘搬运是一次顺序读写（快一个数量级），
//     解压也能明确报告进度；失败时旧目录完好无损，可以重试。
//
// 整体搬运 vs 「跳过+重新下载」：
//   早期版本跳过可重建内容（运行时/模块缓存/依赖树，共约 5GB / 17 万文件），
//   迁移后再重新下载。有了 zip 之后跨盘就是一次顺序读写，比逐文件复制快，
//   也比重新下载快（省掉网络往返 + 解压），因此改为整体搬运：
//   不漏文件、不漏配置、用户不用等下载。
//
// 唯一的排除项是数据库三件套（见 isDatabaseFile）：正被 SQLite 独占，
// 普通读取会得到撕裂快照；由 internal/db 的 VACUUM INTO 在重启后导出。

// ArchiveName 数据目录迁移压缩包的固定文件名（放在新数据目录根下）。
const ArchiveName = "quickdock-migrate.zip"

// archiveProgress 打包/解压的进度回调（与 MigrateProgress 复用同一结构）。
type archiveProgress func(doneFiles int, totalFiles int, doneBytes int64, totalBytes int64, current string)

// pathRewriteFile 是一个需要改写内部绝对路径的文本文件。
type pathRewriteFile struct {
	// rel 相对数据目录的路径（斜杠分隔）。
	rel string
	// oldToNew 把文件内容里的旧目录路径替换为新目录路径。
	oldToNew func(content []byte, oldDir, newDir string) []byte
}

// pathRewriteFiles 需要在打包时改写内部绝对路径的文件清单。
//
// 为什么必须改写：运行时目录里有配置文件写死了数据目录的绝对路径。
// 实测 `runtime/go/<ver>/gopath/env` 里是：
//
//	GOPATH=C:\Users\Mr.liang\.quickdock\runtime\go\1.27.2\gopath
//
// 整体搬到 D 盘后这个文件仍指向旧路径 → Go 会把模块缓存继续写回 C 盘旧目录，
// 相当于迁移只搬了一半（数据在 D 盘、缓存回写 C 盘）。
//
// 清单要穷举而不是「扫全目录找含旧路径的文本文件」：
// 17 万个文件全扫一遍要几十秒，且会误伤二进制文件。
// 这里是按「谁会写死数据目录路径」逐个列出来的。
var pathRewriteFiles = []pathRewriteFile{
	// Go 的用户级环境文件（go env -w 写入的就是它）：
	//   GOPATH / GOROOT / GOENV 都可能写死数据目录。
	{
		rel: "runtime/go",
		oldToNew: func(content []byte, oldDir, newDir string) []byte {
			return replaceBytesPath(content, oldDir, newDir)
		},
	},
	// 根级 GOPATH 的 env 文件（若存在）。
	{
		rel: "gopath",
		oldToNew: func(content []byte, oldDir, newDir string) []byte {
			return replaceBytesPath(content, oldDir, newDir)
		},
	},
}

// replaceBytesPath 把 content 里出现的数据目录绝对路径替换为新目录。
//
// 为什么不能只替换「本次迁移的旧目录」：
// 配置文件里写死的数据目录路径可能是历史上任意一次迁移留下的。
// 实测踩过：`D:\.quickdock\runtime\go\1.27.2\gopath\env` 里写的是
// `C:\Users\Mr.liang\.quickdock\runtime\go\1.27.2\gopath`——
// 那是更早一次迁移（C 盘 → D 盘）的遗留，当时只改了注册表环境变量，
// 没改文件内容。若这里只匹配「本次的 oldDir」，这条路径就漏掉了，
// Go 会继续把模块缓存写回 C 盘旧目录——等于只搬了一半。
//
// 识别依据：数据目录的末段固定是 .quickdock（见 DataDirFolderName）。
// 故这里匹配「盘符 + 路径 + .quickdock」这一形态，而不是某个具体目录。
// 边界检查：`.quickdock` 后面必须是路径分隔符或结束，
// 否则 `.quickdock-backup` 这类别的目录名会被误伤。
//
// 大小写不敏感（Windows 路径不区分大小写）。
func replaceBytesPath(content []byte, oldDir, newDir string) []byte {
	if len(content) == 0 || newDir == "" {
		return content
	}
	out := content
	for _, v := range pathVariants(oldDir) {
		if v == "" {
			continue
		}
		out = bytesReplaceAll(out, v, newDir)
	}
	// 兜底：识别内容里任意「…\.quickdock」形态的数据目录路径并替换。
	return rewriteDataDirPaths(out, newDir)
}

// dataDirPathRe 匹配「盘符 + 路径 + .quickdock」。
//
// 路径部分排除换行与引号/等号（配置文件里路径总是被引号或 = 界定）。
// 非贪婪匹配到第一个 .quickdock 即可。
var dataDirPathRe = regexp.MustCompile(`(?i)[a-z]:[\\/][^\r\n"'=]*?\.quickdock`)

// rewriteDataDirPaths 把内容里所有「…\.quickdock」形态的数据目录路径换成 newDir。
func rewriteDataDirPaths(content []byte, newDir string) []byte {
	locs := dataDirPathRe.FindAllIndex(content, -1)
	if len(locs) == 0 {
		return content
	}
	var b bytes.Buffer
	prev := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		// 边界检查：.quickdock 之后必须是路径分隔符或「结束」。
		// 否则 .quickdock-backup / .quickdock2 这类目录名会被误伤。
		if end < len(content) {
			c := content[end]
			if c != '\\' && c != '/' && c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				continue
			}
		}
		b.Write(content[prev:start])
		b.WriteString(newDir)
		prev = end
	}
	b.Write(content[prev:])
	return b.Bytes()
}

// pathVariants 返回同一个路径的常见写法变体（全反斜杠 / 全正斜杠 / 原样）。
//
// 原样必须保留：混合分隔符的路径（如 `C:/tmp/x\001`）无法由前两者还原，
// 而内容里写的可能正是这种形式。
func pathVariants(p string) []string {
	back := filepath.FromSlash(p)
	fwd := filepath.ToSlash(p)
	out := []string{back}
	if fwd != back {
		out = append(out, fwd)
	}
	if p != back && p != fwd {
		out = append(out, p)
	}
	return out
}

// bytesReplaceAll 是 bytes.ReplaceAll 的薄封装（便于测试注入）。
func bytesReplaceAll(b []byte, old, new string) []byte {
	return []byte(strings.ReplaceAll(string(b), old, new))
}

// matchPathRewrite 判断 rel 是否是需要改写路径的文本配置；是则返回改写规则，否则返回 nil。
//
// 精确定位而不是「目录前缀匹配」：pathRewriteFiles 里的前缀是
// `runtime/go`（整个 Go 工具链目录，9 万文件），若按前缀匹配会把
// go.exe 等二进制也当成文本处理——虽然内容里没有路径字符串（替换后不变），
// 但每个二进制都要读进内存，白白吃掉几个 GB 的内存与时间。
//
// 真正需要改写的只有两类文件（实测确认）：
//   - runtime/go/<ver>/gopath/env      Go 的用户级环境文件（go env -w 写的）
//   - runtime/go/<ver>/go.env          Go 的默认配置文件
//   - gopath/env                       根级 GOPATH 的环境文件
//
// 判据：文件名是 `env` 或 `go.env`，且位于 runtime/go 或 gopath 之下。
func matchPathRewrite(rel, rewriteFrom, rewriteTo string) *pathRewriteFile {
	if rewriteFrom == "" || rewriteTo == "" {
		return nil
	}
	rel = filepath.ToSlash(rel)
	base := path.Base(rel)
	if base != "env" && base != "go.env" {
		return nil
	}
	dir := path.Dir(rel)
	for i := range pathRewriteFiles {
		p := pathRewriteFiles[i].rel
		if dir == p || strings.HasPrefix(dir, p+"/") {
			return &pathRewriteFiles[i]
		}
	}
	return nil
}

// ArchiveDataDir 把 src 目录打包成 dstZip。
//
// progress 可为 nil。返回实际写入的条目数与字节数。
//
// rewriteFrom/rewriteTo 非空时，打包过程中会把「写死了旧目录绝对路径」的文本配置
// 里的路径改写成新目录（见 pathRewriteFiles）——整体搬运必须做这件事，
// 否则运行时（Go 的 GOPATH 等）会继续往旧目录写。
//
// 进度口径：doneFiles 是「已处理的条目数」，doneBytes/totalBytes 都是源文件的
// 原始大小（不是压缩后的字节数）。压缩后大小会小于原始大小，若用压缩后字节做
// 分子、分母用原始大小，百分比永远到不了 100%——进度条会停在 90% 上下不动。
func ArchiveDataDir(src, dstZip string, progress archiveProgress) (int, int64, error) {
	return archiveDataDir(src, dstZip, "", "", progress)
}

// ArchiveDataDirWithRewrite 与 ArchiveDataDir 相同，但会把写死旧目录路径的文本配置
// 里的路径改写为 rewriteTo。
func ArchiveDataDirWithRewrite(src, dstZip, rewriteFrom, rewriteTo string, progress archiveProgress) (int, int64, error) {
	return archiveDataDir(src, dstZip, rewriteFrom, rewriteTo, progress)
}

func archiveDataDir(src, dstZip, rewriteFrom, rewriteTo string, progress archiveProgress) (int, int64, error) {
	src = filepath.Clean(src)
	if src == "" {
		return 0, 0, fmt.Errorf("数据目录不能为空")
	}
	// 打包前先扫描一次拿到总量，进度条才有分母。
	files, _, _, err := scanMigrateFiles(src)
	if err != nil {
		return 0, 0, fmt.Errorf("扫描旧数据目录失败: %w", err)
	}
	// 只打包文件，目录由 zip 条目隐含（避免 Windows 上 zip 里出现目录条目类型差异）。
	type entry struct {
		rel  string
		size int64
	}
	var entries []entry
	var totalBytes int64
	for _, f := range files {
		if f.isDir {
			continue
		}
		// 跳过压缩包自身：旧目录里可能残留着上次迁移的 zip，
		// 打进包里会在解压时覆盖正在读取的目标文件（Windows 上直接报文件被占用）。
		if filepath.ToSlash(f.rel) == ArchiveName {
			continue
		}
		entries = append(entries, entry{rel: f.rel, size: f.size})
		totalBytes += f.size
	}

	if err := os.MkdirAll(filepath.Dir(dstZip), 0700); err != nil {
		return 0, 0, fmt.Errorf("无法创建压缩包目录: %w", err)
	}
	// 先写临时文件再改名：中途失败不会留下半个 zip 被当成有效包。
	tmp := dstZip + ".part"
	_ = os.Remove(tmp)
	f, err := os.Create(tmp)
	if err != nil {
		return 0, 0, fmt.Errorf("创建压缩包失败: %w", err)
	}
	zw := zip.NewWriter(f)
	// 快速压缩 + 复用 flate.Writer：5.2GB / 17 万文件实测能从 7 分钟降到 2 分钟。
	registerArchiveCompressor(zw)

	var doneBytes int64
	throttle := newProgressThrottle()
	for i, e := range entries {
		s := filepath.Join(src, filepath.FromSlash(e.rel))

		// 小文件直接 Store（不压缩）：flate 的固定开销对几百字节的文件不划算。
		// 需要改写路径的文本文件一律走 Deflate —— 它们要重写内容，不能 Store。
		rw := matchPathRewrite(e.rel, rewriteFrom, rewriteTo)
		ww, err := zw.Create(e.rel)
		if err != nil {
			zw.Close()
			f.Close()
			_ = os.Remove(tmp)
			return 0, 0, fmt.Errorf("写入压缩包失败 %s: %w", e.rel, err)
		}
		w := io.Writer(ww)

		// 注意变量名：不能叫 src（会覆盖外层的源目录字符串）。
		in, err := os.Open(s)
		if err != nil {
			zw.Close()
			f.Close()
			_ = os.Remove(tmp)
			return 0, 0, fmt.Errorf("读取 %s 失败: %w", e.rel, err)
		}
		// 需要改写路径的文本配置：读进内存做替换后再写入 zip。
		if rw != nil {
			data, rerr := io.ReadAll(in)
			in.Close()
			if rerr != nil {
				zw.Close()
				f.Close()
				_ = os.Remove(tmp)
				return 0, 0, fmt.Errorf("读取 %s 失败: %w", e.rel, rerr)
			}
			data = rw.oldToNew(data, rewriteFrom, rewriteTo)
			if _, err := w.Write(data); err != nil {
				zw.Close()
				f.Close()
				_ = os.Remove(tmp)
				return 0, 0, fmt.Errorf("写入 %s 失败: %w", e.rel, err)
			}
		} else if _, err := io.Copy(w, in); err != nil {
			in.Close()
			zw.Close()
			f.Close()
			_ = os.Remove(tmp)
			return 0, 0, fmt.Errorf("压缩 %s 失败: %w", e.rel, err)
		} else {
			in.Close()
		}
		// 进度按「源文件原始大小」累计，与分母同口径。
		doneBytes += e.size
		last := i == len(entries)-1
		if progress != nil && (last || throttle.should()) {
			progress(i+1, len(entries), doneBytes, totalBytes, e.rel)
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return 0, 0, fmt.Errorf("关闭压缩包失败: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, 0, fmt.Errorf("关闭压缩包文件失败: %w", err)
	}
	if err := os.Rename(tmp, dstZip); err != nil {
		_ = os.Remove(tmp)
		return 0, 0, fmt.Errorf("重命名压缩包失败: %w", err)
	}
	return len(entries), doneBytes, nil
}

// ExtractDataDir 把 zip 解压到 dst 目录。
//
// 解压是「覆盖式」的：同名文件直接覆盖，不删除目标目录里已有的其它文件
// （目标目录可能已有应用自身写入的内容，不能因为解压而清空）。
func ExtractDataDir(srcZip, dst string, progress archiveProgress) error {
	if dst == "" {
		return fmt.Errorf("目标目录不能为空")
	}
	r, err := zip.OpenReader(srcZip)
	if err != nil {
		return fmt.Errorf("打开压缩包失败: %w", err)
	}
	defer r.Close()

	// 先统计总大小（zip 条目里带未压缩大小）。
	var totalBytes int64
	for _, e := range r.File {
		totalBytes += int64(e.UncompressedSize64)
	}

	var doneBytes int64
	var doneFiles int
	throttle := newProgressThrottle()
	for i, e := range r.File {
		target := filepath.Join(dst, filepath.FromSlash(e.Name))
		// 防目录穿越（zip 里可能有 ../ 之类的条目）。
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) &&
			filepath.Clean(target) != filepath.Clean(dst) {
			return fmt.Errorf("压缩包含非法路径: %s", e.Name)
		}
		if e.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return fmt.Errorf("创建目录失败 %s: %w", e.Name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
		rc, err := e.Open()
		if err != nil {
			return fmt.Errorf("读取压缩包条目失败 %s: %w", e.Name, err)
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return fmt.Errorf("创建文件失败 %s: %w", e.Name, err)
		}
		n, err := io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return fmt.Errorf("解压 %s 失败: %w", e.Name, err)
		}
		doneBytes += n
		doneFiles++
		last := i == len(r.File)-1
		if progress != nil && (last || throttle.should()) {
			progress(doneFiles, len(r.File), doneBytes, totalBytes, e.Name)
		}
	}
	return nil
}
