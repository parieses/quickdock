package bookmark

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const chromiumFixture = `{
  "roots": {
    "bookmark_bar": {
      "type": "folder", "name": "书签栏",
      "children": [
        {"type": "url", "name": "Vue", "url": "https://vuejs.org/", "date_added": "13370000000000000"},
        {"type": "folder", "name": "前端", "children": [
          {"type": "folder", "name": "框架", "children": [
            {"type": "url", "name": "React", "url": "https://react.dev/"}
          ]}
        ]}
      ]
    },
    "other": {"type": "folder", "name": "其他书签", "children": [
      {"type": "url", "name": "", "url": "https://example.com/docs"}
    ]},
    "synced": {"type": "folder", "name": "移动设备书签", "children": []}
  }
}`

// TestParseChromiumBookmarks 校验三件事：根容器不进路径、多级文件夹保序、空标题保留。
func TestParseChromiumBookmarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Bookmarks")
	if err := os.WriteFile(path, []byte(chromiumFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ParseChromiumBookmarks(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d，期望 3", len(entries))
	}

	want := map[string][]string{
		"https://vuejs.org/":       nil,
		"https://react.dev/":       {"前端", "框架"},
		"https://example.com/docs": nil,
	}
	for _, e := range entries {
		w, ok := want[e.URL]
		if !ok {
			t.Errorf("意外的条目 %s", e.URL)
			continue
		}
		if len(e.Folder) != len(w) {
			t.Errorf("%s 路径 = %v，期望 %v", e.URL, e.Folder, w)
			continue
		}
		for i := range w {
			if e.Folder[i] != w[i] {
				t.Errorf("%s 路径 = %v，期望 %v", e.URL, e.Folder, w)
				break
			}
		}
	}

	// date_added 是 1601 纪元的微秒，转换后应是 2024 年附近的 Unix 秒
	for _, e := range entries {
		if e.URL == "https://vuejs.org/" && e.AddDate < 1_700_000_000 {
			t.Errorf("date_added 转换错误：%d", e.AddDate)
		}
	}
}

func TestParseChromiumBookmarksRejectsOtherJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Bookmarks")
	if err := os.WriteFile(path, []byte(`{"hello":"world"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseChromiumBookmarks(path); err == nil {
		t.Fatal("非书签 JSON 应当报错（解析出 0 条）")
	}
}

// TestParseFirefoxPlaces 造一个最小 moz_* 库，验证 parent 链还原与根节点剥离。
func TestParseFirefoxPlaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "places.sqlite")
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE moz_places (id INTEGER PRIMARY KEY, url TEXT)`,
		`CREATE TABLE moz_bookmarks (id INTEGER PRIMARY KEY, type INTEGER, fk INTEGER, parent INTEGER, title TEXT, dateAdded INTEGER)`,
		`INSERT INTO moz_places (id, url) VALUES (100, 'https://react.dev/'), (101, 'https://vuejs.org/'), (102, 'place:sort=8')`,
		`INSERT INTO moz_bookmarks (id, type, fk, parent, title, dateAdded) VALUES
			(1, 2, NULL, 0, '', 0),
			(2, 2, NULL, 1, '书签菜单', 0),
			(3, 2, NULL, 1, '书签工具栏', 0),
			(10, 2, NULL, 3, '前端', 0),
			(11, 2, NULL, 10, '框架', 0),
			(20, 1, 100, 11, 'React', 1600000000000000),
			(21, 1, 101, 3, 'Vue', 1600000000000000),
			(22, 1, 102, 3, '最近书签', 1600000000000000)`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("建库失败: %v", err)
		}
	}
	conn.Close()

	entries, err := ParseFirefoxPlaces(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d，期望 3", len(entries))
	}
	got := map[string][]string{}
	for _, e := range entries {
		got[e.URL] = e.Folder
	}
	if p := got["https://react.dev/"]; len(p) != 2 || p[0] != "前端" || p[1] != "框架" {
		t.Errorf("React 路径 = %v，期望 [前端 框架]", p)
	}
	if p := got["https://vuejs.org/"]; len(p) != 0 {
		t.Errorf("Vue 应挂在工具栏根下，路径应为空，实际 %v", p)
	}
}

func TestFirefoxProfileNames(t *testing.T) {
	dir := t.TempDir()
	ini := "[Profile1]\nName=工作\nIsRelative=1\nPath=Profiles/abcd.default-release\nDefault=1\n\n[Profile0]\nName=个人\nPath=Profiles/wxyz.default\n"
	if err := os.WriteFile(filepath.Join(dir, "profiles.ini"), []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	names := firefoxProfileNames(dir)
	if names["abcd.default-release"] != "工作" {
		t.Errorf("工作配置名未解析: %v", names)
	}
	if names["wxyz.default"] != "个人" {
		t.Errorf("个人配置名未解析: %v", names)
	}
}

// TestLocateChromiumBookmarks 校验书签文件的探测优先级：
// Booksmarks 缺失时必须退到 AccountBookmarks（登录账号的 Chrome 只有这个），
// 两者都没有才用 Bookmarks.bak，且空文件不算数。
func TestLocateChromiumBookmarks(t *testing.T) {
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := func(t *testing.T, dir, expect string) {
		t.Helper()
		got, ok := locateChromiumBookmarks(dir)
		if !ok {
			t.Fatalf("期望找到 %s，实际没有", expect)
		}
		if filepath.Base(got) != expect {
			t.Errorf("期望 %s，实际 %s", expect, filepath.Base(got))
		}
	}

	t.Run("只有 AccountBookmarks", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "AccountBookmarks", chromiumFixture)
		want(t, dir, "AccountBookmarks")
	})

	t.Run("Bookmarks 优先", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "Bookmarks", chromiumFixture)
		write(dir, "AccountBookmarks", chromiumFixture)
		want(t, dir, "Bookmarks")
	})

	t.Run("只剩 bak 兜底", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "Bookmarks.bak", chromiumFixture)
		want(t, dir, "Bookmarks.bak")
	})

	t.Run("空文件跳过", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "Bookmarks", "")
		write(dir, "AccountBookmarks", chromiumFixture)
		want(t, dir, "AccountBookmarks")
	})

	t.Run("一个都没有", func(t *testing.T) {
		if path, ok := locateChromiumBookmarks(t.TempDir()); ok {
			t.Errorf("空目录不该命中: %s", path)
		}
	})
}

// TestDiscoverSourcesNoPanic 只保证扫描在无浏览器的环境里不 panic 且不返回空指针。
func TestDiscoverSourcesNoPanic(t *testing.T) {
	srcs := DiscoverSources()
	t.Logf("本机发现 %d 个书签来源", len(srcs))
	for _, s := range srcs {
		if s.Key == "" || s.Path == "" {
			t.Errorf("来源字段为空: %+v", s)
		}
		t.Logf("%s | %s | %s", s.Browser, s.Profile, s.Path)
	}
}
