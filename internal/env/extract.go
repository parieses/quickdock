package env

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
)

const (
	maxExtractFileSize int64 = 1 << 30 // 1GB 单文件上限（防 zip/tar bomb）
	maxExtractTotal    int64 = 4 << 30 // 4GB 总解压上限
)

// errExtractTooLarge 实际写入字节超过限额。
//
// 为什么不能只信归档头里的声明值：压缩包头是可以伪造的（把 5GB 的条目声明成 10MB），
// 只比对声明值等于把防线交给攻击者。这里按「实际写出去了多少」计数，声明值只用来
// 提前快速失败（省掉一次无谓的解压）。
var errExtractTooLarge = errors.New("解压内容超出大小限制")

// ErrInstallCanceled 安装被调用方取消（用户点「取消安装」）。
// 单独成一个哨兵错误，是为了让服务层能区分「取消」与「真失败」——
// 取消不该弹红色报错，也不该让用户以为环境坏了。
var ErrInstallCanceled = errors.New("安装已取消")

// copyCount 把 src 拷到 dst，最多写 limit 字节；实际超过 limit 返回 errExtractTooLarge。
// 与 io.CopyN 的区别：CopyN 到上限就静默截断，会留下一个「看起来装好了」的残缺文件。
func copyCount(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, errExtractTooLarge
	}
	return n, nil
}

// Extract 根据扩展名自动选择解压方式，解压到 dest。
// 仅当归档内所有条目共享单一顶层目录时才剥离该目录（node / go / nginx 适用）；
// 扁平归档（PHP / redis 直接把 php.exe、ext/、lib/ 铺在根）保留原结构，避免子目录被误删。
// 支持 .zip 与 .tar.gz/.tgz；带路径穿越防护与体积上限。
//
// ctx 取消时尽快返回（逐条目检查 + 读取时中断），供「安装可取消」使用：
// 大归档解压动辄几十秒，只在下载阶段响应取消等于取消不了。
func Extract(ctx context.Context, archivePath, dest string) error {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(ctx, archivePath, dest)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(ctx, archivePath, dest)
	default:
		return fmt.Errorf("不支持的压缩格式: %s", archivePath)
	}
}

// firstSeg 返回路径的首个目录段（以 / 分隔）；无斜杠时返回整个名字。
// 用于判断归档是否为「单一顶层目录 + 内容」结构。
func firstSeg(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// commonTopDir 若所有名字共享同一顶层目录段则返回该段，否则返回空串（不剥离）。
func commonTopDir(names []string) string {
	if len(names) == 0 {
		return ""
	}
	top := firstSeg(names[0])
	if top == "" {
		return ""
	}
	for _, n := range names {
		if firstSeg(n) != top {
			return ""
		}
	}
	return top
}

// stripTop 若 name 以 top/ 开头则去掉该前缀，否则原样返回。
func stripTop(name, top string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if top == "" {
		return name
	}
	prefix := top + "/"
	if strings.HasPrefix(name, prefix) {
		return name[len(prefix):]
	}
	return name
}

func safeJoin(dest, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("非法路径: 空")
	}
	target := filepath.Join(dest, rel)
	cleanDest := filepath.Clean(dest)
	if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
		return "", fmt.Errorf("非法路径: %s", rel)
	}
	return target, nil
}

func extractZip(ctx context.Context, archivePath, dest string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	names := make([]string, 0, len(r.File))
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	top := commonTopDir(names)
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	var total int64
	for _, f := range r.File {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("解压已取消: %w", err)
		}
		rel := stripTop(f.Name, top)
		if rel == "" {
			continue
		}
		target, err := safeJoin(dest, rel)
		if err != nil {
			return err
		}
		if f.UncompressedSize64 > uint64(maxExtractFileSize) {
			return fmt.Errorf("文件过大: %s", f.Name)
		}
		if total+int64(f.UncompressedSize64) > maxExtractTotal {
			return fmt.Errorf("解压总大小超出限制")
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		// 按实际写入字节计限额；声明值可能伪造，只作为提前失败的快路径。
		remain := maxExtractFileSize
		if total > 0 {
			remain = maxExtractTotal - total
			if remain > maxExtractFileSize {
				remain = maxExtractFileSize
			}
		}
		n, cerr := copyCount(out, ctxReader{ctx: ctx, r: rc}, remain)
		out.Close()
		rc.Close()
		if cerr != nil {
			os.Remove(target)
			if errors.Is(cerr, errExtractTooLarge) {
				return fmt.Errorf("%w: %s", errExtractTooLarge, f.Name)
			}
			if errors.Is(cerr, context.Canceled) || errors.Is(cerr, context.DeadlineExceeded) {
				return fmt.Errorf("解压已取消: %w", cerr)
			}
			return cerr
		}
		total += n
	}
	return nil
}

func extractTarGz(ctx context.Context, archivePath, dest string) error {
	// 第一遍：仅收集条目名以确定是否需要剥离顶层目录（轻量，不拷贝数据）
	names, err := scanTarNames(ctx, archivePath)
	if err != nil {
		return err
	}
	top := commonTopDir(names)
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	// 第二遍：单次流式遍历归档，边读边写出（O(n)）。原实现为每个文件重开归档重扫到目标
	// 条目，N 个文件退化为 O(n²)，Node/Go 这类大归档解压会卡死。
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("解压已取消: %w", err)
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel := stripTop(hdr.Name, top)
		if rel == "" {
			continue
		}
		target, err := safeJoin(dest, rel)
		if err != nil {
			return err
		}
		if hdr.Size > maxExtractFileSize {
			return fmt.Errorf("文件过大: %s", hdr.Name)
		}
		if total+hdr.Size > maxExtractTotal {
			return fmt.Errorf("解压总大小超出限制")
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			// 同 zip：按实际写入字节计限额，声明值只作提前失败。
			remain := maxExtractFileSize
			if total > 0 {
				remain = maxExtractTotal - total
				if remain > maxExtractFileSize {
					remain = maxExtractFileSize
				}
			}
			n, cerr := copyCount(out, ctxReader{ctx: ctx, r: tr}, remain)
			out.Close()
			if cerr != nil {
				os.Remove(target)
				if errors.Is(cerr, errExtractTooLarge) {
					return fmt.Errorf("%w: %s", errExtractTooLarge, hdr.Name)
				}
				if errors.Is(cerr, context.Canceled) || errors.Is(cerr, context.DeadlineExceeded) {
					return fmt.Errorf("解压已取消: %w", cerr)
				}
				return cerr
			}
			total += n
		}
	}
	return nil
}

// ctxReader 在每次 Read 前检查 ctx，取消时立刻返回错误——
// 否则解压一个几 GB 的条目期间取消要等到该文件写完才生效。
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// scanTarNames 单次扫描归档收集所有条目名（轻量，不拷贝数据），供剥离顶层目录判定使用。
func scanTarNames(ctx context.Context, archivePath string) ([]string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var names []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("解压已取消: %w", err)
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		names = append(names, hdr.Name)
	}
	return names, nil
}

// mergeInto 将 src 下的条目移动/合并到 dst（解压后布局提升的公共工具）：
//   - 目标不存在 → 直接移动；
//   - 目标存在且同为目录 → 递归合并；
//   - 目标存在且同为文件（含大小写差异的同名重复）→ 跳过源文件（保留目标已有那份）；
//   - 其他冲突（文件 vs 目录）→ 返回错误。
//
// 调用方：apache（提升 Apache24/）、gh（提升 gh_*/ 子目录）等顶层多一层包裹的归档。
func mergeInto(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		toFi, err := os.Lstat(to)
		if err != nil {
			if err := os.Rename(from, to); err != nil {
				return fmt.Errorf("移动 %s 失败: %w", from, err)
			}
			continue
		}
		if e.IsDir() {
			if !toFi.IsDir() {
				return fmt.Errorf("合并冲突：%s 既是目录又是文件", to)
			}
			if err := mergeInto(from, to); err != nil {
				return err
			}
			continue
		}
		// 文件同名（大小写不敏感 fs 上 ReadMe.txt 与 README.txt 视为同一）重复，跳过源文件
		logger.W("[env][extract] 跳过重复文件 %s（目标已存在同名文件）", to)
		if err := os.Remove(from); err != nil {
			logger.W("[env][extract] 删除重复源文件 %s 失败: %v", from, err)
		}
	}
	return nil
}
