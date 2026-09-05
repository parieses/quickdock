package env

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxExtractFileSize int64 = 1 << 30 // 1GB 单文件上限（防 zip/tar bomb）
	maxExtractTotal    int64 = 4 << 30 // 4GB 总解压上限
)

// Extract 根据扩展名自动选择解压方式，解压到 dest。
// 仅当归档内所有条目共享单一顶层目录时才剥离该目录（node / go / nginx 适用）；
// 扁平归档（PHP / redis 直接把 php.exe、ext/、lib/ 铺在根）保留原结构，避免子目录被误删。
// 支持 .zip 与 .tar.gz/.tgz；带路径穿越防护与体积上限。
func Extract(archivePath, dest string) error {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archivePath, dest)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(archivePath, dest)
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

func extractZip(archivePath, dest string) error {
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
		if _, err := io.CopyN(out, rc, maxExtractFileSize); err != nil && err != io.EOF {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
		total += int64(f.UncompressedSize64)
	}
	return nil
}

func extractTarGz(archivePath, dest string) error {
	// 第一遍：仅收集条目名以确定是否需要剥离顶层目录（轻量，不拷贝数据）
	names, err := scanTarNames(archivePath)
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
			if _, err := io.CopyN(out, tr, maxExtractFileSize); err != nil && err != io.EOF {
				out.Close()
				return err
			}
			out.Close()
			total += hdr.Size
		}
	}
	return nil
}

// scanTarNames 单次扫描归档收集所有条目名（轻量，不拷贝数据），供剥离顶层目录判定使用。
func scanTarNames(archivePath string) ([]string, error) {
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

