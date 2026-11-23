package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates one skill directory with a single SKILL.md and returns
// its path relative to root.
func writeSkill(t *testing.T, root, name, body string) string {
	t.Helper()
	rel := skillsLockDir + "/" + name
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel), "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

// writeLock stores a lock whose hashes are computed from the files on disk.
func writeLock(t *testing.T, root string, entries []skillsLockEntry, version int) {
	t.Helper()
	for i := range entries {
		hash, err := hashSkillDir(root, entries[i].Path)
		if err != nil {
			t.Fatal(err)
		}
		entries[i].ComputedHash = hash
	}
	data, err := json.Marshal(skillsLock{Version: version, Skills: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, skillsLockFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsLockAcceptsPinnedSkills(t *testing.T) {
	root := t.TempDir()
	first := writeSkill(t, root, "one", "name: one\n")
	second := writeSkill(t, root, "two", "name: two\n")
	writeLock(t, root, []skillsLockEntry{
		{Name: "one", Path: first},
		{Name: "two", Path: second},
	}, skillsLockVersion)

	findings, err := verifySkillsLock(root)
	if err != nil {
		t.Fatalf("verifySkillsLock: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
}

func TestSkillsLockDetectsChangedHash(t *testing.T) {
	root := t.TempDir()
	first := writeSkill(t, root, "one", "name: one\n")
	writeLock(t, root, []skillsLockEntry{{Name: "one", Path: first}}, skillsLockVersion)

	// A different file in the same skill changes its computedHash.
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(first), "reference.md"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := verifySkillsLock(root)
	if err != nil {
		t.Fatalf("verifySkillsLock: %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Problem, "расходится") {
		t.Fatalf("findings = %v, want one hash mismatch", findings)
	}
}

func TestSkillsLockDetectsUnpinnedSkill(t *testing.T) {
	root := t.TempDir()
	first := writeSkill(t, root, "one", "name: one\n")
	writeLock(t, root, []skillsLockEntry{{Name: "one", Path: first}}, skillsLockVersion)
	writeSkill(t, root, "two", "name: two\n")

	findings, err := verifySkillsLock(root)
	if err != nil {
		t.Fatalf("verifySkillsLock: %v", err)
	}
	if len(findings) != 1 || findings[0].Name != "two" {
		t.Fatalf("findings = %v, want unpinned skill two", findings)
	}
}

func TestSkillsLockRejectsMissingOrWrongVersion(t *testing.T) {
	root := t.TempDir()
	findings, err := verifySkillsLock(root)
	if err != nil {
		t.Fatalf("verifySkillsLock on empty root: %v", err)
	}
	if len(findings) != 1 || findings[0].Name != skillsLockFile {
		t.Fatalf("findings = %v, want missing lock file", findings)
	}

	first := writeSkill(t, root, "one", "name: one\n")
	writeLock(t, root, []skillsLockEntry{{Name: "one", Path: first}}, 2)
	findings, err = verifySkillsLock(root)
	if err != nil {
		t.Fatalf("verifySkillsLock: %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Problem, "version") {
		t.Fatalf("findings = %v, want wrong version", findings)
	}
}

// TestRepoSkillsLockIsCurrent is the checked-in lock's own gate. A skill edit
// without a lock update fails this test, which is what HD-071 asks for.
func TestRepoSkillsLockIsCurrent(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, skillsLockFile)); err != nil {
		t.Fatalf("repo root %s: %v", root, err)
	}
	findings, err := verifySkillsLock(root)
	if err != nil {
		t.Fatalf("verifySkillsLock: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("skills-lock.json is stale: %v", findings)
	}
}

// runSkillsLockForTest drives the command the same way the CLI dispatcher does.
func runSkillsLockForTest(t *testing.T, root string, args ...string) int {
	t.Helper()
	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, root: root}
	return cli.runSkillsLock(args)
}

func readLockBytes(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, skillsLockFile))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWriteSkillsLockIsByteStable(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "one", "name: one\n")
	writeSkill(t, root, "two", "name: two\n")

	if code := runSkillsLockForTest(t, root, "--write"); code != 0 {
		t.Fatalf("write exit = %d, want 0", code)
	}
	first := readLockBytes(t, root)
	if code := runSkillsLockForTest(t, root, "--write"); code != 0 {
		t.Fatalf("second write exit = %d, want 0", code)
	}
	second := readLockBytes(t, root)
	if !bytes.Equal(first, second) {
		t.Fatalf("second write changed the lock:\n%s\n---\n%s", first, second)
	}
	if code := runSkillsLockForTest(t, root, "--check"); code != 0 {
		t.Fatalf("check after write exit = %d, want 0", code)
	}
}

func TestWriteSkillsLockAddsAndRemovesEntries(t *testing.T) {
	root := t.TempDir()
	first := writeSkill(t, root, "one", "name: one\n")
	writeSkill(t, root, "two", "name: two\n")
	if code := runSkillsLockForTest(t, root, "--write"); code != 0 {
		t.Fatalf("write exit = %d, want 0", code)
	}

	if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(first))); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, root, "three", "name: three\n")
	if code := runSkillsLockForTest(t, root, "--write"); code != 0 {
		t.Fatalf("write after skill change exit = %d, want 0", code)
	}

	var lock skillsLock
	if err := json.Unmarshal(readLockBytes(t, root), &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Version != skillsLockVersion {
		t.Fatalf("version = %d, want %d", lock.Version, skillsLockVersion)
	}
	names := make(map[string]bool, len(lock.Skills))
	for _, entry := range lock.Skills {
		names[entry.Name] = true
	}
	if !names["two"] || !names["three"] || names["one"] {
		t.Fatalf("locked skills = %v, want two and three only", names)
	}
}

func TestSkillsLockCheckDetectsDriftAndWriteRepairs(t *testing.T) {
	root := t.TempDir()
	first := writeSkill(t, root, "one", "name: one\n")
	if code := runSkillsLockForTest(t, root, "--write"); code != 0 {
		t.Fatalf("write exit = %d, want 0", code)
	}
	if code := runSkillsLockForTest(t, root, "--check"); code != 0 {
		t.Fatalf("check exit = %d, want 0", code)
	}

	// One edited file inside a skill changes its computedHash.
	extra := filepath.Join(root, filepath.FromSlash(first), "extra.md")
	if err := os.WriteFile(extra, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runSkillsLockForTest(t, root, "--check"); code == 0 {
		t.Fatal("--check passed with an edited skill file")
	}

	// A new skill without a lock entry also fails the check.
	writeSkill(t, root, "two", "name: two\n")
	if code := runSkillsLockForTest(t, root, "--check"); code == 0 {
		t.Fatal("--check passed with an unpinned skill")
	}

	if code := runSkillsLockForTest(t, root, "--write"); code != 0 {
		t.Fatalf("repair write exit = %d, want 0", code)
	}
	if code := runSkillsLockForTest(t, root, "--check"); code != 0 {
		t.Fatalf("check after repair exit = %d, want 0", code)
	}
}

func TestSkillsLockCommandRequiresOneMode(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "one", "name: one\n")
	for _, args := range [][]string{{}, {"--write", "--check"}} {
		if code := runSkillsLockForTest(t, root, args...); code == 0 {
			t.Fatalf("exit = 0 for args %v, want non-zero", args)
		}
	}
}
