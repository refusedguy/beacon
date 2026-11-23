package shared

import (
	"context"
	"log/slog"
	"os"
)

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
	return slog.New(redactHandler{slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l})})
}

type redactHandler struct{ slog.Handler }

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, slog.StringValue(Redact(r.Message)).String(), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(Redact(a.Value.String()))
		}
		if e, ok := a.Value.Any().(error); ok {
			a.Value = slog.StringValue(Redact(e.Error()))
		}
		nr.AddAttrs(a)
		return true
	})
	return h.Handler.Handle(ctx, nr)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(Redact(a.Value.String()))
		}
		clean = append(clean, a)
	}
	return redactHandler{h.Handler.WithAttrs(clean)}
}
