package session

import (
	"reflect"
	"testing"
)

// TestResumeWorkspaceKeepsOnlyRequestedSavedRoots covers the resume case with
// two saved roots and one requested root (CCx-15). The saved record also holds
// an explicit setting, which must stay in one copy after resume.
func TestResumeWorkspaceKeepsOnlyRequestedSavedRoots(t *testing.T) {
	previous := map[string]string{"ai.beacon/title": "manual setting"}
	previous = WithWorkspaceRoots(previous, []string{"/srv/repo/a", "/srv/repo/b"})
	previous = WithReadOnlyRoots(previous, []string{"/srv/repo/a"})

	f := &fakeSessionStore{
		want:  Session{ID: "ses-1", Status: SessionStatusActive, Metadata: previous},
		found: true,
	}
	r := NewSessionRegistry(f)

	resumed, found, err := r.ResumeWorkspace("ses-1", []string{"/srv/repo/b"})
	if err != nil || !found {
		t.Fatalf("ResumeWorkspace: found=%v err=%v", found, err)
	}

	roots, ok := WorkspaceRoots(resumed.Metadata)
	if !ok {
		t.Fatal("resumed session has no workspace roots")
	}
	if want := []string{"/srv/repo/b"}; !reflect.DeepEqual(roots, want) {
		t.Fatalf("roots = %#v, want %#v", roots, want)
	}
	if len(roots) != 1 {
		t.Fatalf("root count = %d, want 1", len(roots))
	}

	instances := 0
	for _, value := range resumed.Metadata {
		if value == "manual setting" {
			instances++
		}
	}
	if instances != 1 {
		t.Fatalf("explicit setting appears %d times, want 1", instances)
	}
	if readOnly, ok := ReadOnlyRoots(resumed.Metadata); !ok || !reflect.DeepEqual(readOnly, []string{"/srv/repo/a"}) {
		t.Fatalf("read-only roots = %#v, ok=%v", readOnly, ok)
	}

	savedRoots, _ := WorkspaceRoots(previous)
	if want := []string{"/srv/repo/a", "/srv/repo/b"}; !reflect.DeepEqual(savedRoots, want) {
		t.Fatalf("saved record was mutated: %#v", savedRoots)
	}
}

// TestResumeWorkspaceDropsUnrequestedRoots checks that a request for a root
// that was never saved cannot widen the workspace.
func TestResumeWorkspaceDropsUnrequestedRoots(t *testing.T) {
	saved := WithWorkspaceRoots(nil, []string{"/srv/repo/a", "/srv/repo/b"})
	got := ResumeWorkspaceRoots(saved, []string{"/srv/repo/c", "/srv/repo/d"})
	if roots, ok := WorkspaceRoots(got); ok && len(roots) != 0 {
		t.Fatalf("roots = %#v, want none", roots)
	}
}

// TestWithWorkspaceRootsDropsEmptyEntries checks the setter keeps only usable
// paths and does not touch the input map.
func TestWithWorkspaceRootsDropsEmptyEntries(t *testing.T) {
	input := map[string]string{"taskID": "T9.9"}
	got := WithWorkspaceRoots(input, []string{" /srv/repo/a ", "", "  ", "/srv/repo/b"})
	if roots, ok := WorkspaceRoots(got); !ok || !reflect.DeepEqual(roots, []string{"/srv/repo/a", "/srv/repo/b"}) {
		t.Fatalf("roots = %#v, ok=%v", roots, ok)
	}
	if _, ok := input[MetadataWorkspaceRoots]; ok {
		t.Fatal("setter changed the input map")
	}
}
