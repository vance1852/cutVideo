// Package logging provides the structured logger used across the service. Log
// records never contain bearer tokens, password material or credential
// references.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type contextKey string

const (
	requestIDKey contextKey = "request_id"
	actorIDKey   contextKey = "actor_id"
	actorRoleKey contextKey = "actor_role"
)

// Redacted replaces any sensitive value that must not reach the log stream.
const Redacted = "[redacted]"

var sensitiveKeys = map[string]struct{}{
	"token":          {},
	"password":       {},
	"password_hash":  {},
	"credential_ref": {},
	"authorization":  {},
	"pepper":         {},
}

// New builds a logger for the configured level and format.
func New(level, format string, out io.Writer) *slog.Logger {
	if out == nil {
		out = os.Stdout
	}
	opts := &slog.HandlerOptions{Level: parseLevel(level), ReplaceAttr: redactAttr}
	var handler slog.Handler
	if strings.EqualFold(format, "text") {
		handler = slog.NewTextHandler(out, opts)
	} else {
		handler = slog.NewJSONHandler(out, opts)
	}
	return slog.New(handler)
}

// Discard builds a logger that drops every record; used by tests.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// WithRequestID stores the request identifier on the context.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestID reads the request identifier from the context.
func RequestID(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDKey).(string); ok {
		return value
	}
	return ""
}

// WithActor stores the authenticated actor on the context for log correlation.
func WithActor(ctx context.Context, actorID, role string) context.Context {
	ctx = context.WithValue(ctx, actorIDKey, actorID)
	return context.WithValue(ctx, actorRoleKey, role)
}

// Actor reads the actor identifier and role from the context.
func Actor(ctx context.Context) (string, string) {
	id, _ := ctx.Value(actorIDKey).(string)
	role, _ := ctx.Value(actorRoleKey).(string)
	return id, role
}

// FromContext decorates the logger with the correlation fields on the context.
func FromContext(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = Discard()
	}
	attrs := make([]any, 0, 6)
	if requestID := RequestID(ctx); requestID != "" {
		attrs = append(attrs, slog.String("request_id", requestID))
	}
	if actorID, role := Actor(ctx); actorID != "" {
		attrs = append(attrs, slog.String("actor_id", actorID), slog.String("actor_role", role))
	}
	if len(attrs) == 0 {
		return logger
	}
	return logger.With(attrs...)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
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

func redactAttr(_ []string, attr slog.Attr) slog.Attr {
	if _, sensitive := sensitiveKeys[strings.ToLower(attr.Key)]; sensitive {
		return slog.String(attr.Key, Redacted)
	}
	return attr
}
