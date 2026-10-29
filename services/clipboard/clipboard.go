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

// CopyText 将文本写入系统剪贴板（SSH 终端 / 命令面板等处的显式复制）。
// 直接走 platform 的 Win32 写入，避免 WebView2 中 navigator.clipboard
// 在文档未聚焦时被静默拦截的问题。
//
// 写失败必须让前端看见：这条路径曾是「点了复制没反应」的源头——底层返回 void，
// 上层只能一律报成功，用户拿到的永远是旧剪贴板内容。
//
// 用 SetClipboardTextRecord（不带回环令牌）而不是 SetClipboardText：这是用户显式
// 复制的新内容，要能被剪贴板捕获协程记入历史；否则「提示复制成功、历史里却没有」。
func (a *ClipboardService) CopyText(text string) *services.ApiResult {
	if !services.SetClipboardTextRecord(text) {
		return services.FailMsg("写入剪贴板失败：系统剪贴板可能被其他程序占用，请稍后重试")
	}
	return services.Ok(nil)
}

func (a *ClipboardService) ListClipboardEntries(limit int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.App.DB.ListClipboardEntries(limit)
	return services.Wrap(data, err)
}

// ListClipboardEntriesByType 按类型过滤列出历史（content_type 已在表内，无需新增字段）。
// CountClipboardEntries 历史总条数（不受列表 limit 影响）
func (a *ClipboardService) CountClipboardEntries() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	n, err := a.App.DB.CountClipboardEntries()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(n)
}

func (a *ClipboardService) ListClipboardEntriesByType(limit int, contentType string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.App.DB.ListClipboardEntriesByType(limit, contentType)
	return services.Wrap(data, err)
}

// ListClipboardEntriesBySmartType 按文本智能分类（url/email/json/code/phone）过滤列出历史。
// 对应前端的智能分类标签：text_type 在入库时已算好，这里直接下推 SQL，既快又覆盖全量历史。
func (a *ClipboardService) ListClipboardEntriesBySmartType(limit int, smartType string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.App.DB.ListClipboardEntriesBySmartType(limit, smartType)
	return services.Wrap(data, err)
}

func (a *ClipboardService) InsertClipboardEntry(text, sourceApp string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.App.DB.InsertClipboardEntry(text, sourceApp)
	return services.Wrap(data, err)
}

func (a *ClipboardService) DeleteExpiredClipboardEntries() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	days, _ := a.App.DB.GetClipboardRetentionDays()
	// 图片文件必须跟着条目一起删：只删库里的行会在图片目录留下永远无人引用的 PNG。
	// 先删库（拿到路径），再删文件；文件删失败只记日志，不影响清理结果。
	count, imgs, err := a.App.DB.DeleteExpiredClipboardEntries(days)
	if err == nil {
		services.RemoveClipboardImages(imgs)
	}
	return services.Wrap(count, err)
}

// writeEntryToClipboard 将一条剪贴板条目写入系统剪贴板（image/file/text 三分支）。
func (a *ClipboardService) writeEntryToClipboard(entry *db.ClipboardEntry, hwnd uintptr) error {
	switch {
	case entry.ContentType == "image" && entry.ImagePath != "":
		// 回环防护的哈希必须在写入剪贴板**之前**登记：写入成功到登记之间，剪贴板
		// 工作协程可能已经把这次变更抓走，导致「从历史复制图片」被误记成一次新复制。
		// 该哈希口径与 processImage 的 DIB→PNG MD5 一致（PNG 文件经 DIB 往返后重编码字节确定性相同）。
		imageData, readErr := os.ReadFile(entry.ImagePath)
		if readErr == nil {
			services.SetLastClipboardImageHash(platform.MD5Hash(imageData))
		}
		err := a.writeImageEntryToClipboard(entry, hwnd, readErr)
		if err != nil {
			// 没写进剪贴板就不能留下这枚「通行证」，否则用户下次真正复制这张图会被误吞。
			services.SetLastClipboardImageHash("")
			return err
		}
	case entry.ContentType == "file" && entry.TextContent != "":
		if err := platform.SetClipboardFiles(hwnd, strings.Split(entry.TextContent, "\n")); err != nil {
			return fmt.Errorf("文件写入剪贴板失败: %v", err)
		}
	default:
		if !services.SetClipboardText(entry.TextContent) {
			return fmt.Errorf("文本写入剪贴板失败（剪贴板可能被其他程序占用）")
		}
	}
	return nil
}

// writeImageEntryToClipboard 图片条目的回写。
//
// 图片条目有两种来源，必须分开处理：
//   - 纯图片（截图/网页图片）：只写 CF_DIB；
//   - 「复制的图片文件」：条目上还带着文件路径，需要**一次会话**里同时写
//     CF_HDROP(文件) + CF_DIB(图片)。旧实现先写文件再写图片，两次 EmptyClipboard
//     把文件格式冲掉了，粘到资源管理器没有任何反应。
//
// 图片文件缺失（历史清理/被手动删除）时退化为只写文件路径，而不是整条失败。
func (a *ClipboardService) writeImageEntryToClipboard(entry *db.ClipboardEntry, hwnd uintptr, readErr error) error {
	paths := []string(nil)
	if entry.TextContent != "" {
		paths = strings.Split(entry.TextContent, "\n")
	}
	if readErr != nil || entry.ImagePath == "" {
		if len(paths) > 0 {
			if err := platform.SetClipboardFiles(hwnd, paths); err != nil {
				return fmt.Errorf("文件写入剪贴板失败: %v", err)
			}
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("读取图片失败: %v", readErr)
		}
		return fmt.Errorf("该条目没有可复制的内容")
	}
	if len(paths) > 0 {
		if err := platform.SetClipboardFilesAndImage(hwnd, paths, entry.ImagePath); err == nil {
			return nil
		}
		// 文件+图片合并写入失败（图片损坏等）时，至少把文件路径写进去。
		if err := platform.SetClipboardFiles(hwnd, paths); err != nil {
			return fmt.Errorf("文件写入剪贴板失败: %v", err)
		}
		return nil
	}
	if err := platform.SetClipboardImage(hwnd, entry.ImagePath); err != nil {
		return fmt.Errorf("图片写入剪贴板失败: %v", err)
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
	count, imgs, err := a.App.DB.DeleteExpiredClipboardEntries(days)
	if err == nil {
		services.RemoveClipboardImages(imgs)
	}
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
	imgPath, err := a.App.DB.DeleteClipboardEntry(id)
	if err != nil {
		return services.Fail(err)
	}
	services.RemoveClipboardImages([]string{imgPath})
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
	Note        string `json:"note,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

// ExportClipboard 导出全部剪贴板历史为 JSON 数组（图片条目含 base64，前端可下载为文件）
func (a *ClipboardService) ExportClipboard() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	// 走无 LIMIT 的专用方法：导出是备份手段，被 5000 条静默截断的备份等于没备份。
	entries, err := a.App.DB.ListAllClipboardEntries()
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
			// 备注是用户手写的唯一信息，丢了就再也找不回来
			Note:      e.Note,
			CreatedAt: e.CreatedAt,
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
	n, imgs, err := a.App.DB.ClearClipboardHistory()
	if err != nil {
		return services.Fail(err)
	}
	services.RemoveClipboardImages(imgs)
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

// ===== 窗口隐藏 / 热键控制 =====
//
// 这一组此前全部无返回值：窗口没藏住、热键没挂上时前端拿不到任何信号，
// 只能静默失败。按 R11 统一改成 *services.ApiResult，调用方至少能判断成败。

// HideWindow 隐藏主窗口
func (a *ClipboardService) HideWindow() *services.ApiResult {
	a.App.Flags.Clipboard.Store(false)
	a.App.Flags.Main.Store(false)
	if win := a.App.MainWindow; win != nil {
		win.Hide()
	}
	return services.Ok(nil)
}

// HideClipboardWindow 隐藏剪贴板独立窗口
func (a *ClipboardService) HideClipboardWindow() *services.ApiResult {
	a.App.Flags.Clipboard.Store(false)
	if fn := a.App.GetClipboardWindow; fn != nil {
		if win := fn(); win != nil {
			win.Hide()
		}
	}
	return services.Ok(nil)
}

// HideNoteWindow 隐藏快捷笔记独立窗口（并复位置 noteMode 标志，保证热键开关正确）
func (a *ClipboardService) HideNoteWindow() *services.ApiResult {
	a.App.Flags.Note.Store(false)
	if fn := a.App.GetNoteWindow; fn != nil {
		if win := fn(); win != nil {
			win.Hide()
		}
	}
	return services.Ok(nil)
}

// SuspendHotkeys 暂停全局热键（如编辑热键时避免与输入框抢按键）
func (a *ClipboardService) SuspendHotkeys() *services.ApiResult {
	if a.App.SuspendHotkeysFn != nil {
		a.App.SuspendHotkeysFn()
	}
	return services.Ok(nil)
}

// ResumeHotkeys 恢复全局热键
func (a *ClipboardService) ResumeHotkeys() *services.ApiResult {
	if a.App.ResumeHotkeysFn != nil {
		a.App.ResumeHotkeysFn()
	}
	return services.Ok(nil)
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
