// Package logger provides structured logging helpers built on log/slog.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type contextKey struct{}

// New creates a structured JSON logger at the given log level writing to stdout.
func New(level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(level),
	}))
}

// NewConsole creates a human-readable text logger for CLI output.
func NewConsole(level string, out io.Writer) *slog.Logger {
	if out == nil {
		out = os.Stdout
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
		Level: parseLevel(level),
	}))
}

// Nop returns a logger that discards all output. Useful in tests.
func Nop() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// WithContext attaches a logger to a context.
func WithContext(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, log)
}

// FromContext retrieves the logger from a context, falling back to a default info logger.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return New("info")
}

// WithLab returns a child logger with lab-scoped fields attached.
func WithLab(log *slog.Logger, labID, company string, level int) *slog.Logger {
	return log.With("lab_id", labID, "company", company, "level", level)
}

// RedactedValue is a sentinel used to replace sensitive values in logs.
const RedactedValue = "[REDACTED]"

// sensitiveKeys lists substrings that identify credential-bearing log field names.
var sensitiveKeys = []string{
	"token", "password", "secret", "key", "credential",
	"access_key", "aws_secret", "kubeconfig",
}

// IsSensitiveKey returns true if the log field name looks like a credential.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
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
