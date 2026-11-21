package observe

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// LogFilter selects records when reading logs back from disk. cmd/beacon uses
// it for `beacon logs` so the command never has to call os.ReadFile itself.
type LogFilter struct {
	Tail       int               // keep only the last N matching records; <= 0 keeps all (CLI: --tail 0)
	WatchID    string            // keep records with this watchID; empty keeps all
	PR         int               // keep records with this pr; zero keeps all
	OnlyErrors bool              // keep ERROR records and records carrying an error field
	JSON       bool              // encode every record as a JSON object; false renders text lines
	OnTruncate func(dropped int) // called when Tail drops older matching records
}

// ReadLogs reads the log files under <dir>/logs and returns the records that
// match opts. Symlinks (beacon.latest.log and beacon.mcp.latest.log) are
// skipped so a record is not returned twice through its link. Files are read
// oldest first by modification time; Tail then keeps the most recent matches.
func ReadLogs(dir string, opts LogFilter) ([]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("observe: пустой каталог логов")
	}
	logsDir := filepath.Join(dir, logsDirName)
	entries, err := os.ReadDir(logsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("observe: каталог логов: %w", err)
	}

	type logFile struct {
		path string
		mod  int64
	}
	var files []logFile
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if !isLogFileName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{
			path: filepath.Join(logsDir, entry.Name()),
			mod:  info.ModTime().UnixNano(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mod != files[j].mod {
			return files[i].mod < files[j].mod
		}
		return files[i].path < files[j].path
	})

	var matched []string
	var scanErr error
	for _, file := range files {
		f, err := os.Open(file.path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r")
			if line == "" || !matchLogLine(line, opts) {
				continue
			}
			if opts.JSON {
				matched = append(matched, encodeLogRecordJSON(line))
			} else {
				matched = append(matched, formatLogLine(line))
			}
		}
		// A line longer than the scanner buffer stops Scan and leaves valid
		// records unread. Surface it instead of dropping the rest in silence.
		if err := scanner.Err(); err != nil && scanErr == nil {
			scanErr = fmt.Errorf("observe: чтение %s: %w", file.path, err)
		}
		_ = f.Close()
	}
	if scanErr != nil {
		return nil, scanErr
	}
	if opts.Tail > 0 && len(matched) > opts.Tail {
		dropped := len(matched) - opts.Tail
		matched = matched[len(matched)-opts.Tail:]
		if opts.OnTruncate != nil {
			opts.OnTruncate(dropped)
		}
	}
	if matched == nil {
		return []string{}, nil
	}
	return matched, nil
}

// isLogFileName reports whether name is a run log or a server JSONL log,
// including lumberjack's rotated backups.
func isLogFileName(name string) bool {
	return strings.HasPrefix(name, "beacon") &&
		(strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".jsonl"))
}

// matchLogLine applies the field filters to one line. A line that is not a
// JSON object cannot match a field filter.
func matchLogLine(line string, opts LogFilter) bool {
	if opts.WatchID == "" && opts.PR == 0 && !opts.OnlyErrors {
		return true
	}
	record, ok := parseLogRecord(line)
	if !ok {
		return false
	}
	if opts.WatchID != "" {
		got, _ := record[FieldWatchID].(string)
		if got != opts.WatchID {
			return false
		}
	}
	if opts.PR != 0 {
		pr, ok := numericField(record[FieldPR])
		if !ok || pr != opts.PR {
			return false
		}
	}
	if opts.OnlyErrors {
		level, _ := record["level"].(string)
		isError := strings.EqualFold(level, "ERROR")
		if errValue, present := record[FieldError]; present && errValue != nil {
			isError = true
		}
		if !isError {
			return false
		}
	}
	return true
}

// parseLogRecord decodes one JSONL record. Both the camelCase cycleID written
// by generic loggers and the snake_case cycle_id of the server contract are
// left as-is; callers interested in a cycle can accept either key.
func parseLogRecord(line string) (map[string]any, bool) {
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return nil, false
	}
	return record, true
}

// encodeLogRecordJSON turns one stored line into a JSON object. A JSONL line
// is already a JSON object and passes through with its numbers and key order
// intact. slog text-handler lines and plain text are parsed into fields and
// encoded as objects. The --json stream therefore never mixes formats.
func encodeLogRecordJSON(line string) string {
	if _, ok := parseLogRecord(line); ok {
		return line
	}
	record := parseLogFmtLine(line)
	if encoded, err := json.Marshal(record); err == nil {
		return string(encoded)
	}
	encoded, _ := json.Marshal(map[string]any{"message": line})
	return string(encoded)
}

// parseLogFmtLine decodes a slog text-handler line such as
// `time=... level=INFO event=verdict_published watchID=w1 pr=10`. Quoted values
// may contain spaces and a backslash escapes the next byte. Tokens without an
// equals sign are joined into the message field so no text is lost.
func parseLogFmtLine(line string) map[string]any {
	record := make(map[string]any)
	var extra []string
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		keyStart := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= len(line) || line[i] != '=' {
			extra = append(extra, line[keyStart:i])
			continue
		}
		key := line[keyStart:i]
		i++ // skip '='
		valueStart := i
		var value string
		if i < len(line) && line[i] == '"' {
			i++
			var b strings.Builder
			for i < len(line) {
				if line[i] == '\\' && i+1 < len(line) {
					b.WriteByte(line[i+1])
					i += 2
					continue
				}
				if line[i] == '"' {
					i++
					break
				}
				b.WriteByte(line[i])
				i++
			}
			value = b.String()
		} else {
			for i < len(line) && line[i] != ' ' {
				i++
			}
			value = line[valueStart:i]
		}
		if key != "" {
			record[key] = value
		}
	}
	if len(extra) > 0 {
		record["message"] = strings.Join(extra, " ")
	}
	return record
}

// numericField converts a JSON number (or a numeric string) to an int.
func numericField(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	case int:
		return v, true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	default:
		return 0, false
	}
}

// formatLogLine renders one record as "<time> <LEVEL> <event> key=value ...".
// Non-JSON lines are already text and pass through unchanged.
func formatLogLine(line string) string {
	record, ok := parseLogRecord(line)
	if !ok {
		return line
	}
	timeValue, _ := record["time"].(string)
	levelValue, _ := record["level"].(string)
	eventValue, _ := record[FieldEvent].(string)
	var b strings.Builder
	if timeValue != "" {
		b.WriteString(timeValue)
		b.WriteByte(' ')
	}
	if levelValue != "" {
		b.WriteString(levelValue)
		b.WriteByte(' ')
	}
	b.WriteString(eventValue)
	keys := make([]string, 0, len(record))
	for k := range record {
		switch k {
		case "time", "level", "msg", FieldEvent:
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(fmt.Sprint(record[k]))
	}
	return strings.TrimSpace(b.String())
}
