package platform

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 整机数据迁移包（跨机备份/恢复）：与同机「换数据目录」共用 ArchiveDataDir 的打包引擎，
// 但有两点不同：
//  1. 包里额外含一份 DB 的一致性快照（VACUUM INTO 产物，见 services 层）——
//     同机迁移靠重启后再导 DB，跨机包必须自包含，否则到新机器无从补 DB。
//  2. 包里含 manifest，记录导出时的源数据目录路径。导入时据此把写死在运行时配置里的
//     绝对路径从「源目录」改写成「目标目录」（跨机盘符/用户名几乎必然不同）。
//
// 依赖方向：本文件只依赖标准库，不 import db（db 反过来依赖 platform）。
// DB 快照的 VACUUM 与整体编排放在 services 层。

const (
	// TransferManifestName 迁移包内描述文件（固定名，导入端据此识别这是整机包）。
	TransferManifestName = "quickdock-transfer.json"
	// TransferDBName 迁移包内 DB 快照的文件名；解压后放回数据目录根即 quickdock.db。
	TransferDBName = "quickdock.db"
	// TransferVaultName 密码库便携包（口令加密，见 internal/credentials）。
	// 密码库的条目是用本机 DPAPI 的 DEK 加密的，逐行搬过去解不开，
	// 所以导出时解密后重新用用户口令封一份，导入时按同一口令还原。
	TransferVaultName = "quickdock-vault.qdv"
	// transferFormatVersion 包格式版本，后续字段有破坏性变化时递增。
	transferFormatVersion = 1

	// 解压安全上限：防止恶意/损坏的 zip 把磁盘塞满（参照插件安装的量级，整机包可大很多）。
	transferMaxUncompressed = 32 << 30 // 单包解压总量上限 32 GiB
	transferMaxSingleFile   = 16 << 30 // 单文件上限 16 GiB
	transferMaxEntries      = 800000   // 条目数上限（整机含运行时，17 万量级，留足余量）
)

// TransferManifest 整机迁移包描述文件。
type TransferManifest struct {
	Format    int    `json:"format"`
	SourceDir string `json:"sourceDir"` // 导出端的数据目录绝对路径（用于路径改写）
	CreatedAt string `json:"createdAt"` // RFC3339 时间，仅展示
	DBBytes   int64  `json:"dbBytes"`   // DB 快照大小，展示用
	// HasVault 包里带了一份密码库便携包（见 TransferVaultName）。
	// 旧包没有这个字段 → false，导入端据此决定要不要问口令。
	HasVault bool `json:"hasVault"`
}

// MarshalTransferManifest 序列化 manifest。
func MarshalTransferManifest(m TransferManifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// ParseTransferManifest 校验 manifest。缺失或不识别的格式直接报错，
// 避免把普通 zip（如插件包）当成整机包导入、覆盖掉用户数据。
func ParseTransferManifest(data []byte) (TransferManifest, error) {
	var m TransferManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("迁移包描述文件损坏: %w", err)
	}
	if m.Format != transferFormatVersion {
		return m, fmt.Errorf("迁移包格式版本不兼容（包内为 %d，当前支持 %d）", m.Format, transferFormatVersion)
	}
	if m.SourceDir == "" {
		return m, fmt.Errorf("迁移包缺少源数据目录信息")
	}
	return m, nil
}

// WriteTransferArchive 把数据目录打成整机迁移包。
//
// dbSnapshot 是外部已生成好的 DB 一致性快照文件（VACUUM INTO 产物），会作为
// quickdock.db 写进包根；manifest 记录源目录。vaultFile 非空时再塞一份密码库
// 便携包（口令加密）。其余文件内容与 ArchiveDataDir 一致。progress 可为 nil。
func WriteTransferArchive(src, dstZip, dbSnapshot, vaultFile string, m TransferManifest, progress archiveProgress) (int, int64, error) {
	manifestBytes, err := MarshalTransferManifest(m)
	if err != nil {
		return 0, 0, err
	}
	extra := []archiveExtra{
		{name: TransferDBName, srcFile: dbSnapshot},
		{name: TransferManifestName, data: manifestBytes},
	}
	if vaultFile != "" {
		extra = append(extra, archiveExtra{name: TransferVaultName, srcFile: vaultFile})
	}
	return archiveDataDir(src, dstZip, "", "", extra, progress)
}

// ReadTransferManifest 从已有 zip 里只读地取出 manifest，用于导入前的格式校验
// （在解压落盘之前就能拒绝不是整机包的 zip）。
func ReadTransferManifest(zipPath string) (TransferManifest, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return TransferManifest{}, fmt.Errorf("打开迁移包失败: %w", err)
	}
	defer zr.Close()
	for _, e := range zr.File {
		if e.Name != TransferManifestName {
			continue
		}
		r, err := e.Open()
		if err != nil {
			return TransferManifest{}, err
		}
		b, err := io.ReadAll(io.LimitReader(r, 1<<20))
		r.Close()
		if err != nil {
			return TransferManifest{}, err
		}
		return ParseTransferManifest(b)
	}
	return TransferManifest{}, fmt.Errorf("这不是 QuickDock 整机迁移包（缺少 %s）", TransferManifestName)
}

// ExtractTransferArchive 把整机包解压到 dst，并把写死在配置里的源数据目录路径改写为 dst。
//
// 与 ExtractDataDir 的区别：
//   - 校验包大小/条目数上限（zip bomb 防护）；
//   - 对需要改写路径的文本配置（runtime/go/env、gopath/env 等，见 pathRewriteFiles）
//     按 manifest.SourceDir → dst 改写——跨机时源与目标盘符/用户名不同，不改会让
//     便携运行时继续把模块缓存写回不存在的源路径。
//
// 解压是覆盖式的：同名文件覆盖，不删除 dst 里已有的其它文件（目标通常是新目录）。
func ExtractTransferArchive(zipPath, dst, sourceDir string, progress archiveProgress) error {
	if dst == "" {
		return fmt.Errorf("目标目录不能为空")
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开迁移包失败: %w", err)
	}
	defer zr.Close()

	var totalBytes int64
	for _, e := range zr.File {
		totalBytes += int64(e.UncompressedSize64)
	}
	if len(zr.File) > transferMaxEntries {
		return fmt.Errorf("迁移包条目过多（%d > %d），已拒绝", len(zr.File), transferMaxEntries)
	}
	if totalBytes > transferMaxUncompressed {
		return fmt.Errorf("迁移包解压后过大（%.1f GiB），已拒绝", float64(totalBytes)/(1<<30))
	}

	dstClean := filepath.Clean(dst)
	throttle := newProgressThrottle()
	var doneBytes int64
	var doneFiles int
	for _, e := range zr.File {
		if e.UncompressedSize64 > transferMaxSingleFile {
			return fmt.Errorf("迁移包内单文件过大: %s", e.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(e.Name))
		// 防目录穿越（zip slip）。
		if !strings.HasPrefix(filepath.Clean(target), dstClean+string(os.PathSeparator)) &&
			filepath.Clean(target) != dstClean {
			return fmt.Errorf("迁移包含非法路径: %s", e.Name)
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
		r, err := e.Open()
		if err != nil {
			return fmt.Errorf("读取迁移包条目失败 %s: %w", e.Name, err)
		}
		var werr error
		if rw := matchPathRewrite(e.Name, sourceDir, dst); rw != nil && sourceDir != "" {
			// 需要改写路径的文本配置：整份读进内存替换后再落盘（这类文件都很小）。
			var data []byte
			data, werr = io.ReadAll(io.LimitReader(r, transferMaxSingleFile))
			r.Close()
			if werr == nil {
				data = rw.oldToNew(data, sourceDir, dst)
				werr = os.WriteFile(target, data, 0600)
			}
		} else {
			var out *os.File
			out, werr = os.Create(target)
			if werr == nil {
				_, werr = io.Copy(out, r)
				out.Close()
			}
			r.Close()
		}
		if werr != nil {
			return fmt.Errorf("解压 %s 失败: %w", e.Name, werr)
		}
		doneBytes += int64(e.UncompressedSize64)
		doneFiles++
		last := doneFiles == len(zr.File)
		if progress != nil && (last || throttle.should()) {
			progress(doneFiles, len(zr.File), doneBytes, totalBytes, e.Name)
		}
	}
	return nil
}
