package db

import (
	"github.com/google/uuid"
)

// DbConnection 已保存的数据库连接配置。password 字段在落库前已由 service 层用 DPAPI 加密。
type DbConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	DbType   string `json:"dbType"` // mysql | redis | sqlite
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"` // 已加密（DPAPI）
	Database string `json:"database"`
	FilePath string `json:"filePath"`
	CreatedAt string `json:"createdAt"`
}

// ListDbConnections 返回全部连接（按创建时间倒序）。
func (d *Database) ListDbConnections() ([]DbConnection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query("SELECT id, name, db_type, host, port, username, password, \"database\", file_path, created_at FROM db_connections ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []DbConnection
	for rows.Next() {
		var c DbConnection
		if err := rows.Scan(&c.ID, &c.Name, &c.DbType, &c.Host, &c.Port, &c.Username, &c.Password, &c.Database, &c.FilePath, &c.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// GetDbConnection 按 ID 取单条连接。
func (d *Database) GetDbConnection(id string) (*DbConnection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow("SELECT id, name, db_type, host, port, username, password, \"database\", file_path, created_at FROM db_connections WHERE id = ?", id)
	var c DbConnection
	err := row.Scan(&c.ID, &c.Name, &c.DbType, &c.Host, &c.Port, &c.Username, &c.Password, &c.Database, &c.FilePath, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateDbConnection 插入新连接。
func (d *Database) CreateDbConnection(c *DbConnection) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	if c.CreatedAt == "" {
		c.CreatedAt = now()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`INSERT INTO db_connections (id, name, db_type, host, port, username, password, "database", file_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.DbType, c.Host, c.Port, c.Username, c.Password, c.Database, c.FilePath, c.CreatedAt, now(),
	)
	return err
}

// UpdateDbConnection 更新连接（按 ID）。
func (d *Database) UpdateDbConnection(c *DbConnection) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`UPDATE db_connections SET name = ?, db_type = ?, host = ?, port = ?, username = ?, password = ?, "database" = ?, file_path = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.DbType, c.Host, c.Port, c.Username, c.Password, c.Database, c.FilePath, now(), c.ID,
	)
	return err
}

// DeleteDbConnection 删除连接。
func (d *Database) DeleteDbConnection(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM db_connections WHERE id = ?", id)
	return err
}
