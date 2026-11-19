package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type t313Call struct {
	dir  string
	name string
	args []string
	env  []string
}

// t313CLI builds a CLI with a fake runner that records calls, so no real
// opencode process is spawned during the test.
func t313CLI(t *testing.T, respond func(t313Call) runResult) (*CLI, *bytes.Buffer, *bytes.Buffer, *[]t313Call) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	calls := new([]t313Call)
	c := &CLI{
		out:    out,
		errOut: errOut,
		root:   t.TempDir(),
		run: func(dir, name string, args, env []string) runResult {
			call := t313Call{dir, name, append([]string(nil), args...), append([]string(nil), env...)}
			*calls = append(*calls, call)
			if respond == nil {
				return runResult{}
			}
			return respond(call)
		},
	}
	return c, out, errOut, calls
}

// t313Service writes a registration file with the given url into a fresh
// XDG_STATE_HOME and returns that base directory.
func t313Service(t *testing.T, url string) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "opencode")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"url":"` + url + `","pid":123,"version":"1.0.0","password":"secret"}`
	if err := os.WriteFile(filepath.Join(dir, "service.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", base)
	return base
}

func TestT313AttachFormsCommand(t *testing.T) {
	url := "http://127.0.0.1:4096"
	t313Service(t, url)
	res := runResult{stdout: []byte("tui\n"), code: 0}
	c, out, errOut, calls := t313CLI(t, func(t313Call) runResult { return res })

	if code := c.runAttach([]string{"sess-abc"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0, stderr %q", code, errOut.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов %d, ожидался 1: %+v", len(*calls), *calls)
	}
	call := (*calls)[0]
	if call.name != "opencode" {
		t.Fatalf("процесс %q, ожидался opencode: %+v", call.name, call)
	}
	wantArgs := []string{"--server", url, "--session", "sess-abc"}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("аргументы %q, ожидались %q", call.args, wantArgs)
	}
	if call.dir != c.root {
		t.Fatalf("dir %q, ожидался %q", call.dir, c.root)
	}
	if out.String() != "tui\n" {
		t.Fatalf("stdout не передан: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
}

func TestT313AttachServiceNotRunning(t *testing.T) {
	// Свежий XDG_STATE_HOME без registration-файла.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, out, errOut, calls := t313CLI(t, func(call t313Call) runResult {
		t.Fatalf("не ожидался дочерний процесс: %+v", call)
		return runResult{}
	})

	if code := c.runAttach([]string{"sess-abc"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	msg := errOut.String()
	if !strings.Contains(msg, "сервис opencode не запущен") {
		t.Fatalf("нет понятной ошибки: %q", msg)
	}
	if !strings.Contains(msg, "opencode service start") {
		t.Fatalf("нет команды запуска сервиса: %q", msg)
	}
	if len(*calls) != 0 {
		t.Fatalf("дочерний процесс не должен запускаться: %+v", *calls)
	}
}

func TestT313AttachBadServiceFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "opencode")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "service.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", base)

	c, _, errOut, calls := t313CLI(t, func(call t313Call) runResult {
		t.Fatalf("не ожидался дочерний процесс: %+v", call)
		return runResult{}
	})

	if code := c.runAttach([]string{"sess-abc"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), "сервис opencode не запущен") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("дочерний процесс не должен запускаться: %+v", *calls)
	}
}

func TestT313AttachRequiresSessionID(t *testing.T) {
	c, out, errOut, calls := t313CLI(t, func(call t313Call) runResult {
		t.Fatalf("не ожидался дочерний процесс: %+v", call)
		return runResult{}
	})

	for _, args := range [][]string{nil, {}, {"", "extra"}, {"a", "b"}} {
		if code := c.runAttach(args); code != 2 {
			t.Fatalf("аргументы %q: код выхода %d, ожидался 2", args, code)
		}
		if !strings.Contains(errOut.String(), "beacon attach <sessionID>") {
			t.Fatalf("нет подсказки по использованию: %q", errOut.String())
		}
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("дочерний процесс не должен запускаться: %+v", *calls)
	}
}

// ---- ADR 0043 п6 / T3.95: возобновление по сохранённому session_id ----

// t395WriteSessions writes a session registry into the XDG_STATE_HOME created
// by t313Service. DeadlineAt is filled by the caller so a test can model a
// task that was killed after its hard limit.
func t395WriteSessions(t *testing.T, st attachSessionState) string {
	t.Helper()
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		t.Fatal("XDG_STATE_HOME не задан")
	}
	dir := filepath.Join(base, "beacon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, attachSessionsFile)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestT395AttachSavesSessionID(t *testing.T) {
	base := t313Service(t, "http://127.0.0.1:4096")
	t.Setenv("BEACON_TASK_ID", "T9.9")
	t.Setenv("BEACON_TASK_DEADLINE", "")
	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })

	if code := c.runAttach([]string{"sess-saved"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0, stderr %q", code, errOut.String())
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", "sess-saved"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("вызовы %+v, ожидался один с аргументами %q", *calls, wantArgs)
	}

	data, err := os.ReadFile(filepath.Join(base, "beacon", attachSessionsFile))
	if err != nil {
		t.Fatalf("реестр сессий не записан: %v", err)
	}
	var st attachSessionState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("неверный реестр сессий: %v", err)
	}
	if st.Last != "T9.9" || len(st.Sessions) != 1 {
		t.Fatalf("реестр %+v, ожидалась одна сессия задачи T9.9", st)
	}
	if rec := st.Sessions[0]; rec.TaskID != "T9.9" || rec.SessionID != "sess-saved" || rec.SavedAt.IsZero() {
		t.Fatalf("запись %+v, ожидалась sess-saved для T9.9 с временем сохранения", rec)
	}
}

func TestT395ResumeOpensSavedSession(t *testing.T) {
	t313Service(t, "http://127.0.0.1:4096")
	past := time.Now().UTC().Add(-time.Hour)
	path := t395WriteSessions(t, attachSessionState{
		Sessions: []attachSession{{
			TaskID: "T9.9", SessionID: "sess-after-timeout", DeadlineAt: past,
		}},
		Last: "T9.9",
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0, stderr %q", code, errOut.String())
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", "sess-after-timeout"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("вызовы %+v, ожидался возврат в сохранённую сессию %q", *calls, wantArgs)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("resume изменил реестр: до %q, после %q", before, after)
	}
}

func TestT395ResumeByTaskID(t *testing.T) {
	t313Service(t, "http://127.0.0.1:4096")
	t395WriteSessions(t, attachSessionState{
		Sessions: []attachSession{
			{TaskID: "T9.8", SessionID: "sess-old"},
			{TaskID: "T9.9", SessionID: "sess-wanted"},
		},
		Last: "T9.8",
	})

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume", "T9.9"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0, stderr %q", code, errOut.String())
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", "sess-wanted"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("вызовы %+v, ожидалась сессия задачи T9.9 %q", *calls, wantArgs)
	}
}

func TestT395ResumeWithoutSavedSession(t *testing.T) {
	t313Service(t, "http://127.0.0.1:4096")
	c, out, errOut, calls := t313CLI(t, func(call t313Call) runResult {
		t.Fatalf("не ожидался дочерний процесс: %+v", call)
		return runResult{}
	})

	if code := c.runAttach([]string{"--resume"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "сохранённой сессии нет") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("дочерний процесс не должен запускаться: %+v", *calls)
	}
}

func TestT395ResumeUnknownTask(t *testing.T) {
	t313Service(t, "http://127.0.0.1:4096")
	t395WriteSessions(t, attachSessionState{
		Sessions: []attachSession{{TaskID: "T9.8", SessionID: "sess-old"}},
		Last:     "T9.8",
	})

	c, _, errOut, calls := t313CLI(t, func(call t313Call) runResult {
		t.Fatalf("не ожидался дочерний процесс: %+v", call)
		return runResult{}
	})
	if code := c.runAttach([]string{"--resume", "T9.9"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), "для задачи T9.9 нет сохранённой сессии") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("дочерний процесс не должен запускаться: %+v", *calls)
	}
}

func TestT395ResumeCorruptRegistry(t *testing.T) {
	t313Service(t, "http://127.0.0.1:4096")
	path := t395WriteSessions(t, attachSessionState{})
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, _, errOut, calls := t313CLI(t, func(call t313Call) runResult {
		t.Fatalf("не ожидался дочерний процесс: %+v", call)
		return runResult{}
	})
	if code := c.runAttach([]string{"--resume"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), "не удалось прочитать сохранённую сессию") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("дочерний процесс не должен запускаться: %+v", *calls)
	}
}

func TestT395AttachReplacesSessionForSameTask(t *testing.T) {
	t313Service(t, "http://127.0.0.1:4096")
	t.Setenv("BEACON_TASK_ID", "T9.9")
	t.Setenv("BEACON_TASK_DEADLINE", "")
	c, _, errOut, _ := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })

	for _, id := range []string{"sess-first", "sess-second"} {
		if code := c.runAttach([]string{id}); code != 0 {
			t.Fatalf("attach %s: код %d, stderr %q", id, code, errOut.String())
		}
	}

	st, err := loadAttachSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Sessions) != 1 || st.Sessions[0].SessionID != "sess-second" {
		t.Fatalf("реестр %+v, ожидалась замена сессии задачи T9.9", st)
	}
	saved, err := savedAttachSession("T9.9")
	if err != nil || saved != "sess-second" {
		t.Fatalf("savedAttachSession = %q, %v; ожидалась sess-second", saved, err)
	}
}
