//go:build darwin || linux

package platform

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// moveToTrash 分派到各平台的回收站实现。
func moveToTrash(abs string) error {
	if runtime.GOOS == "darwin" {
		return moveToTrashDarwin(abs)
	}
	return moveToTrashXDG(abs)
}

// moveToTrashDarwin 移动到 ~/.Trash。
//
// 为什么不用 osascript 调 Finder（原方案里写的那条路）：它会触发
// 「QuickDock 想要控制 Finder」的自动化授权弹窗，还要求 Finder 正在运行——
// 对一个本该静默完成的删除来说成本过高。
// 代价：不支持 Finder 的「放回原处」（那依赖 .DS_Store 记录原始位置），
// 但文件确实进了废纸篓、可被清空前找回，可恢复这个核心承诺没有丢。
func moveToTrashDarwin(abs string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("无法定位用户主目录: %w", err)
	}
	trashDir := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return fmt.Errorf("创建废纸篓目录失败: %w", err)
	}
	name, err := uniqueTrashName(trashDir, filepath.Base(abs))
	if err != nil {
		return err
	}
	if err := os.Rename(abs, filepath.Join(trashDir, name)); err != nil {
		return fmt.Errorf("移入废纸篓失败: %w", err)
	}
	return nil
}

// moveToTrashXDG 按 XDG Trash 规范把路径移到 $XDG_DATA_HOME/Trash（默认 ~/.local/share/Trash）。
//
// 规范结构：files/ 存实体，info/<name>.trashinfo 记录原始路径与删除时间——
// 缺了 info 文件，回收站里会多出一个无法还原的孤儿，所以 info 写失败时要把文件挪回去。
//
// 不做的事：
//   - 不调用 gio / trash-put：依赖发行版预装，且行为不可控。规范实现约 40 行，换来零外部依赖。
//   - 不做跨文件系统的顶层 .Trash-$UID（同一设备才放行，跨设备直接报错），
//     避免为了「删得掉」而退化成复制+永久删除。
func moveToTrashXDG(abs string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("无法定位用户主目录: %w", err)
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "share")
	}
	trashDir := filepath.Join(base, "Trash")
	filesDir := filepath.Join(trashDir, "files")
	infoDir := filepath.Join(trashDir, "info")
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return fmt.Errorf("创建回收站目录失败: %w", err)
	}
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return fmt.Errorf("创建回收站目录失败: %w", err)
	}
	if !sameDevice(abs, filesDir) {
		return fmt.Errorf("拒绝删除 %s：目标与回收站不在同一文件系统，且未实现顶层 .Trash-$UID", abs)
	}

	name, err := uniqueTrashName(filesDir, filepath.Base(abs))
	if err != nil {
		return err
	}
	dst := filepath.Join(filesDir, name)
	if err := os.Rename(abs, dst); err != nil {
		return fmt.Errorf("移入回收站失败: %w", err)
	}

	info := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		(&url.URL{Path: abs}).EscapedPath(), time.Now().Format("2006-01-02T15:04:05"))
	if err := os.WriteFile(filepath.Join(infoDir, name+".trashinfo"), []byte(info), 0o600); err != nil {
		// 文件已经不在原位、回收站里又没有它的来源信息 = 两边都够不着。
		// 把它挪回去比留下一个「看得见却还原不了」的孤儿好。
		_ = os.Rename(dst, abs)
		return fmt.Errorf("写入 trashinfo 失败，已撤销移动: %w", err)
	}
	return nil
}

// sameDevice 判断两个路径是否在同一文件系统（同 Dev 号）。
// XDG 的 files/ 与源必须同设备，rename 才能保持「移动」而非「复制+删除」。
func sameDevice(a, b string) bool {
	var sa, sb syscall.Stat_t
	if err := syscall.Lstat(a, &sa); err != nil {
		return false
	}
	if err := syscall.Lstat(b, &sb); err != nil {
		return false
	}
	return sa.Dev == sb.Dev
}
