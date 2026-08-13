package services

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
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
