package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"quickdock/internal/db"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ServiceStartup 应用启动时调用（v3 生命周期）
func (a *AppService) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	// 打开数据库
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	dbDir := filepath.Join(homeDir, ".quickdock")
	os.MkdirAll(dbDir, 0755)
	dbPath := filepath.Join(dbDir, "quickdock.db")
	fmt.Println("QuickDock: 正在打开数据库", dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Println("QuickDock: 数据库打开失败:", err.Error())
		return err
	}
	a.DB = database
	fmt.Println("QuickDock: 数据库已打开")

	// 检查 PRAGMA 状态
	fkRow, err := database.QueryOne("PRAGMA foreign_keys")
	if err != nil {
		fmt.Println("QuickDock: PRAGMA 检查失败:", err.Error())
	} else {
		fmt.Println("QuickDock: PRAGMA foreign_keys =", fmt.Sprintf("%v", fkRow["foreign_keys"]))
	}

	// 确保默认工作空间存在
	workspaces, err := a.DB.ListWorkspaces()
	if err != nil {
		return fmt.Errorf("工作空间列表获取失败: %w", err)
	}
	fmt.Println("QuickDock: 找到", len(workspaces), "个工作空间")

	if len(workspaces) == 0 {
		ws, err := a.DB.CreateWorkspace(DefaultWorkspaceName)
		if err != nil {
			fmt.Println("QuickDock: 创建默认工作空间失败:", err.Error())
		} else {
			fmt.Println("QuickDock: 默认工作空间已创建, id=", ws.ID)
		}
	} else {
		for _, w := range workspaces {
			fmt.Println("QuickDock: 工作空间 id=", w.ID, "名称=", w.Name)
		}
	}

	// 确保默认工具存在
	if err := a.DB.EnsureDefaultTools(); err != nil {
		fmt.Println("QuickDock: 默认工具初始化失败:", err.Error())
	}

	// 清理过期剪贴板条目
	if count, err := a.DeleteExpiredClipboardEntries(); err != nil {
		fmt.Println("QuickDock: 剪贴板过期清理失败:", err.Error())
	} else if count > 0 {
		fmt.Printf("QuickDock: 已清理 %d 条过期剪贴板记录\n", count)
	}

	// 设置全局 App 引用（供 SetClipboardText 等函数使用）
	AppRef = a.app

	// 启动全局快捷键和系统托盘（由 main 包注入的回调）
	if a.WindowVisible != nil {
		a.WindowVisible.Store(true)
	}
	if a.StartHotkeyListenerFn != nil {
		a.StartHotkeyListenerFn(a.app, a)
	}

	return nil
}

// ServiceShutdown 应用退出时调用（v3 生命周期）
func (a *AppService) ServiceShutdown() error {
	if a.DB != nil {
		a.DB.Close()
	}
	return nil
}
