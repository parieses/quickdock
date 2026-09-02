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
	// 第一遍：收集条目名以确定是否需要剥离顶层目录
	names, headers, err := scanTar(archivePath)
	if err != nil {
		return err
	}
	top := commonTopDir(names)
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	var total int64
	for _, h := range headers {
		rel := stripTop(h.Name, top)
		if rel == "" {
			continue
		}
		target, err := safeJoin(dest, rel)
		if err != nil {
			return err
		}
		if h.Size > maxExtractFileSize {
			return fmt.Errorf("文件过大: %s", h.Name)
		}
		if total+h.Size > maxExtractTotal {
			return fmt.Errorf("解压总大小超出限制")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(h.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := extractTarFile(archivePath, h, target); err != nil {
				return err
			}
			total += h.Size
		}
	}
	return nil
}

// scanTar 两遍读取：先收集名字与头信息（不拷贝数据），供剥离判定与解压复用。
func scanTar(archivePath string) ([]string, []tar.Header, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var names []string
	var headers []tar.Header
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		names = append(names, hdr.Name)
		headers = append(headers, *hdr)
	}
	return names, headers, nil
}

// extractTarFile 重新打开归档并定位到指定头，将其内容写出到 target。
func extractTarFile(archivePath string, h tar.Header, target string) error {
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
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("未找到条目: %s", h.Name)
		}
		if err != nil {
			return err
		}
		if hdr.Name != h.Name {
			continue
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode))
		if err != nil {
			return err
		}
		if _, err := io.CopyN(out, tr, maxExtractFileSize); err != nil && err != io.EOF {
			out.Close()
			return err
		}
		out.Close()
		return nil
	}
}
