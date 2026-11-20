package registry

import (
	"bytes"
	"encoding/json"
	"testing"
)

const v1Config = `{
  "schema": "beacon.agent-registry/v1",
  "version": 1,
  "project": "beacon",
  "roster": [
    {"id": "orchestrator", "runtime": "subagent", "capabilities": ["watch", "merge"]}
  ],
  "configurations": [],
  "limits": {"maxWatchers": 8}
}`

func TestMigrateV1ReadsWithWarning(t *testing.T) {
	in := []byte(v1Config)
	got, err := Migrate(in)
	if err != nil {
		t.Fatalf("Migrate v1: %v", err)
	}
	if got.Version != SchemaV2 {
		t.Errorf("Version = %d, want %d", got.Version, SchemaV2)
	}
	if got.Schema != "beacon.agent-registry/v2" {
		t.Errorf("Schema = %q, want beacon.agent-registry/v2", got.Schema)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("Warnings = %+v, want one note", got.Warnings)
	}
	if got.Warnings[0].Code != migratedWarningCode {
		t.Errorf("warning code = %q, want %q", got.Warnings[0].Code, migratedWarningCode)
	}

	var migrated map[string]json.RawMessage
	if err := json.Unmarshal(got.Data, &migrated); err != nil {
		t.Fatalf("migrated Data is not a JSON object: %v", err)
	}
	var version int
	if err := json.Unmarshal(migrated["version"], &version); err != nil {
		t.Fatalf("migrated version: %v", err)
	}
	if version != SchemaV2 {
		t.Errorf("migrated version = %d, want %d", version, SchemaV2)
	}
	var schema string
	if err := json.Unmarshal(migrated["schema"], &schema); err != nil {
		t.Fatalf("migrated schema: %v", err)
	}
	if schema != "beacon.agent-registry/v2" {
		t.Errorf("migrated schema = %q, want beacon.agent-registry/v2", schema)
	}

	var original map[string]json.RawMessage
	if err := json.Unmarshal(in, &original); err != nil {
		t.Fatalf("input is not a JSON object: %v", err)
	}
	for _, key := range []string{"project", "roster", "configurations", "limits"} {
		want := compactJSON(t, original[key])
		got := compactJSON(t, migrated[key])
		if !bytes.Equal(got, want) {
			t.Errorf("field %q changed during migration: got %s, want %s", key, got, want)
		}
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact JSON %s: %v", raw, err)
	}
	return buf.Bytes()
}

func TestMigrateV2PassesThroughUnchanged(t *testing.T) {
	in := []byte(`{"schema":"beacon.agent-registry/v2","version":2,"project":"beacon"}`)
	got, err := Migrate(in)
	if err != nil {
		t.Fatalf("Migrate v2: %v", err)
	}
	if got.Version != SchemaV2 {
		t.Errorf("Version = %d, want %d", got.Version, SchemaV2)
	}
	if got.Schema != "beacon.agent-registry/v2" {
		t.Errorf("Schema = %q, want beacon.agent-registry/v2", got.Schema)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("Warnings = %+v, want none for a current config", got.Warnings)
	}
	if !bytes.Equal(got.Data, in) {
		t.Errorf("Data = %s, want unchanged input %s", got.Data, in)
	}
}

func TestMigrateV2WithoutSchemaField(t *testing.T) {
	got, err := Migrate([]byte(`{"version":2,"project":"beacon"}`))
	if err != nil {
		t.Fatalf("Migrate v2 without schema: %v", err)
	}
	if got.Schema != "" {
		t.Errorf("Schema = %q, want empty when the field is absent", got.Schema)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("Warnings = %+v, want none", got.Warnings)
	}
}

func TestMigrateRejectsUnknownVersion(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{"zero", `{"version":0}`},
		{"negative", `{"version":-1}`},
		{"future", `{"version":3}`},
		{"string version", `{"version":"2"}`},
		{"float version", `{"version":2.5}`},
		{"missing version", `{"project":"beacon"}`},
		{"malformed json", `{`},
		{"array is not an object", `[]`},
		{"null is not an object", `null`},
		{"empty input", ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Migrate([]byte(tc.config)); err == nil {
				t.Fatalf("Migrate(%q) accepted an unknown version", tc.config)
			}
		})
	}
}

func TestMigrateRejectsSchemaDisagreement(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{"v1 schema with v2 version", `{"schema":"beacon.agent-registry/v1","version":2}`},
		{"v2 schema with v1 version", `{"schema":"beacon.agent-registry/v2","version":1}`},
		{"foreign schema", `{"schema":"other/v1","version":1}`},
		{"schema without integer", `{"schema":"beacon.agent-registry/vX","version":1}`},
		{"schema is not a string", `{"schema":1,"version":1}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Migrate([]byte(tc.config)); err == nil {
				t.Fatalf("Migrate(%q) accepted a conflicting schema", tc.config)
			}
		})
	}
}

func TestMigrateIsIdempotentAndLeavesInputAlone(t *testing.T) {
	in := []byte(v1Config)
	original := append([]byte(nil), in...)

	first, err := Migrate(in)
	if err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if !bytes.Equal(in, original) {
		t.Fatalf("Migrate mutated its input: got %s, want %s", in, original)
	}

	second, err := Migrate(first.Data)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(second.Warnings) != 0 {
		t.Errorf("Warnings on re-read = %+v, want none", second.Warnings)
	}
	if !bytes.Equal(second.Data, first.Data) {
		t.Errorf("second pass changed the config: got %s, want %s", second.Data, first.Data)
	}
}
