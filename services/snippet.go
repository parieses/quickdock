package services

import (
	"fmt"
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
		return dberr(err)
	}
	return Ok(s)
}

func (a *AppService) ListSnippets() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	snippets, err := a.DB.ListSnippets()
	if err != nil {
		return dberr(err)
	}
	return Ok(snippets)
}

func (a *AppService) SearchSnippets(query string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	snippets, err := a.DB.SearchSnippets(query)
	if err != nil {
		return dberr(err)
	}
	return Ok(snippets)
}

func (a *AppService) DeleteSnippet(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteSnippet(id); err != nil {
		return dberr(err)
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
		return dberr(err)
	}
	return Ok(nil)
}

// PasteSnippet 将片段内容复制到剪贴板并粘贴
func (a *AppService) PasteSnippet(content string) *ApiResult {
	if content == "" {
		return Ok(nil)
	}
	SetClipboardText(content)
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
