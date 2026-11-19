package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
