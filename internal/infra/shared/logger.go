package shared

import (
	"context"
	"log/slog"
	"os"

	"github.com/refusedguy/beacon/pkg/core/redact"
)

// NewLogger returns a JSON logger that redacts secrets from messages and attributes.
func NewLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l})
	return slog.New(redactHandler{h})
}

// redactHandler is the single slog handler that applies Redact to log output.
type redactHandler struct{ slog.Handler }

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	if !recordHasCandidate(r) {
		return h.Handler.Handle(ctx, r)
	}
	nr := slog.NewRecord(r.Time, r.Level, redact.Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redactAttr(a))
		return true
	})
	return h.Handler.Handle(ctx, nr)
}

// recordHasCandidate reports whether the message or any attribute needs
// redaction. A false result lets Handle pass the original record straight
// through instead of building a replacement.
func recordHasCandidate(r slog.Record) bool {
	if redact.HasCandidate(r.Message) {
		return true
	}
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if hasCandidateAttr(a) {
			found = true
			return false
		}
		return true
	})
	return found
}

func hasCandidateAttr(a slog.Attr) bool {
	return hasCandidateValue(a.Value)
}

func hasCandidateValue(v slog.Value) bool {
	switch v.Kind() {
	case slog.KindString:
		return redact.HasCandidate(v.String())
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return redact.HasCandidate(err.Error())
		}
	case slog.KindGroup:
		for _, a := range v.Group() {
			if hasCandidateAttr(a) {
				return true
			}
		}
	}
	return false
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = redactAttr(a)
	}
	return redactHandler{h.Handler.WithAttrs(clean)}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{h.Handler.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = redactValue(a.Value)
	return a
}

func redactValue(v slog.Value) slog.Value {
	switch v.Kind() {
	case slog.KindString:
		return slog.StringValue(redact.Redact(v.String()))
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.StringValue(redact.Redact(err.Error()))
		}
	case slog.KindGroup:
		attrs := v.Group()
		clean := make([]slog.Attr, len(attrs))
		for i, a := range attrs {
			clean[i] = redactAttr(a)
		}
		return slog.GroupValue(clean...)
	}
	return v
}
