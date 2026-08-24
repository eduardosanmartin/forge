// Package logging provides forge's structured JSON logger. Every record —
// message text and string attribute values alike — passes through the
// best-effort secret redaction baseline from redact.go before reaching any
// destination.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config controls logger construction.
type Config struct {
	// Level is required: one of "debug", "info", "warn", "error".
	Level string
	// File optionally names a file that additionally receives records in
	// create+append mode. Stderr always receives records regardless.
	File string
}

// New builds a slog JSON logger writing to stderr and, when cfg.File is set,
// appending to that file as well. It returns the logger plus the opened file
// (nil when none) so callers can close it on shutdown.
func New(cfg Config) (*slog.Logger, *os.File, error) {
	level, err := ParseLevel(cfg.Level)
	if err != nil {
		return nil, nil, err
	}

	var w io.Writer = os.Stderr
	var file *os.File
	if cfg.File != "" {
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file %s: %w", cfg.File, err)
		}
		file = f
		w = io.MultiWriter(os.Stderr, f)
	}

	return slog.New(NewJSONHandler(w, level)), file, nil
}

// ParseLevel maps a level name onto its slog level. Unknown names (including
// the empty string) return an error listing the supported levels.
func ParseLevel(s string) (slog.Leveler, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return nil, fmt.Errorf("invalid log level %q (supported levels: debug, info, warn, error)", s)
}

// NewJSONHandler returns an slog.Handler emitting JSON records to w with all
// message text and string attribute values passed through Redact. It is
// exported so tests and embedders can wrap any io.Writer.
func NewJSONHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return &redactingHandler{
		inner: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}),
	}
}

// redactingHandler wraps another slog.Handler, redacting sensitive material
// from record messages and attribute values before delegation.
type redactingHandler struct {
	inner slog.Handler
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(cleaned)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	cleaned := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		cleaned.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, cleaned)
}

// redactAttr returns a copy of a with every contained string value redacted,
// recursing through groups and common slice/map shapes. Non-string values are
// returned untouched.
func redactAttr(a slog.Attr) slog.Attr {
	value := a.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(value.String()))
	case slog.KindAny:
		switch typed := value.Any().(type) {
		case string:
			return slog.String(a.Key, Redact(typed))
		case []string:
			out := make([]string, len(typed))
			for i, s := range typed {
				out[i] = Redact(s)
			}
			return slog.Any(a.Key, out)
		case map[string]string:
			out := make(map[string]string, len(typed))
			for k, v := range typed {
				out[k] = Redact(v)
			}
			return slog.Any(a.Key, out)
		case []any:
			out := make([]any, len(typed))
			for i, v := range typed {
				if s, ok := v.(string); ok {
					out[i] = Redact(s)
					continue
				}
				out[i] = v
			}
			return slog.Any(a.Key, out)
		case map[string]any:
			out := make(map[string]any, len(typed))
			for k, v := range typed {
				if s, ok := v.(string); ok {
					out[k] = Redact(s)
					continue
				}
				out[k] = v
			}
			return slog.Any(a.Key, out)
		default:
			return slog.Attr{Key: a.Key, Value: value}
		}
	case slog.KindGroup:
		groupAttrs := value.Group()
		cleaned := make([]slog.Attr, len(groupAttrs))
		for i, ga := range groupAttrs {
			cleaned[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(cleaned...)}
	default:
		return slog.Attr{Key: a.Key, Value: value}
	}
}
