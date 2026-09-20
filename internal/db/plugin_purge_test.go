package db

import "testing"

// TestPurgePluginTraces 覆盖卸载 / 启动残留清理共用的 PurgePlugin：
// 插件记录、私有数据、使用记录、执行日志四条痕迹都要清掉，
// 且各表清理必须精确——插件 ID 含 "_" 时不能被 LIKE 当成通配符，前缀更长的 ID 与非插件 key 不能被误删。
func TestPurgePluginTraces(t *testing.T) {
	d := openTestDB(t)

	for _, id := range []string{"com.quickdock.disk_analyzer", "com.quickdock.diskXanalyzer", "com.quickdock.port-scanner"} {
		if _, err := d.conn.Exec(
			"INSERT INTO plugins (id, name, version, installed_at, updated_at) VALUES (?, ?, '1.0.0', '2026-09-20', '2026-09-20')",
			id, id,
		); err != nil {
			t.Fatalf("seed plugin 失败: %v", err)
		}
		if _, err := d.conn.Exec(
			"INSERT INTO plugin_data (plugin_id, key, value) VALUES (?, 'k', 'v')", id,
		); err != nil {
			t.Fatalf("seed plugin_data 失败: %v", err)
		}
	}

	seed := []struct {
		key   string
		count int
	}{
		{"plugin:com.quickdock.disk_analyzer.scan", 3},
		{"plugin:com.quickdock.diskXanalyzer.scan", 2}, // 不该被 disk_analyzer 的清理波及
		{"plugin:com.quickdock.port-scanner.status", 5},
		{"plugin:com.quickdock.port-scanner2.status", 1}, // 前缀更长，不该被 port-scanner 波及
		{"item:some-item", 9},                            // 非插件 key，绝不能动
	}
	for _, s := range seed {
		if _, err := d.conn.Exec(
			"INSERT INTO usage_frecency (key, type, label, description, input, count, last_used) VALUES (?, 'plugin', '', '', '', ?, 0)",
			s.key, s.count,
		); err != nil {
			t.Fatalf("seed usage 失败: %v", err)
		}
	}

	logs := []struct{ id, pid string }{
		{"l1", "com.quickdock.disk_analyzer"},
		{"l2", "com.quickdock.diskXanalyzer"},
		{"l3", "com.quickdock.port-scanner"},
	}
	for _, l := range logs {
		if _, err := d.conn.Exec(
			"INSERT INTO plugin_exec_logs (id, plugin_id, command_id, executed_ts) VALUES (?, ?, 'cmd', 0)",
			l.id, l.pid,
		); err != nil {
			t.Fatalf("seed log 失败: %v", err)
		}
	}

	if err := d.PurgePlugin("com.quickdock.disk_analyzer"); err != nil {
		t.Fatalf("PurgePlugin 失败: %v", err)
	}

	// 目标插件的四条痕迹全部清空
	var n int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM plugins WHERE id = 'com.quickdock.disk_analyzer'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("plugins 记录残留 %d 条", n)
	}
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM plugin_data WHERE plugin_id = 'com.quickdock.disk_analyzer'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("plugin_data 残留 %d 条", n)
	}
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM plugin_exec_logs WHERE plugin_id = 'com.quickdock.disk_analyzer'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("plugin_exec_logs 残留 %d 条", n)
	}

	// 其它插件的痕迹必须原样保留（含 LIKE 通配符误伤与前缀误伤两种情况）
	var usageKeys []string
	rows, err := d.conn.Query("SELECT key FROM usage_frecency ORDER BY key")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		usageKeys = append(usageKeys, k)
	}
	rows.Close()

	wantUsage := []string{
		"item:some-item",
		"plugin:com.quickdock.diskXanalyzer.scan",
		"plugin:com.quickdock.port-scanner.status",
		"plugin:com.quickdock.port-scanner2.status",
	}
	if len(usageKeys) != len(wantUsage) {
		t.Fatalf("usage 残留 %v，期望 %v", usageKeys, wantUsage)
	}
	for i, k := range wantUsage {
		if usageKeys[i] != k {
			t.Fatalf("usage[%d]=%q，期望 %q", i, usageKeys[i], k)
		}
	}

	for _, tbl := range []string{"plugins", "plugin_data", "plugin_exec_logs"} {
		if err := d.conn.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("%s 残留 %d 条，期望 2 条（diskXanalyzer / port-scanner）", tbl, n)
		}
	}
}
