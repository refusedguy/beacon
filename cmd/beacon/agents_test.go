package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type w68Call struct {
	dir  string
	name string
	args []string
	env  []string
}

func w68CLI(t *testing.T, respond func(w68Call) runResult) (*CLI, *bytes.Buffer, *bytes.Buffer, *[]w68Call) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	calls := new([]w68Call)
	c := &CLI{
		out:    out,
		errOut: errOut,
		root:   t.TempDir(),
		run: func(dir, name string, args, env []string) runResult {
			call := w68Call{dir, name, append([]string(nil), args...), append([]string(nil), env...)}
			*calls = append(*calls, call)
			if respond == nil {
				return runResult{}
			}
			return respond(call)
		},
	}
	return c, out, errOut, calls
}

// w68Service replaces the registration reader and returns a pointer to the
// path it was asked to read.
func w68Service(t *testing.T, data []byte, err error) *string {
	t.Helper()
	prev := readServiceFile
	asked := new(string)
	readServiceFile = func(path string) ([]byte, error) {
		*asked = path
		return data, err
	}
	t.Cleanup(func() { readServiceFile = prev })
	return asked
}

const w68AgentList = `[
  {"id":"build","name":"Build","mode":"primary"},
  {"id":"reviewer","name":"Reviewer","mode":"subagent"},
  {"id":"general","name":"General","mode":"all"}
]`

const w68Registration = `{"url":"http://127.0.0.1:4096","pid":4242,"version":"2.3.0"}`

func TestW68AgentsTableFiltersAndUsesRunner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	asked := w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, calls := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов раннера %d, ожидался 1", len(*calls))
	}
	call := (*calls)[0]
	if call.name != "opencode" || !reflect.DeepEqual(call.args, []string{"api", "app.agents"}) {
		t.Fatalf("не тот вызов: %+v", call)
	}
	if call.dir != c.root || len(call.env) != 0 {
		t.Fatalf("неверный каталог или окружение: %+v", call)
	}
	if want := filepath.Join("state", "opencode", "service.json"); !strings.HasSuffix(*asked, want) {
		t.Fatalf("читался путь %q, ожидался с суффиксом %q", *asked, want)
	}

	for _, want := range []string{"ID", "NAME", "MODE", "reviewer", "Reviewer", "subagent", "general", "all"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("в таблице нет %q: %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "primary") || strings.Contains(out.String(), "Build") {
		t.Fatalf("агент primary попал в таблицу: %q", out.String())
	}
}

func TestW68AgentsJSON(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("stdout не JSON: %q", out.String())
	}
	var got []agentRow
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []agentRow{
		{ID: "reviewer", Name: "Reviewer", Mode: "subagent"},
		{ID: "general", Name: "General", Mode: "all"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("строки %+v, ожидались %+v", got, want)
	}
}

func TestW68AgentsGroupedResponse(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	grouped := `{
	  "primary": [{"id":"build","name":"Build"}],
	  "subagent": [{"id":"reviewer","name":"Reviewer"}],
	  "all": [{"id":"general","name":"General"}]
	}`
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(grouped)}
	})

	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var got []agentRow
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []agentRow{
		{ID: "general", Name: "General", Mode: "all"},
		{ID: "reviewer", Name: "Reviewer", Mode: "subagent"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("строки %+v, ожидались %+v", got, want)
	}
}

func TestW68AgentsServiceDown(t *testing.T) {
	w68Service(t, nil, os.ErrNotExist)
	c, out, errOut, calls := w68CLI(t, nil)

	if code := c.runAgents(nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "фоновый сервис не запущен") ||
		!strings.Contains(errOut.String(), "opencode service start") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("раннер не должен вызываться: %+v", *calls)
	}
}

func TestW68AgentsBrokenRegistration(t *testing.T) {
	w68Service(t, []byte("{не json"), nil)
	c, _, errOut, calls := w68CLI(t, nil)

	if code := c.runAgents(nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), "фоновый сервис не запущен") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("раннер не должен вызываться: %+v", *calls)
	}
}

func TestW68AgentsRunnerFailure(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{code: 2, stderr: []byte("connect ECONNREFUSED\n")}
	})

	if code := c.runAgents(nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "ECONNREFUSED") {
		t.Fatalf("нет причины отказа: %q", errOut.String())
	}
}

func TestW68AgentsMalformedAPIResponse(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	c, _, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte("not json")}
	})

	if code := c.runAgents(nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), "не удалось разобрать") {
		t.Fatalf("нет ошибки разбора: %q", errOut.String())
	}
}

func TestW68AgentsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"неизвестный флаг", []string{"--bogus"}},
		{"неизвестный формат", []string{"--format", "yaml"}},
		{"лишний аргумент", []string{"extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w68Service(t, []byte(w68Registration), nil)
			c, out, errOut, calls := w68CLI(t, nil)

			if code := c.runAgents(tc.args); code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout не пустой: %q", out.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("stderr пустой")
			}
			if len(*calls) != 0 {
				t.Fatalf("раннер не должен вызываться: %+v", *calls)
			}
		})
	}
}

func TestW68AgentsEmptyListIsValidJSON(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(`[{"id":"build","name":"Build","mode":"primary"}]`)}
	})

	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("пустой список: %q, ожидался []", got)
	}
}

func TestW92SandboxCloneArgsUsesReference(t *testing.T) {
	got := sandboxCloneArgs("/srv/beacon.git", "/work/agents/a1")
	want := []string{"clone", "--reference", "/srv/beacon.git", "/srv/beacon.git", "/work/agents/a1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("аргументы клона %q, ожидались %q", got, want)
	}
}

func TestW92ProvisionSandboxRunsGitWithReference(t *testing.T) {
	c, out, errOut, calls := w68CLI(t, func(w68Call) runResult {
		return runResult{}
	})

	if code := c.provisionSandbox("/srv/beacon.git", "/work/agents/a1"); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов раннера %d, ожидался 1", len(*calls))
	}
	call := (*calls)[0]
	if call.name != "git" {
		t.Fatalf("раннер вызвал %q, ожидался git", call.name)
	}
	want := []string{"clone", "--reference", "/srv/beacon.git", "/srv/beacon.git", "/work/agents/a1"}
	if !reflect.DeepEqual(call.args, want) {
		t.Fatalf("аргументы %q, ожидались %q", call.args, want)
	}
	if call.dir != c.root || len(call.env) != 0 {
		t.Fatalf("неверный каталог или окружение: %+v", call)
	}
}

func TestW92ProvisionSandboxCloneFailure(t *testing.T) {
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{code: 128, stderr: []byte("fatal: destination path already exists\n")}
	})

	if code := c.provisionSandbox("/srv/beacon.git", "/work/agents/a1"); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "destination path already exists") ||
		!strings.Contains(errOut.String(), "--reference") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
}

func TestW92ProvisionSandboxRejectsEmptyPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		dest   string
	}{
		{"пустой источник", "", "/work/agents/a1"},
		{"пробельный источник", "  ", "/work/agents/a1"},
		{"пустая песочница", "/srv/beacon.git", ""},
		{"пробельная песочница", "/srv/beacon.git", "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, out, errOut, calls := w68CLI(t, nil)

			if code := c.provisionSandbox(tc.source, tc.dest); code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout не пустой: %q", out.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("stderr пустой")
			}
			if len(*calls) != 0 {
				t.Fatalf("раннер не должен вызываться: %+v", *calls)
			}
		})
	}
}
