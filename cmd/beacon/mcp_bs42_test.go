package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// TestRemoteModeUsesConnectionFileWithoutStore is the BS.42 acceptance seam:
// remote mode builds the thin client from connection.json and opens no state
// store in the MCP process. The transport is replaced so the test needs no
// listener; the endpoint is read from a real file.
func TestRemoteModeUsesConnectionFileWithoutStore(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(mcpRemoteEnv, "1")
	t.Setenv("BEACON_INIT_EVENT", "")
	t.Setenv("BEACON_INIT_BYPASS", "")
	state := filepath.Join(base, "beacon")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	want := mcpConnection{Host: "127.0.0.1", Port: 4321, Token: "tok"}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, mcpConnectionFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	previousServe := serveMCPRemote
	previousActive := activeMCPConnection
	t.Cleanup(func() {
		serveMCPRemote = previousServe
		activeMCPConnection = previousActive
	})
	called := false
	serveMCPRemote = func(_ context.Context, client *mcp.RemoteClient) error {
		called = true
		got := client.Connection()
		if got.Host != want.Host || got.Port != want.Port || got.Token != want.Token {
			t.Fatalf("client endpoint = %+v, want %+v", got, want)
		}
		return nil
	}

	errOut := new(bytes.Buffer)
	if code := mcpRunner(errOut); code != 0 {
		t.Fatalf("remote run exit %d, stderr %q", code, errOut.String())
	}
	if !called {
		t.Fatal("remote transport was not used")
	}
	if _, err := os.Stat(filepath.Join(state, mcpStateDBName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote mode opened a local store: %v", err)
	}
}

func TestRemoteModeRequiresConnectionFile(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(mcpRemoteEnv, "1")
	t.Setenv("BEACON_INIT_EVENT", "")
	t.Setenv("BEACON_INIT_BYPASS", "")

	errOut := new(bytes.Buffer)
	if code := mcpRunner(errOut); code == 0 {
		t.Fatal("remote mode without connection.json should fail")
	}
	if !strings.Contains(errOut.String(), "connection.json") {
		t.Fatalf("stderr lacks the configuration reason: %q", errOut.String())
	}
}

func TestRemoteToolsEnabledParsing(t *testing.T) {
	for _, raw := range []string{"1", "true", "YES", " on "} {
		t.Setenv(mcpRemoteEnv, raw)
		if !remoteToolsEnabled() {
			t.Fatalf("%q should enable remote mode", raw)
		}
	}
	for _, raw := range []string{"", "0", "false", "maybe"} {
		t.Setenv(mcpRemoteEnv, raw)
		if remoteToolsEnabled() {
			t.Fatalf("%q should keep local mode", raw)
		}
	}
}
