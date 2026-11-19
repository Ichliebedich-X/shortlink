// Package logger 提供输出 JSON 的结构化日志器。
package logger

import (
	"log/slog"
	"os"
)

// New 初始化并返回一个输出 JSON 格式的 Logger
func New() *slog.Logger {
	// 创建 JSON 格式的 Handler，设置日志级别为 Debug
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	return slog.New(h)
}
