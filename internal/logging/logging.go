// Package logging 提供全局 slog 初始化与请求级 logger。
// 选 slog 而不是 zap/zerolog：标准库，零依赖，结构化输出够用（PLAN 7 依赖最小化）。
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// Options 是日志初始化参数。
type Options struct {
	Level  string // debug / info / warn / error
	Format string // text / json
}

// Setup 初始化全局 logger 并返回它。
func Setup(opts Options) *slog.Logger {
	level := parseLevel(opts.Level)
	handlerOpts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	if strings.EqualFold(opts.Format, "json") {
		h = slog.NewJSONHandler(os.Stdout, handlerOpts)
	} else {
		h = slog.NewTextHandler(os.Stdout, handlerOpts)
	}

	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithLogger 把 logger 放进 context，供下游（含 http 层）取用。
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext 取出请求级 logger；没有则退回全局 logger，绝不返回 nil。
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
