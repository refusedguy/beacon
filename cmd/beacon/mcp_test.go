package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// TestW43MCPDispatchesAndRuns proves that `beacon mcp` reaches the MCP runner,
// that the runner is called exactly once and that its exit code and diagnostics
// propagate through the CLI. The runner is replaced so the test does not start
// a real stdio server.
func TestW43MCPDispatchesAndRuns(t *testing.T) {
	previous := mcpRunner
	if previous == nil {
		t.Fatal("mcpRunner не установлен: настоящая реализация отсутствует")
	}

	calls := 0
	mcpRunner = func(errOut io.Writer) int {
		calls++
		fmt.Fprint(errOut, "mcp served\n")
		return 7
	}
	t.Cleanup(func() { mcpRunner = previous })

	c, out, errOut, childCalls := t06NewCLI(t, t06NoChild(t))
	if code := c.Run([]string{"mcp"}); code != 7 {
		t.Fatalf("код выхода %d, ожидался 7", code)
	}
	if calls != 1 {
		t.Fatalf("раннер вызван %d раз, ожидался 1", calls)
	}
	if len(*childCalls) != 0 {
		t.Fatalf("beacon mcp не должен запускать дочерний процесс: %+v", *childCalls)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if errOut.String() != "mcp served\n" {
		t.Fatalf("stderr не передан в раннер: %q", errOut.String())
	}
}

// TestW43MCPInCatalog checks that mcp is discoverable through list and what in
// the same way as task and doctor.
func TestW43MCPInCatalog(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))
	if code := c.Run([]string{"list", "--format", "json"}); code != 0 {
		t.Fatalf("list: код %d, stderr %q", code, errOut.String())
	}
	var entries []command
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "mcp" {
			found = true
			if entry.Usage != "beacon mcp" || entry.Summary == "" {
				t.Fatalf("запись mcp неполная: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("в каталоге нет mcp: %q", out.String())
	}

	whatOut, whatErr := new(bytes.Buffer), new(bytes.Buffer)
	if code := runWhat([]string{"mcp"}, whatOut, whatErr); code != 0 {
		t.Fatalf("what mcp: код %d, stderr %q", code, whatErr.String())
	}
	if !strings.Contains(whatOut.String(), "beacon mcp") {
		t.Fatalf("what mcp не знает команду: %q", whatOut.String())
	}
}

// TestMCPCompositionRootBuildsServerWithStore proves that `beacon mcp` opens a
// real SQLite store and passes it, together with the other production ports,
// into the server. The transport is replaced so the test does not block on
// stdin; the store itself is real and is exercised through a save/read pair.
func TestMCPCompositionRootBuildsServerWithStore(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	previous := serveMCP
	t.Cleanup(func() { serveMCP = previous })

	called := false
	serveMCP = func(_ context.Context, deps mcp.Dependencies) error {
		called = true
		if deps.Store == nil {
			t.Fatal("composition root передал nil Store в сервер")
		}
		if _, ok := deps.Store.(*store.DB); !ok {
			t.Fatalf("Store имеет тип %T, ожидался *store.DB", deps.Store)
		}
		if deps.Stamper == nil || deps.Writer == nil || deps.Events == nil || deps.Snapshot == nil {
			t.Fatalf("не все SQL-порты подключены: %+v", deps)
		}

		w := domain.Watch{
			ID:        "composition-root-smoke",
			Repo:      "refusedguy/beacon",
			PR:        42,
			HeadSHA:   "composition-head",
			Status:    domain.StatusWatching,
			Verdict:   domain.VerdictWatching,
			CreatedAt: time.Now(),
		}
		if err := deps.Store.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch через настоящий store: %v", err)
		}
		got, err := deps.Store.Watch(w.ID)
		if err != nil {
			t.Fatalf("Watch через настоящий store: %v", err)
		}
		if got.HeadSHA != w.HeadSHA || got.Repo != w.Repo {
			t.Fatalf("watch не сохранился: %+v", got)
		}
		return nil
	}

	errOut := new(bytes.Buffer)
	if code := mcpRunner(errOut); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if !called {
		t.Fatal("сервер не был запущен с собранными зависимостями")
	}
	if _, err := os.Stat(filepath.Join(base, "beacon", mcpStateDBName)); err != nil {
		t.Fatalf("файл состояния не создан: %v", err)
	}
}
