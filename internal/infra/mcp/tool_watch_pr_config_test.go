package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func writeWatchConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWatchDefaultsLayersGlobalThenProject(t *testing.T) {
	global := filepath.Join(t.TempDir(), "config.yaml")
	writeWatchConfig(t, global, "maxRebases: 5\nmaxWakesPerWatch: 9\n")
	t.Setenv(watchConfigPathEnv, global)

	project := t.TempDir()
	writeWatchConfig(t, filepath.Join(project, watchConfigDirName, watchConfigFileName),
		"maxRebases: 7\n")

	got := loadWatchDefaults(project)
	if got.MaxRebases != 7 || got.MaxWakesPerWatch != 9 {
		t.Fatalf("defaults = %+v, want maxRebases 7 and maxWakesPerWatch 9", got)
	}
}

func TestLoadWatchDefaultsIgnoresBrokenFiles(t *testing.T) {
	global := filepath.Join(t.TempDir(), "config.yaml")
	writeWatchConfig(t, global, "maxRebases: many\n")
	t.Setenv(watchConfigPathEnv, global)

	got := loadWatchDefaults(t.TempDir())
	if got.MaxRebases != watchDefaultMaxRebases || got.MaxWakesPerWatch != domain.DefaultMaxWakesPerWatch {
		t.Fatalf("defaults = %+v, want built-in values", got)
	}
}

func TestLoadWatchDefaultsReadsProjectFlaky(t *testing.T) {
	t.Setenv(watchConfigPathEnv, filepath.Join(t.TempDir(), "missing.yaml"))
	project := t.TempDir()
	writeWatchConfig(t, filepath.Join(project, watchConfigDirName, watchFlakyFileName),
		`{"checks":["ci/flaky","docs/spell"],"maxRetries":2}`)

	got := loadWatchDefaults(project)
	if got.Flaky == nil || got.Flaky.MaxRetries != 2 || len(got.Flaky.Names) != 2 {
		t.Fatalf("flaky = %+v, want two names and maxRetries 2", got.Flaky)
	}
}

func TestWatchPRRegistrationUsesProjectDefaults(t *testing.T) {
	store := newMemStore()
	clock := &stepClock{t: time.Now()}
	h := NewWatchPRHandler(store, nil, clock)
	h.defaultsLoader = func(string) WatchDefaults {
		return WatchDefaults{
			MaxRebases:       8,
			MaxWakesPerWatch: 11,
			Flaky:            &WatchPRFlaky{Names: []string{"ci/flaky"}, MaxRetries: 2},
		}
	}
	in := WatchPRInput{
		Repo: "acme/widgets", PR: 7, HeadSHA: "sha7", Role: domain.RoleExecutor,
	}
	out, err := h.Register(context.Background(), sessionReq("ses_defaults"), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	watch, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Policy.MaxRebases != 8 || watch.Policy.MaxWakesPerWatch != 11 {
		t.Fatalf("policy = %+v, want 8/11", watch.Policy)
	}
	if len(watch.Flaky.Names) != 1 || watch.Flaky.Names[0] != "ci/flaky" || watch.Flaky.MaxRetries != 2 {
		t.Fatalf("flaky = %+v, want project set", watch.Flaky)
	}
}

func TestWatchPRClientValuesWinOverDefaults(t *testing.T) {
	store := newMemStore()
	clock := &stepClock{t: time.Now()}
	h := NewWatchPRHandler(store, nil, clock)
	h.defaultsLoader = func(string) WatchDefaults {
		return WatchDefaults{
			MaxRebases:       8,
			MaxWakesPerWatch: 11,
			Flaky:            &WatchPRFlaky{Names: []string{"ci/flaky"}, MaxRetries: 2},
		}
	}
	in := WatchPRInput{
		Repo: "acme/widgets", PR: 7, HeadSHA: "sha7", Role: domain.RoleExecutor,
		Flaky:  &WatchPRFlaky{Names: []string{"ci/client"}, MaxRetries: 4},
		Policy: &WatchPRPolicy{MaxRebases: 2, MaxWakesPerWatch: 3},
	}
	out, err := h.Register(context.Background(), sessionReq("ses_override"), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	watch, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Policy.MaxRebases != 2 || watch.Policy.MaxWakesPerWatch != 3 {
		t.Fatalf("policy = %+v, want 2/3", watch.Policy)
	}
	if len(watch.Flaky.Names) != 1 || watch.Flaky.Names[0] != "ci/client" || watch.Flaky.MaxRetries != 4 {
		t.Fatalf("flaky = %+v, want client set", watch.Flaky)
	}
}
