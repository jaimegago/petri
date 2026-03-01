// Package logger provides structured JSON and console logging via zerolog.
package logger

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

type contextKey struct{}

// New creates a new structured JSON logger at the given log level.
func New(level string) zerolog.Logger {
	lvl := parseLevel(level)
	return zerolog.New(os.Stdout).
		Level(lvl).
		With().
		Timestamp().
		Logger()
}

// NewConsole creates a human-readable logger for CLI output.
func NewConsole(level string, out io.Writer) zerolog.Logger {
	if out == nil {
		out = os.Stdout
	}
	lvl := parseLevel(level)
	return zerolog.New(zerolog.ConsoleWriter{
		Out:        out,
		TimeFormat: time.RFC3339,
		NoColor:    false,
	}).
		Level(lvl).
		With().
		Timestamp().
		Logger()
}

// WithContext attaches a logger to a context.
func WithContext(ctx context.Context, log zerolog.Logger) context.Context {
	return log.WithContext(ctx)
}

// FromContext retrieves the logger from a context, falling back to a default.
func FromContext(ctx context.Context) *zerolog.Logger {
	l := zerolog.Ctx(ctx)
	if l.GetLevel() == zerolog.Disabled {
		def := New("info")
		return &def
	}
	return l
}

// WithLab returns a child logger with lab-scoped fields.
func WithLab(log zerolog.Logger, labID, company string, level int) zerolog.Logger {
	return log.With().
		Str("lab_id", labID).
		Str("company", company).
		Int("level", level).
		Logger()
}

// RedactedValue is a sentinel used to replace sensitive values in logs.
const RedactedValue = "[REDACTED]"

// Sensitive types that should never appear in logs.
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

func parseLevel(level string) zerolog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	default:
		return zerolog.InfoLevel
	}
}
