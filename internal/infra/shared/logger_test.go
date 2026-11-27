package shared

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

const testSecret = "ghp_abcdefghijklmnopqrstuvwxyz123456"

func TestRedactHandlerMessage(t *testing.T) {
	out := renderRedacted(t, func(l *slog.Logger) { l.Info("token " + testSecret) })
	assertRedacted(t, out)
}

func TestRedactHandlerAttrs(t *testing.T) {
	cases := []struct {
		name  string
		attrs []slog.Attr
	}{
		{
			name:  "string",
			attrs: []slog.Attr{slog.String("token", testSecret)},
		},
		{
			name:  "error",
			attrs: []slog.Attr{slog.Any("err", errors.New("token "+testSecret))},
		},
		{
			name:  "group",
			attrs: []slog.Attr{slog.Group("details", slog.String("token", testSecret))},
		},
		{
			name: "nested group with error",
			attrs: []slog.Attr{
				slog.Group("details", slog.Any("err", errors.New("token "+testSecret))),
			},
		},
	}

	for _, tc := range cases {
		for _, path := range []string{"record", "with", "group"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				out := renderRedacted(t, func(l *slog.Logger) {
					args := make([]any, len(tc.attrs))
					for i, a := range tc.attrs {
						args[i] = a
					}
					switch path {
					case "record":
						l.Info("hello", args...)
					case "with":
						l.With(args...).Info("hello")
					case "group":
						l.WithGroup("outer").With(args...).Info("hello")
					}
				})
				assertRedacted(t, out)
			})
		}
	}
}

func renderRedacted(t *testing.T, fn func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	h := redactHandler{slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
	fn(slog.New(h))
	return buf.String()
}

func assertRedacted(t *testing.T, got string) {
	t.Helper()
	if strings.Contains(got, testSecret) {
		t.Fatalf("secret leaked: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("nothing redacted: %s", got)
	}
}

func BenchmarkRedactHandler(b *testing.B) {
	h := redactHandler{slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})}
	l := slog.New(h)
	cases := []struct {
		name  string
		msg   string
		attrs []any
	}{
		{"clean", "plain message with no secret", []any{slog.String("path", "/tmp/file"), slog.Int("count", 42), slog.Bool("ok", true)}},
		{"secret message", "token " + testSecret, []any{slog.String("path", "/tmp/file")}},
		{"secret attr", "plain message", []any{slog.String("token", testSecret), slog.Int("count", 42)}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				l.Info(tc.msg, tc.attrs...)
			}
		})
	}
}
