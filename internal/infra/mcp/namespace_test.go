package mcp

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// testToolSchema builds a flat object schema with one string property per name.
func testToolSchema(names ...string) *jsonschema.Schema {
	props := make(map[string]*jsonschema.Schema, len(names))
	for _, name := range names {
		props[name] = &jsonschema.Schema{Type: "string"}
	}
	return &jsonschema.Schema{Type: "object", Properties: props}
}

// TestToolNamespaceSurvivesJSON checks that a namespace description stays
// attached after the schema is serialized and read back.
func TestToolNamespaceSurvivesJSON(t *testing.T) {
	schema := testToolSchema("value")
	want := ToolNamespace{
		Name: "spawn",
		Tools: []NamespacedTool{
			{Name: "child", Schema: testToolSchema("value")},
		},
	}
	SetToolNamespace(schema, want)

	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var decoded jsonschema.Schema
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	got, ok := ToolNamespaceOf(&decoded)
	if !ok {
		t.Fatal("namespace keyword missing after JSON round trip")
	}
	if got.Name != want.Name {
		t.Errorf("namespace name = %q, want %q", got.Name, want.Name)
	}
	if len(got.Tools) != 1 {
		t.Fatalf("nested tools = %d, want 1", len(got.Tools))
	}
	if got.Tools[0].Name != "child" {
		t.Errorf("nested tool name = %q, want child", got.Tools[0].Name)
	}
}

// TestSubstituteToolSchemaKeepsNamespaceAndEncryptedMarker is the CCx-22
// acceptance case: after substitution one namespace holds one nested tool and
// one parameter still carries encrypted: true.
func TestSubstituteToolSchemaKeepsNamespaceAndEncryptedMarker(t *testing.T) {
	base := testToolSchema("payload")
	if !MarkParameterEncrypted(base, "payload") {
		t.Fatal("base parameter payload is missing")
	}
	SetToolNamespace(base, ToolNamespace{
		Name:  "spawn",
		Tools: []NamespacedTool{{Name: "child", Schema: testToolSchema("payload")}},
	})
	override := testToolSchema("payload")

	out, err := SubstituteToolSchema(base, override)
	if err != nil {
		t.Fatalf("substitute schema: %v", err)
	}
	if out == override {
		t.Fatal("substitution mutated the override instead of copying it")
	}
	if !ParameterEncrypted(out, "payload") {
		t.Fatal("encrypted marker lost during substitution")
	}

	ns, ok := ToolNamespaceOf(out)
	if !ok {
		t.Fatal("namespace lost during substitution")
	}
	if ns.Name != "spawn" || len(ns.Tools) != 1 || ns.Tools[0].Name != "child" {
		t.Errorf("namespace after substitution = %+v", ns)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal substituted schema: %v", err)
	}
	var decoded jsonschema.Schema
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal substituted schema: %v", err)
	}
	if !ParameterEncrypted(&decoded, "payload") {
		t.Fatal("encrypted marker missing from serialized schema")
	}
	if _, ok := ToolNamespaceOf(&decoded); !ok {
		t.Fatal("namespace missing from serialized schema")
	}
}

// TestSubstituteToolSchemaDoesNotInventMarkers checks that a new parameter in
// the override is not treated as encrypted just because the base had a
// different encrypted parameter.
func TestSubstituteToolSchemaDoesNotInventMarkers(t *testing.T) {
	base := testToolSchema("known")
	MarkParameterEncrypted(base, "known")
	override := testToolSchema("new")

	out, err := SubstituteToolSchema(base, override)
	if err != nil {
		t.Fatalf("substitute schema: %v", err)
	}
	if ParameterEncrypted(out, "new") {
		t.Fatal("new parameter got an encrypted marker it never had")
	}
	if ParameterEncrypted(override, "new") {
		t.Fatal("override was mutated during substitution")
	}
}

// TestAdvertisedSchemasHaveNoNamespace checks the flat ten-tool contract. A
// namespace added to an advertised tool would change tools/list and break the
// golden response.
func TestAdvertisedSchemasHaveNoNamespace(t *testing.T) {
	schemas, err := AllOutputSchemas()
	if err != nil {
		t.Fatalf("all output schemas: %v", err)
	}
	for name, schema := range schemas {
		if _, ok := ToolNamespaceOf(schema); ok {
			t.Errorf("advertised tool %s carries a namespace", name)
		}
	}
}
