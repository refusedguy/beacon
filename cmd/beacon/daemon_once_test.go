package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

func TestDaemonOnceExitCode(t *testing.T) {
	cases := []struct {
		name   string
		before uint64
		after  uint64
		want   int
	}{
		{name: "clean pass", before: 3, after: 3, want: 0},
		{name: "quiet pass", before: 0, after: 0, want: 0},
		{name: "failed pass", before: 3, after: 4, want: 1},
		{name: "two failures", before: 1, after: 3, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := daemonOnceExitCode(tc.before, tc.after); got != tc.want {
				t.Fatalf("daemonOnceExitCode(%d, %d) = %d, want %d", tc.before, tc.after, got, tc.want)
			}
		})
	}
}

func TestDaemonActorErrorCountReadsSink(t *testing.T) {
	previous := observe.Default()
	observe.SetDefault(observe.NewRecorder(nil, observe.NewMetrics()))
	t.Cleanup(func() { observe.SetDefault(previous) })

	if got := daemonActorErrorCount(); got != 0 {
		t.Fatalf("fresh counter = %d, want 0", got)
	}
	observe.ActorError("test", "", errors.New("boom"))
	if got := daemonActorErrorCount(); got != 1 {
		t.Fatalf("counter after one error = %d, want 1", got)
	}
}

func TestDaemonIntervalDescriptionAppearsInUsageAndReadme(t *testing.T) {
	if !strings.Contains(usageText, daemonIntervalDescription) {
		t.Fatalf("usage text misses %q", daemonIntervalDescription)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !strings.Contains(string(raw), daemonIntervalDescription) {
		t.Fatalf("README misses %q", daemonIntervalDescription)
	}
}
