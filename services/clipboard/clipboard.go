package clipboard

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/platform"
	"quickdock/services"
)

// ===== 剪贴板历史 =====

// CopyText 将文本写入系统剪贴板（命令面板结果复制用）。
// 直接走 Wails Clipboard.SetText，避免 WebView2 中 navigator.clipboard
// 在文档未聚焦时被静默拦截的问题。
func (a *ClipboardService) CopyText(text string) *services.ApiResult {
	services.SetClipboardText(text)
	return services.Ok(nil)
}

func (a *ClipboardService) ListClipboardEntries(limit int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.App.DB.ListClipboardEntries(limit)
	return services.Wrap(data, err)
}

func (a *ClipboardService) InsertClipboardEntry(text, sourceApp string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.App.DB.InsertClipboardEntry(text, sourceApp)
	return services.Wrap(data, err)
}

func (a *ClipboardService) DeleteExpiredClipboardEntries() (int64, error) {
	if a.App.DB == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	days, _ := a.App.DB.GetClipboardRetentionDays()
	return a.App.DB.DeleteExpiredClipboardEntries(days)
}

// writeEntryToClipboard 将一条剪贴板条目写入系统剪贴板（image/file/text 三分支）。
func (a *ClipboardService) writeEntryToClipboard(entry *db.ClipboardEntry, hwnd uintptr) error {
	switch {
	case entry.ContentType == "image" && entry.ImagePath != "":
		if entry.TextContent != "" {
			_ = platform.SetClipboardFiles(hwnd, strings.Split(entry.TextContent, "\n"))
		}
		if err := platform.SetClipboardImage(hwnd, entry.ImagePath); err != nil {
			return fmt.Errorf("图片写入剪贴板失败: %v", err)
		}
		// 回环防护：记录刚写回剪贴板的图片哈希，使后续捕获能识别"从历史复制的图片"并跳过。
		// 该哈希口径与 processImage 的 DIB→PNG MD5 一致（PNG 文件经 DIB 往返后重编码字节确定性相同）。
		if data, e := os.ReadFile(entry.ImagePath); e == nil {
			services.SetLastClipboardImageHash(platform.MD5Hash(data))
		}
	case entry.ContentType == "file" && entry.TextContent != "":
		if err := platform.SetClipboardFiles(hwnd, strings.Split(entry.TextContent, "\n")); err != nil {
			return fmt.Errorf("文件写入剪贴板失败: %v", err)
		}
	default:
		services.SetClipboardText(entry.TextContent)
	}
	return nil
}
func (a *ClipboardService) CopyClipboardEntry(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entry, err := a.App.DB.GetClipboardEntry(id)
	if err != nil {
		return services.Fail(fmt.Errorf("获取剪贴板条目失败: %v", err))
	}
	hwnd := platform.ClipboardWindowHandle()
	if err := a.writeEntryToClipboard(entry, hwnd); err != nil {
		return services.Fail(err)
	}
	if err := a.App.DB.IncrementClipboardCopyCount(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

func (a *ClipboardService) GetClipboardRetentionDays() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	days, err := a.App.DB.GetClipboardRetentionDays()
	return services.Wrap(days, err)
}

func (a *ClipboardService) SetClipboardRetentionDays(days int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.SetClipboardRetentionDays(days); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// GetClipboardImageBase64 获取剪贴板图片的 base64 数据 URI
func (a *ClipboardService) GetClipboardImageBase64(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entry, err := a.App.DB.GetClipboardEntry(id)
	if err != nil {
		return services.Fail(fmt.Errorf("获取条目失败: %v", err))
	}
	if entry.ContentType != "image" || entry.ImagePath == "" {
		return services.FailMsg("该条目不是图片")
	}

	allowedDir := filepath.Join(platform.DefaultDataDir(), "images") + string(filepath.Separator)
	absPath, err := filepath.Abs(entry.ImagePath)
	if err != nil {
		return services.Fail(fmt.Errorf("路径解析失败: %v", err))
	}
	if !strings.HasPrefix(absPath, allowedDir) {
		return services.FailMsg("不允许读取该路径下的文件")
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return services.Fail(fmt.Errorf("读取图片失败: %v", err))
	}
	return services.Ok(base64.StdEncoding.EncodeToString(data))
}

func (a *ClipboardService) CleanupClipboardNow() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	days, _ := a.App.DB.GetClipboardRetentionDays()
	count, err := a.App.DB.DeleteExpiredClipboardEntries(days)
	return services.Wrap(count, err)
}

func (a *ClipboardService) TogglePinClipboardEntry(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	pinned, err := a.App.DB.TogglePinClipboardEntry(id)
	return services.Wrap(pinned, err)
}

func (a *ClipboardService) DeleteClipboardEntry(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.DeleteClipboardEntry(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// exportClipboardItem 导出单条剪贴板条目（文本内联、图片附 base64）
type exportClipboardItem struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType"`
	Text        string `json:"text,omitempty"`
	ImageBase64 string `json:"imageBase64,omitempty"`
	SourceApp   string `json:"sourceApp,omitempty"`
	IsPinned    int    `json:"isPinned"`
	CopyCount   int    `json:"copyCount"`
	CreatedAt   int64  `json:"createdAt"`
}

// ExportClipboard 导出全部剪贴板历史为 JSON 数组（图片条目含 base64，前端可下载为文件）
func (a *ClipboardService) ExportClipboard() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entries, err := a.App.DB.ListClipboardEntries(0)
	if err != nil {
		return services.Fail(err)
	}
	out := make([]exportClipboardItem, 0, len(entries))
	for _, e := range entries {
		item := exportClipboardItem{
			ID:          e.ID,
			ContentType: e.ContentType,
			Text:        e.TextContent,
			SourceApp:   e.SourceApp,
			IsPinned:    e.IsPinned,
			CopyCount:   e.CopyCount,
			CreatedAt:   e.CreatedAt,
		}
		if e.ContentType == "image" && e.ImagePath != "" {
			if data, rerr := os.ReadFile(e.ImagePath); rerr == nil {
				item.ImageBase64 = base64.StdEncoding.EncodeToString(data)
			}
		}
		out = append(out, item)
	}
	return services.Ok(out)
}

// ClearClipboardHistory 清空剪贴板历史（保留固定的条目）
func (a *ClipboardService) ClearClipboardHistory() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	n, err := a.App.DB.ClearClipboardHistory()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]interface{}{"deleted": n})
}

// PasteClipboardEntry 复制剪贴板条目并模拟 Ctrl+V 粘贴
func (a *ClipboardService) PasteClipboardEntry(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entry, err := a.App.DB.GetClipboardEntry(id)
	if err != nil {
		return services.Fail(fmt.Errorf("获取剪贴板条目失败: %v", err))
	}
	hwnd := platform.ClipboardWindowHandle()
	if err := a.writeEntryToClipboard(entry, hwnd); err != nil {
		return services.Fail(err)
	}
	a.HideClipboardWindow()
	go func() {
		defer recoverPanic("clipboard paste")
		time.Sleep(80 * time.Millisecond)
		platform.SimulatePaste()
		_ = a.App.DB.IncrementClipboardCopyCount(id)
	}()
	return services.Ok(nil)
}

// ===== 窗口隐藏 =====

// HideWindow 隐藏主窗口
func (a *ClipboardService) HideWindow() {
	if a.App.ClipboardMode != nil {
		a.App.ClipboardMode.Store(false)
	}
	if a.App.WindowVisible != nil {
		a.App.WindowVisible.Store(false)
	}
	if win := a.App.MainWindow; win != nil {
		win.Hide()
	}
}

// HideClipboardWindow 隐藏剪贴板独立窗口
func (a *ClipboardService) HideClipboardWindow() {
	if a.App.ClipboardMode != nil {
		a.App.ClipboardMode.Store(false)
	}
	if fn := a.App.GetClipboardWindow; fn != nil {
		if win := fn(); win != nil {
			win.Hide()
		}
	}
}

// HideNoteWindow 隐藏快捷笔记独立窗口（并复位置 noteMode 标志，保证热键开关正确）
func (a *ClipboardService) HideNoteWindow() {
	if a.App.NoteMode != nil {
		a.App.NoteMode.Store(false)
	}
	if fn := a.App.GetNoteWindow; fn != nil {
		if win := fn(); win != nil {
			win.Hide()
		}
	}
}

// ===== 热键控制 =====

func (a *ClipboardService) SuspendHotkeys() {
	if a.App.SuspendHotkeysFn != nil {
		a.App.SuspendHotkeysFn()
	}
}

func (a *ClipboardService) ResumeHotkeys() {
	if a.App.ResumeHotkeysFn != nil {
		a.App.ResumeHotkeysFn()
	}
}

// UpdateClipboardNote 更新剪贴板条目的备注
func (a *ClipboardService) UpdateClipboardNote(id, note string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.UpdateClipboardNote(id, note); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
