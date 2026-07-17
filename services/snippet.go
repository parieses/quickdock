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
