package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// fakeRegistryListSource is a read-only stand-in that records the project key
// it was asked about. It has no write method, matching registryListSource.
type fakeRegistryListSource struct {
	records   []store.ExecutorRecord
	err       error
	projectID string
}

func (f *fakeRegistryListSource) Executors(projectKey string) ([]store.ExecutorRecord, error) {
	f.projectID = projectKey
	return f.records, f.err
}

func TestRegistryListRowsMapsScopedRecords(t *testing.T) {
	source := &fakeRegistryListSource{records: []store.ExecutorRecord{
		{AgentID: "a", Role: "executor", Capabilities: []string{"watch", "ack"}, ConfigID: "cfg-1"},
		{AgentID: "b", Role: "fixer", Capabilities: nil, ConfigID: "cfg-2"},
	}}
	rows, err := registryListRows(source, "project-key")
	if err != nil {
		t.Fatalf("registryListRows: %v", err)
	}
	if source.projectID != "project-key" {
		t.Fatalf("project key = %q, want project-key", source.projectID)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Capabilities are sorted for stable output.
	if got := strings.Join(rows[0].Capabilities, ","); got != "ack,watch" {
		t.Fatalf("capabilities = %q, want ack,watch", got)
	}
	if rows[0].AgentID != "a" || rows[0].Role != "executor" || rows[0].ConfigID != "cfg-1" {
		t.Fatalf("row 0 = %+v, want agent a / executor / cfg-1", rows[0])
	}
	if rows[1].Capabilities == nil || len(rows[1].Capabilities) != 0 {
		t.Fatalf("empty capabilities = %v, want empty non-nil slice", rows[1].Capabilities)
	}
}

func TestRegistryListRowsEmptyKeyDoesNotReadStore(t *testing.T) {
	source := &fakeRegistryListSource{err: errors.New("must not be called")}
	rows, err := registryListRows(source, "  ")
	if err != nil {
		t.Fatalf("registryListRows: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
	if source.projectID != "" {
		t.Fatalf("store was read with key %q", source.projectID)
	}
}

func TestRegistryListRowsPropagatesReadError(t *testing.T) {
	source := &fakeRegistryListSource{err: errors.New("boom")}
	if _, err := registryListRows(source, "project-key"); err == nil {
		t.Fatal("registryListRows returned no error for a failing source")
	}
}

func TestWriteRegistryListTableEmptyIsHeaderOnly(t *testing.T) {
	var buf bytes.Buffer
	if err := writeRegistryListTable(&buf, nil); err != nil {
		t.Fatalf("writeRegistryListTable: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "AGENT") || !strings.Contains(got, "CONFIG") {
		t.Fatalf("header missing from %q", got)
	}
	if strings.Count(strings.TrimSpace(got), "\n") != 0 {
		t.Fatalf("empty table has data rows: %q", got)
	}
}

func TestRunRegistryListTableEmptyRegistry(t *testing.T) {
	var out, errOut bytes.Buffer
	source := &fakeRegistryListSource{}
	code := runRegistryList(nil, &out, &errOut, source, "project-key")
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "AGENT") {
		t.Fatalf("empty registry did not print a table: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
}

func TestRunRegistryListJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	source := &fakeRegistryListSource{records: []store.ExecutorRecord{
		{AgentID: "a", Role: "executor", Capabilities: []string{"ack"}, ConfigID: "cfg-1"},
	}}
	code := runRegistryList([]string{"--format", "json"}, &out, &errOut, source, "project-key")
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, errOut.String())
	}
	var rows []registryListEntry
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if len(rows) != 1 || rows[0].AgentID != "a" || rows[0].ConfigID != "cfg-1" {
		t.Fatalf("decoded rows = %+v", rows)
	}
}

func TestRunRegistryListBadFormat(t *testing.T) {
	var out, errOut bytes.Buffer
	source := &fakeRegistryListSource{}
	code := runRegistryList([]string{"--format", "yaml"}, &out, &errOut, source, "project-key")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "yaml") {
		t.Fatalf("error does not name the bad format: %q", errOut.String())
	}
}

func TestRunRegistryListReadError(t *testing.T) {
	var out, errOut bytes.Buffer
	source := &fakeRegistryListSource{err: errors.New("store down")}
	code := runRegistryList(nil, &out, &errOut, source, "project-key")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "store down") {
		t.Fatalf("error not reported: %q", errOut.String())
	}
}
