package db

import "testing"

// TestGetAllPluginUsageCounts 覆盖插件使用次数归组：
// key = "plugin:{pluginID}.{commandID}"，两端都可能带点号，
// 因此只能「以已安装 id 为前缀最长匹配」，不能从 key 反解。
// （历史实现用 LastIndex(".") 反解，dotted 插件 id 的计数恒为 0。）
func TestGetAllPluginUsageCounts(t *testing.T) {
	d := openTestDB(t)

	ids := []string{"io.github.x.database", "io.github.x.disk-analyzer", "io.github.x.port-scanner", "io.github.x", "io.github.x.port-scanner2"}
	for _, id := range ids {
		if _, err := d.conn.Exec(
			"INSERT INTO plugins (id, name, version, installed_at, updated_at) VALUES (?, ?, '1.0.0', '2026-10-01', '2026-10-01')",
			id, id,
		); err != nil {
			t.Fatalf("seed plugin %s 失败: %v", id, err)
		}
	}

	seed := []struct {
		key   string
		count int
	}{
		{"plugin:io.github.x.database.table.rows", 27},         // 插件 id 与命令 id 都含点
		{"plugin:io.github.x.database.kv.get", 19},             // 同一插件的另一条命令要累加
		{"plugin:io.github.x.database.table.structure", 13},    // 同一插件第三条
		{"plugin:io.github.x.disk-analyzer.disk-scan-root", 1}, // 命令名不含点（旧实现唯一能碰对的情形）
		{"plugin:io.github.x.disk-analyzer.disk-scan-full", 1},
		{"plugin:io.github.x.port-scanner.status", 5},
		{"plugin:io.github.x.port-scanner2.status", 1}, // 前缀更长，不能算到 port-scanner 头上
		{"plugin:io.github.x.hotkey", 4},               // id 是 io.github.x 的命令级 key
		{"plugin:io.github.y.uninstalled.cmd", 99},     // plugins 表里没有 → 不计
		{"item:some-item", 9},                          // 非插件 key，绝不能计
	}
	for _, s := range seed {
		if _, err := d.conn.Exec(
			"INSERT INTO usage_frecency (key, type, label, description, input, count, last_used) VALUES (?, 'plugin', '', '', '', ?, 0)",
			s.key, s.count,
		); err != nil {
			t.Fatalf("seed usage %s 失败: %v", s.key, err)
		}
	}

	got, err := d.GetAllPluginUsageCounts()
	if err != nil {
		t.Fatalf("GetAllPluginUsageCounts 失败: %v", err)
	}

	want := map[string]int{
		"io.github.x.database":      59, // 27+19+13
		"io.github.x.disk-analyzer": 2,
		"io.github.x.port-scanner":  5,
		"io.github.x":               4, // 最长前缀优先：不被 io.github.x.database 抢走
		"io.github.x.port-scanner2": 1,
	}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("插件 %s 计数 = %d，期望 %d", id, got[id], n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("归组结果 %v，期望恰好 %d 个插件", got, len(want))
	}
	if _, ok := got["io.github.x.database.table"]; ok {
		t.Errorf("出现了按点号切错的反解 id: %v", got)
	}
}
