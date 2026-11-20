// Package otel keeps a local, dependency-free trace file next to Beacon's
// state directory. Each finished span is appended as one NDJSON line, so the
// file is the source of truth for a local run and can be read with a text
// editor and no collector. An OTLP exporter and its kill switches are a
// separate concern: they never turn this file off.
package otel

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// DefaultFileName is the NDJSON trace file kept next to state.
	DefaultFileName = "server.trace.ndjson"

	defaultMaxBytes = 16 << 20 // 16 MiB
	defaultMaxAge   = 24 * time.Hour
	defaultMaxFiles = 5
)

// Record is one NDJSON line: a finished span in a stable, collector-free
// shape. Times are UTC and durations are microseconds so line tools can read
// the file without a decoder.
type Record struct {
	Time         time.Time         `json:"time"`
	Name         string            `json:"name"`
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	DurationUS   int64             `json:"duration_us"`
	Attrs        map[string]string `json:"attrs,omitempty"`
}

// RecordFromSpan converts a finished observe.Span into its on-disk shape.
func RecordFromSpan(s observe.Span) Record {
	r := Record{
		Time:       s.End.UTC(),
		Name:       s.Name,
		TraceID:    hex.EncodeToString(s.Ctx.TraceID[:]),
		SpanID:     hex.EncodeToString(s.Ctx.SpanID[:]),
		DurationUS: s.Duration().Microseconds(),
	}
	var zeroParent [8]byte
	if s.ParentSpanID != zeroParent {
		r.ParentSpanID = hex.EncodeToString(s.ParentSpanID[:])
	}
	if len(s.Attrs) > 0 {
		r.Attrs = make(map[string]string, len(s.Attrs))
		for _, a := range s.Attrs {
			r.Attrs[a.Key] = a.Value
		}
	}
	return r
}

// Option tunes a Sink. Zero or negative values are ignored, keeping the
// defaults in force for accidental configuration.
type Option func(*Sink)

// WithMaxBytes rotates once appending a line would exceed n bytes.
func WithMaxBytes(n int64) Option {
	return func(s *Sink) {
		if n > 0 {
			s.maxBytes = n
		}
	}
}

// WithMaxAge rotates once the active file is older than d.
func WithMaxAge(d time.Duration) Option {
	return func(s *Sink) {
		if d > 0 {
			s.maxAge = d
		}
	}
}

// WithMaxFiles keeps at most n rotated files; older ones are deleted.
func WithMaxFiles(n int) Option {
	return func(s *Sink) {
		if n > 0 {
			s.maxFiles = n
		}
	}
}

// WithClock replaces the clock used for age rotation. Tests inject a fixed
// clock so rotation does not depend on wall time.
func WithClock(now func() time.Time) Option {
	return func(s *Sink) {
		if now != nil {
			s.now = now
		}
	}
}

// Sink appends finished spans to one NDJSON file and rotates it by size or
// age. It is safe for concurrent use and implements observe.SpanSink.
type Sink struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	size     int64
	openedAt time.Time
	maxBytes int64
	maxAge   time.Duration
	maxFiles int
	now      func() time.Time
	lastErr  error
	closed   bool
}

// NewSink creates or opens the trace file at path and prunes old archives.
func NewSink(path string, opts ...Option) (*Sink, error) {
	s := &Sink{
		path:     path,
		maxBytes: defaultMaxBytes,
		maxAge:   defaultMaxAge,
		maxFiles: defaultMaxFiles,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("otel: create state dir: %w", err)
	}
	if err := s.openActive(); err != nil {
		return nil, err
	}
	s.pruneLocked()
	return s, nil
}

// openActive opens the active file in append mode and records its state. A
// pre-existing file inherits its modification time as the age anchor, so a
// long-idle file rotates on the next write instead of looking brand new.
func (s *Sink) openActive() error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("otel: open trace file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("otel: stat trace file: %w", err)
	}
	s.file = f
	s.size = st.Size()
	if s.size == 0 {
		s.openedAt = s.now()
	} else {
		s.openedAt = st.ModTime()
	}
	return nil
}

// Write appends one record, rotating first if the file is full or old.
func (s *Sink) Write(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("otel: encode record: %w", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		err := errors.New("otel: sink closed")
		s.lastErr = err
		return err
	}
	bySize := s.maxBytes > 0 && s.size+int64(len(line)) > s.maxBytes
	byAge := s.maxAge > 0 && s.now().Sub(s.openedAt) >= s.maxAge
	if bySize || byAge {
		if err := s.rotateLocked(); err != nil {
			s.lastErr = err
			return err
		}
	}
	if _, err := s.file.Write(line); err != nil {
		s.lastErr = err
		return fmt.Errorf("otel: append record: %w", err)
	}
	s.size += int64(len(line))
	return nil
}

// Emit implements observe.SpanSink. Write errors are retained and exposed via
// LastError: the sink interface has no error return and must never block the
// caller's span end.
func (s *Sink) Emit(span observe.Span) {
	_ = s.Write(RecordFromSpan(span))
}

// Rotate closes the active file, moves it to a numbered archive and opens a
// fresh one. It is also called automatically by Write.
func (s *Sink) Rotate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("otel: sink closed")
	}
	return s.rotateLocked()
}

func (s *Sink) rotateLocked() error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return fmt.Errorf("otel: close trace file: %w", err)
		}
		s.file = nil
	}
	archives := archivePaths(s.path)
	next := 1
	if len(archives) > 0 {
		next = archiveIndex(archives[len(archives)-1]) + 1
	}
	target := archivePath(s.path, next)
	if _, err := os.Stat(s.path); err == nil {
		if err := os.Rename(s.path, target); err != nil {
			return fmt.Errorf("otel: rotate trace file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("otel: stat trace file: %w", err)
	}
	s.pruneLocked()
	return s.openActive()
}

// pruneLocked drops the oldest archives so at most maxFiles remain.
func (s *Sink) pruneLocked() {
	archives := archivePaths(s.path)
	if len(archives) <= s.maxFiles {
		return
	}
	for _, p := range archives[:len(archives)-s.maxFiles] {
		_ = os.Remove(p)
	}
}

// Path reports the active file path.
func (s *Sink) Path() string { return s.path }

// LastError reports the most recent write or rotation failure, if any.
func (s *Sink) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Close flushes and closes the active file. It is idempotent.
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

// Decode reads NDJSON records from r. Blank lines are skipped; a malformed
// line stops decoding and reports its line number.
func Decode(r io.Reader) ([]Record, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var out []Record
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(b, &rec); err != nil {
			return out, fmt.Errorf("otel: trace line %d: %w", line, err)
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// Read reads the active trace file at path. A missing file is an error so a
// caller can tell "no file" from "empty file"; ReadAll tolerates it.
func Read(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Decode(f)
}

// ReadAll reads every rotated file oldest-first, then the active file. It is
// how a local run reconstructs its span history without a collector.
func ReadAll(path string) ([]Record, error) {
	var out []Record
	for _, p := range archivePaths(path) {
		recs, err := Read(p)
		if err != nil {
			return out, err
		}
		out = append(out, recs...)
	}
	recs, err := Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	return append(out, recs...), nil
}

// ReadStateDir reads the local trace history from a state directory.
func ReadStateDir(stateDir string) ([]Record, error) {
	return ReadAll(filepath.Join(stateDir, DefaultFileName))
}

// Install wires a local-first tracer into observe. Every finished span is
// written to the NDJSON file in stateDir, regardless of OTLP export settings
// or their kill switches. The returned function restores the previous tracer
// and closes the file.
func Install(stateDir string, opts ...Option) (restore func(), err error) {
	sink, err := NewSink(filepath.Join(stateDir, DefaultFileName), opts...)
	if err != nil {
		return nil, err
	}
	prev := observe.SetTracer(observe.NewTracer(observe.Config{Enabled: true, SpanSink: sink}))
	return func() {
		prev()
		_ = sink.Close()
	}, nil
}

// archivePaths returns rotated files for path, ordered oldest to newest.
func archivePaths(path string) []string {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type pair struct {
		index int
		path  string
	}
	var found []pair
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, stem+".") || !strings.HasSuffix(name, ext) {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(name, stem+"."), ext)
		n, err := strconv.Atoi(mid)
		if err != nil || n < 1 {
			continue
		}
		found = append(found, pair{n, filepath.Join(dir, name)})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].index < found[j].index })
	out := make([]string, 0, len(found))
	for _, p := range found {
		out = append(out, p.path)
	}
	return out
}

func archiveIndex(path string) int {
	// The numeric part is the text after the last dot of the archive stem.
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	idx := strings.LastIndex(stem, ".")
	if idx < 0 {
		return 0
	}
	n, err := strconv.Atoi(stem[idx+1:])
	if err != nil {
		return 0
	}
	return n
}

func archivePath(path string, index int) string {
	ext := filepath.Ext(path)
	return fmt.Sprintf("%s.%d%s", strings.TrimSuffix(path, ext), index, ext)
}
