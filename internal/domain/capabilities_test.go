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

func TestScopeDictionary(t *testing.T) {
	canonical := []Scope{ScopeWake, ScopeRead, ScopeStop}
	for _, sc := range canonical {
		if !sc.Valid() {
			t.Errorf("canonical scope %q is not valid", sc)
		}
		if got, ok := ParseScope(string(sc)); !ok || got != sc {
			t.Errorf("ParseScope(%q) = %q, %v; want %q, true", sc, got, ok, sc)
		}
	}

	for _, raw := range []string{"", "  ", "WAKE", "executor", "stop,wake", "delete"} {
		if Scope(raw).Valid() {
			t.Errorf("unknown scope %q is valid", raw)
		}
		if got, ok := ParseScope(raw); ok || got != "" {
			t.Errorf("ParseScope(%q) = %q, %v; want empty, false", raw, got, ok)
		}
	}

	if got, ok := ParseScope("  read\n"); !ok || got != ScopeRead {
		t.Errorf("ParseScope must trim whitespace, got %q, %v", got, ok)
	}

	scopes := ValidScopes()
	if !reflect.DeepEqual(scopes, canonical) {
		t.Fatalf("ValidScopes() = %v", scopes)
	}
	scopes[0] = Scope("mutated")
	if ValidScopes()[0] != ScopeWake {
		t.Fatal("ValidScopes returned a shared slice")
	}
}

func TestScopeMaskCeiling(t *testing.T) {
	ceiling := ScopeWake.Flag() | ScopeRead.Flag()
	for _, sc := range []Scope{ScopeWake, ScopeRead} {
		if !ceiling.Allows(sc) {
			t.Errorf("ceiling %08b must allow %q", ceiling, sc)
		}
	}
	if ceiling.Allows(ScopeStop) {
		t.Fatal("ceiling without stop granted stop")
	}
	if ceiling.Allows(Scope("delete")) {
		t.Fatal("unknown scope was granted")
	}

	var none ScopeMask
	if none.Allows(ScopeWake) || none.Allows(ScopeRead) || none.Allows(ScopeStop) {
		t.Fatal("zero ceiling granted a right")
	}
	if !none.Valid() {
		t.Fatal("zero ceiling must be valid")
	}
	if (ScopeMask(0xff)).Valid() {
		t.Fatal("ceiling with unknown bits reported valid")
	}

	all := ScopeFlagWake | ScopeFlagRead | ScopeFlagStop
	for _, sc := range ValidScopes() {
		if !all.Allows(sc) {
			t.Errorf("full ceiling must allow %q", sc)
		}
	}
}

func TestParseScopes(t *testing.T) {
	t.Run("set semantics and order", func(t *testing.T) {
		got, err := ParseScopes([]string{"stop", " wake ", "read", "wake"})
		if err != nil {
			t.Fatalf("ParseScopes: %v", err)
		}
		want := ScopeWake.Flag() | ScopeRead.Flag() | ScopeStop.Flag()
		if got != want {
			t.Fatalf("ParseScopes() = %08b, want %08b", got, want)
		}
	})

	t.Run("empty list grants nothing", func(t *testing.T) {
		got, err := ParseScopes(nil)
		if err != nil {
			t.Fatalf("ParseScopes(nil): %v", err)
		}
		if got != 0 {
			t.Fatalf("ParseScopes(nil) = %08b, want zero", got)
		}
	})

	t.Run("rejects unknown and names it", func(t *testing.T) {
		got, err := ParseScopes([]string{"wake", "teleport"})
		if err == nil {
			t.Fatal("unknown scope was accepted")
		}
		if got != 0 {
			t.Fatalf("failed parse returned %08b, want zero", got)
		}
		var app *AppError
		if !errors.As(err, &app) || app.Code != CodeInvalidInput {
			t.Fatalf("error = %v, want INVALID_INPUT AppError", err)
		}
		if !strings.Contains(app.Message, "teleport") {
			t.Fatalf("error message does not name the unknown scope: %q", app.Message)
		}
	})
}

// TestScopeTokensDoNotAlterRoles guards the constraint that adding scope rights
// does not widen the existing role dictionary.
func TestScopeTokensDoNotAlterRoles(t *testing.T) {
	want := []Role{RoleExecutor, RoleOrchestrator, RoleFixer}
	if got := ValidRoles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ValidRoles() = %v, want %v", got, want)
	}
	for _, r := range want {
		if got, ok := ParseRole(string(r)); !ok || got != r {
			t.Errorf("ParseRole(%q) = %q, %v; want %q, true", r, got, ok, r)
		}
	}
}

func TestParseAddress(t *testing.T) {
	seat, err := ParseAddress("worker-1@rig-a")
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	if seat.Member != "worker-1" || seat.Rig != "rig-a" {
		t.Fatalf("ParseAddress = %+v, want {worker-1 rig-a}", seat)
	}
	if seat.String() != "worker-1@rig-a" {
		t.Fatalf("Seat.String() = %q", seat.String())
	}
	if got, err := ParseAddress("  worker-1@rig-a  "); err != nil || got != seat {
		t.Fatalf("ParseAddress must trim whitespace, got %+v, %v", got, err)
	}
}

func TestParseAddressRejectsRoleWideBroadcast(t *testing.T) {
	// A role name, a wildcard or a bare role is a broadcast-style address. None
	// names one seat, so none may become a wake target.
	for _, raw := range []string{
		"executor@rig-a",   // role name in the member slot
		"orchestrator@rig", // role name in the member slot
		"*@rig-a",          // every member of a rig
		"worker-1@*",       // one member across every rig
		"executor",         // bare role, no seat
		"rig-a",            // bare rig
		"",                 // empty
		"worker-1",         // missing rig
		"@rig-a",           // empty member
		"worker-1@",        // empty rig
		"a@b@c",            // extra separator
	} {
		if _, err := ParseAddress(raw); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("ParseAddress(%q) error = %v, want ErrInvalidAddress", raw, err)
		}
	}
}

func TestResolveAddressToSession(t *testing.T) {
	resolve := func(s Seat) (string, bool) {
		if s.Member == "worker-1" && s.Rig == "rig-a" {
			return "ses_occupant123", true
		}
		return "", false
	}

	session, err := ResolveAddress("worker-1@rig-a", resolve)
	if err != nil {
		t.Fatalf("ResolveAddress: %v", err)
	}
	if session != "ses_occupant123" {
		t.Fatalf("ResolveAddress = %q, want ses_occupant123", session)
	}

	// An unoccupied seat, a missing resolver and a resolver that returns a role
	// name instead of a session are all refused: no broad target is produced.
	if _, err := ResolveAddress("worker-2@rig-a", resolve); !errors.Is(err, ErrUnresolvedAddress) {
		t.Errorf("unresolved seat error = %v, want ErrUnresolvedAddress", err)
	}
	if _, err := ResolveAddress("worker-1@rig-a", nil); !errors.Is(err, ErrUnresolvedAddress) {
		t.Errorf("nil resolver error = %v, want ErrUnresolvedAddress", err)
	}
	roleResolve := func(Seat) (string, bool) { return "executor", true }
	if _, err := ResolveAddress("worker-1@rig-a", roleResolve); !errors.Is(err, ErrUnresolvedAddress) {
		t.Errorf("role-name target error = %v, want ErrUnresolvedAddress", err)
	}
	if _, err := ResolveAddress("executor@rig-a", resolve); !errors.Is(err, ErrInvalidAddress) {
		t.Errorf("role-wide address error = %v, want ErrInvalidAddress", err)
	}
}
