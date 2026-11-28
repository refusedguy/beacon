package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

func TestStatusCommandRegistersExplainFlag(t *testing.T) {
	cmd := newStatusCommand(new(string))
	flag := cmd.Flags().Lookup("explain")
	if flag == nil {
		t.Fatal("status flag --explain is not registered")
	}
	if flag.DefValue != "" {
		t.Fatalf("--explain default = %q, want empty", flag.DefValue)
	}
	if !strings.Contains(cmd.Use, "--explain") {
		t.Fatalf("status Use %q missing --explain", cmd.Use)
	}
}

func explainConfig(t *testing.T) (string, core.RunID) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	dbPath := filepath.Join(dir, "state.db")
	if err := os.WriteFile(cfgPath, []byte("state_db: "+dbPath+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	id := core.RunID("run-cli")
	run := core.NewRun(id, "T-CLI", time.Now().UTC().Add(-time.Hour))
	run.State = core.RunBlocked
	run.Reason = "base incident open"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatalf("save run: %v", err)
	}
	return cfgPath, id
}

func TestStatusExplainPrintsOneRunAnswer(t *testing.T) {
	cfgPath, id := explainConfig(t)
	var out bytes.Buffer
	cmd := newStatusCommand(&cfgPath)
	cmd.SetArgs([]string{"--explain", string(id)})
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --explain: %v", err)
	}
	text := out.String()
	for _, want := range []string{"is blocked", "reason: base incident open", "action: unblock"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q missing %q", text, want)
		}
	}
}

func TestStatusExplainRejectsUnknownRun(t *testing.T) {
	cfgPath, _ := explainConfig(t)
	cmd := newStatusCommand(&cfgPath)
	cmd.SetArgs([]string{"--explain", "run-missing"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "run-missing") {
		t.Fatalf("error = %v, want not-found for run-missing", err)
	}
}
