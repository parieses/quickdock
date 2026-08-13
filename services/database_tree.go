package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	"quickdock/internal/platform"
)

// ListDbObjects 返回某个库下的表/视图结构（含字段），用于树展开时按需加载。
func (a *AppService) ListDbObjects(id string, dbName string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	saved, err := a.DB.GetDbConnection(id)
	if err != nil {
		return Fail(err)
	}
	plainPwd, e := platform.DecryptSecret(saved.Password)
	if e != nil {
		plainPwd = ""
	}
	conn, err := toDbConn(DbConnectionInput{
		DbType:   saved.DbType,
		Host:     saved.Host,
		Port:     saved.Port,
		Username: saved.Username,
		Password: plainPwd,
		Database: saved.Database,
		FilePath: saved.FilePath,
	})
	if err != nil {
		return Fail(err)
	}
	defer conn.close()
	if conn.kind == "mysql" || conn.kind == "sqlite" {
		tree, e := listSchemaTree(conn.sqlDB, conn.kind, dbName)
		if e != nil {
			return Fail(fmt.Errorf("读取表结构失败: %w", e))
		}
		return Ok(tree)
	}
	return Ok([]DbTreeNode{})
}

// listSqlDatabases 列出 SQL 连接下「当前有权限的库」。
func listSqlDatabases(d *sql.DB, kind, defaultDb string) ([]DbTreeNode, error) {
	if kind == "mysql" {
		ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
		defer cancel()
		rows, err := d.QueryContext(ctx, "SHOW DATABASES")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var tree []DbTreeNode
		for rows.Next() {
			var dbName string
			if err := rows.Scan(&dbName); err != nil {
				continue
			}
			tree = append(tree, DbTreeNode{Name: dbName, Kind: "database"})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return tree, nil
	}
	// sqlite：单文件通常只有一个 main 库
	name := strings.TrimSpace(defaultDb)
	if name == "" {
		name = "main"
	}
	children, err := listSchemaTree(d, "sqlite", name)
	if err != nil {
		return nil, err
	}
	return []DbTreeNode{{Name: name, Kind: "database", Children: children}}, nil
}

// listSchemaTree 列出某个库下的 表/视图 及其字段（不含库层），用于按需加载。
func listSchemaTree(d *sql.DB, kind, dbName string) ([]DbTreeNode, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	var tables, views []DbTreeNode
	if kind == "mysql" {
		q := "SHOW FULL TABLES FROM `" + dbName + "`"
		rows, err := d.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var tbl, ttype string
			if err := rows.Scan(&tbl, &ttype); err != nil {
				return nil, err
			}
			if strings.EqualFold(ttype, "VIEW") {
				views = append(views, DbTreeNode{Name: tbl, Kind: "view"})
			} else {
				tables = append(tables, DbTreeNode{Name: tbl, Kind: "table"})
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		rows, err := d.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var tbl string
			if err := rows.Scan(&tbl); err != nil {
				return nil, err
			}
			tables = append(tables, DbTreeNode{Name: tbl, Kind: "table"})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		vrows, verr := d.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='view' ORDER BY name")
		if verr == nil {
			defer vrows.Close()
			for vrows.Next() {
				var v string
				if err := vrows.Scan(&v); err != nil {
					continue
				}
				views = append(views, DbTreeNode{Name: v, Kind: "view"})
			}
		}
	}

	for i := range tables {
		tables[i].Children = columnsOf(d, kind, dbName, tables[i].Name)
	}
	for i := range views {
		views[i].Children = columnsOf(d, kind, dbName, views[i].Name)
	}

	var tree []DbTreeNode
	if len(tables) > 0 {
		tree = append(tree, DbTreeNode{Name: "tables", Kind: "folder", Children: tables})
	}
	if len(views) > 0 {
		tree = append(tree, DbTreeNode{Name: "views", Kind: "folder", Children: views})
	}
	return tree, nil
}

// columnsOf 列出某张表的字段（含类型）。
func columnsOf(d *sql.DB, kind, dbName, table string) []DbTreeNode {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	var cols []DbTreeNode
	if kind == "mysql" {
		q := "SHOW COLUMNS FROM `" + dbName + "`.`" + table + "`"
		rows, err := d.QueryContext(ctx, q)
		if err != nil {
			return cols
		}
		defer rows.Close()
		for rows.Next() {
			var field, ctype, nullFlag string
			var key, def sql.NullString
			if err := rows.Scan(&field, &ctype, &nullFlag, &key, &def); err != nil {
				continue
			}
			cols = append(cols, DbTreeNode{Name: field, Kind: "column", Detail: ctype})
		}
	} else {
		rows, err := d.QueryContext(ctx, "PRAGMA table_info(`"+table+"`)")
		if err != nil {
			return cols
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				continue
			}
			cols = append(cols, DbTreeNode{Name: name, Kind: "column", Detail: ctype})
		}
	}
	return cols
}

// listRedisKeys 用 SCAN 列出键（上限 300，避免大库卡死），并附带类型。
func listRedisKeys(client *redis.Client, dbIndex int) ([]DbTreeNode, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	var keys []DbTreeNode
	iter := client.Scan(ctx, 0, "*", 200).Iterator()
	limit := 300
	for iter.Next(ctx) {
		k := iter.Val()
		kt, err := client.Type(ctx, k).Result()
		if err != nil {
			kt = "?"
		}
		keys = append(keys, DbTreeNode{Name: k, Kind: "key", Detail: kt})
		limit--
		if limit <= 0 {
			break
		}
	}
	if err := iter.Err(); err != nil {
		return keys, err
	}
	tree := []DbTreeNode{{Name: "DB " + fmt.Sprintf("%d", dbIndex), Kind: "folder", Children: keys}}
	return tree, nil
}
