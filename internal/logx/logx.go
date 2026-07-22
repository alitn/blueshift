// Package logx builds the application's structured logger. Output is JSON on
// stdout with Cloud Logging-compatible keys (severity, message, time) so log
// entries are parsed into structured fields without an agent-side config.
package logx

import (
	"io"
	"log/slog"
)

// New returns a slog.Logger that writes JSON with Cloud Logging keys at or
// above the given level.
func New(level slog.Level, w io.Writer) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceAttr,
	})
	return slog.New(h)
}

// replaceAttr remaps the top-level slog keys to the names Cloud Logging expects
// and translates the numeric level into a severity string. Attributes inside
// groups are left untouched.
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.LevelKey:
		a.Key = "severity"
		if lvl, ok := a.Value.Any().(slog.Level); ok {
			a.Value = slog.StringValue(severity(lvl))
		}
	case slog.MessageKey:
		a.Key = "message"
	}
	return a
}

// severity maps an slog level to a Cloud Logging severity string.
func severity(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARNING"
	default:
		return "ERROR"
	}
}
