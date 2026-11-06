package db

import (
	"fmt"
	"strings"
	"time"
)

// ShelfItem 是「文件置物架」里的一条记录。
//
// 关键约定：只记路径，**绝不搬动、复制、删除用户文件**。原文件被移动或删掉了，
// 这里保留记录并标记失效（Exists 由 service 层 os.Stat 算出，不落库），让用户自己
// 决定要不要移除 —— 自动清理会变成「程序偷偷删我东西」。
type ShelfItem struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Name         string `json:"name"`
	Kind         string `json:"kind"` // file | dir
	Sort         int    `json:"sort"`
	CreatedTs    int64  `json:"createdTs"`
	LastOpenedTs int64  `json:"lastOpenedTs"`
	Exists       bool   `json:"exists"` // 展示时算，非表字段
}

const shelfCols = `id, path, name, kind, sort, created_ts, last_opened_ts`

func mapToShelf(m map[string]interface{}) ShelfItem {
	return ShelfItem{
		ID:           str(m["id"]),
		Path:         str(m["path"]),
		Name:         str(m["name"]),
		Kind:         str(m["kind"]),
		Sort:         integer(m["sort"]),
		CreatedTs:    int64(integer(m["created_ts"])),
		LastOpenedTs: int64(integer(m["last_opened_ts"])),
	}
}

func (d *Database) ListShelf() ([]ShelfItem, error) {
	rows, err := d.Query(`SELECT ` + shelfCols + ` FROM file_shelf ORDER BY sort ASC, created_ts ASC`)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToShelf), nil
}

func (d *Database) GetShelf(id string) (*ShelfItem, error) {
	row, err := d.QueryOne(`SELECT `+shelfCols+` FROM file_shelf WHERE id = ?`, id)
	if err != nil || row == nil {
		return nil, err
	}
	it := mapToShelf(row)
	return &it, nil
}

// AddShelfPaths 批量加入并返回实际新增条数。已存在的路径按大小写不敏感去重
// （Windows 路径不区分大小写），重复拖入同一个文件不会堆出一排磁贴。
func (d *Database) AddShelfPaths(items []ShelfItem) (int, error) {
	existing, err := d.Query(`SELECT path FROM file_shelf`)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(existing))
	for _, row := range existing {
		seen[strings.ToLower(strings.TrimSpace(str(row["path"])))] = true
	}
	rows, err := d.QueryOne(`SELECT COALESCE(MAX(sort), 0) AS m FROM file_shelf`)
	if err != nil {
		return 0, err
	}
	nextSort := integer(rows["m"]) + 1

	added := 0
	for _, it := range items {
		path := strings.TrimSpace(it.Path)
		key := strings.ToLower(path)
		if path == "" || seen[key] {
			continue
		}
		seen[key] = true
		if err := d.ExecuteParams(
			`INSERT INTO file_shelf (`+shelfCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{newID(), path, it.Name, it.Kind, nextSort, time.Now().UnixMilli(), int64(0)},
		); err != nil {
			return added, fmt.Errorf("加入置物架失败: %w", err)
		}
		nextSort++
		added++
	}
	return added, nil
}

func (d *Database) RemoveShelf(id string) error {
	return d.ExecuteParams(`DELETE FROM file_shelf WHERE id = ?`, []interface{}{id})
}

func (d *Database) TouchShelf(id string) error {
	return d.ExecuteParams(`UPDATE file_shelf SET last_opened_ts = ? WHERE id = ?`, []interface{}{time.Now().UnixMilli(), id})
}

func (d *Database) ReorderShelf(ids []string) error {
	for i, id := range ids {
		if err := d.ExecuteParams(`UPDATE file_shelf SET sort = ? WHERE id = ?`, []interface{}{i, id}); err != nil {
			return err
		}
	}
	return nil
}
