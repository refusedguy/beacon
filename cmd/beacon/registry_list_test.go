package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// fakeRegistryListStore is a read-only registry port for command tests. It
// stamps the requested project key onto every row, matching the real store's
// project scoping, and records the key it was asked for.
type fakeRegistryListStore struct {
	records    []store.ExecutorRecord
	err        error
	gotProject string
}

func (f *fakeRegistryListStore) Executors(projectKey string) ([]store.ExecutorRecord, error) {
	f.gotProject = projectKey
	if f.err != nil {
		return nil, f.err
	}
	out := make([]store.ExecutorRecord, 0, len(f.records))
	for _, r := range f.records {
		r.ProjectKey = projectKey
		out = append(out, r)
	}
	return out, nil
}

func setRegistryListFake(t *testing.T, s registryListStore) {
	t.Helper()
	SetRegistryListDeps(func(*CLI) (registryListDeps, func() error, error) {
		return registryListDeps{Store: s}, func() error { return nil }, nil
	})
	t.Cleanup(func() { SetRegistryListDeps(nil) })
}

func TestRegistryListEntriesScopedSortedAndCopied(t *testing.T) {
	caps := []string{"watch", "read-verdict"}
	records := []store.ExecutorRecord{
		{ProjectKey: "proj", AgentID: "zeta", Role: "executor", Capabilities: caps, ConfigID: "cfg-z", Status: "active"},
		{ProjectKey: "other", AgentID: "foreign", Role: "executor", Status: "active"},
		{ProjectKey: "proj", AgentID: "alpha", Role: "executor", Capabilities: []string{"ci-retry"}, ConfigID: "cfg-a", Status: "stale"},
		{ProjectKey: "proj", AgentID: "nocaps", Role: "observer"},
	}

	got := registryListEntries("proj", records)
	want := []registryListEntry{
		{AgentID: "alpha", Role: "executor", Capabilities: []string{"ci-retry"}, ConfigID: "cfg-a", Status: "stale"},
		{AgentID: "nocaps", Role: "observer", Capabilities: []string{}, ConfigID: "", Status: ""},
		{AgentID: "zeta", Role: "executor", Capabilities: []string{"watch", "read-verdict"}, ConfigID: "cfg-z", Status: "active"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %#v, want %#v", got, want)
	}

	// The result must not alias the store's capability slice.
	got[2].Capabilities[0] = "mutated"
	if caps[0] != "watch" {
		t.Fatalf("result aliased store capabilities: %v", caps)
	}
}

func TestRegistryListEntriesEmptyIsNonNil(t *testing.T) {
	got := registryListEntries("proj", nil)
	if got == nil {
		t.Fatal("empty registry must be a non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("empty registry returned %d rows", len(got))
	}
}

func TestRenderRegistryListTable(t *testing.T) {
	entries := []registryListEntry{
		{AgentID: "alpha", Role: "executor", Capabilities: []string{"watch", "ci-retry"}, ConfigID: "cfg-a", Status: "active"},
	}
	out, err := renderRegistryList("table", entries)
	if err != nil {
		t.Fatalf("render table: %v", err)
	}
	for _, want := range []string{"AGENT", "ROLE", "CAPS", "CONFIG", "STATUS", "alpha", "watch,ci-retry", "cfg-a", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q: %s", want, out)
		}
	}
}

func TestRenderRegistryListEmptyTableHasNoRows(t *testing.T) {
	out, err := renderRegistryList("table", []registryListEntry{})
	if err != nil {
		t.Fatalf("render empty table: %v", err)
	}
	if !strings.Contains(out, "AGENT") {
		t.Fatalf("empty table missing header: %s", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("empty table must be header only, got %q", out)
	}
}

func TestRenderRegistryListJSON(t *testing.T) {
	entries := []registryListEntry{{AgentID: "a", Role: "executor", Capabilities: []string{"watch"}, ConfigID: "cfg", Status: "active"}}
	out, err := renderRegistryList("json", entries)
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	var decoded []registryListEntry
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(decoded) != 1 || decoded[0].AgentID != "a" || decoded[0].ConfigID != "cfg" || !reflect.DeepEqual(decoded[0].Capabilities, []string{"watch"}) {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestRenderRegistryListEmptyJSONIsArray(t *testing.T) {
	out, err := renderRegistryList("json", []registryListEntry{})
	if err != nil {
		t.Fatalf("render empty json: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty json = %q, want []", out)
	}
}

func TestRenderRegistryListUnknownFormat(t *testing.T) {
	if _, err := renderRegistryList("yaml", nil); err == nil {
		t.Fatal("unknown format must fail")
	}
}

func TestRunRegistryListTable(t *testing.T) {
	fake := &fakeRegistryListStore{records: []store.ExecutorRecord{
		{AgentID: "a", Role: "executor", Capabilities: []string{"watch"}, ConfigID: "cfg-1", Status: "active"},
	}}
	setRegistryListFake(t, fake)

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := runRegistryList(c, nil); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "a") || !strings.Contains(out.String(), "cfg-1") {
		t.Fatalf("output = %q", out.String())
	}
	if fake.gotProject != orchestratorProjectKey() {
		t.Fatalf("store project = %q, want %q", fake.gotProject, orchestratorProjectKey())
	}
}

func TestRunRegistryListJSON(t *testing.T) {
	fake := &fakeRegistryListStore{records: []store.ExecutorRecord{
		{AgentID: "a", Role: "executor", ConfigID: "cfg-1", Status: "active"},
	}}
	setRegistryListFake(t, fake)

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := runRegistryList(c, []string{"--format", "json"}); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"configId": "cfg-1"`) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunRegistryListEmptyRegistryIsNotAnError(t *testing.T) {
	setRegistryListFake(t, &fakeRegistryListStore{})

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := runRegistryList(c, nil); code != 0 {
		t.Fatalf("empty registry code = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "AGENT") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("empty registry must print header-only table, got %q", out.String())
	}
}

func TestRunRegistryListInvalidFormat(t *testing.T) {
	setRegistryListFake(t, &fakeRegistryListStore{})

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := runRegistryList(c, []string{"--format", "yaml"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("invalid format wrote to stdout: %q", out.String())
	}
}

func TestRunRegistryListStoreError(t *testing.T) {
	setRegistryListFake(t, &fakeRegistryListStore{err: errors.New("store down")})

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := runRegistryList(c, nil); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "store down") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("store error wrote to stdout: %q", out.String())
	}
}
