package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	// MySQL / SQLite 驱动（SQLite 用 modernc 纯 Go 实现，无 CGO）
	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"github.com/redis/go-redis/v9"
	"quickdock/internal/db"
	"quickdock/internal/platform"
)

// DbConnectionInput 前端传入的连接配置。password 为明文，落库前由本层用 DPAPI 加密。
type DbConnectionInput struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	DbType   string `json:"dbType"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"` // 明文；空表示不修改（更新时保留原值）
	Database string `json:"database"`
	FilePath string `json:"filePath"`
}

// DbQueryResult 查询结果（不落库）。
type DbQueryResult struct {
	Success    bool       `json:"success"`
	Columns    []string   `json:"columns"`
	Rows       [][]string `json:"rows"`
	Nulls      [][]bool   `json:"nulls"` // 与 Rows 同维度，标记该单元格是否为 SQL NULL
	RowCount   int        `json:"rowCount"`
	Affected   int64      `json:"affected"`
	Message    string     `json:"message"`
	Error      string     `json:"error"`
	DurationMs int64      `json:"durationMs"`
	// 可编辑元数据：当结果为「单表 SELECT 且含主键列」时填充，前端据此渲染可编辑网格。
	Editable   bool   `json:"editable"`
	TableName  string `json:"tableName"`
	PrimaryKey string `json:"primaryKey"`
	EditReason string `json:"editReason"` // 不可编辑时的原因，便于前端诊断（如复合主键 / 无主键 / 非单表查询）
}

// DbRowUpdateInput 前端提交的单行修改：以主键定位行，Sets 为列→新值，Nulls 为需置 NULL 的列。
type DbRowUpdateInput struct {
	TableName string            `json:"tableName"`
	PkColumn  string            `json:"pkColumn"`
	PkValue   string            `json:"pkValue"`
	Sets      map[string]string `json:"sets"`
	Nulls     []string          `json:"nulls"`
}

// DbTreeNode 库表浏览器树节点（SQL：库→表/视图→字段；Redis：键）。
type DbTreeNode struct {
	Name     string       `json:"name"`
	Kind     string       `json:"kind"` // folder | table | view | column | key
	Detail   string       `json:"detail"` // 字段类型 / Redis 类型
	Children []DbTreeNode `json:"children,omitempty"`
}

const dbQueryTimeout = 30 * time.Second
const dbMaxRows = 1000

// ListDbConnections 返回全部连接，password 脱敏（返回空串，避免泄露）。
func (a *AppService) ListDbConnections() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListDbConnections()
	if err != nil {
		return Fail(err)
	}
	for i := range list {
		list[i].Password = ""
	}
	return Ok(list)
}

// SaveDbConnection 新建或更新连接。密码明文入参，落库前 DPAPI 加密。
func (a *AppService) SaveDbConnection(input DbConnectionInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	input.DbType = strings.ToLower(strings.TrimSpace(input.DbType))
	if input.DbType == "" {
		input.DbType = "mysql"
	}
	switch input.DbType {
	case "mysql", "redis", "sqlite":
	default:
		return FailMsg("不支持的数据库类型: " + input.DbType)
	}
	if input.Name == "" {
		input.Name = input.Host
	}
	if input.Port == 0 {
		input.Port = defaultPort(input.DbType)
	}

	rec := &db.DbConnection{
		ID:       input.ID,
		Name:     strings.TrimSpace(input.Name),
		DbType:   input.DbType,
		Host:     strings.TrimSpace(input.Host),
		Port:     input.Port,
		Username: input.Username,
		Database: strings.TrimSpace(input.Database),
		FilePath: strings.TrimSpace(input.FilePath),
	}

	if input.ID != "" {
		// 更新：若密码为空，保留原有加密值
		existing, err := a.DB.GetDbConnection(input.ID)
		if err != nil {
			return Fail(err)
		}
		rec.Password = existing.Password
		if input.Password != "" {
			enc, e := platform.EncryptSecret(input.Password)
			if e != nil {
				return Fail(fmt.Errorf("密码加密失败: %w", e))
			}
			rec.Password = enc
		}
		if err := a.DB.UpdateDbConnection(rec); err != nil {
			return Fail(err)
		}
	} else {
		if input.Password != "" {
			enc, e := platform.EncryptSecret(input.Password)
			if e != nil {
				return Fail(fmt.Errorf("密码加密失败: %w", e))
			}
			rec.Password = enc
		}
		if err := a.DB.CreateDbConnection(rec); err != nil {
			return Fail(err)
		}
	}
	rec.Password = ""
	return Ok(rec)
}

// DeleteDbConnection 删除连接。
func (a *AppService) DeleteDbConnection(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteDbConnection(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// TestDbConnection 用传入配置（未保存也可）测试连通性。
func (a *AppService) TestDbConnection(input DbConnectionInput) *ApiResult {
	conn, err := toDbConn(input)
	if err != nil {
		return Ok(map[string]any{"ok": false, "error": err.Error()})
	}
	defer conn.close()
	if err := conn.ping(); err != nil {
		return Ok(map[string]any{"ok": false, "error": err.Error()})
	}
	return Ok(map[string]any{"ok": true})
}

// QueryDbConnection 对已保存连接执行查询/命令，返回结果。
func (a *AppService) QueryDbConnection(id string, query string) *ApiResult {
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
		return Ok(&DbQueryResult{Success: false, Error: err.Error()})
	}
	defer conn.close()

	start := time.Now()
	res, err := conn.exec(query)
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Success = false
		res.Error = err.Error()
		return Ok(res)
	}
	res.Success = true
	// 单表 SELECT 且含主键列时，标记结果可编辑（供前端网格内联修改）。
	if conn.kind != "redis" {
		enrichEditable(conn, saved.Database, query, res)
	}
	return Ok(res)
}

// enrichEditable 探测结果是否来自「单表 SELECT 且含主键列」，若是则填充可编辑元数据。
// 不可编辑时设置 EditReason，便于前端向用户解释为什么该结果只读。
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
	// MySQL：未显式给出库名时，用当前连接所在库兜底（避免连接未配置默认库时漏检主键）。
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
	// 主键必须出现在结果列中，才能安全构造 WHERE 定位行。
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
// 仅支持 FROM 后紧跟单表（可带库限定/反引号/别名），拒绝 JOIN / 子查询 / UNION / 多表。
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
	// 截到第一个终止关键字（WHERE/GROUP/ORDER/HAVING/LIMIT）
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
		rest = rest[:sp] // 去掉别名
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
// 第二返回值仅在 pk 为空时有意义，说明无法编辑的原因。
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
	// sqlite：PRAGMA table_info，pk>0 的列为主键
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

// currentDatabase 返回 MySQL 连接当前所在的数据库（用于未显式指定库名时兜底主键检测）。
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

// UpdateDbRow 以主键定位单行并提交修改（参数化，标识符按库类型安全引用）。
func (a *AppService) UpdateDbRow(id string, input DbRowUpdateInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(input.TableName) == "" || strings.TrimSpace(input.PkColumn) == "" {
		return FailMsg("缺少表名或主键列")
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
	if conn.kind == "redis" {
		return FailMsg("Redis 不支持行编辑")
	}

	var sets []string
	var args []interface{}
	for col, val := range input.Sets {
		sets = append(sets, quoteIdent(conn.kind, col)+" = ?")
		args = append(args, val)
	}
	for _, col := range input.Nulls {
		sets = append(sets, quoteIdent(conn.kind, col)+" = NULL")
	}
	if len(sets) == 0 {
		return Ok(map[string]any{"affected": 0, "message": "no changes"})
	}
	q := "UPDATE " + quoteIdent(conn.kind, input.TableName) +
		" SET " + strings.Join(sets, ", ") +
		" WHERE " + quoteIdent(conn.kind, input.PkColumn) + " = ?"
	args = append(args, input.PkValue)

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	r, err := conn.sqlDB.ExecContext(ctx, q, args...)
	if err != nil {
		return Fail(fmt.Errorf("更新失败: %w", err))
	}
	n, _ := r.RowsAffected()
	return Ok(map[string]any{"affected": n, "message": fmt.Sprintf("OK, %d row(s) affected", n)})
}

// quoteIdent 按数据库类型安全引用标识符（mysql 反引号，sqlite 双引号）。
func quoteIdent(kind, name string) string {
	name = strings.TrimSpace(name)
	if kind == "sqlite" {
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// ListDbTree 返回连接下的库表/字段结构（SQL）或键列表（Redis），用于左侧浏览器树。
func (a *AppService) ListDbTree(id string) *ApiResult {
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

	switch conn.kind {
	case "mysql", "sqlite":
		tree, e := listSqlDatabases(conn.sqlDB, conn.kind, saved.Database)
		if e != nil {
			return Fail(fmt.Errorf("读取库列表失败: %w", e))
		}
		return Ok(tree)
	case "redis":
		keys, e := listRedisKeys(conn.redis)
		if e != nil {
			return Fail(fmt.Errorf("读取键失败: %w", e))
		}
		return Ok(keys)
	}
	return Ok([]DbTreeNode{})
}

// ---- 内部：统一连接抽象 ----

type dbConnAdapter struct {
	sqlDB  *sql.DB
	redis  *redis.Client
	kind   string
}

func (c *dbConnAdapter) close() {
	if c.sqlDB != nil {
		_ = c.sqlDB.Close()
	}
	if c.redis != nil {
		_ = c.redis.Close()
	}
}

func (c *dbConnAdapter) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	if c.sqlDB != nil {
		return c.sqlDB.PingContext(ctx)
	}
	if c.redis != nil {
		return c.redis.Ping(ctx).Err()
	}
	return fmt.Errorf("未知连接类型")
}

func (c *dbConnAdapter) exec(query string) (*DbQueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	if c.redis != nil {
		return execRedis(ctx, c.redis, query)
	}
	return execSQL(ctx, c.sqlDB, query)
}

func toDbConn(input DbConnectionInput) (*dbConnAdapter, error) {
	input.DbType = strings.ToLower(strings.TrimSpace(input.DbType))
	switch input.DbType {
	case "mysql":
		port := input.Port
		if port == 0 {
			port = 3306
		}
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=10s&readTimeout=30s",
			input.Username, input.Password, input.Host, port, input.Database)
		d, err := sql.Open("mysql", dsn)
		if err != nil {
			return nil, err
		}
		d.SetMaxOpenConns(1)
		return &dbConnAdapter{sqlDB: d, kind: "mysql"}, nil
	case "sqlite":
		path := strings.TrimSpace(input.FilePath)
		if path == "" {
			return nil, fmt.Errorf("SQLite 需要指定数据库文件路径")
		}
		dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
		d, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		d.SetMaxOpenConns(1)
		return &dbConnAdapter{sqlDB: d, kind: "sqlite"}, nil
	case "redis":
		port := input.Port
		if port == 0 {
			port = 6379
		}
		opts := &redis.Options{
			Addr:        fmt.Sprintf("%s:%d", input.Host, port),
			Password:    input.Password,
			DB:          0,
			DialTimeout: 10 * time.Second,
			ReadTimeout: dbQueryTimeout,
		}
		client := redis.NewClient(opts)
		return &dbConnAdapter{redis: client, kind: "redis"}, nil
	}
	return nil, fmt.Errorf("不支持的数据库类型: %s", input.DbType)
}

func defaultPort(t string) int {
	switch t {
	case "mysql":
		return 3306
	case "redis":
		return 6379
	case "sqlite":
		return 0
	}
	return 0
}

// isSelectLike 判断 SQL 是否返回结果集。
// listSqlDatabases 列出 SQL 连接下「当前有权限的库」：
// MySQL 用 SHOW DATABASES（仅返回当前用户有权限的库）；SQLite 用 PRAGMA database_list（多为 main）。
// 顶层每个库是一个 database 节点；MySQL 库节点初始不展开（表结构按需加载），SQLite 直接挂完整 schema。
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

// columnsOf 列出某张表的字段（含类型）。mysql 用 `db`.`table` 全限定，避免依赖连接的默认库。
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

// ListDbObjects 返回某个库（MySQL）下的 表/视图 结构（含字段），用于树展开时按需加载。
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

// listRedisKeys 用 SCAN 列出键（上限 300，避免大库卡死），并附带类型。
func listRedisKeys(client *redis.Client) ([]DbTreeNode, error) {
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
	tree := []DbTreeNode{{Name: "keys", Kind: "folder", Children: keys}}
	return tree, nil
}

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
