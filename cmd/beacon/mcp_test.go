package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

func TestMCPWithoutDaemonDoesNotOwnLock(t *testing.T) {
	dir := t.TempDir()
	w := &wiring{cfg: config.Config{StateDB: filepath.Join(dir, "state.db")}}

	_, err := mcpServicesForDaemon(w)
	if !errors.Is(err, errNoLiveDaemon) {
		t.Fatalf("error = %v, want errNoLiveDaemon", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "beacon.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("MCP created a lock file with no daemon: %v", statErr)
	}
	got := readDaemonStatus(dir)
	if got.Error == nil || got.Error.Kind != "daemon" {
		t.Fatalf("error record = %+v, want kind daemon", got.Error)
	}
	if !strings.Contains(got.Error.Message, errNoLiveDaemon.Error()) {
		t.Fatalf("error record message = %q, want %q", got.Error.Message, errNoLiveDaemon)
	}
}

func TestMCPWithLiveDaemonUsesRemoteControl(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := writeConnection(dir, "http://"+ln.Addr().String()); err != nil {
		t.Fatal(err)
	}
	w := &wiring{cfg: config.Config{StateDB: filepath.Join(dir, "state.db")}}

	services, err := mcpServicesForDaemon(w)
	if err != nil {
		t.Fatal(err)
	}
	if services.Remote == nil {
		t.Fatal("MCP did not wire the live daemon's control caller")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "beacon.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("MCP took the lock while a daemon is live: %v", statErr)
	}
}
