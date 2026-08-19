package services

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"github.com/redis/go-redis/v9"
)

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
		// 用 mysql.Config.FormatDSN 构建 DSN：密码/库名含 @ : / 等特殊字符时自动转义，避免拼错
		mc := mysql.NewConfig()
		mc.User = input.Username
		mc.Passwd = input.Password
		mc.Net = "tcp"
		mc.Addr = net.JoinHostPort(input.Host, strconv.Itoa(port))
		mc.DBName = input.Database
		mc.ParseTime = true
		mc.Timeout = 10 * time.Second
		mc.ReadTimeout = 30 * time.Second
		mc.AllowNativePasswords = true
		d, err := sql.Open("mysql", mc.FormatDSN())
		if err != nil {
			return nil, err
		}
		// MySQL 可由服务端并发处理，开放一个小的连接池（4），提升 DatabasePage 多查询吞吐；
		// SQLite 保持单连接（WAL + 事务一致性需要）。
		d.SetMaxOpenConns(4)
		d.SetMaxIdleConns(4)
		d.SetConnMaxLifetime(5 * time.Minute)
		return &dbConnAdapter{sqlDB: d, kind: "mysql"}, nil
	case "sqlite":
		path := strings.TrimSpace(input.FilePath)
		if path == "" {
			return nil, fmt.Errorf("SQLite 需要指定数据库文件路径")
		}
		// Windows/含特殊字符路径：% ? # 会干扰 SQLite 的 file: DSN 解析，做转义；/ 与盘符冒号保留
		esc := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(filepath.ToSlash(path))
		dsn := "file:" + esc + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
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
			DB:          parseRedisDB(input.Database),
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

// parseRedisDB 从连接配置的 Database 字段解析 Redis 库索引（0-15，默认 0）。
func parseRedisDB(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	if n > 15 {
		n = 15
	}
	return n
}
