package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---- 通用设置 ----

// settingEntry 缓存条目。found=false 表示「库里没有这个键」也要缓存：
// 剪贴板暂停/忽略列表这类键默认不存在，不缓存 miss 就等于没省。
type settingEntry struct {
	val   string
	found bool
}

// ---- app_state 键白名单 ----
//
// 为什么要有这张表：app_state 是全应用共用的键值表，前端能通过 SetValue 绑定直接写。
// 只要键名拼得出来，就能覆盖凭据密钥（cred_dek）、同步配置（sync_config）、
// AI 配置（ai_*）——这不是「设置项被改坏」，而是把加密密钥换成攻击者已知的值。
//
// 两级规则：
//   - public：前端可经 GetValue/SetValue 读写（用户可见的主题、语言、面板偏好等）
//   - internal：只有后端服务内部能写（凭据、同步、AI、启动密码、热键等）
//
// 新增键必须登记到这里，否则 SetSetting 直接报错——宁可在开发时炸出来，
// 也不要上线后某个设置「保存成功但重启就没了」。

// internalSettingKeys 仅内部写的固定键。
var internalSettingKeys = map[string]bool{
	// 凭据与密钥
	"cred_dek":       true,
	"cred_ext_token": true,
	"cred_ext_port":  true,
	// 同步 / 网盘
	"sync_config":   true,
	"webdav_config": true, // 旧键，仅迁移期读取
	// AI 配置（含审批规则、工具策略、记忆开关）
	"ai_agent_engine":    true,
	"ai_approval_grants": true,
	"ai_approval_rules":  true,
	"ai_profiles":        true,
	"ai_active_profile":  true,
	// 一次性问答（SSH / 剪贴板 / 笔记 / 插件）单独指定的档案；空=跟随激活档案。
	"ai_ask_profile":    true,
	"ai_config":         true,
	"ai_memory_auto":    true,
	"ai_tool_policy":    true,
	"ai_trace_enabled":  true,
	"ai_hooks_approved": true,
	// 启动锁
	"startup_password_hash":     true,
	"startup_password_recovery": true,
	"startup_password_len":      true,
	// 剪贴板内部状态
	"clipboard_paste_queue":    true,
	"clipboard_rules_enabled":  true,
	"clipboard_ignored_apps":   true,
	"clipboard_pause_until":    true,
	"clipboard_retention_days": true,
	// 其它服务内部配置
	"ssh_forward_rules":     true,
	"plugin_new_flags":      true,
	"dsh_auto_start":        true,
	"git_repos":             true,
	"kb_settings":           true,
	"notify_webhook":        true,
	"notify_extra_channels": true,
	// 热键与窗口管理
	"hotkey":              true,
	"clipboard_hotkey":    true,
	"palette_hotkey":      true,
	"note_hotkey":         true,
	"screenshot_hotkey":   true,
	"ssh_hotkey":          true,
	"lock_hotkey":         true,
	"winmgr_ratio":        true,
	"winmgr_float_hotkey": true,
	"winmgr_left":         true,
	"winmgr_right":        true,
	"winmgr_top":          true,
	"winmgr_bottom":       true,
	"winmgr_tl":           true,
	"winmgr_tr":           true,
	"winmgr_bl":           true,
	"winmgr_br":           true,
	"winmgr_center":       true,
	"winmgr_maximize":     true,
	"winmgr_restore":      true,
	"winmgr_minimize":     true,
	"winmgr_topmost":      true,
	"winmgr_monitor_prev": true,
	"winmgr_monitor_next": true,
}

// publicSettingKeys 前端可读写的固定键（动态键由 publicSettingPrefixes 覆盖）。
var publicSettingKeys = map[string]bool{
	"theme":           true,
	"locale":          true,
	"lastWorkspaceId": true,
	"lastSceneId":     true,
}

// publicSettingPrefixes 前端可读写的动态键前缀：
//   - openedSceneIds:<workspaceId> 已打开的标签页
//   - plugin_icon_<pluginId>       插件图标缓存
//   - qd_                          各类前端自管配置（看板天气/日偏好/更新忽略版本/
//     插件市场源/快照定时配置…）
var publicSettingPrefixes = []string{"openedSceneIds:", "plugin_icon_", "qd_"}

// IsPublicSettingKey 判断某个键是否允许从绑定层（GetValue/SetValue）读写。
func IsPublicSettingKey(key string) bool {
	if publicSettingKeys[key] {
		return true
	}
	for _, p := range publicSettingPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// settingKeyRegistered 判断键是否在册（public 或 internal）。
func settingKeyRegistered(key string) bool {
	return IsPublicSettingKey(key) || internalSettingKeys[key]
}

// GetSetting 从 app_state 读取配置值。
//
// 结果走进程内缓存：剪贴板捕获是全应用最热的路径（每次 Ctrl+C 查 2-3 个
// 几乎不变的设置键），database/sql 每次调用都是一轮 prepare+exec+close。
// 一致性前提：app_state 的全部写入收敛在 SetSetting（写穿缓存），且本表
// 不参与快照恢复（snapshot.go 已注明），不存在绕过缓存的写入方。
func (d *Database) GetSetting(key string) (string, error) {
	if v, ok := d.settingCache.Load(key); ok {
		e := v.(settingEntry)
		if e.found {
			return e.val, nil
		}
		return "", sql.ErrNoRows
	}
	d.mu.RLock()
	var val string
	err := d.conn.QueryRow("SELECT value FROM app_state WHERE key = ?", key).Scan(&val)
	d.mu.RUnlock()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			d.settingCache.Store(key, settingEntry{found: false})
		}
		// 其他查询错误不缓存，下次重试真库
		return "", err
	}
	d.settingCache.Store(key, settingEntry{val: val, found: true})
	return val, nil
}

// SetSetting 写入或更新 app_state 配置（写穿缓存）。
//
// 键必须在册（见 settingKeyRegistered）：拼错一个键名不该静默写进一条永远读不到的
// 孤儿记录，新增键时也该被迫想清楚它属于 public 还是 internal。
func (d *Database) SetSetting(key, value string) error {
	if !settingKeyRegistered(key) {
		return fmt.Errorf("未登记的设置键: %q（新增键须登记到 internalSettingKeys / publicSettingKeys）", key)
	}
	d.mu.Lock()
	_, err := d.conn.Exec("INSERT OR REPLACE INTO app_state (key, value) VALUES (?, ?)", key, value)
	d.mu.Unlock()
	if err != nil {
		// 写失败时丢掉可能的旧缓存，宁可下次回库查也别留旧值
		d.settingCache.Delete(key)
		return err
	}
	d.settingCache.Store(key, settingEntry{val: value, found: true})
	return nil
}

// updateByID 通过主键安全更新表记录（列名经白名单校验）
func (d *Database) updateByID(table, id string, updates map[string]interface{}) error {
	if err := validateTable(table); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	setClause := ""
	values := make([]interface{}, 0, len(updates)+1)
	i := 0
	for col, val := range updates {
		if err := validateColumn(col); err != nil {
			return err
		}
		if i > 0 {
			setClause += ", "
		}
		setClause += fmt.Sprintf("%s = ?", col)
		values = append(values, val)
		i++
	}
	values = append(values, id)

	_, err := d.conn.Exec(fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", table, setClause), values...)
	return err
}
