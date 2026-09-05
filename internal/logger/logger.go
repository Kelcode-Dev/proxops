// Package logger provides the process-wide structured logger.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New builds a *slog.Logger with a JSON handler at the given level.
// level may be "debug", "info", "warn" or "error".
func New(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	return slog.New(h)
}
