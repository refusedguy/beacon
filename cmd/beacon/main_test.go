package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type t06Call struct {
	dir  string
	name string
	args []string
	env  []string
}

func t06NewCLI(t *testing.T, respond func(t06Call) runResult) (*CLI, *bytes.Buffer, *bytes.Buffer, *[]t06Call) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	calls := new([]t06Call)
	c := &CLI{
		out:    out,
		errOut: errOut,
		root:   t.TempDir(),
		run: func(dir, name string, args, env []string) runResult {
			call := t06Call{dir, name, append([]string(nil), args...), append([]string(nil), env...)}
			*calls = append(*calls, call)
			if respond == nil {
				return runResult{}
			}
			return respond(call)
		},
	}
	return c, out, errOut, calls
}

func t06NoChild(t *testing.T) func(t06Call) runResult {
	t.Helper()
	return func(call t06Call) runResult {
		t.Fatalf("не ожидался дочерний процесс: %+v", call)
		return runResult{}
	}
}

func t06GatesPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "beacon-gates")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func t06StateOK(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "beacon"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", base)
	return filepath.Join(base, "beacon")
}

func TestT06ListJSONCatalog(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"list", "--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0", code)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("stdout не является JSON: %q", out.String())
	}

	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"agents", "attach", "build", "doctor", "gates", "list", "mcp", "task", "watch", "what"}
	if len(entries) != len(wantNames) {
		t.Fatalf("в каталоге %d команд, ожидалось %d", len(entries), len(wantNames))
	}
	for i, entry := range entries {
		if len(entry) != 3 {
			t.Fatalf("команда %d: поля %v, ожидались name, summary, usage", i, entry)
		}
		for _, key := range []string{"name", "summary", "usage"} {
			if _, ok := entry[key]; !ok {
				t.Fatalf("команда %d: нет поля %s", i, key)
			}
		}
		var name string
		if err := json.Unmarshal(entry["name"], &name); err != nil {
			t.Fatal(err)
		}
		if name != wantNames[i] {
			t.Fatalf("команда %d: имя %q, ожидалось %q", i, name, wantNames[i])
		}
	}

	var parsed []command
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]command, len(parsed))
	for _, cmd := range parsed {
		byName[cmd.Name] = cmd
	}
	for _, name := range wantNames {
		want, ok := byName[name]
		if !ok {
			t.Fatalf("в каталоге нет команды %s", name)
		}
		whatOut, whatErr := new(bytes.Buffer), new(bytes.Buffer)
		if code := runWhat([]string{name}, whatOut, whatErr); code != 0 {
			t.Fatalf("what %s: код %d, stderr %q", name, code, whatErr.String())
		}
		if !strings.Contains(whatOut.String(), want.Summary) || !strings.Contains(whatOut.String(), want.Usage) {
			t.Fatalf("what %s расходится с каталогом: %q", name, whatOut.String())
		}
	}
}

func TestT06ListTableAndWhatAgree(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"list"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0", code)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
	for _, name := range []string{"agents", "attach", "build", "doctor", "gates", "list", "mcp", "task", "watch", "what"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("таблица не содержит %s: %q", name, out.String())
		}
	}

	for _, cmd := range catalog {
		whatOut, whatErr := new(bytes.Buffer), new(bytes.Buffer)
		if code := runWhat([]string{cmd.Name}, whatOut, whatErr); code != 0 {
			t.Fatalf("what %s: код %d", cmd.Name, code)
		}
		if !strings.Contains(whatOut.String(), cmd.Summary) || !strings.Contains(whatOut.String(), cmd.Usage) {
			t.Fatalf("what %s расходится с каталогом: %q", cmd.Name, whatOut.String())
		}
	}
}

func TestT06BadInputWritesUsageToStderr(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"нет аргументов", nil, "Использование:"},
		{"неизвестная подкоманда", []string{"nope"}, "неизвестная подкоманда"},
		{"неизвестный флаг list", []string{"list", "--bogus"}, "flag provided but not defined"},
		{"неизвестный формат", []string{"list", "--format", "yaml"}, "неизвестный формат"},
		{"лишний аргумент", []string{"what", "build", "extra"}, "нужна одна команда"},
		{"неизвестная команда what", []string{"what", "nope"}, "неизвестная команда"},
		{"неизвестный флаг build", []string{"build", "--bogus"}, "flag provided but not defined"},
		{"неизвестный флаг doctor", []string{"doctor", "--bogus"}, "flag provided but not defined"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))
			if code := c.Run(tc.args); code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout не пустой: %q", out.String())
			}
			if !strings.Contains(errOut.String(), tc.wantErr) {
				t.Fatalf("stderr %q не содержит %q", errOut.String(), tc.wantErr)
			}
			if !strings.Contains(errOut.String(), "Использование:") {
				t.Fatalf("в stderr нет usage: %q", errOut.String())
			}
		})
	}
}

func TestT06DoctorChecks(t *testing.T) {
	t.Run("go 1.25 не проходит требование 1.26", func(t *testing.T) {
		t.Setenv("BEACON_GATES", t06GatesPath(t))
		wantState := t06StateOK(t)
		c, out, errOut, _ := t06NewCLI(t, func(call t06Call) runResult {
			return runResult{stdout: []byte("go version go1.25.7 linux/amd64\n")}
		})

		if code := c.Run([]string{"doctor"}); code != 1 {
			t.Fatalf("код выхода %d, ожидался 1", code)
		}
		if !strings.Contains(out.String(), "go: FAIL") || !strings.Contains(out.String(), "1.26") {
			t.Fatalf("нет FAIL для Go 1.25: %q", out.String())
		}
		if !strings.Contains(out.String(), "beacon-gates: OK") || !strings.Contains(out.String(), "state: OK "+wantState) {
			t.Fatalf("остальные проверки должны быть OK: %q", out.String())
		}
		if errOut.Len() != 0 {
			t.Fatalf("stderr не пустой: %q", errOut.String())
		}
	})

	t.Run("go 1.27 проходит", func(t *testing.T) {
		t.Setenv("BEACON_GATES", t06GatesPath(t))
		t06StateOK(t)
		c, out, errOut, _ := t06NewCLI(t, func(call t06Call) runResult {
			return runResult{stdout: []byte("go version go1.27.4 linux/amd64\n")}
		})

		if code := c.Run([]string{"doctor"}); code != 0 {
			t.Fatalf("код выхода %d, ожидался 0", code)
		}
		if strings.Contains(out.String(), "FAIL") {
			t.Fatalf("есть FAIL: %q", out.String())
		}
		for _, name := range []string{"go: OK", "beacon-gates: OK", "state: OK"} {
			if !strings.Contains(out.String(), name) {
				t.Fatalf("нет строки %s: %q", name, out.String())
			}
		}
		if errOut.Len() != 0 {
			t.Fatalf("stderr не пустой: %q", errOut.String())
		}
	})

	t.Run("go version не запустился", func(t *testing.T) {
		t.Setenv("BEACON_GATES", t06GatesPath(t))
		t06StateOK(t)
		c, out, _, _ := t06NewCLI(t, func(call t06Call) runResult {
			return runResult{code: 2, stderr: []byte("permission denied\n")}
		})

		if code := c.Run([]string{"doctor"}); code != 1 {
			t.Fatalf("код выхода %d, ожидался 1", code)
		}
		if !strings.Contains(out.String(), "go: FAIL") || !strings.Contains(out.String(), "permission denied") {
			t.Fatalf("нет причины отказа: %q", out.String())
		}
	})

	t.Run("beacon-gates не найден", func(t *testing.T) {
		t.Setenv("BEACON_GATES", "")
		t.Setenv("PATH", t.TempDir())
		t06StateOK(t)
		c, out, _, _ := t06NewCLI(t, func(call t06Call) runResult {
			return runResult{stdout: []byte("go version go1.27.4 linux/amd64\n")}
		})

		if code := c.Run([]string{"doctor"}); code != 1 {
			t.Fatalf("код выхода %d, ожидался 1", code)
		}
		if !strings.Contains(out.String(), "beacon-gates: FAIL") {
			t.Fatalf("нет FAIL для beacon-gates: %q", out.String())
		}
	})
}

func TestT06BuildReproducibleAndPassesCode(t *testing.T) {
	res := runResult{stdout: []byte("build out\n"), stderr: []byte("build err\n"), code: 7}
	c, out, errOut, calls := t06NewCLI(t, func(call t06Call) runResult { return res })

	if code := c.Run([]string{"build"}); code != 7 {
		t.Fatalf("код выхода %d, ожидался 7", code)
	}
	if out.String() != "build out\n" || errOut.String() != "build err\n" {
		t.Fatalf("потоки не переданы: stdout %q stderr %q", out.String(), errOut.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов %d, ожидался 1", len(*calls))
	}
	call := (*calls)[0]
	if call.name != "go" || call.dir != c.root {
		t.Fatalf("не тот процесс: %+v", call)
	}
	wantArgs := []string{
		"build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid= -s -w",
		"-o", filepath.Join(c.root, "bin", "beacon"), "./cmd/beacon",
	}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("аргументы %q, ожидались %q", call.args, wantArgs)
	}
	if !reflect.DeepEqual(call.env, []string{"CGO_ENABLED=0"}) {
		t.Fatalf("окружение %q, ожидалось CGO_ENABLED=0", call.env)
	}
	if info, err := os.Stat(filepath.Join(c.root, "bin")); err != nil || !info.IsDir() {
		t.Fatalf("каталог bin не создан: %v", err)
	}
}

func TestT06TestPassesGoCall(t *testing.T) {
	res := runResult{stdout: []byte("ok\n"), stderr: []byte("warning\n"), code: 5}
	c, out, errOut, calls := t06NewCLI(t, func(call t06Call) runResult { return res })

	if code := c.Run([]string{"test"}); code != 5 {
		t.Fatalf("код выхода %d, ожидался 5", code)
	}
	if out.String() != "ok\n" || errOut.String() != "warning\n" {
		t.Fatalf("потоки не переданы: stdout %q stderr %q", out.String(), errOut.String())
	}
	call := (*calls)[0]
	if call.name != "go" || !reflect.DeepEqual(call.args, []string{"test", "./..."}) {
		t.Fatalf("не тот вызов: %+v", call)
	}
}

func TestT06GatesPassesStreamsAndCodes(t *testing.T) {
	path := t06GatesPath(t)
	t.Setenv("BEACON_GATES", path)

	for _, code := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("код %d", code), func(t *testing.T) {
			res := runResult{
				stdout: []byte(fmt.Sprintf("gate stdout %d\n", code)),
				stderr: []byte(fmt.Sprintf("gate stderr %d\n", code)),
				code:   code,
			}
			c, out, errOut, calls := t06NewCLI(t, func(call t06Call) runResult { return res })

			args := []string{"capabilities", "--self-test"}
			if got := c.Run(append([]string{"gates"}, args...)); got != code {
				t.Fatalf("код выхода %d, ожидался %d", got, code)
			}
			if out.String() != string(res.stdout) || errOut.String() != string(res.stderr) {
				t.Fatalf("потоки изменены: stdout %q stderr %q", out.String(), errOut.String())
			}
			call := (*calls)[0]
			if call.name != path || !reflect.DeepEqual(call.args, args) {
				t.Fatalf("не тот вызов: %+v", call)
			}
		})
	}
}

func TestT06GatesMissingBinary(t *testing.T) {
	t.Setenv("BEACON_GATES", "")
	t.Setenv("PATH", t.TempDir())
	c, out, errOut, calls := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"gates", "all"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "beacon-gates не найден") {
		t.Fatalf("нет причины в stderr: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("дочерний процесс не должен запускаться: %+v", *calls)
	}
}

func TestT06TaskDelegates(t *testing.T) {
	previous := taskRunner
	taskRunner = func(args []string, out, errOut io.Writer) int {
		fmt.Fprintf(out, "task:%v\n", args)
		return 3
	}
	t.Cleanup(func() { taskRunner = previous })

	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))
	if code := c.Run([]string{"task", "list", "--format", "json"}); code != 3 {
		t.Fatalf("код выхода %d, ожидался 3", code)
	}
	if out.String() != "task:[list --format json]\n" || errOut.Len() != 0 {
		t.Fatalf("аргументы не переданы: stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestT17DispatchCallsRunners(t *testing.T) {
	t.Run("agents", func(t *testing.T) {
		previous := readServiceFile
		t.Cleanup(func() { readServiceFile = previous })
		readServiceFile = func(string) ([]byte, error) {
			return []byte(`{"url":"http://127.0.0.1:4096","pid":1,"version":"1"}`), nil
		}
		c, _, errOut, calls := t06NewCLI(t, func(t06Call) runResult {
			return runResult{stdout: []byte(`[{"id":"reviewer","name":"Reviewer","mode":"subagent"}]`)}
		})

		if code := c.Run([]string{"agents"}); code != 0 {
			t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
		}
		if len(*calls) != 1 {
			t.Fatalf("вызовов %d, ожидался 1: %+v", len(*calls), *calls)
		}
		call := (*calls)[0]
		if call.name != "opencode" || !reflect.DeepEqual(call.args, []string{"api", "app.agents"}) {
			t.Fatalf("не тот вызов: %+v", call)
		}
	})

	t.Run("watch", func(t *testing.T) {
		previous := watchRunner
		t.Cleanup(func() { watchRunner = previous })
		var gotCLI *CLI
		var gotArgs []string
		RegisterWatch(func(c *CLI, args []string) int {
			gotCLI, gotArgs = c, append([]string(nil), args...)
			return 4
		})
		c, _, _, _ := t06NewCLI(t, t06NoChild(t))

		if code := c.Run([]string{"watch", "--format", "json"}); code != 4 {
			t.Fatalf("код выхода %d, ожидался 4", code)
		}
		if gotCLI != c {
			t.Fatal("раннер watch получил другой CLI")
		}
		if !reflect.DeepEqual(gotArgs, []string{"--format", "json"}) {
			t.Fatalf("аргументы %q, ожидались [--format json]", gotArgs)
		}
	})

	t.Run("attach", func(t *testing.T) {
		base := t.TempDir()
		dir := filepath.Join(base, "opencode")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		registration := `{"url":"http://127.0.0.1:4096","pid":1,"version":"1"}`
		if err := os.WriteFile(filepath.Join(dir, "service.json"), []byte(registration), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_STATE_HOME", base)
		c, _, errOut, calls := t06NewCLI(t, func(t06Call) runResult {
			return runResult{stdout: []byte("tui\n")}
		})

		if code := c.Run([]string{"attach", "sess-abc"}); code != 0 {
			t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
		}
		if len(*calls) != 1 {
			t.Fatalf("вызовов %d, ожидался 1: %+v", len(*calls), *calls)
		}
		call := (*calls)[0]
		wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", "sess-abc"}
		if call.name != "opencode" || !reflect.DeepEqual(call.args, wantArgs) {
			t.Fatalf("не тот вызов: %+v", call)
		}
	})
}
