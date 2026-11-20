package observe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NewLogger builds a slog logger. Anything other than the json format uses
// the human-readable text handler, which is the default.
func NewLogger(format string, level Level, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == "msg" && a.Value.Kind() == slog.KindString && a.Value.String() == "" {
				return slog.Attr{}
			}
			return a
		},
	}
	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

// LogWriter adapts the standard logger to the same slog stream. Legacy
// log.Printf calls reach the file as structured records instead of raw text,
// so a JSON log stays parseable line by line.
type LogWriter struct {
	log *slog.Logger
}

// NewLogWriter builds a writer that forwards each standard-log line to logger.
func NewLogWriter(logger *slog.Logger) *LogWriter {
	return &LogWriter{log: logger}
}

// Write implements io.Writer. A trailing newline is removed; an empty write is
// ignored so the file never gains blank records.
func (w *LogWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w == nil || w.log == nil {
		return n, nil
	}
	message := strings.TrimRight(string(p), "\r\n")
	if message == "" {
		return n, nil
	}
	w.log.Log(context.Background(), LevelInfo, "", FieldEvent, "legacy_log", "message", message)
	return n, nil
}

// RunLog is the log file of one process run. Path is the name an operator
// reads; the file stays open for the lifetime of the process.
type RunLog struct {
	Path string
	file *os.File
}

// OpenRunLog creates a fresh log file under <dir>/logs. The name carries a UTC
// timestamp and a process id, so a restart never appends to an older run. A
// suffix keeps the name unique if the same timestamp and pid occur twice.
// beacon.latest.log in the same directory points at the file just created.
func OpenRunLog(dir string, now time.Time, pid int) (*RunLog, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("observe: пустой каталог логов")
	}
	logsDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return nil, fmt.Errorf("observe: каталог логов: %w", err)
	}
	stamp := now.UTC().Format("20060102T150405.000000000Z")
	base := fmt.Sprintf("beacon-%s-%d", stamp, pid)
	for n := 1; ; n++ {
		name := base + ".log"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.log", base, n)
		}
		path := filepath.Join(logsDir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("observe: открыть лог: %w", err)
		}
		if err := pointLatest(logsDir, name); err != nil {
			_ = f.Close()
			return nil, err
		}
		return &RunLog{Path: path, file: f}, nil
	}
}

// pointLatest replaces the latest pointer with a relative symlink to name.
func pointLatest(logsDir, name string) error {
	latest := filepath.Join(logsDir, "beacon.latest.log")
	if err := os.Remove(latest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("observe: удалить beacon.latest.log: %w", err)
	}
	if err := os.Symlink(name, latest); err != nil {
		return fmt.Errorf("observe: symlink beacon.latest.log: %w", err)
	}
	return nil
}

// Writer returns the open log file. A nil RunLog discards everything.
func (l *RunLog) Writer() io.Writer {
	if l == nil || l.file == nil {
		return io.Discard
	}
	return l.file
}

// Close closes the log file.
func (l *RunLog) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}
