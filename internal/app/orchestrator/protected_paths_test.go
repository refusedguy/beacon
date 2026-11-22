package orchestrator

import (
	"strings"
	"testing"
)

// TestProtectedPathsBlockWritesInsideWritableRoot proves that a writable root
// cannot reopen a service directory. The three paths come from CCx-07: two
// service paths and one ordinary task card.
func TestProtectedPathsBlockWritesInsideWritableRoot(t *testing.T) {
	policy := SandboxPolicy{
		Mode:          SandboxWorkspaceWrite,
		WritableRoots: []string{"docs", ".git", ".codex"},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	cases := []struct {
		path       string
		allowed    bool
		wantReason string
	}{
		{path: ".git/hooks/x", allowed: false, wantReason: "protected path component: .git"},
		{path: ".codex/config", allowed: false, wantReason: "protected path component: .codex"},
		{path: "docs/tasks/CCx-07.md", allowed: true, wantReason: "writable root: docs"},
	}
	for _, tc := range cases {
		check := policy.CheckWrite(tc.path)
		if check.Allowed != tc.allowed {
			t.Errorf("CheckWrite(%q).Allowed = %v, want %v (reason %q)", tc.path, check.Allowed, tc.allowed, check.Reason)
		}
		if check.Reason != tc.wantReason {
			t.Errorf("CheckWrite(%q).Reason = %q, want %q", tc.path, check.Reason, tc.wantReason)
		}
	}
}

// TestProtectedPathsDeniedInDangerFullAccess keeps the protection above the
// mode switch: an unrestricted mode is not a way around the service list.
func TestProtectedPathsDeniedInDangerFullAccess(t *testing.T) {
	policy := SandboxPolicy{Mode: SandboxDangerFullAccess}
	for _, path := range []string{".git/hooks/x", ".codex/config", ".git/HEAD"} {
		check := policy.CheckWrite(path)
		if check.Allowed {
			t.Errorf("CheckWrite(%q) allowed in danger-full-access", path)
		}
	}
	if !policy.CheckWrite("docs/tasks/CCx-07.md").Allowed {
		t.Fatal("ordinary path denied in danger-full-access")
	}
}

// TestReadOnlySubpaths records the other half of the Codex list: a path can
// be denied by prefix even when no component is a protected name.
func TestReadOnlySubpaths(t *testing.T) {
	policy := SandboxPolicy{
		Mode:             SandboxDangerFullAccess,
		ReadOnlySubpaths: []string{"config/secrets"},
	}
	check := policy.CheckWrite("config/secrets/token")
	if check.Allowed {
		t.Fatal("read-only subpath write allowed")
	}
	if !strings.Contains(check.Reason, "read-only subpath: config/secrets") {
		t.Fatalf("unexpected reason %q", check.Reason)
	}
	if !policy.CheckWrite("config/public/note").Allowed {
		t.Fatal("sibling path denied")
	}
}

// TestProtectedPathsWriteCount mirrors the card check: zero files written in
// the service paths and one file written at the task card path.
func TestProtectedPathsWriteCount(t *testing.T) {
	policy := SandboxPolicy{Mode: SandboxWorkspaceWrite, WritableRoots: []string{"docs"}}
	written := map[string]bool{}
	write := func(path string) {
		if policy.CheckWrite(path).Allowed {
			written[path] = true
		}
	}
	write(".git/hooks/x")
	write(".codex/config")
	write("docs/tasks/CCx-07.md")
	if written[".git/hooks/x"] || written[".codex/config"] {
		t.Fatalf("service paths written: %v", written)
	}
	if len(written) != 1 || !written["docs/tasks/CCx-07.md"] {
		t.Fatalf("want one task-card write, got %v", written)
	}
}

func TestSandboxPolicyValidateProtectedFields(t *testing.T) {
	good := SandboxPolicy{
		Mode:             SandboxWorkspaceWrite,
		ProtectedNames:   []string{".codex", ".git"},
		ReadOnlySubpaths: []string{".git/hooks", "config/secrets"},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("Validate(good): %v", err)
	}
	bad := []SandboxPolicy{
		{Mode: SandboxDangerFullAccess, ProtectedNames: []string{"a/b"}},
		{Mode: SandboxDangerFullAccess, ProtectedNames: []string{".."}},
		{Mode: SandboxDangerFullAccess, ReadOnlySubpaths: []string{"../etc"}},
	}
	for _, policy := range bad {
		if err := policy.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted invalid protected field", policy)
		}
	}
}
