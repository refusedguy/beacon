package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCanonicalConcurrencyDefaults asserts that the two config construction
// paths agree and that both read the exported constants.
func TestCanonicalConcurrencyDefaults(t *testing.T) {
	var viaApply Config
	ApplyDefaults(&viaApply)
	fromDefaults := defaults()

	if viaApply.Concurrency != fromDefaults.Concurrency {
		t.Fatalf("ApplyDefaults gave %+v, defaults gave %+v", viaApply.Concurrency, fromDefaults.Concurrency)
	}
	if got := fromDefaults.Concurrency.MaxInFlight; got != DefaultMaxInFlight {
		t.Errorf("defaults max_in_flight = %d, want %d", got, DefaultMaxInFlight)
	}
	if got := fromDefaults.Concurrency.MaxActiveAgents; got != DefaultMaxActiveAgents {
		t.Errorf("defaults max_active_agents = %d, want %d", got, DefaultMaxActiveAgents)
	}
}

// TestTimeoutDefaults asserts that both construction paths publish the same
// timeout strings and that the accessors return the canonical durations.
func TestTimeoutDefaults(t *testing.T) {
	var viaApply Config
	ApplyDefaults(&viaApply)
	fromDefaults := defaults()

	for _, c := range []Config{viaApply, fromDefaults} {
		if got := c.OrphanTimeoutDuration(); got != DefaultOrphanTimeout {
			t.Errorf("orphan timeout = %s, want %s", got, DefaultOrphanTimeout)
		}
		if got := c.ModelTimeoutDuration(); got != DefaultModelTimeout {
			t.Errorf("model timeout = %s, want %s", got, DefaultModelTimeout)
		}
	}
	if DefaultOrphanTimeout != 90*time.Second {
		t.Errorf("DefaultOrphanTimeout = %s, want 1m30s", DefaultOrphanTimeout)
	}
	if DefaultModelTimeout != 3*time.Second {
		t.Errorf("DefaultModelTimeout = %s, want 3s", DefaultModelTimeout)
	}
}

// TestConfiguredTimeoutsAreParsed checks that an operator override reaches the
// duration accessors.
func TestConfiguredTimeoutsAreParsed(t *testing.T) {
	c := Config{OrphanTimeout: "5m", ModelTimeout: "750ms"}
	if got := c.OrphanTimeoutDuration(); got != 5*time.Minute {
		t.Errorf("orphan timeout = %s, want 5m", got)
	}
	if got := c.ModelTimeoutDuration(); got != 750*time.Millisecond {
		t.Errorf("model timeout = %s, want 750ms", got)
	}
}

// TestLoadRejectsMalformedTimeouts checks that a bad duration fails at the
// boundary instead of silently falling back.
func TestLoadRejectsMalformedTimeouts(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"orphan", "orphan_timeout: soon\n", "invalid orphan_timeout"},
		{"model", "model_timeout: soon\n", "invalid model_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load accepted a malformed timeout")
			}
			if got := err.Error(); !strings.Contains(got, tc.want) {
				t.Fatalf("Load error = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}
