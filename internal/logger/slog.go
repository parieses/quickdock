// slog / 标准 log 桥接：把第三方库日志汇入本包的统一日志文件，
// 使设置页日志面板成为唯一排障入口（否则依赖库默认打到 stderr，打包后不可见）。
//
// 用法：logger.Init(...) 之后调用 EnableSlogBridge()（见 main.go）。
//
// 级别映射：Debug 及以下丢弃（第三方 Debug 噪音大）；Info→I；Warn→W；Error+→E。
// 标准库 log（未标级别的库居多）一律按 I 记录，保留其原有时间戳前缀剥离后的正文。

package logger

import (
	"context"
	"log"
	"log/slog"
	"strings"
)

// bridgeHandler 把 slog 记录转发到 Logf。零状态（WithAttrs/WithGroup 返回副本），
// 可并发使用——Logf 内部自带锁。
type bridgeHandler struct{}

func (bridgeHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo // Debug 噪音不进文件
}

func (bridgeHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		v := a.Value.String()
		if v == "" {
			return true
		}
		if strings.ContainsAny(v, " \t\n") {
			v = `"` + v + `"`
		}
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		b.WriteString(v)
		return true
	})
	level := "I"
	switch {
	case r.Level >= slog.LevelError:
		level = "E"
	case r.Level >= slog.LevelWarn:
		level = "W"
	}
	// msg 走 %s：第三方消息可能含 % 等格式动词，绝不能当 format 用
	Logf(level, "%s", b.String())
	return nil
}

func (h bridgeHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h bridgeHandler) WithGroup(name string) slog.Handler  { return h }

// stdLogWriter 让标准库 log 包的输出汇入统一日志（按 Info 级别）。
type stdLogWriter struct{}

func (stdLogWriter) Write(p []byte) (int, error) {
	Logf("I", "%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// EnableSlogBridge 接管 slog 默认记录器与标准库 log 输出。
// 必须在 Init 之后调用；幂等（重复调用仅重复设置，无副作用）。
func EnableSlogBridge() {
	slog.SetDefault(slog.New(bridgeHandler{}))
	log.SetFlags(0) // 时间戳由 Logf 统一加，避免双重前缀
	log.SetOutput(stdLogWriter{})
}
