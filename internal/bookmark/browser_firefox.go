package bookmark

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Firefox 的书签存在 places.sqlite（和浏览历史同一张库）。
//
// 两个坑必须处理：
//  1. Firefox 运行时独占这个库，直接打开会拿到「database is locked」。这里把
//     places.sqlite 连同 -wal / -shm 复制到临时目录再读——不加锁、不干扰用户浏览，
//     代价是几百 KB 到几十 MB 的一次拷贝。
//  2. 层级不是自引用字段而是 parent 链，需要自己从叶子往上走到根。根节点
//     （places / 书签菜单 / 书签工具栏 / 其他书签 / 移动设备书签）要对齐 HTML 导入
//     的口径剥掉，否则每条书签都会多一层「书签菜单」前缀。

// mozRootID places 根节点在 moz_bookmarks 里的固定 id（SQLite 里 type=2 的顶层）。
const mozRootID = 1

// ParseFirefoxPlaces 读取 Firefox 的 places.sqlite。
func ParseFirefoxPlaces(path string) ([]Entry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("选中的是文件夹，不是 places.sqlite")
	}

	tmpDir, err := os.MkdirTemp("", "qd-places-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	copied := false
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := path + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFileTo(src, filepath.Join(tmpDir, "places.sqlite"+suffix)); err != nil {
			if suffix == "" {
				return nil, fmt.Errorf("读取 places.sqlite 失败：%w", err)
			}
			continue // wal/shm 缺失只是少一部分未落盘的改动，不影响主体
		}
		if suffix == "" {
			copied = true
		}
	}
	if !copied {
		return nil, fmt.Errorf("读取 places.sqlite 失败")
	}

	// mode=ro：拷贝品也只读打开，避免驱动顺手建 journal 覆盖掉原数据。
	dsn := "file:" + filepath.ToSlash(filepath.Join(tmpDir, "places.sqlite")) + "?mode=ro&_pragma=busy_timeout(2000)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 places.sqlite 失败：%w", err)
	}
	defer conn.Close()

	parents, titles, err := mozFolders(conn)
	if err != nil {
		return nil, err
	}
	return mozBookmarks(conn, parents, titles)
}

// mozFolders 读取全部文件夹，返回 parent 与 title 两张表。
func mozFolders(conn *sql.DB) (map[int64]int64, map[int64]string, error) {
	rows, err := conn.Query(`SELECT id, parent, title FROM moz_bookmarks WHERE type = 2`)
	if err != nil {
		return nil, nil, fmt.Errorf("读取 Firefox 文件夹失败：%w", err)
	}
	defer rows.Close()

	parents := map[int64]int64{}
	titles := map[int64]string{}
	for rows.Next() {
		var (
			id     int64
			parent sql.NullInt64
			title  sql.NullString
		)
		if err := rows.Scan(&id, &parent, &title); err != nil {
			return nil, nil, err
		}
		parents[id] = parent.Int64
		titles[id] = title.String
	}
	return parents, titles, rows.Err()
}

// mozBookmarks 读取全部书签条目（type=1）。
func mozBookmarks(conn *sql.DB, parents map[int64]int64, titles map[int64]string) ([]Entry, error) {
	rows, err := conn.Query(`
		SELECT b.title, p.url, b.parent, b.dateAdded
		FROM moz_bookmarks b
		JOIN moz_places p ON p.id = b.fk
		WHERE b.type = 1 AND p.url IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("读取 Firefox 书签失败：%w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0, 64)
	for rows.Next() {
		var (
			title  sql.NullString
			url    sql.NullString
			parent sql.NullInt64
			added  sql.NullInt64
		)
		if err := rows.Scan(&title, &url, &parent, &added); err != nil {
			return nil, err
		}
		if strings.TrimSpace(url.String) == "" {
			continue
		}
		entries = append(entries, Entry{
			Title:   strings.TrimSpace(title.String),
			URL:     url.String,
			Folder:  mozFolderPath(parent.Int64, parents, titles),
			AddDate: added.Int64 / 1_000_000, // Firefox 的 dateAdded 是 Unix 微秒
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("这个 Firefox 配置里没有书签")
	}
	return entries, nil
}

// mozFolderPath 从叶子文件夹往上走到根，返回从根到叶的路径。
//
// 停止条件是「当前节点的父节点就是 places 根」：那一层是书签菜单 / 工具栏这类
// 浏览器固定容器，不是用户建的分类，与 HTML 导入剥掉书签栏的口径一致。
func mozFolderPath(parent int64, parents map[int64]int64, titles map[int64]string) []string {
	var rev []string
	cur := parent
	for i := 0; cur > mozRootID && i < chromiumNodeLimit; i++ {
		if parents[cur] == mozRootID {
			break
		}
		if name := sanitizeFolder(titles[cur]); name != "" {
			rev = append(rev, name)
		}
		next, ok := parents[cur]
		if !ok || next == cur {
			break
		}
		cur = next
	}
	if len(rev) == 0 {
		return nil
	}
	// 反向
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
