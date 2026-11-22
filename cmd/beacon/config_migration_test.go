package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeConfigRules(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, configMigrationRules)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	return path
}

func TestMigrateConfigOnceCleansStaleRulesAndWritesMarker(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigRules(t, dir, `{"version":1,"rules":[{"name":"keep"},{"name":"old","deprecated":true}]}`+"\n")

	res, err := migrateConfigOnce(dir)
	if err != nil {
		t.Fatalf("migrateConfigOnce: %v", err)
	}
	if !res.RulesCleaned || !res.MarkerCreated {
		t.Fatalf("result = %+v, want cleaned and marker created", res)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	if strings.Contains(string(got), `"old"`) {
		t.Fatalf("rules still contain stale entry: %s", got)
	}
	if !strings.Contains(string(got), `"keep"`) {
		t.Fatalf("rules lost the kept entry: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, configMigrationMarker)); err != nil {
		t.Fatalf("marker not created: %v", err)
	}
}

func TestMigrateConfigOnceLaterRunsAreNoop(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigRules(t, dir, `{"version":1,"rules":[{"name":"keep"},{"name":"old","deprecated":true}]}`+"\n")

	if _, err := migrateConfigOnce(dir); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after first pass: %v", err)
	}

	for pass := 2; pass <= 3; pass++ {
		res, err := migrateConfigOnce(dir)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if res.RulesCleaned || res.MarkerCreated {
			t.Fatalf("pass %d result = %+v, want no-op", pass, res)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read after pass %d: %v", pass, err)
		}
		if string(got) != string(afterFirst) {
			t.Fatalf("pass %d changed rules file: %q -> %q", pass, afterFirst, got)
		}
	}
}

func TestMigrateConfigOnceNoStaleRulesLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	initial := `{"version":1,"rules":[{"name":"keep"}]}` + "\n"
	path := writeConfigRules(t, dir, initial)

	res, err := migrateConfigOnce(dir)
	if err != nil {
		t.Fatalf("migrateConfigOnce: %v", err)
	}
	if res.RulesCleaned {
		t.Fatalf("RulesCleaned = true, want false when nothing is stale")
	}
	if !res.MarkerCreated {
		t.Fatalf("MarkerCreated = false, want a marker after the first pass")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	if string(got) != initial {
		t.Fatalf("rules file changed without stale entries: %q -> %q", initial, got)
	}
}

func TestMigrateConfigOnceConcurrentRunsCreateOneMarker(t *testing.T) {
	dir := t.TempDir()
	writeConfigRules(t, dir, `{"version":1,"rules":[{"name":"old","deprecated":true}]}`+"\n")

	const workers = 8
	results := make([]configMigrationResult, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = migrateConfigOnce(dir)
		}(i)
	}
	wg.Wait()

	created := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		if results[i].MarkerCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("MarkerCreated true in %d workers, want exactly 1", created)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	markers := 0
	for _, entry := range entries {
		if entry.Name() == configMigrationMarker {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("marker files = %d, want 1", markers)
	}
}
