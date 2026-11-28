package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// pinBuildHash sets the embedded hash for one test. The hash is a package
// global, so these tests do not run in parallel.
func pinBuildHash(t *testing.T, hash string) {
	t.Helper()
	previous := sourceBuildHash
	sourceBuildHash = hash
	t.Cleanup(func() { sourceBuildHash = previous })
}

func staleBinaryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("BEACON_PROJECT_ROOT", root)
	return root
}

func executeQuietly(t *testing.T, cmd *cobra.Command) error {
	t.Helper()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return cmd.Execute()
}

func assertStaleBinaryError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("stale binary started")
	}
	msg := err.Error()
	if !strings.Contains(msg, "binary built from") {
		t.Fatalf("error does not name the stale binary: %s", msg)
	}
	if !strings.Contains(msg, rebuildCommand) {
		t.Fatalf("error does not name the rebuild command: %s", msg)
	}
}

func assertNoStartupArtifacts(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".beacon")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale binary created state before the freshness guard: %v", err)
	}
}

func TestServeRefusesStaleBinary(t *testing.T) {
	root := staleBinaryRoot(t)
	pinBuildHash(t, "stale-serve-hash")
	configPath := ""
	err := executeQuietly(t, newServeCommand(&configPath))
	assertStaleBinaryError(t, err)
	assertNoStartupArtifacts(t, root)
}

func TestMCPRefusesStaleBinary(t *testing.T) {
	root := staleBinaryRoot(t)
	pinBuildHash(t, "stale-mcp-hash")
	configPath := ""
	err := executeQuietly(t, newMCPCommand(&configPath))
	assertStaleBinaryError(t, err)
	assertNoStartupArtifacts(t, root)
}

func TestMatchingHashLeavesStartupUnchanged(t *testing.T) {
	root := staleBinaryRoot(t)
	current, err := doctorBuildHash(root)
	if err != nil {
		t.Fatalf("doctorBuildHash: %v", err)
	}
	pinBuildHash(t, current)
	if err := refuseStaleBinary(); err != nil {
		t.Fatalf("fresh binary refused to start: %v", err)
	}

	// Make config loading fail before wiring. Reaching that error proves the
	// commands passed the freshness guard and kept their normal startup path.
	badConfig := filepath.Join(root, ".beacon", "config.yaml")
	if err := os.MkdirAll(badConfig, 0o755); err != nil {
		t.Fatalf("MkdirAll(config.yaml): %v", err)
	}
	for _, tc := range []struct {
		name string
		new  func(*string) *cobra.Command
	}{
		{"serve", newServeCommand},
		{"mcp", newMCPCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := ""
			err := executeQuietly(t, tc.new(&configPath))
			if err == nil {
				t.Fatal("command returned no error")
			}
			msg := err.Error()
			if strings.Contains(msg, rebuildCommand) || strings.Contains(msg, "binary built from") {
				t.Fatalf("fresh binary refused to start: %s", msg)
			}
			if !strings.Contains(msg, "config.yaml") {
				t.Fatalf("command did not reach config loading: %s", msg)
			}
		})
	}
}
