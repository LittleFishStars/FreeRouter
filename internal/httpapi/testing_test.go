package httpapi

import (
	"io"
	"log/slog"
)

// discardLogger 给测试用的静音 logger：测试输出里不该混进服务端日志。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
