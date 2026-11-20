package observe

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var levelRank = map[Level]int{
	LevelDebug: 0,
	LevelInfo:  1,
	LevelWarn:  2,
	LevelError: 3,
}

// Logger writes one JSON object per line to every configured output. Two
// outputs are typical: stderr for the operator and a file for later reading.
// Writes are serialized so lines from concurrent goroutines do not interleave.
type Logger struct {
	mu      sync.Mutex
	min     Level
	outputs []io.Writer
}

// NewLogger builds a Logger that emits events at or above min to outputs.
// An unknown min falls back to info.
func NewLogger(min Level, outputs ...io.Writer) *Logger {
	if _, ok := levelRank[min]; !ok {
		min = LevelInfo
	}
	writers := make([]io.Writer, 0, len(outputs))
	for _, w := range outputs {
		if w != nil {
			writers = append(writers, w)
		}
	}
	return &Logger{min: min, outputs: writers}
}

// OpenLogFile opens path for appending and creates its parent directory. The
// caller owns the returned closer and should hand it to Logger.Close.
func OpenLogFile(path string) (io.WriteCloser, error) {
	if path == "" {
		return nil, fmt.Errorf("observe: пустой путь лога")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("observe: каталог лога: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("observe: открыть лог: %w", err)
	}
	return f, nil
}

// Write emits ev to every output as one JSON line. A field that cannot be
// encoded is reported on stderr and does not stop the other outputs.
func (l *Logger) Write(ev Event) {
	if l == nil || !l.enabled(ev.Level) {
		return
	}
	line := make(map[string]any, len(ev.Fields)+2)
	line["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	line["level"] = string(ev.Level)
	for k, v := range ev.Fields {
		line[k] = v
	}
	raw, err := json.Marshal(line)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observe: кодирование события: %v\n", err)
		return
	}
	raw = append(raw, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.outputs {
		if _, err := w.Write(raw); err != nil {
			fmt.Fprintf(os.Stderr, "observe: запись лога: %v\n", err)
		}
	}
}

func (l *Logger) enabled(level Level) bool {
	rank, ok := levelRank[level]
	if !ok {
		rank = levelRank[LevelInfo]
	}
	return rank >= levelRank[l.min]
}

// Close closes every output that implements io.Closer. stderr does not, so it
// is left open. The first error is returned.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var first error
	for _, w := range l.outputs {
		if c, ok := w.(io.Closer); ok {
			if err := c.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}
