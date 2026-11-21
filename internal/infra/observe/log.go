package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Names of the log files and latest links under <stateDir>/logs. The server
// file is a fixed name so both links can always resolve to it; short commands
// use timestamped files and never touch either link.
const (
	logsDirName          = "logs"
	serverLogName        = "beacon.mcp.jsonl"
	latestLinkName       = "beacon.latest.log"
	serverLatestLinkName = "beacon.mcp.latest.log"
	serverMaxSizeMB      = 15
	serverMaxBackups     = 5
)

// LogLevelEnv selects the minimum level of a preconfigured server logger. It
// matches the variable the CLI reads; an unknown value falls back to info.
const LogLevelEnv = "BEACON_LOG_LEVEL"

// NewLogger builds a slog logger. Anything other than the json format uses
// the human-readable text handler, which is the default.
func NewLogger(format string, level Level, w io.Writer) *slog.Logger {
	opts := newHandlerOptions(level)
	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

// newHandlerOptions drops the empty message so an event-only record does not
// carry a useless "msg" key in either output format.
func newHandlerOptions(level Level) *slog.HandlerOptions {
	return &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == "msg" && a.Value.Kind() == slog.KindString && a.Value.String() == "" {
				return slog.Attr{}
			}
			return a
		},
	}
}

// serverHandlerOptions is the JSONL contract of a long-lived server. Records
// carry time and level from slog, event from eventAttrs, and the structured
// fields watchID, pr, revision and error when set. The cycle identifier is
// emitted as cycle_id; callers still use the FieldCycleID constant in Go.
func serverHandlerOptions(level Level) *slog.HandlerOptions {
	opts := newHandlerOptions(level)
	base := opts.ReplaceAttr
	opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
		a = base(groups, a)
		if a.Key == FieldCycleID {
			a.Key = "cycle_id"
		}
		return a
	}
	return opts
}

// Secret patterns shared with store.Redact. observe cannot import store: both
// are infrastructure packages, so the filter is duplicated here and applied
// to the bytes before they reach beacon.mcp.jsonl.
var (
	githubTokenRE   = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)
	telegramTokenRE = regexp.MustCompile(`[0-9]+:[A-Za-z0-9_-]{35}`)
	authHeaderRE    = regexp.MustCompile(`(?i)(authorization["'[:space:]]*[:=]["'[:space:]]*)(?:(?:bearer|token|basic)[[:space:]]+)?[A-Za-z0-9._~+/=-]+`)
	urlCredsRE      = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@[:space:]:]+:[^/@[:space:]]+@`)
)

// Redact removes GitHub tokens, Telegram bot tokens, Authorization credentials
// and URL credentials from a log line. It is deliberately byte-based so it
// catches a secret in a structured field, in a message and in a legacy
// log.Printf line alike.
func Redact(src []byte) []byte {
	if len(src) == 0 {
		return append([]byte(nil), src...)
	}
	out := githubTokenRE.ReplaceAll(src, []byte("[REDACTED]"))
	out = telegramTokenRE.ReplaceAll(out, []byte("[REDACTED]"))
	out = authHeaderRE.ReplaceAll(out, []byte("${1}[REDACTED]"))
	out = urlCredsRE.ReplaceAll(out, []byte("${1}[REDACTED]@"))
	return out
}

// redactingWriter applies Redact to every write before handing it to the
// rotating file. It reports the original length so slog's writer contract is
// satisfied even though the redacted bytes can be shorter.
type redactingWriter struct{ w io.Writer }

func (w *redactingWriter) Write(p []byte) (int, error) {
	if _, err := w.w.Write(Redact(p)); err != nil {
		return 0, err
	}
	return len(p), nil
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

// RunLog is the log file of one process. Path is the name an operator reads;
// the writer stays open for the lifetime of the process.
type RunLog struct {
	Path   string
	out    io.Writer
	close  func() error
	logger *slog.Logger
}

// ensureLogsDir validates dir and creates <dir>/logs.
func ensureLogsDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("observe: пустой каталог логов")
	}
	logsDir := filepath.Join(dir, logsDirName)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return "", fmt.Errorf("observe: каталог логов: %w", err)
	}
	return logsDir, nil
}

// openFreshLog creates a timestamped file under logsDir. It never changes a
// symlink: whether a link moves is the caller's decision.
func openFreshLog(logsDir string, now time.Time, pid int) (*RunLog, error) {
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
		return &RunLog{Path: path, out: f, close: f.Close}, nil
	}
}

// OpenCLILog opens a fresh log for a short command (task, doctor, logs,
// inspect, gates). It deliberately leaves beacon.latest.log and
// beacon.mcp.latest.log alone so a one-shot command cannot hide the log of the
// long-lived MCP server from an operator following the link.
func OpenCLILog(dir string) (*RunLog, error) {
	logsDir, err := ensureLogsDir(dir)
	if err != nil {
		return nil, err
	}
	return openFreshLog(logsDir, time.Now(), os.Getpid())
}

// OpenServerLog opens the rotating JSONL log of a long-lived process (beacon
// mcp or beacon daemon). The active file is always <dir>/logs/beacon.mcp.jsonl;
// lumberjack moves older data into sibling files at 15 MB and keeps five
// backups. Both latest links point at the active file, so following either one
// keeps working after short CLI commands have written their own logs.
func OpenServerLog(dir string) (*RunLog, error) {
	logsDir, err := ensureLogsDir(dir)
	if err != nil {
		return nil, err
	}
	name := serverLogName
	path := filepath.Join(logsDir, name)

	// Create the active file before linking it: both links must resolve even
	// before the first record is written.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return nil, fmt.Errorf("observe: открыть лог сервера: %w", err)
	} else {
		_ = f.Close()
	}

	rotating := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    serverMaxSizeMB,
		MaxBackups: serverMaxBackups,
		LocalTime:  true,
	}
	for _, link := range []string{serverLatestLinkName, latestLinkName} {
		if err := pointLatestTo(logsDir, link, name); err != nil {
			_ = rotating.Close()
			return nil, err
		}
	}

	level := LevelInfo
	if raw := os.Getenv(LogLevelEnv); raw != "" {
		if parsed, ok := ParseLevel(raw); ok {
			level = parsed
		}
	}
	filtered := &redactingWriter{w: rotating}
	return &RunLog{
		Path:   path,
		out:    filtered,
		close:  rotating.Close,
		logger: slog.New(slog.NewJSONHandler(filtered, serverHandlerOptions(level))),
	}, nil
}

// OpenRunLog is the original constructor kept for callers that still rely on
// it. It creates a timestamped file and repoints beacon.latest.log. New code
// should use OpenServerLog for long-lived processes and OpenCLILog for short
// commands; a short command must never move the server's latest link.
func OpenRunLog(dir string, now time.Time, pid int) (*RunLog, error) {
	logsDir, err := ensureLogsDir(dir)
	if err != nil {
		return nil, err
	}
	run, err := openFreshLog(logsDir, now, pid)
	if err != nil {
		return nil, err
	}
	if err := pointLatest(logsDir, filepath.Base(run.Path)); err != nil {
		_ = run.Close()
		return nil, err
	}
	return run, nil
}

// pointLatest replaces the legacy latest pointer with a relative symlink.
func pointLatest(logsDir, name string) error {
	return pointLatestTo(logsDir, latestLinkName, name)
}

// pointLatestTo replaces one named pointer with a relative symlink to name.
func pointLatestTo(logsDir, linkName, name string) error {
	latest := filepath.Join(logsDir, linkName)
	if err := os.Remove(latest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("observe: удалить %s: %w", linkName, err)
	}
	if err := os.Symlink(name, latest); err != nil {
		return fmt.Errorf("observe: symlink %s: %w", linkName, err)
	}
	return nil
}

// Writer returns the open log writer. A nil RunLog discards everything.
func (l *RunLog) Writer() io.Writer {
	if l == nil || l.out == nil {
		return io.Discard
	}
	return l.out
}

// Logger returns the slog logger preconfigured for a server log. CLI logs
// leave it nil: their callers build a logger with NewLogger and Writer.
func (l *RunLog) Logger() *slog.Logger {
	if l == nil {
		return nil
	}
	return l.logger
}

// Close closes the log writer, flushing a rotating writer as needed.
func (l *RunLog) Close() error {
	if l == nil || l.close == nil {
		return nil
	}
	return l.close()
}

// GateResultFence is the info string of the machine-readable fence at the end
// of a gate report (ADR 0235, ADR 0236). Fences that appear earlier in a report
// are illustrative and are never the gate result (HD-048).
const GateResultFence = "aif-gate-result"

// fencedBlock is one closed fenced code block of a report. info is the text on
// the opening fence line after the marker. body is the lines up to the closing
// fence.
type fencedBlock struct {
	info string
	body string
}

// fenceMarker reports the marker run and the info string of an opening fence
// line. A fence opens with at least three backticks or tildes after optional
// indentation. marker is empty when the line does not open a fence.
func fenceMarker(line string) (marker, info string) {
	s := strings.TrimLeft(line, " \t")
	if len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return "", ""
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	if n < 3 {
		return "", ""
	}
	if s[0] == '`' && strings.ContainsRune(s[n:], '`') {
		return "", ""
	}
	return s[:n], strings.TrimSpace(s[n:])
}

// closesFence reports whether line closes a fence opened with marker. A closing
// fence holds only the same marker character, at least as many times as the
// opener, and may be indented.
func closesFence(line, marker string) bool {
	if marker == "" {
		return false
	}
	s := strings.TrimSpace(line)
	if len(s) < len(marker) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != marker[0] {
			return false
		}
	}
	return true
}

// lastFencedBlock returns the last closed fenced block in out. An unclosed
// fence takes the rest of the output, so no block after it can be returned.
func lastFencedBlock(out string) (fencedBlock, bool) {
	lines := strings.Split(out, "\n")
	var last fencedBlock
	found := false
	for i := 0; i < len(lines); i++ {
		marker, info := fenceMarker(lines[i])
		if marker == "" {
			continue
		}
		body := make([]string, 0, len(lines)-i-1)
		closed := false
		for j := i + 1; j < len(lines); j++ {
			if closesFence(lines[j], marker) {
				closed = true
				i = j
				break
			}
			body = append(body, lines[j])
		}
		if !closed {
			break
		}
		last = fencedBlock{info: info, body: strings.Join(body, "\n")}
		found = true
	}
	return last, found
}

// LastGateResult returns the JSON body of the final fenced code block of out
// when that block is a gate result. The block qualifies when its info string
// is GateResultFence and its body is a JSON object. Only the final block can
// qualify: an earlier fence is illustrative, so the function ignores it even
// when it carries a gate result marker and valid JSON. The boolean is false
// for empty output, an unclosed fence, a wrong marker and malformed JSON.
func LastGateResult(out string) (string, bool) {
	block, ok := lastFencedBlock(out)
	if !ok || !strings.EqualFold(block.info, GateResultFence) {
		return "", false
	}
	body := strings.TrimSpace(block.body)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err != nil || obj == nil {
		return "", false
	}
	return body, true
}
