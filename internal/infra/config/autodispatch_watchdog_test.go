package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// TestAutoDispatchDefaults checks both construction paths. A fresh config
// keeps today's behavior: no automatic dispatch and an uncapped batch.
func TestAutoDispatchDefaults(t *testing.T) {
	fromMissingFile, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	fromEmptyFile, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load(empty): %v", err)
	}
	var viaApply Config
	ApplyDefaults(&viaApply)

	for name, c := range map[string]Config{
		"missing file": fromMissingFile,
		"empty file":   fromEmptyFile,
		"apply":        viaApply,
	} {
		t.Run(name, func(t *testing.T) {
			if c.AutoDispatch.Enabled {
				t.Error("auto_dispatch.enabled = true, want false")
			}
			if c.AutoDispatch.BatchSize != DefaultAutoDispatchBatchSize {
				t.Errorf("auto_dispatch.batch_size = %d, want %d", c.AutoDispatch.BatchSize, DefaultAutoDispatchBatchSize)
			}
			if !c.AutoDispatch.DispatchOpen {
				t.Error("auto_dispatch.dispatch_open = false, want true")
			}
		})
	}
}

func TestAutoDispatchExplicitFalseSurvivesLoad(t *testing.T) {
	c, err := Load(writeConfig(t, "auto_dispatch:\n  enabled: true\n  batch_size: 3\n  dispatch_open: false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.AutoDispatch.Enabled {
		t.Error("auto_dispatch.enabled = false, want true")
	}
	if c.AutoDispatch.BatchSize != 3 {
		t.Errorf("auto_dispatch.batch_size = %d, want 3", c.AutoDispatch.BatchSize)
	}
	if c.AutoDispatch.DispatchOpen {
		t.Error("auto_dispatch.dispatch_open = true, want the explicit false")
	}
}

func TestAutoDispatchBatchSizeFor(t *testing.T) {
	cases := []struct {
		name      string
		batchSize int
		freeSlots int
		want      int
	}{
		{"zero fills every free slot", 0, 4, 4},
		{"positive caps a large batch", 2, 4, 2},
		{"positive never exceeds free slots", 3, 1, 1},
		{"no free slots", 2, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := AutoDispatch{BatchSize: tc.batchSize}
			if got := a.BatchSizeFor(tc.freeSlots); got != tc.want {
				t.Fatalf("BatchSizeFor(%d) = %d, want %d", tc.freeSlots, got, tc.want)
			}
		})
	}
}

// TestWatchdogDisabledByDefault checks that a fresh checkout has no age alerts
// and that the configured ages still have a safe fallback.
func TestWatchdogDisabledByDefault(t *testing.T) {
	fromMissingFile, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	var viaApply Config
	ApplyDefaults(&viaApply)

	for name, c := range map[string]Config{"missing file": fromMissingFile, "apply": viaApply} {
		t.Run(name, func(t *testing.T) {
			if c.Watchdog.Enabled {
				t.Error("watchdog.enabled = true, want false")
			}
			for _, state := range watchedStates() {
				got, err := c.Watchdog.AgeFor(state)
				if err != nil {
					t.Fatalf("AgeFor(%s): %v", state, err)
				}
				if got != DefaultWatchdogAge {
					t.Errorf("AgeFor(%s) = %s, want %s", state, got, DefaultWatchdogAge)
				}
			}
		})
	}
}

func TestWatchdogAgesParse(t *testing.T) {
	c, err := Load(writeConfig(t, `watchdog:
  enabled: true
  waiting_ci: 10m
  needs_rebase: 20m
  needs_agent_fix: 30s
  blocked: 1h
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Watchdog.Enabled {
		t.Fatal("watchdog.enabled = false, want true")
	}
	for _, tc := range []struct {
		state core.RunState
		want  time.Duration
	}{
		{core.RunWaitingCI, 10 * time.Minute},
		{core.RunNeedsRebase, 20 * time.Minute},
		{core.RunNeedsAgentFix, 30 * time.Second},
		{core.RunBlocked, time.Hour},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			got, err := c.Watchdog.AgeFor(tc.state)
			if err != nil {
				t.Fatalf("AgeFor(%s): %v", tc.state, err)
			}
			if got != tc.want {
				t.Fatalf("AgeFor(%s) = %s, want %s", tc.state, got, tc.want)
			}
		})
	}
}

func TestLoadRejectsMalformedWatchdogAge(t *testing.T) {
	for _, state := range watchedStates() {
		t.Run(string(state), func(t *testing.T) {
			body := "watchdog:\n  " + string(state) + ": soon\n"
			_, err := Load(writeConfig(t, body))
			if err == nil {
				t.Fatal("Load accepted a malformed watchdog age")
			}
			want := "watchdog." + string(state)
			if got := err.Error(); !strings.Contains(got, want) {
				t.Fatalf("Load error = %q, want it to contain %q", got, want)
			}
		})
	}
}

func watchedStates() []core.RunState {
	return []core.RunState{core.RunWaitingCI, core.RunNeedsRebase, core.RunNeedsAgentFix, core.RunBlocked}
}
