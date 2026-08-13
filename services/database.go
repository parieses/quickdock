package services

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		keys, e := listRedisKeys(conn.redis, parseRedisDB(saved.Database))
		if e != nil {
			return Fail(fmt.Errorf("读取键失败: %w", e))
		}
		return Ok(keys)
	}
	return Ok([]DbTreeNode{})
}
