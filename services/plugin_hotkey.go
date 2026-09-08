package services

import (
	"fmt"
	"sync"

	"quickdock/internal/plugin"
)

// PluginHotkeyRegistry 管理插件声明的全局热键
type PluginHotkeyRegistry struct {
	mu       sync.Mutex
	accelMap map[string]string   // "Ctrl+Shift+T" → "pluginID.commandID"
	byPlugin map[string][]string // pluginID → []accel （便于卸载时批量清理）
}

func NewPluginHotkeyRegistry() *PluginHotkeyRegistry {
	return &PluginHotkeyRegistry{
		accelMap: make(map[string]string),
		byPlugin: make(map[string][]string),
	}
}

// Register 注册插件热键，返回错误如果冲突
func (r *PluginHotkeyRegistry) Register(accel, pluginID, commandID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 冲突检测
	if existing, ok := r.accelMap[accel]; ok {
		return fmt.Errorf("热键 %s 已被 %s 占用: %w", accel, existing, plugin.ErrHotkeyConflict)
	}

	r.accelMap[accel] = pluginID + "." + commandID
	r.byPlugin[pluginID] = append(r.byPlugin[pluginID], accel)
	return nil
}

// UnregisterAll 卸载插件时清理所有热键
func (r *PluginHotkeyRegistry) UnregisterAll(pluginID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	accels := r.byPlugin[pluginID]
	for _, accel := range accels {
		delete(r.accelMap, accel)
	}
	delete(r.byPlugin, pluginID)
	return accels
}

// GetPluginAccels 返回插件注册的所有热键（用于外部注销系统快捷键）
func (r *PluginHotkeyRegistry) GetPluginAccels(pluginID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]string, len(r.byPlugin[pluginID]))
	copy(result, r.byPlugin[pluginID])
	return result
}
