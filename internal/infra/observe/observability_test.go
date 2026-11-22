package observe

import (
	"os"
	"strings"
	"testing"
)

// TestObserveTestsHaveNoFixedSleeps guards observability tests against fixed
// delays. A wait ends on an observed condition or the runner deadline, not on
// an elapsed sleep (CCx-18). The pattern is assembled so the guard does not
// flag itself, and it is checked against a known delay before any source scan.
func TestObserveTestsHaveNoFixedSleeps(t *testing.T) {
	call := strings.Join([]string{"time", "Sleep("}, ".")
	empty := strings.Join([]string{"times", "Sleep("}, ".")
	sample := "x := " + call + " time.Millisecond)"
	if !strings.Contains(sample, call) || strings.Contains(sample, empty) {
		t.Fatal("guard pattern is empty or does not match a fixed delay")
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read test dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, call) {
				t.Errorf("%s:%d: fixed delay in observability test: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
