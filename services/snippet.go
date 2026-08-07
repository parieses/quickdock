package services

import (
	"fmt"
	"os"
	"strings"
	"time"

	"quickdock/internal/platform"
)

// ===== 文本片段 (Snippets) =====

func (a *AppService) CreateSnippet(keyword, content, category string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	s, err := a.DB.CreateSnippet(keyword, content, category)
	if err != nil {
		// 关键词唯一约束冲突：片段已存在，提示请勿重复保存
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return OkMsg(nil, "已保存，请勿重复保存")
		}
		return Fail(err)
	}
	a.reloadTextExpansion()
	return Ok(s)
}

func (a *AppService) ListSnippets() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	snippets, err := a.DB.ListSnippets()
	return wrap(snippets, err)
}

// GetSnippetByKeyword 按关键词查询片段（快捷笔记用）
func (a *AppService) GetSnippetByKeyword(keyword string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	s, err := a.DB.GetSnippetByKeyword(keyword)
	if err != nil {
		return Fail(err)
	}
	return Ok(s)
}

const quickNoteKeyword = "__quicknote__"

// GetNote 读取快捷笔记内容（find-or-create 固定关键词片段）
func (a *AppService) GetNote() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	s, err := a.DB.GetOrCreateNoteSnippet(quickNoteKeyword)
	if err != nil {
		return Fail(err)
	}
	return Ok(s)
}

// SaveNote 保存快捷笔记内容（整段防抖保存，upsert 固定关键词片段）
func (a *AppService) SaveNote(content string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	s, err := a.DB.GetOrCreateNoteSnippet(quickNoteKeyword)
	if err != nil {
		return Fail(err)
	}
	if err := a.DB.UpdateNoteSnippet(s.ID, content); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) SearchSnippets(query string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	snippets, err := a.DB.SearchSnippets(query)
	return wrap(snippets, err)
}

func (a *AppService) DeleteSnippet(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteSnippet(id); err != nil {
		return Fail(err)
	}
	a.reloadTextExpansion()
	return Ok(nil)
}

func (a *AppService) UpdateSnippet(id, keyword, content, category string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateSnippet(id, keyword, content, category); err != nil {
		// 关键词唯一约束冲突
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return OkMsg(nil, "关键词已存在，请使用其他关键词")
		}
		return Fail(err)
	}
	a.reloadTextExpansion()
	return Ok(nil)
}

const textExpansionSettingKey = "textexpand_enabled"

// reloadTextExpansion 根据设置加载/启动/停止片段自动展开引擎。
// 仅当 textexpand_enabled=1 时安装键盘钩子，并把已开启的片段写入映射。
func (a *AppService) reloadTextExpansion() {
	if a.DB == nil {
		return
	}
	enabled := false
	if v, err := a.DB.GetSetting(textExpansionSettingKey); err == nil && v == "1" {
		enabled = true
	}
	if !enabled {
		platform.TextExpansionStop()
		return
	}
	snips, err := a.DB.ListEnabledExpansionSnippets()
	if err != nil {
		platform.TextExpansionStop()
		return
	}
	m := make(map[string]string, len(snips))
	for _, s := range snips {
		m[s.Keyword] = s.Content
	}
	platform.TextExpansionSetSnippets(m)
	platform.TextExpansionStart(m)
}

// GetTextExpansionEnabled 返回自动展开总开关状态。
func (a *AppService) GetTextExpansionEnabled() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	v, err := a.DB.GetSetting(textExpansionSettingKey)
	if err != nil {
		return Fail(err)
	}
	return Ok(v == "1")
}

// SetTextExpansionEnabled 设置自动展开总开关。
func (a *AppService) SetTextExpansionEnabled(enabled bool) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	v := "0"
	if enabled {
		v = "1"
	}
	if err := a.DB.SetSetting(textExpansionSettingKey, v); err != nil {
		return Fail(err)
	}
	a.reloadTextExpansion()
	return Ok(nil)
}

// SetSnippetExpand 设置单个片段的自动展开开关。
func (a *AppService) SetSnippetExpand(id string, enabled bool) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	e := 0
	if enabled {
		e = 1
	}
	if err := a.DB.SetSnippetExpand(id, e); err != nil {
		return Fail(err)
	}
	a.reloadTextExpansion()
	return Ok(nil)
}

// resolveSnippetVars replaces built-in placeholders in snippet content:
// {date} {time} {username} {clipboard}. Clipboard is read live; on failure
// it resolves to empty string rather than erroring.
func resolveSnippetVars(content string) string {
	now := time.Now()
	content = strings.NewReplacer(
		"{date}", now.Format("2006-01-02"),
		"{time}", now.Format("15:04:05"),
		"{username}", os.Getenv("USERNAME"),
	).Replace(content)
	if strings.Contains(content, "{clipboard}") {
		content = strings.ReplaceAll(content, "{clipboard}", platform.GetClipboardText())
	}
	return content
}

// PasteSnippet 将片段内容复制到剪贴板并粘贴
func (a *AppService) PasteSnippet(content string) *ApiResult {
	if content == "" {
		return Ok(nil)
	}
	SetClipboardText(resolveSnippetVars(content))
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: [PANIC] snippet paste: %v\n", r)
			}
		}()
		time.Sleep(80 * time.Millisecond)
		platform.SimulatePaste()
	}()
	return Ok(nil)
}
