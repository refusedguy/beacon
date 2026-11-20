package registry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validConfig() map[string]any {
	return map[string]any{
		"schema":  "beacon.agent-registry/v1",
		"version": "1",
		"project": "beacon",
		"roster": []any{
			map[string]any{
				"id":           "builder",
				"runtime":      "claude",
				"roles":        []any{"executor"},
				"capabilities": []any{"go"},
				"path":         "agents/builder.md",
			},
		},
		"configurations": []any{
			map[string]any{"id": "build", "members": []any{"builder"}},
		},
		"limits": map[string]any{"max_watch": 10},
	}
}

func withMutation(t *testing.T, mutate func(map[string]any)) map[string]any {
	t.Helper()
	raw, err := json.Marshal(validConfig())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	mutate(config)
	return config
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	if err := Validate(validConfig()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateAcceptsMappingRoster(t *testing.T) {
	config := map[string]any{
		"roster": map[string]any{
			"builder": map[string]any{
				"runtime": "claude",
				"roles":   []string{"executor"},
				"path":    "agents/builder.md",
			},
		},
	}
	if err := Validate(config); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsByField(t *testing.T) {
	rosterAgent := func(c map[string]any) map[string]any {
		return c["roster"].([]any)[0].(map[string]any)
	}
	cases := []struct {
		name      string
		mutate    func(map[string]any)
		wantField string
	}{
		{"unknown top-level key", func(c map[string]any) { c["exta"] = true }, "exta"},
		{"unknown roster key", func(c map[string]any) { rosterAgent(c)["rolez"] = true }, "roster[0].rolez"},
		{"unknown configuration key", func(c map[string]any) {
			c["configurations"].([]any)[0].(map[string]any)["memberz"] = true
		}, "configurations[0].memberz"},
		{"absolute path", func(c map[string]any) { rosterAgent(c)["path"] = "/etc/passwd" }, "roster[0].path"},
		{"windows absolute path", func(c map[string]any) { rosterAgent(c)["path"] = `C:\agents\builder.md` }, "roster[0].path"},
		{"parent traversal", func(c map[string]any) { rosterAgent(c)["path"] = "agents/../../secret.md" }, "roster[0].path"},
		{"empty id", func(c map[string]any) { rosterAgent(c)["id"] = "  " }, "roster[0].id"},
		{"missing id", func(c map[string]any) { delete(rosterAgent(c), "id") }, "roster[0].id"},
		{"empty id key in mapping", func(c map[string]any) {
			c["roster"] = map[string]any{"": map[string]any{"roles": []any{"executor"}}}
		}, "roster.id"},
		{"empty roles", func(c map[string]any) { rosterAgent(c)["roles"] = []any{} }, "roster[0].roles"},
		{"missing roles", func(c map[string]any) { delete(rosterAgent(c), "roles") }, "roster[0].roles"},
		{"blank role", func(c map[string]any) { rosterAgent(c)["roles"] = []any{"executor", " "} }, "roster[0].roles[1]"},
		{"roles not a list", func(c map[string]any) { rosterAgent(c)["roles"] = "executor" }, "roster[0].roles"},
		{"empty configuration id", func(c map[string]any) {
			c["configurations"].([]any)[0].(map[string]any)["id"] = ""
		}, "configurations[0].id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(withMutation(t, tc.mutate))
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Validate error = %v, want *ValidationError", err)
			}
			if validationErr.Field != tc.wantField {
				t.Fatalf("field = %q, want %q", validationErr.Field, tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("error %q does not name field %q", err, tc.wantField)
			}
		})
	}
}
