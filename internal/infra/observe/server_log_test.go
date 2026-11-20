package observe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenServerLogWritesRotatingJSONLAndBothSymlinks(t *testing.T) {
	dir := t.TempDir()
	run, err := OpenServerLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()

	wantPath := filepath.Join(dir, logsDirName, serverLogName)
	if run.Path != wantPath {
		t.Fatalf("Path = %q, ожидалось %q", run.Path, wantPath)
	}

	logsDir := filepath.Join(dir, logsDirName)
	for _, link := range []string{serverLatestLinkName, latestLinkName} {
		target, err := os.Readlink(filepath.Join(logsDir, link))
		if err != nil {
			t.Fatalf("%s не symlink: %v", link, err)
		}
		if target != serverLogName {
			t.Errorf("%s указывает на %q, ожидалось %q", link, target, serverLogName)
		}
	}

	logger := run.Logger()
	if logger == nil {
		t.Fatal("серверный лог должен нести готовый slog-логгер")
	}
	logger.Log(context.Background(), LevelInfo, "",
		FieldEvent, "cycle",
		FieldCycleID, "cycle-1",
		FieldWatchID, "watch:1",
		FieldPR, 12,
		FieldRevision, uint64(7),
	)

	raw, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("ожидалась одна строка, получено %d: %q", len(lines), raw)
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("первая строка невалидный JSON %q: %v", lines[0], err)
	}
	for key, want := range map[string]any{
		"event": "cycle", "cycle_id": "cycle-1", FieldWatchID: "watch:1",
		FieldPR: float64(12), FieldRevision: float64(7), "level": "INFO",
	} {
		if first[key] != want {
			t.Errorf("поле %s = %v, ожидалось %v", key, first[key], want)
		}
	}
	if _, ok := first["time"].(string); !ok {
		t.Error("нет поля time")
	}
	if _, ok := first["msg"]; ok {
		t.Error("пустое сообщение slog не должно попадать в JSON")
	}
}

func TestServerLogErrorFieldIsSerialized(t *testing.T) {
	dir := t.TempDir()
	run, err := OpenServerLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()

	run.Logger().Log(context.Background(), LevelWarn, "", FieldEvent, "merge_fail",
		FieldWatchID, "watch:9", FieldPR, 42, FieldError, "refused")

	raw, err := os.ReadFile(run.Path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		_ = json.Unmarshal(scanner.Bytes(), &record)
		if record[FieldEvent] == "merge_fail" {
			break
		}
	}
	if record[FieldError] != "refused" {
		t.Errorf("error = %v, ожидалось refused", record[FieldError])
	}
}

func TestOpenCLILogNeverTouchesSymlinks(t *testing.T) {
	dir := t.TempDir()
	server, err := OpenServerLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	logsDir := filepath.Join(dir, logsDirName)
	readLinks := func() map[string]string {
		out := make(map[string]string)
		for _, link := range []string{serverLatestLinkName, latestLinkName} {
			target, err := os.Readlink(filepath.Join(logsDir, link))
			if err != nil {
				t.Fatalf("%s не symlink: %v", link, err)
			}
			out[link] = target
		}
		return out
	}
	before := readLinks()

	first, err := OpenCLILog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := OpenCLILog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()

	if first.Path == second.Path {
		t.Fatalf("две CLI-команды получили один файл: %s", first.Path)
	}
	if filepath.Dir(first.Path) != logsDir {
		t.Errorf("CLI-лог не в logs: %s", first.Path)
	}
	if filepath.Base(first.Path) == serverLogName || filepath.Base(second.Path) == serverLogName {
		t.Error("CLI-лог не должен перезаписывать файл сервера")
	}

	after := readLinks()
	for link, target := range before {
		if after[link] != target {
			t.Errorf("%s изменился: было %q, стало %q", link, target, after[link])
		}
	}
}

func TestOpenRunLogStillRepointsLegacyLink(t *testing.T) {
	dir := t.TempDir()
	run, err := OpenRunLog(dir, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), 4321)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()

	target, err := os.Readlink(filepath.Join(dir, logsDirName, latestLinkName))
	if err != nil {
		t.Fatalf("beacon.latest.log не symlink: %v", err)
	}
	if target != filepath.Base(run.Path) {
		t.Errorf("symlink указывает на %q, ожидалось %q", target, filepath.Base(run.Path))
	}
}
