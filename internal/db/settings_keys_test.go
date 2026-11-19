package db

import "testing"

// TestSettingKeysRegistered 遍历白名单里的每一个键，确认 SetSetting 都放行。
//
// 这条测试守的是「漏登记」：白名单一旦落地，漏一个键的表现是某个设置
// 「保存成功但重启就没了」或者直接报错，且只有用到那个功能时才会发现。
// 数据驱动地跑一遍，加键时忘了同步两处也能立刻炸出来。
func TestSettingKeysRegistered(t *testing.T) {
	d := openTestDB(t)

	for k := range internalSettingKeys {
		if err := d.SetSetting(k, "1"); err != nil {
			t.Errorf("内部键 %q 应可写入，实际报错: %v", k, err)
		}
	}
	for k := range publicSettingKeys {
		if err := d.SetSetting(k, "1"); err != nil {
			t.Errorf("公开键 %q 应可写入，实际报错: %v", k, err)
		}
	}
	// 动态键：前端按 id 拼出来的那些，必须靠前缀放行
	for _, k := range []string{
		"openedSceneIds:ws-123",
		"plugin_icon_demo.plugin",
		"qd_weather_config",
		"qd_day_prefs",
		"qd_update_skipped_versions",
	} {
		if err := d.SetSetting(k, "1"); err != nil {
			t.Errorf("动态键 %q 应可写入，实际报错: %v", k, err)
		}
	}

	// 未登记的键必须被拒：否则拼错一个字母就会静默写进一条读不到的行
	if err := d.SetSetting("totally_unknown_key", "1"); err == nil {
		t.Error("未登记的键不应写入成功")
	}
}

// TestSetValueRejectsInternalKeys 绑定层（SetValue/GetValue）不得碰内部键。
//
// 拦截点必须在 db 的键值接口上：只要 SetValue("cred_dek") 能写，
// 前端就等于能把凭据密钥换成自己已知的值，后面所有「加密存储」都失去意义。
func TestSetValueRejectsInternalKeys(t *testing.T) {
	d := openTestDB(t)

	for _, k := range []string{"cred_dek", "cred_ext_token", "sync_config", "ai_profiles", "startup_password_hash"} {
		if err := d.SetValue(k, "hacked"); err == nil {
			t.Errorf("SetValue(%q) 应被拒绝", k)
		}
		if _, err := d.GetValue(k); err == nil {
			t.Errorf("GetValue(%q) 应被拒绝", k)
		}
	}

	// 公开键照常可用（这是设置页真正在走的路径）
	if err := d.SetValue("theme", "dark"); err != nil {
		t.Fatalf("SetValue(theme) 失败: %v", err)
	}
	if v, err := d.GetValue("theme"); err != nil || v != "dark" {
		t.Fatalf("GetValue(theme) = %q, %v；期望 dark", v, err)
	}
}
