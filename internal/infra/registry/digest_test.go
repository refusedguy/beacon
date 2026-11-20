package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const digestBaseJSON = `{
  "schema": "beacon.agent-registry",
  "version": 1,
  "project": "beacon-impl-1a2b3c4d",
  "roster": [
    {"id": "executor", "role": "executor", "runtime": "subagent", "capabilities": ["watch"]},
    {"id": "orchestrator", "role": "orchestrator", "runtime": "primary", "capabilities": ["merge"]}
  ],
  "configurations": {"presets": {"default": ["executor", "orchestrator"]}, "recommended": "default"},
  "limits": {"max_parallel_polls": 15}
}`

func decodeDigestConfig(t *testing.T, raw string) map[string]any {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return config
}

func cloneDigestConfig(t *testing.T, config map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return decodeDigestConfig(t, string(raw))
}

func TestDigestIgnoresFieldOrder(t *testing.T) {
	const reorderedJSON = `{
  "limits": {"max_parallel_polls": 15},
  "configurations": {"recommended": "default", "presets": {"default": ["executor", "orchestrator"]}},
  "roster": [
    {"runtime": "subagent", "capabilities": ["watch"], "id": "executor", "role": "executor"},
    {"capabilities": ["merge"], "role": "orchestrator", "id": "orchestrator", "runtime": "primary"}
  ],
  "project": "beacon-impl-1a2b3c4d",
  "version": 1,
  "schema": "beacon.agent-registry"
}`
	base := Digest(decodeDigestConfig(t, digestBaseJSON))
	got := Digest(decodeDigestConfig(t, reorderedJSON))
	if got != base {
		t.Fatalf("digest changed with field order: got %s, want %s", got, base)
	}
}

func TestDigestIgnoresRosterOrder(t *testing.T) {
	config := decodeDigestConfig(t, digestBaseJSON)
	base := Digest(config)

	reordered := cloneDigestConfig(t, config)
	roster := reordered["roster"].([]any)
	reordered["roster"] = []any{roster[1], roster[0]}
	if got := Digest(reordered); got != base {
		t.Fatalf("digest changed with roster order: got %s, want %s", got, base)
	}
}

func TestDigestChangesWithRoster(t *testing.T) {
	config := decodeDigestConfig(t, digestBaseJSON)
	base := Digest(config)

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "runtime changed",
			mutate: func(c map[string]any) {
				c["roster"].([]any)[0].(map[string]any)["runtime"] = "primary"
			},
		},
		{
			name: "capability added",
			mutate: func(c map[string]any) {
				entry := c["roster"].([]any)[0].(map[string]any)
				entry["capabilities"] = []any{"watch", "ci-retry"}
			},
		},
		{
			name: "entry added",
			mutate: func(c map[string]any) {
				c["roster"] = append(c["roster"].([]any), map[string]any{
					"id": "reviewer", "role": "executor", "runtime": "subagent",
				})
			},
		},
		{
			name: "entry removed",
			mutate: func(c map[string]any) {
				c["roster"] = c["roster"].([]any)[:1]
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := cloneDigestConfig(t, config)
			tc.mutate(changed)
			if got := Digest(changed); got == base {
				t.Fatalf("digest did not change after %s: %s", tc.name, got)
			}
		})
	}
}

func TestDigestChangesWithOtherFields(t *testing.T) {
	config := decodeDigestConfig(t, digestBaseJSON)
	base := Digest(config)

	changed := cloneDigestConfig(t, config)
	changed["project"] = "other-1a2b3c4d"
	if got := Digest(changed); got == base {
		t.Fatalf("digest did not change with project: %s", got)
	}
}

func TestDigestIsHexSHA256OfCanonicalJSON(t *testing.T) {
	config := decodeDigestConfig(t, digestBaseJSON)
	canonical := CanonicalJSON(config)

	if strings.Contains(string(canonical), "\n") {
		t.Fatalf("canonical JSON is not compact: %q", canonical)
	}
	sum := sha256.Sum256(canonical)
	want := hex.EncodeToString(sum[:])
	if got := Digest(config); got != want {
		t.Fatalf("Digest = %s, want SHA-256 of canonical JSON %s", got, want)
	}
	if len(want) != 64 {
		t.Fatalf("digest length = %d, want 64", len(want))
	}
}
