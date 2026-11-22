package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// registryCheckValidConfig is a config that passes every local check.
func registryCheckValidConfig() map[string]any {
	return map[string]any{
		"schema":  "beacon.agent-registry/v2",
		"version": "2",
		"project": "demo",
		"roster": []any{
			map[string]any{
				"id":           "exec-1",
				"roles":        []any{"executor"},
				"capabilities": []any{"go", "review"},
				"path":         "agents/exec-1",
			},
			map[string]any{
				"id":           "exec-2",
				"roles":        []any{"fixer"},
				"capabilities": []any{"review"},
			},
		},
		"limits": map[string]any{"max_retries": 3},
	}
}

func TestCheckRegistryConfigClean(t *testing.T) {
	res := checkRegistryConfig(registryCheckValidConfig())
	if !res.OK() {
		t.Fatalf("valid config has issues: %+v", res.Issues)
	}
	if res.Digest == "" {
		t.Fatal("clean result has no digest")
	}
}

func TestCheckRegistryConfigNamesFieldAndReason(t *testing.T) {
	config := registryCheckValidConfig()
	config["bogus_key"] = "typo"

	res := checkRegistryConfig(config)
	if res.OK() {
		t.Fatal("unknown key passed the check")
	}
	found := false
	for _, issue := range res.Issues {
		if issue.Field == "bogus_key" && strings.TrimSpace(issue.Reason) != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no field-named issue for bogus_key: %+v", res.Issues)
	}
}

func TestCheckRegistryConfigLimits(t *testing.T) {
	config := registryCheckValidConfig()
	config["limits"] = map[string]any{"max_retries": 99}

	res := checkRegistryConfig(config)
	if res.OK() {
		t.Fatal("out-of-range limit passed the check")
	}
	found := false
	for _, issue := range res.Issues {
		if issue.Field == "limits" && strings.Contains(issue.Reason, "max_retries") {
			found = true
		}
	}
	if !found {
		t.Fatalf("limits issue does not name max_retries: %+v", res.Issues)
	}
}

func TestCheckRegistryConfigLimitsNotMapping(t *testing.T) {
	config := registryCheckValidConfig()
	config["limits"] = []any{"max_retries"}

	res := checkRegistryConfig(config)
	if res.OK() {
		t.Fatal("non-mapping limits passed the check")
	}
}

func TestCheckRegistryConfigIndexRejectsEmptyCapability(t *testing.T) {
	config := registryCheckValidConfig()
	config["roster"] = []any{
		map[string]any{
			"id":           "exec-1",
			"roles":        []any{"executor"},
			"capabilities": []any{""},
		},
	}

	res := checkRegistryConfig(config)
	if res.OK() {
		t.Fatal("empty capability passed the index check")
	}
	found := false
	for _, issue := range res.Issues {
		if issue.Field == "capabilities" || issue.Field == "agent_capabilities" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no index issue: %+v", res.Issues)
	}
}

func TestCheckRegistryConfigKeyedRoster(t *testing.T) {
	config := registryCheckValidConfig()
	config["roster"] = map[string]any{
		"exec-1": map[string]any{
			"roles":        []any{"executor"},
			"capabilities": []any{"go"},
		},
	}

	if res := checkRegistryConfig(config); !res.OK() {
		t.Fatalf("keyed roster failed: %+v", res.Issues)
	}
}

func TestCheckRegistryConfigDoesNotMutate(t *testing.T) {
	config := registryCheckValidConfig()
	before, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}

	checkRegistryConfig(config)

	after, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("config changed by check:\nbefore %s\nafter  %s", before, after)
	}
}

func TestCheckRegistryConfigIsRepeatable(t *testing.T) {
	config := registryCheckValidConfig()
	first := checkRegistryConfig(config)
	second := checkRegistryConfig(config)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("check result changed between runs:\n%+v\n%+v", first, second)
	}
}

// registryCheckSecretValue must never appear in a negative-case report.
const registryCheckSecretValue = "s3cr3t-DO-NOT-LOG-9f8e7d6c"

func TestCheckRegistryConfigGuardRejectsYAMLMapSecret(t *testing.T) {
	config := registryCheckValidConfig()
	config["token"] = registryCheckSecretValue

	res := checkRegistryConfig(config)
	if res.OK() {
		t.Fatal("secret field in decoded YAML map passed the check")
	}
	found := false
	for _, issue := range res.Issues {
		if issue.Field == "token" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no issue names the token field: %+v", res.Issues)
	}

	var buf bytes.Buffer
	if code := registryCheckRunner(res, &buf); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if strings.Contains(buf.String(), registryCheckSecretValue) {
		t.Fatalf("report leaked the secret value: %q", buf.String())
	}
}

func TestCheckRegistryConfigRejectsUnknownVersion(t *testing.T) {
	tests := []struct {
		name    string
		version any
	}{
		{"int", 99},
		{"string", "99"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := registryCheckValidConfig()
			config["version"] = tc.version

			res := checkRegistryConfig(config)
			if res.OK() {
				t.Fatalf("version %v passed the check", tc.version)
			}
			found := false
			for _, issue := range res.Issues {
				if issue.Field == "version" {
					found = true
				}
			}
			if !found {
				t.Fatalf("no issue names the version field: %+v", res.Issues)
			}
		})
	}
}

func TestCheckRegistryConfigRejectsForeignSchema(t *testing.T) {
	config := registryCheckValidConfig()
	config["schema"] = "other/v2"

	res := checkRegistryConfig(config)
	if res.OK() {
		t.Fatal("foreign schema passed the check")
	}
	found := false
	for _, issue := range res.Issues {
		if issue.Field == "schema" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no issue names the schema field: %+v", res.Issues)
	}
}

func TestCheckRegistryConfigRejectsRosterWithoutCapabilities(t *testing.T) {
	config := registryCheckValidConfig()
	config["roster"] = []any{
		map[string]any{
			"id":    "exec-1",
			"roles": []any{"executor"},
		},
	}

	res := checkRegistryConfig(config)
	if res.OK() {
		t.Fatal("roster without capabilities passed the check")
	}
	found := false
	for _, issue := range res.Issues {
		if issue.Field == "agent_capabilities" || issue.Field == "capabilities" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no index issue: %+v", res.Issues)
	}
}

func TestRegistryCheckRunner(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		res := checkRegistryConfig(registryCheckValidConfig())
		var buf bytes.Buffer
		if code := registryCheckRunner(res, &buf); code != 0 {
			t.Fatalf("clean exit = %d, want 0", code)
		}
		if got, want := strings.TrimSpace(buf.String()), "ok "+res.Digest; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})

	t.Run("issues", func(t *testing.T) {
		config := registryCheckValidConfig()
		config["bogus_key"] = "typo"
		res := checkRegistryConfig(config)
		var buf bytes.Buffer
		if code := registryCheckRunner(res, &buf); code != 1 {
			t.Fatalf("issue exit = %d, want 1", code)
		}
		if !strings.Contains(buf.String(), "bogus_key:") {
			t.Fatalf("output has no field-named issue: %q", buf.String())
		}
	})
}
