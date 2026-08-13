package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const dbQueryTimeout = 30 * time.Second
const dbMaxRows = 1000

// enrichEditable 探测结果是否来自「单表 SELECT 且含主键列」，若是则填充可编辑元数据。
func enrichEditable(c *dbConnAdapter, defaultDB, query string, res *DbQueryResult) {
	if !isSelectLike(query) {
		res.EditReason = "非 SELECT 查询"
		return
	}
	if res.RowCount == 0 || len(res.Columns) == 0 {
		res.EditReason = "查询无结果或无可显示列"
		return
	}
	table, dbName := detectEditableTable(query)
	if table == "" {
		res.EditReason = "非单表 SELECT（含 JOIN / 子查询 / UNION / 多表 / 聚合）"
		return
	}
	if c.kind == "mysql" && dbName == "" {
		if cur := currentDatabase(c.sqlDB); cur != "" {
			dbName = cur
		}
	}
	pk, reason := primaryKeyOf(c.sqlDB, c.kind, orDefault(dbName, defaultDB), table)
	if pk == "" {
		res.EditReason = reason
		return
	}
	hasPK := false
	for _, col := range res.Columns {
		if strings.EqualFold(col, pk) {
			hasPK = true
			break
		}
	}
	if !hasPK {
		res.EditReason = "主键列「" + pk + "」未包含在结果中"
		return
	}
	res.Editable = true
	res.TableName = table
	res.PrimaryKey = pk
}

// detectEditableTable 从简单 SELECT 中提取「单一表名」。
func detectEditableTable(q string) (table, dbName string) {
	s := strings.TrimSpace(q)
	if !strings.HasPrefix(strings.ToUpper(s), "SELECT") {
		return "", ""
	}
	fromIdx := strings.Index(strings.ToUpper(s), " FROM ")
	if fromIdx < 0 {
		return "", ""
	}
	rest := s[fromIdx+6:]
	up := strings.ToUpper(rest)
	if strings.Contains(up, "JOIN") || strings.Contains(up, "UNION") || strings.Contains(up, "(") || strings.Contains(up, ",") {
		return "", ""
	}
	for _, kw := range []string{" WHERE ", " GROUP ", " ORDER ", " HAVING ", " LIMIT ", " PROCEDURE "} {
		if i := strings.Index(strings.ToUpper(rest), kw); i >= 0 {
			rest = rest[:i]
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", ""
	}
	if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
		rest = rest[:sp]
	}
	rest = strings.Trim(rest, "`\"[]")
	if rest == "" {
		return "", ""
	}
	if dot := strings.Index(rest, "."); dot >= 0 {
		dbName = strings.Trim(rest[:dot], "`\"[]")
		rest = strings.Trim(rest[dot+1:], "`\"[]")
	}
	return rest, dbName
}

// primaryKeyOf 返回表的主键列（仅支持单列主键）。
func primaryKeyOf(d *sql.DB, kind, dbName, table string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	if d == nil {
		return "", "数据库连接不可用"
	}
	if kind == "mysql" {
		q := "SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE " +
			"WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY' " +
			"ORDER BY ORDINAL_POSITION"
		rows, err := d.QueryContext(ctx, q, dbName, table)
		if err != nil {
			return "", "无法读取主键信息：" + err.Error()
		}
		defer rows.Close()
		var cols []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				continue
			}
			cols = append(cols, c)
		}
		if len(cols) == 1 {
			return cols[0], ""
		}
		if len(cols) == 0 {
			return "", "表无主键（PRIMARY KEY）"
		}
		return "", "复合主键暂不支持编辑"
	}
	q := `PRAGMA table_info("` + strings.ReplaceAll(table, `"`, `""`) + `")`
	rows, err := d.QueryContext(ctx, q)
	if err != nil {
		return "", "无法读取主键信息：" + err.Error()
	}
	defer rows.Close()
	var pk string
	n := 0
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var isPk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &isPk); err != nil {
			continue
		}
		if isPk > 0 {
			if n == 0 {
				pk = name
			}
			n++
		}
	}
	if n == 1 {
		return pk, ""
	}
	if n == 0 {
		return "", "表无主键（PRIMARY KEY）"
	}
	return "", "复合主键暂不支持编辑"
}

// currentDatabase 返回 MySQL 连接当前所在的数据库。
func currentDatabase(d *sql.DB) string {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	var name string
	if err := d.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
		return ""
	}
	return name
}

func orDefault(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// quoteIdent 按数据库类型安全引用标识符。
func quoteIdent(kind, name string) string {
	name = strings.TrimSpace(name)
	if kind == "sqlite" {
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// isSelectLike 判断 SQL 是否返回结果集。
func isSelectLike(q string) bool {
	t := strings.TrimSpace(q)
	t = strings.TrimLeft(t, "(")
	upper := strings.ToUpper(t)
	for _, p := range []string{"SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "PRAGMA", "WITH"} {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

func execSQL(ctx context.Context, d *sql.DB, query string) (*DbQueryResult, error) {
	res := &DbQueryResult{}
	if isSelectLike(query) {
		rows, err := d.QueryContext(ctx, query)
		if err != nil {
			return res, err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return res, err
		}
		res.Columns = cols
		for rows.Next() {
			raw := make([]sql.RawBytes, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return res, err
			}
			cells := make([]string, len(cols))
			rowNull := make([]bool, len(cols))
			for i, b := range raw {
				if b == nil {
					cells[i] = ""
					rowNull[i] = true
				} else {
					cells[i] = string(b)
				}
			}
			res.Rows = append(res.Rows, cells)
			res.Nulls = append(res.Nulls, rowNull)
			if len(res.Rows) >= dbMaxRows {
				break
			}
		}
		res.RowCount = len(res.Rows)
		res.Success = true
		return res, rows.Err()
	}
	r, err := d.ExecContext(ctx, query)
	if err != nil {
		return res, err
	}
	n, _ := r.RowsAffected()
	res.Affected = n
	res.Message = fmt.Sprintf("OK, %d row(s) affected", n)
	res.Success = true
	return res, nil
}

func execRedis(ctx context.Context, client *redis.Client, query string) (*DbQueryResult, error) {
	res := &DbQueryResult{}
	parts := strings.Fields(query)
	if len(parts) == 0 {
		return res, fmt.Errorf("命令为空")
	}
	args := make([]interface{}, len(parts))
	for i, p := range parts {
		args[i] = p
	}
	cmd := client.Do(ctx, args...)
	val, err := cmd.Result()
	if err != nil {
		return res, err
	}
	res.Message = redisValueToString(val)
	res.Success = true
	return res, nil
}

// redisValueToString 递归把 Redis 回复（RESP）转成可读字符串。
func redisValueToString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "(nil)"
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%v", t)
	case []interface{}:
		var sb strings.Builder
		for i, item := range t {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprintf("%d) %s", i+1, redisValueToString(item)))
		}
		return sb.String()
	case map[string]interface{}:
		var sb strings.Builder
		i := 0
		for k, val := range t {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprintf("%s: %s", k, redisValueToString(val)))
			i++
		}
		return sb.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}
