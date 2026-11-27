package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/pkg/core"
)

// TestInitFlagDefaultsMatchConfigConstants checks that the visible init
// defaults are the same values the config loader uses.
func TestInitFlagDefaultsMatchConfigConstants(t *testing.T) {
	cmd := newInitCommand()
	for _, tc := range []struct {
		name string
		want int
	}{
		{"max-in-flight", config.DefaultMaxInFlight},
		{"max-active-agents", config.DefaultMaxActiveAgents},
	} {
		f := cmd.Flags().Lookup(tc.name)
		if f == nil {
			t.Fatalf("init flag %q is missing", tc.name)
		}
		if want := strconv.Itoa(tc.want); f.DefValue != want {
			t.Errorf("init flag %q default = %s, want %s", tc.name, f.DefValue, want)
		}
	}
}

// TestWireServicesCopiesOrphanTimeout checks that a configured orphan timeout
// reaches the reconciler instead of leaving it on the built-in fallback.
func TestWireServicesCopiesOrphanTimeout(t *testing.T) {
	cfg := config.Config{BaseBranch: "main", OrphanTimeout: "5m"}
	infra := &wiredInfra{changes: make(chan int, 1)}
	w := wireServices(cfg, t.TempDir(), "", infra, bareHarness{}, mergePolicy(cfg), nil)
	if got := w.recon.OrphanTimeout; got != 5*time.Minute {
		t.Fatalf("reconciler orphan timeout = %s, want 5m", got)
	}
}

// TestResolveModelUsesConfiguredProvider checks that a config or environment
// model carries the resolved harness provider rather than a hard-coded one.
func TestResolveModelUsesConfiguredProvider(t *testing.T) {
	t.Setenv("BEACON_MODEL", "env-model")
	cfg := config.Config{Harness: config.Harness{Provider: "claude", Model: "config-model"}}
	want := core.ModelRef{ProviderID: "claude", ID: "env-model"}
	if got := resolveModel(cfg, "", ""); got != want {
		t.Fatalf("resolveModel() = %+v, want %+v", got, want)
	}

	t.Setenv("BEACON_MODEL", "")
	want = core.ModelRef{ProviderID: "claude", ID: "config-model"}
	if got := resolveModel(cfg, "", ""); got != want {
		t.Fatalf("resolveModel() = %+v, want %+v", got, want)
	}
}

// TestResolveModelHonoursConfiguredTimeout checks that the parent-session
// lookup stops at model_timeout instead of the hard-coded three seconds.
func TestResolveModelHonoursConfiguredTimeout(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":{"model":{"id":"slow-model","providerID":"opencode"}}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		Harness: config.Harness{
			Provider: "opencode", Endpoint: srv.URL,
		},
		ModelTimeout: "10ms",
	}
	if got := resolveModel(cfg, "token", "ses_parent"); !got.IsZero() {
		t.Fatalf("resolveModel() = %+v, want zero value after timeout", got)
	}
}
