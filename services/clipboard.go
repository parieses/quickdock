package services

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/platform"
)

// ===== 剪贴板历史 =====

func (a *AppService) ListClipboardEntries(limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListClipboardEntries(limit)
	return wrap(data, err)
}

func (a *AppService) InsertClipboardEntry(text, sourceApp string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.InsertClipboardEntry(text, sourceApp)
	return wrap(data, err)
}

func (a *AppService) DeleteExpiredClipboardEntries() (int64, error) {
	if a.DB == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	days, _ := a.DB.GetClipboardRetentionDays()
	return a.DB.DeleteExpiredClipboardEntries(days)
}

// writeEntryToClipboard 将一条剪贴板条目写入系统剪贴板（image/file/text 三分支）。
func (a *AppService) writeEntryToClipboard(entry *db.ClipboardEntry, hwnd uintptr) error {
	switch {
	case entry.ContentType == "image" && entry.ImagePath != "":
		if entry.TextContent != "" {
			_ = platform.SetClipboardFiles(hwnd, strings.Split(entry.TextContent, "\n"))
		}
		if err := platform.SetClipboardImage(hwnd, entry.ImagePath); err != nil {
			return fmt.Errorf("图片写入剪贴板失败: %v", err)
		}
	case entry.ContentType == "file" && entry.TextContent != "":
		if err := platform.SetClipboardFiles(hwnd, strings.Split(entry.TextContent, "\n")); err != nil {
			return fmt.Errorf("文件写入剪贴板失败: %v", err)
		}
	default:
		SetClipboardText(entry.TextContent)
	}
	return nil
}
func (a *AppService) CopyClipboardEntry(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entry, err := a.DB.GetClipboardEntry(id)
	if err != nil {
		return Fail(fmt.Errorf("获取剪贴板条目失败: %v", err))
	}
	hwnd := uintptr(a.HiddenHWND.Load())
	if err := a.writeEntryToClipboard(entry, hwnd); err != nil {
		return Fail(err)
	}
	if err := a.DB.IncrementClipboardCopyCount(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) GetClipboardRetentionDays() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	days, err := a.DB.GetClipboardRetentionDays()
	return wrap(days, err)
}

func (a *AppService) SetClipboardRetentionDays(days int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.SetClipboardRetentionDays(days); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// GetClipboardImageBase64 获取剪贴板图片的 base64 数据 URI
func (a *AppService) GetClipboardImageBase64(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entry, err := a.DB.GetClipboardEntry(id)
	if err != nil {
		return Fail(fmt.Errorf("获取条目失败: %v", err))
	}
	if entry.ContentType != "image" || entry.ImagePath == "" {
		return FailMsg("该条目不是图片")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return Fail(fmt.Errorf("获取用户目录失败: %v", err))
	}
	allowedDir := filepath.Join(homeDir, ".quickdock", "images") + string(filepath.Separator)
	absPath, err := filepath.Abs(entry.ImagePath)
	if err != nil {
		return Fail(fmt.Errorf("路径解析失败: %v", err))
	}
	if !strings.HasPrefix(absPath, allowedDir) {
		return FailMsg("不允许读取该路径下的文件")
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return Fail(fmt.Errorf("读取图片失败: %v", err))
	}
	return Ok(base64.StdEncoding.EncodeToString(data))
}

func (a *AppService) CleanupClipboardNow() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	days, _ := a.DB.GetClipboardRetentionDays()
	count, err := a.DB.DeleteExpiredClipboardEntries(days)
	return wrap(count, err)
}

func (a *AppService) TogglePinClipboardEntry(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	pinned, err := a.DB.TogglePinClipboardEntry(id)
	return wrap(pinned, err)
}

func (a *AppService) DeleteClipboardEntry(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteClipboardEntry(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// PasteClipboardEntry 复制剪贴板条目并模拟 Ctrl+V 粘贴
func (a *AppService) PasteClipboardEntry(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entry, err := a.DB.GetClipboardEntry(id)
	if err != nil {
		return Fail(fmt.Errorf("获取剪贴板条目失败: %v", err))
	}
	hwnd := uintptr(a.HiddenHWND.Load())
	if err := a.writeEntryToClipboard(entry, hwnd); err != nil {
		return Fail(err)
	}
	a.HideClipboardWindow()
	go func() {
		time.Sleep(80 * time.Millisecond)
		platform.SimulatePaste()
		_ = a.DB.IncrementClipboardCopyCount(id)
	}()
	return Ok(nil)
}

// ===== 窗口隐藏 =====

// HideWindow 隐藏主窗口
func (a *AppService) HideWindow() {
	if a.ClipboardMode != nil {
		a.ClipboardMode.Store(false)
	}
	if a.WindowVisible != nil {
		a.WindowVisible.Store(false)
	}
	if win := a.MainWindow; win != nil {
		win.Hide()
	}
}

// HideClipboardWindow 隐藏剪贴板独立窗口
func (a *AppService) HideClipboardWindow() {
	if a.ClipboardMode != nil {
		a.ClipboardMode.Store(false)
	}
	if fn := a.GetClipboardWindow; fn != nil {
		if win := fn(); win != nil {
			win.Hide()
		}
	}
}

// ===== 热键控制 =====

func (a *AppService) SuspendHotkeys() {
	if a.SuspendHotkeysFn != nil {
		a.SuspendHotkeysFn()
	}
}

func (a *AppService) ResumeHotkeys() {
	if a.ResumeHotkeysFn != nil {
		a.ResumeHotkeysFn()
	}
}
