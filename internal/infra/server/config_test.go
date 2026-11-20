package server

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func wantDefaultConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	return cfg
}

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv(stateDirEnv, filepath.Join(t.TempDir(), "xdg"))
	path := filepath.Join(t.TempDir(), "missing.yaml")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q): %v", path, err)
	}
	want := wantDefaultConfig(t)
	if got != want {
		t.Fatalf("Load(%q) = %+v, want %+v", path, got, want)
	}
}

func TestDefaultConfigUsesXDGStateDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "xdg")
	t.Setenv(stateDirEnv, base)

	got, err := DefaultStateDir()
	if err != nil {
		t.Fatalf("DefaultStateDir: %v", err)
	}
	if want := filepath.Join(base, beaconStateName); got != want {
		t.Fatalf("DefaultStateDir = %q, want %q", got, want)
	}
}

func TestDefaultConfigFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(stateDirEnv, "")
	t.Setenv("HOME", home)

	got, err := DefaultStateDir()
	if err != nil {
		t.Fatalf("DefaultStateDir: %v", err)
	}
	want := filepath.Join(home, ".local", "state", beaconStateName)
	if got != want {
		t.Fatalf("DefaultStateDir = %q, want %q", got, want)
	}
}

func TestLoadExplicitValues(t *testing.T) {
	t.Setenv(stateDirEnv, filepath.Join(t.TempDir(), "xdg"))
	state := filepath.Join(t.TempDir(), "state")
	body := fmt.Sprintf("addr: 127.0.0.1:9090\nstateDir: %s\nreadHeaderTimeout: 2s\nreadTimeout: 7s\nwriteTimeout: 11s\nshutdownTimeout: 1500ms\n", state)
	path := writeConfigFile(t, body)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q): %v", path, err)
	}
	want := Config{
		Addr:              "127.0.0.1:9090",
		StateDir:          state,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       7 * time.Second,
		WriteTimeout:      11 * time.Second,
		ShutdownTimeout:   1500 * time.Millisecond,
	}
	if got != want {
		t.Fatalf("Load(%q) = %+v, want %+v", path, got, want)
	}
}

func TestLoadEmptyFileKeepsDefaults(t *testing.T) {
	t.Setenv(stateDirEnv, filepath.Join(t.TempDir(), "xdg"))
	want := wantDefaultConfig(t)

	for _, body := range []string{"", "\n\n", "  # only a comment\n"} {
		path := writeConfigFile(t, body)
		got, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%q with %q): %v", path, body, err)
		}
		if got != want {
			t.Fatalf("Load(%q with %q) = %+v, want %+v", path, body, got, want)
		}
	}
}

func TestLoadCommentsQuotesAndKeyForms(t *testing.T) {
	t.Setenv(stateDirEnv, filepath.Join(t.TempDir(), "xdg"))
	state := filepath.Join(t.TempDir(), "state")
	body := fmt.Sprintf("# server settings\naddr: \"127.0.0.1:0\" # loopback\nstate-dir: %q\nREAD_TIMEOUT: 9s\n", state)
	path := writeConfigFile(t, body)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q): %v", path, err)
	}
	if got.Addr != DefaultAddr {
		t.Fatalf("Addr = %q, want %q", got.Addr, DefaultAddr)
	}
	if got.StateDir != state {
		t.Fatalf("StateDir = %q, want %q", got.StateDir, state)
	}
	if got.ReadTimeout != 9*time.Second {
		t.Fatalf("ReadTimeout = %s, want 9s", got.ReadTimeout)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Setenv(stateDirEnv, filepath.Join(t.TempDir(), "xdg"))

	cases := []struct {
		name string
		body string
	}{
		{"unknown key", "bogus: 1\n"},
		{"malformed line", "addr 127.0.0.1:0\n"},
		{"empty key", ": 127.0.0.1:0\n"},
		{"unterminated quote", "addr: \"127.0.0.1:0\n"},
		{"bad address", "addr: not-an-addr\n"},
		{"bad port", "addr: 127.0.0.1:abc\n"},
		{"port above range", "addr: 127.0.0.1:70000\n"},
		{"bad duration", "readTimeout: soon\n"},
		{"zero duration", "writeTimeout: 0s\n"},
		{"negative duration", "shutdownTimeout: -1s\n"},
		{"empty state dir", "stateDir: \"\"\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, tc.body)
			if _, err := Load(path); err == nil {
				t.Fatalf("Load(%q) succeeded, want error", tc.body)
			}
		})
	}
}

func TestValidateRejectsZeroDuration(t *testing.T) {
	cfg := Config{
		Addr:              DefaultAddr,
		StateDir:          filepath.Join(t.TempDir(), "state"),
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       time.Second,
		WriteTimeout:      time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate succeeded with zero shutdownTimeout, want error")
	}
}
