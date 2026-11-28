package tasksource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestCardIndexDetectsEqualSizeEqualMtimeEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T-SAME.md")
	first := "---\nid: T-SAME\ntitle: AAAA\nstatus: available\n---\n# T-SAME. A\n"
	second := "---\nid: T-SAME\ntitle: BBBB\nstatus: available\n---\n# T-SAME. B\n"
	if len(first) != len(second) {
		t.Fatal("test fixtures differ in length")
	}
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	store := CardStore{Dir: dir}
	resetCardIndex(dir)
	c, err := store.indexedFind("T-SAME")
	if err != nil {
		t.Fatal(err)
	}
	if c.task.Title != "AAAA" {
		t.Fatalf("initial title = %q", c.task.Title)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	c, err = store.indexedFind("T-SAME")
	if err != nil {
		t.Fatal(err)
	}
	if c.task.Title != "BBBB" {
		t.Fatalf("title after equal-size edit = %q, want BBBB", c.task.Title)
	}
}

func TestCardIndexDropsDeletedCardWithoutDirMtimeChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T-GONE.md")
	card := "---\nid: T-GONE\nstatus: available\n---\n# T-GONE. Gone\n"
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
	store := CardStore{Dir: dir}
	resetCardIndex(dir)
	if _, err := store.indexedFind("T-GONE"); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, dirInfo.ModTime(), dirInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.indexedFind("T-GONE"); !os.IsNotExist(err) {
		t.Fatalf("deleted card error = %v, want not exist", err)
	}
	tasks, err := NewMarkdown(dir).ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("available = %+v, want none after deletion", tasks)
	}
}

func TestCardIndexRejectsDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	card := "---\nid: T-DUP\nstatus: available\n---\n# T-DUP. Duplicate\n"
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(card), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resetCardIndex(dir)
	tasks, err := NewMarkdown(dir).ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "T-DUP" {
		t.Fatalf("available = %+v, want one T-DUP", tasks)
	}
}

func TestStatusWriteIsAtomicAndReread(t *testing.T) {
	dir := t.TempDir()
	path := writeIndexCard(t, dir, "T-WRITE", "available")
	store := CardStore{Dir: dir}
	resetCardIndex(dir)
	if err := store.indexedPatchStatus(context.Background(), "T-WRITE", "done"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "T-WRITE.md" {
			t.Fatalf("leftover file after atomic write: %s", e.Name())
		}
	}
	c, err := store.indexedFind("T-WRITE")
	if err != nil {
		t.Fatal(err)
	}
	if c.task.Status != core.TaskDone {
		t.Fatalf("indexed status = %q, want done", c.task.Status)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fresh, ok, _ := parseCardFields(string(b), path)
	if !ok || fresh.Status != c.task.Status || string(b) != c.content {
		t.Fatalf("index and disk disagree: indexed = %+v/%q, disk = %+v/%q", c.task, c.content, fresh, b)
	}
}

func TestMarkdownAndIssueDecodersResolveAliasesAlike(t *testing.T) {
	legacy := "# T-ALIAS. Legacy\nTaskID: T-ALIAS\nTitle: Flat title\nStatus: in_progress\nScope: a.go\nВладелец файлов: b.go\nDepends: T-0\nЗависимости: T-1\n"
	var fields taskFields
	decodeAliases(cardLines(legacy), "", "", &fields)
	issue := "```yaml\ntask_id: T-ALIAS\ntitle: Flat title\nstatus: in_progress\nscope: [a.go, b.go]\ndepends: [T-0, T-1]\n```\n"
	got := ParseTaskFields(issue, "", "")
	if got.ID != fields.ID || got.Title != fields.Title || got.Status != fields.Status {
		t.Fatalf("identity differs: markdown = %+v, issue = %+v", fields, got)
	}
	if len(got.Scope) != len(fields.Scope) || len(got.Depends) != len(fields.Depends) {
		t.Fatalf("aliases differ: markdown = %+v, issue = %+v", fields, got)
	}
	for i := range got.Scope {
		if got.Scope[i] != fields.Scope[i] {
			t.Fatalf("scope[%d] = %q, want %q", i, got.Scope[i], fields.Scope[i])
		}
	}
	for i := range got.Depends {
		if got.Depends[i] != fields.Depends[i] {
			t.Fatalf("depends[%d] = %q, want %q", i, got.Depends[i], fields.Depends[i])
		}
	}
}
