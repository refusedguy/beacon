package observe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeQueryFixture(t *testing.T, dir string) {
	t.Helper()
	logs := filepath.Join(dir, logsDirName)
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	older := []string{
		`{"time":"2026-11-20T10:00:00Z","level":"INFO","event":"cycle","watchID":"w1","pr":10}`,
		`{"time":"2026-11-20T10:00:01Z","level":"ERROR","event":"merge_fail","watchID":"w2","pr":11,"error":"refused"}`,
		`plain text line`,
	}
	newer := []string{
		`{"time":"2026-11-20T10:01:00Z","level":"INFO","event":"verdict_published","watchID":"w1","pr":10,"revision":3}`,
		`{"time":"2026-11-20T10:01:01Z","level":"WARN","event":"actor_error","watchID":"w2","pr":11,"error":"boom"}`,
	}
	olderPath := filepath.Join(logs, "beacon-20261120T100000Z-1.log")
	newerPath := filepath.Join(logs, serverLogName)
	if err := os.WriteFile(olderPath, []byte(strings.Join(older, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newerPath, []byte(strings.Join(newer, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Both latest links are symlinks and must be skipped, not double-counted.
	for _, link := range []string{latestLinkName, serverLatestLinkName} {
		if err := os.Symlink(serverLogName, filepath.Join(logs, link)); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(olderPath, past, past); err != nil {
		t.Fatal(err)
	}
}

func TestReadLogsReturnsAllRecordsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	writeQueryFixture(t, dir)

	lines, err := ReadLogs(dir, LogFilter{JSON: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("получено %d строк, ожидалось 5: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"event":"cycle"`) || !strings.Contains(lines[4], `"event":"actor_error"`) {
		t.Errorf("порядок строк нарушен: %v", lines)
	}
	if lines[2] != "plain text line" {
		t.Errorf("не-JSON строка потеряна: %q", lines[2])
	}
}

func TestReadLogsFilters(t *testing.T) {
	dir := t.TempDir()
	writeQueryFixture(t, dir)

	tests := []struct {
		name string
		opts LogFilter
		want []string
	}{
		{
			name: "watchID",
			opts: LogFilter{WatchID: "w1", JSON: true},
			want: []string{`"event":"cycle"`, `"event":"verdict_published"`},
		},
		{
			name: "pr",
			opts: LogFilter{PR: 11, JSON: true},
			want: []string{`"event":"merge_fail"`, `"event":"actor_error"`},
		},
		{
			name: "only errors",
			opts: LogFilter{OnlyErrors: true, JSON: true},
			want: []string{`"event":"merge_fail"`, `"event":"actor_error"`},
		},
		{
			name: "tail",
			opts: LogFilter{Tail: 2, JSON: true},
			want: []string{`"event":"verdict_published"`, `"event":"actor_error"`},
		},
		{
			name: "watchID and tail",
			opts: LogFilter{WatchID: "w2", Tail: 1, JSON: true},
			want: []string{`"event":"actor_error"`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines, err := ReadLogs(dir, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != len(tc.want) {
				t.Fatalf("получено %d строк, ожидалось %d: %v", len(lines), len(tc.want), lines)
			}
			for i, want := range tc.want {
				if !strings.Contains(lines[i], want) {
					t.Errorf("строка %d = %q, ожидалось содержание %q", i, lines[i], want)
				}
			}
		})
	}
}

func TestReadLogsJSONFlagControlsFormat(t *testing.T) {
	dir := t.TempDir()
	writeQueryFixture(t, dir)

	jsonLines, err := ReadLogs(dir, LogFilter{JSON: true, WatchID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	textLines, err := ReadLogs(dir, LogFilter{JSON: false, WatchID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range jsonLines {
		if !strings.HasPrefix(jsonLines[i], "{") {
			t.Errorf("JSON-режим вернул не JSON: %q", jsonLines[i])
		}
		if strings.HasPrefix(textLines[i], "{") {
			t.Errorf("текстовый режим вернул JSON: %q", textLines[i])
		}
		if !strings.Contains(textLines[i], "watchID=w1") {
			t.Errorf("в текстовой строке нет поля: %q", textLines[i])
		}
	}
}

func TestReadLogsEmptyDirAndBadInput(t *testing.T) {
	dir := t.TempDir()
	lines, err := ReadLogs(dir, LogFilter{})
	if err != nil {
		t.Fatalf("отсутствие каталога логов не должно быть ошибкой: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("пустой каталог вернул строки: %v", lines)
	}
	if _, err := ReadLogs("", LogFilter{}); err == nil {
		t.Error("пустой dir должен давать ошибку")
	}
}
