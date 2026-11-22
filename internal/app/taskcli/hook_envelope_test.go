package taskcli

import (
	"strings"
	"testing"
)

func hookEnvAsMap(t *testing.T, pairs []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			t.Fatalf("env pair without '=': %q", pair)
		}
		if _, dup := out[key]; dup {
			t.Fatalf("duplicate env key: %s", key)
		}
		out[key] = value
	}
	return out
}

func mergeEnvelope() HookEnvelope {
	return HookEnvelope{
		Event:        "merged",
		CardID:       "CD-06",
		Time:         "2026-11-22T12:00:00Z",
		WatchID:      "watch:refusedguy/beacon:1234:abc",
		PRNumber:     "1234",
		Branch:       "fix/cd06",
		WorktreePath: "/mnt/projects/beacon-handoff/beacon-impl/worktrees/cd06",
		TargetBranch: "main",
	}
}

func TestBuildHookEnvMergeEvent(t *testing.T) {
	env := mergeEnvelope()
	got := hookEnvAsMap(t, BuildHookEnv(env))

	want := map[string]string{
		HookEnvEvent:        env.Event,
		HookEnvCardID:       env.CardID,
		HookEnvTime:         env.Time,
		HookEnvWatchID:      env.WatchID,
		HookEnvPRNumber:     env.PRNumber,
		HookEnvBranch:       env.Branch,
		HookEnvWorktreePath: env.WorktreePath,
		HookEnvTargetBranch: env.TargetBranch,
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("env count = %d, want %d: %v", len(got), len(want), got)
	}
	if err := ValidateHookEnvelope(env); err != nil {
		t.Fatalf("merge envelope is incomplete: %v", err)
	}
}

func TestBuildHookEnvCloseEvent(t *testing.T) {
	env := mergeEnvelope()
	env.Event = "closed"
	env.TargetBranch = ""

	pairs := BuildHookEnv(env)
	got := hookEnvAsMap(t, pairs)

	if _, exists := got[HookEnvTargetBranch]; exists {
		t.Errorf("close contract must not contain %s", HookEnvTargetBranch)
	}
	for _, field := range mandatoryHookEnv {
		if got[field.key] == "" {
			t.Errorf("close contract misses mandatory %s", field.key)
		}
	}
	if len(pairs) != MandatoryHookEnvCount {
		t.Errorf("close env count = %d, want %d", len(pairs), MandatoryHookEnvCount)
	}
	if err := ValidateHookEnvelope(env); err != nil {
		t.Fatalf("close envelope is incomplete: %v", err)
	}
}

func TestMandatoryHookEnvCount(t *testing.T) {
	if len(mandatoryHookEnv) != MandatoryHookEnvCount {
		t.Fatalf("mandatory fields = %d, constant = %d", len(mandatoryHookEnv), MandatoryHookEnvCount)
	}

	seen := make(map[string]bool, len(mandatoryHookEnv))
	for _, field := range mandatoryHookEnv {
		if seen[field.key] {
			t.Errorf("duplicate mandatory key: %s", field.key)
		}
		seen[field.key] = true
	}
	for _, key := range []string{HookEnvEvent, HookEnvCardID, HookEnvTime} {
		if !seen[key] {
			t.Errorf("legacy field %s is missing from mandatory set", key)
		}
	}
	if seen[HookEnvTargetBranch] {
		t.Errorf("%s must stay optional", HookEnvTargetBranch)
	}
}

func TestValidateHookEnvelopeMissingFields(t *testing.T) {
	blank := map[string]func(*HookEnvelope){
		"Event":        func(e *HookEnvelope) { e.Event = "" },
		"CardID":       func(e *HookEnvelope) { e.CardID = "" },
		"Time":         func(e *HookEnvelope) { e.Time = "" },
		"WatchID":      func(e *HookEnvelope) { e.WatchID = "" },
		"PRNumber":     func(e *HookEnvelope) { e.PRNumber = "" },
		"Branch":       func(e *HookEnvelope) { e.Branch = "" },
		"WorktreePath": func(e *HookEnvelope) { e.WorktreePath = "" },
	}
	for name, clear := range blank {
		t.Run(name, func(t *testing.T) {
			env := mergeEnvelope()
			clear(&env)
			err := ValidateHookEnvelope(env)
			if err == nil {
				t.Fatalf("expected error for empty %s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name %s", err, name)
			}
		})
	}
}

func TestBuildHookEnvDoesNotCopyParentSecrets(t *testing.T) {
	t.Setenv("BEACON_SECRET_TEST", "parent-secret-value")
	t.Setenv("SECRET_TOKEN", "another-parent-secret")

	pairs := BuildHookEnv(mergeEnvelope())
	got := hookEnvAsMap(t, pairs)

	for _, key := range []string{"BEACON_SECRET_TEST", "SECRET_TOKEN"} {
		if _, exists := got[key]; exists {
			t.Errorf("parent secret %s leaked into contract", key)
		}
	}
	for _, pair := range pairs {
		if strings.Contains(pair, "parent-secret-value") || strings.Contains(pair, "another-parent-secret") {
			t.Errorf("parent secret value leaked into contract: %q", pair)
		}
	}
	for key := range got {
		if !strings.HasPrefix(key, "BEACON_") {
			t.Errorf("unexpected key in contract: %s", key)
		}
	}
}
