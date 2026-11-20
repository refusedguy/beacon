package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRoleDictionary(t *testing.T) {
	for _, r := range []Role{RoleExecutor, RoleOrchestrator, RoleFixer} {
		if !r.Valid() {
			t.Errorf("canonical role %q is not valid", r)
		}
		if got, ok := ParseRole(string(r)); !ok || got != r {
			t.Errorf("ParseRole(%q) = %q, %v; want %q, true", r, got, ok, r)
		}
	}

	for _, raw := range []string{"", "  ", "EXECUTOR", "primary", "watcher"} {
		if Role(raw).Valid() {
			t.Errorf("unknown role %q is valid", raw)
		}
		if got, ok := ParseRole(raw); ok || got != "" {
			t.Errorf("ParseRole(%q) = %q, %v; want empty, false", raw, got, ok)
		}
	}

	if got, ok := ParseRole("  executor\n"); !ok || got != RoleExecutor {
		t.Errorf("ParseRole must trim whitespace, got %q, %v", got, ok)
	}
	if got, ok := ParseRole("fixer"); !ok || got != RoleFixer {
		t.Errorf("ParseRole(fixer) = %q, %v; want fixer, true", got, ok)
	}

	roles := ValidRoles()
	if !reflect.DeepEqual(roles, []Role{RoleExecutor, RoleOrchestrator, RoleFixer}) {
		t.Fatalf("ValidRoles() = %v", roles)
	}
	roles[0] = Role("mutated")
	if ValidRoles()[0] != RoleExecutor {
		t.Fatal("ValidRoles returned a shared slice")
	}
}

func TestRuntimeDictionary(t *testing.T) {
	for _, r := range []Runtime{RuntimeSubagent, RuntimePrimary} {
		if !r.Valid() {
			t.Errorf("canonical runtime %q is not valid", r)
		}
		if got, ok := ParseRuntime(string(r)); !ok || got != r {
			t.Errorf("ParseRuntime(%q) = %q, %v; want %q, true", r, got, ok, r)
		}
	}

	for _, raw := range []string{"", "  ", "SUBAGENT", "executor", "local"} {
		if Runtime(raw).Valid() {
			t.Errorf("unknown runtime %q is valid", raw)
		}
		if got, ok := ParseRuntime(raw); ok || got != "" {
			t.Errorf("ParseRuntime(%q) = %q, %v; want empty, false", raw, got, ok)
		}
	}

	if got, ok := ParseRuntime("\tprimary  "); !ok || got != RuntimePrimary {
		t.Errorf("ParseRuntime must trim whitespace, got %q, %v", got, ok)
	}

	runtimes := ValidRuntimes()
	if !reflect.DeepEqual(runtimes, []Runtime{RuntimeSubagent, RuntimePrimary}) {
		t.Fatalf("ValidRuntimes() = %v", runtimes)
	}
	runtimes[0] = Runtime("mutated")
	if ValidRuntimes()[0] != RuntimeSubagent {
		t.Fatal("ValidRuntimes returned a shared slice")
	}
}

func TestCapabilityDictionary(t *testing.T) {
	canonical := []Capability{
		CapabilityWatch,
		CapabilityReadVerdict,
		CapabilityCIRetry,
		CapabilityRebase,
		CapabilityReview,
		CapabilityMerge,
		CapabilityAck,
		CapabilitySpawn,
	}
	for _, c := range canonical {
		if !c.Valid() {
			t.Errorf("canonical capability %q is not valid", c)
		}
		if got, ok := ParseCapability(string(c)); !ok || got != c {
			t.Errorf("ParseCapability(%q) = %q, %v; want %q, true", c, got, ok, c)
		}
	}

	// The canonical strings are fixed by the dictionary; near misses and
	// arbitrary names are rejected (the T5.20 acceptance check).
	for _, raw := range []string{
		"", "  ", "WATCH", "read_verdict", "ci_retry",
		"teleport", "deploy", "watch,merge",
	} {
		if Capability(raw).Valid() {
			t.Errorf("unknown capability %q is valid", raw)
		}
		if got, ok := ParseCapability(raw); ok || got != "" {
			t.Errorf("ParseCapability(%q) = %q, %v; want empty, false", raw, got, ok)
		}
	}

	if got, ok := ParseCapability("\nread-verdict\t"); !ok || got != CapabilityReadVerdict {
		t.Errorf("ParseCapability must trim whitespace, got %q, %v", got, ok)
	}

	caps := ValidCapabilities()
	if !reflect.DeepEqual(caps, canonical) {
		t.Fatalf("ValidCapabilities() = %v", caps)
	}
	caps[0] = Capability("mutated")
	if ValidCapabilities()[0] != CapabilityWatch {
		t.Fatal("ValidCapabilities returned a shared slice")
	}
}

func TestParseCapabilities(t *testing.T) {
	t.Run("keeps order", func(t *testing.T) {
		got, err := ParseCapabilities([]string{"merge", " watch ", "ci-retry", "merge"})
		if err != nil {
			t.Fatalf("ParseCapabilities: %v", err)
		}
		want := []Capability{CapabilityMerge, CapabilityWatch, CapabilityCIRetry, CapabilityMerge}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseCapabilities() = %v, want %v", got, want)
		}
	})

	t.Run("empty list", func(t *testing.T) {
		got, err := ParseCapabilities(nil)
		if err != nil {
			t.Fatalf("ParseCapabilities(nil): %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("ParseCapabilities(nil) = %v, want empty non-nil slice", got)
		}
	})

	t.Run("rejects unknown and names it", func(t *testing.T) {
		got, err := ParseCapabilities([]string{"watch", "teleport", "merge"})
		if err == nil {
			t.Fatal("unknown capability was accepted")
		}
		if got != nil {
			t.Fatalf("failed parse returned %v, want nil", got)
		}
		var app *AppError
		if !errors.As(err, &app) || app.Code != CodeInvalidInput {
			t.Fatalf("error = %v, want INVALID_INPUT AppError", err)
		}
		if !strings.Contains(app.Message, "teleport") {
			t.Fatalf("error message does not name the unknown capability: %q", app.Message)
		}
	})

	t.Run("rejects empty entry", func(t *testing.T) {
		if _, err := ParseCapabilities([]string{"watch", "   "}); err == nil {
			t.Fatal("empty capability was accepted")
		}
	})
}
