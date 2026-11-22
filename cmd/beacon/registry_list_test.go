package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// scopedRegistryListReader is a test reader bound to one project. It refuses
// rows from another project, the same guarantee store.NewScopedRegistry gives
// the real command (T5.29).
type scopedRegistryListReader struct {
	project string
	entries []registryListEntry
	err     error
}

func (r scopedRegistryListReader) Executors() ([]registryListEntry, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]registryListEntry, 0, len(r.entries))
	for _, e := range r.entries {
		if e.Project == r.project {
			out = append(out, e)
		}
	}
	return out, nil
}

// runRegistryListForTest installs a reader for one call and restores the wiring
// seam afterwards, so a test never leaks its source into another test.
func runRegistryListForTest(t *testing.T, reader registryListReader, args ...string) (int, string, string) {
	t.Helper()
	prev := registryListSource
	registryListSource = func(string) (registryListReader, error) { return reader, nil }
	t.Cleanup(func() { registryListSource = prev })
	var out, errOut bytes.Buffer
	c := &CLI{out: &out, errOut: &errOut}
	code := runRegistryList(c, args)
	return code, out.String(), errOut.String()
}

func TestRegistryListEmptyTable(t *testing.T) {
	code, out, errOut := runRegistryListForTest(t, scopedRegistryListReader{project: "alpha"})
	if code != 0 {
		t.Fatalf("empty registry exited %d, want 0: %s", code, errOut)
	}
	if errOut != "" {
		t.Fatalf("empty registry wrote to stderr: %q", errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "AGENT") {
		t.Fatalf("empty registry should print only a header, got %q", out)
	}
}

func TestRegistryListEmptyJSON(t *testing.T) {
	code, out, errOut := runRegistryListForTest(t, scopedRegistryListReader{project: "alpha"}, "--format", "json")
	if code != 0 {
		t.Fatalf("empty registry exited %d, want 0: %s", code, errOut)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty registry JSON = %q, want []", out)
	}
	var rows []registryListRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("empty registry JSON is invalid: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("empty registry JSON decoded to %v, want an empty slice", rows)
	}
}

func TestRegistryListShowsRoleCapabilitiesAndConfig(t *testing.T) {
	reader := scopedRegistryListReader{project: "alpha", entries: []registryListEntry{
		{Project: "alpha", ID: "exec-b", Role: "fixer", Capabilities: []string{"go", "review"}, Config: "cfg-b"},
		{Project: "alpha", ID: "exec-a", Role: "executor", Capabilities: []string{"review"}, Config: "cfg-a"},
	}}
	code, out, errOut := runRegistryListForTest(t, reader)
	if code != 0 {
		t.Fatalf("registry list exited %d, want 0: %s", code, errOut)
	}
	for _, want := range []string{"exec-a", "executor", "review", "cfg-a", "exec-b", "fixer", "go,review", "cfg-b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table output %q missing %q", out, want)
		}
	}
	if strings.Index(out, "exec-a") > strings.Index(out, "exec-b") {
		t.Fatalf("rows are not ordered by agent id: %q", out)
	}
}

func TestRegistryListJSONHasCardFields(t *testing.T) {
	reader := scopedRegistryListReader{project: "alpha", entries: []registryListEntry{
		{Project: "alpha", ID: "exec-1", Role: "executor", Capabilities: []string{"go"}, Config: "cfg-1"},
	}}
	code, out, errOut := runRegistryListForTest(t, reader, "--format=json")
	if code != 0 {
		t.Fatalf("registry list exited %d, want 0: %s", code, errOut)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	for _, key := range []string{"id", "role", "capabilities", "config"} {
		if _, ok := rows[0][key]; !ok {
			t.Fatalf("JSON row %v missing %q", rows[0], key)
		}
	}
	if _, ok := rows[0]["Project"]; ok {
		t.Fatalf("internal project field leaked into JSON: %v", rows[0])
	}
}

func TestRegistryListLimitedToProject(t *testing.T) {
	reader := scopedRegistryListReader{project: "alpha", entries: []registryListEntry{
		{Project: "alpha", ID: "mine", Role: "executor", Capabilities: []string{"go"}, Config: "cfg-a"},
		{Project: "beta", ID: "theirs", Role: "fixer", Capabilities: []string{"review"}, Config: "cfg-b"},
	}}
	code, out, errOut := runRegistryListForTest(t, reader)
	if code != 0 {
		t.Fatalf("registry list exited %d, want 0: %s", code, errOut)
	}
	if !strings.Contains(out, "mine") {
		t.Fatalf("own project's row is missing: %q", out)
	}
	if strings.Contains(out, "theirs") || strings.Contains(out, "cfg-b") {
		t.Fatalf("another project's row leaked into output: %q", out)
	}
}

func TestRegistryListUnknownFormat(t *testing.T) {
	code, out, errOut := runRegistryListForTest(t, scopedRegistryListReader{project: "alpha"}, "--format", "yaml")
	if code == 0 {
		t.Fatal("unknown format exited 0, want non-zero")
	}
	if out != "" {
		t.Fatalf("unknown format wrote to stdout: %q", out)
	}
	if !strings.Contains(errOut, "format") {
		t.Fatalf("unknown format error does not name the format: %q", errOut)
	}
}

func TestRegistryListReaderError(t *testing.T) {
	reader := scopedRegistryListReader{project: "alpha", err: errRegistryListProbe}
	code, out, errOut := runRegistryListForTest(t, reader)
	if code == 0 {
		t.Fatal("reader error exited 0, want non-zero")
	}
	if out != "" {
		t.Fatalf("reader error wrote to stdout: %q", out)
	}
	if !strings.Contains(errOut, "registry list") {
		t.Fatalf("reader error is not labelled: %q", errOut)
	}
}

func TestRegistryListUnwiredReaderRefuses(t *testing.T) {
	prev := registryListSource
	registryListSource = func(string) (registryListReader, error) {
		return nil, errors.New("registry reader not connected")
	}
	t.Cleanup(func() { registryListSource = prev })

	var out, errOut bytes.Buffer
	c := &CLI{out: &out, errOut: &errOut}
	if code := runRegistryList(c, nil); code == 0 {
		t.Fatal("unwired command exited 0, want non-zero")
	}
	if !strings.Contains(errOut.String(), "not connected") {
		t.Fatalf("unwired error should say the reader is not connected: %q", errOut.String())
	}
}

// TestRegistryListSourceScopedToCurrentProject checks the real read path, not
// the test double. Two projects share one database; the default source must
// return only the rows of the project it was built for.
func TestRegistryListSourceScopedToCurrentProject(t *testing.T) {
	state := t.TempDir()
	t.Setenv(beaconStateDirEnv, state)
	root := t.TempDir()

	dir, err := stateDir(root)
	if err != nil {
		t.Fatalf("stateDir: %v", err)
	}
	db, err := store.Open(filepath.Join(dir, mcpStateDBName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	key, err := projectStateKey(root)
	if err != nil {
		t.Fatalf("projectStateKey: %v", err)
	}
	for _, rec := range []store.ExecutorRecord{
		{ProjectKey: key, AgentID: "mine", Role: "executor", Capabilities: []string{"go"}, ConfigID: "cfg-mine"},
		{ProjectKey: "other-project", AgentID: "theirs", Role: "fixer", Capabilities: []string{"review"}, ConfigID: "cfg-theirs"},
	} {
		if _, err := db.UpsertExecutor(rec); err != nil {
			t.Fatalf("UpsertExecutor(%s): %v", rec.AgentID, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reader, err := registryListSource(root)
	if err != nil {
		t.Fatalf("registryListSource: %v", err)
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	entries, err := reader.Executors()
	if err != nil {
		t.Fatalf("Executors: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].ID != "mine" || entries[0].Project != key {
		t.Fatalf("entry = %+v, want the current project's row only", entries[0])
	}
}

type registryListProbeError string

func (e registryListProbeError) Error() string { return string(e) }

const errRegistryListProbe = registryListProbeError("probe failure")
