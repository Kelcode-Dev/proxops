package logger

import (
	"log/slog"
	"testing"
)

func TestNewLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		l := New(level)
		if l == nil {
			t.Fatalf("New(%q) returned nil", level)
		}
	}
	// Unknown levels fall back to info without panicking.
	if l := New("nonsense"); l == nil {
		t.Error("New(nonsense) returned nil")
	}
	// Case-insensitive.
	if l := New("DEBUG"); l == nil {
		t.Error("New(DEBUG) returned nil")
	}
	_ = slog.Default
}
