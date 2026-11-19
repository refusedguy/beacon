package gate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseOptions(t *testing.T) {
	o, err := ParseOptions([]string{"-root", "/tmp/x", "-min-findings=3"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Root != "/tmp/x" || o.MinFindings != 3 {
		t.Fatalf("got %+v", o)
	}
	for _, args := range [][]string{
		{"-min-findings=abc"},
		{"--bogus"},
		{"-root"},
		{"-min-findings=-1"},
	} {
		if _, err := ParseOptions(args); err == nil {
			t.Errorf("ParseOptions(%q) should fail", args)
		}
	}
}

func TestFixture(t *testing.T) {
	root, err := Fixture(map[string]string{"internal/domain/plant.go": "package domain\n"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	b, err := os.ReadFile(filepath.Join(root, "internal/domain/plant.go"))
	if err != nil || string(b) != "package domain\n" {
		t.Fatalf("fixture content: %q, err %v", b, err)
	}
}

func TestHasRuleAt(t *testing.T) {
	fs := []Finding{{Rule: "R", Path: "a/b.go", Line: 3}}
	if !HasRule(fs, "R") || !HasRuleAt(fs, "R", "a/b.go", 3) {
		t.Fatal("finding not matched")
	}
	if HasRuleAt(fs, "R", "a/b.go", 4) || HasRule(fs, "X") {
		t.Fatal("false positive")
	}
}

func TestMatrix(t *testing.T) {
	m, err := LoadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	// Nested packages inherit their parent's layer by prefix.
	for _, p := range []string{"internal/domain", "internal/app/taskcli", "internal/infra/taskstore", "cmd/beacon", "tools/beacon-gates/gate"} {
		if !m.Has(p) {
			t.Errorf("package %q is not covered by the matrix", p)
		}
	}
	if !m.Allows("cmd/beacon", "internal/app/taskcli") {
		t.Fatal("matrix does not allow outer to inner")
	}
	if m.Allows("internal/domain", "internal/infra") || m.Allows("internal/infra", "internal/app") {
		t.Fatal("matrix allows an outward dependency")
	}
	// Every permission must be one the real tree uses; no dead rows.
	if len(m.AllowedEdges()) != 4 {
		t.Fatalf("AllowedEdges = %d, want 4: %v", len(m.AllowedEdges()), m.AllowedEdges())
	}
}

func TestLayerPassesSkipTmp(t *testing.T) {
	root, err := Fixture(map[string]string{
		"go.mod":                    "module " + LayerFixtureModule + "\n\ngo 1.26.0\n",
		"internal/domain/domain.go": "package domain\n",
		"tmp/spike/spike.go":        "package spike\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	m, err := LoadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	passes := map[string]func() ([]Edge, []string, error){
		"ast":  func() ([]Edge, []string, error) { return ASTEdges(root, m) },
		"list": func() ([]Edge, []string, error) { return ListEdges(root, m) },
	}
	for name, pass := range passes {
		edges, pkgs, err := pass()
		if err != nil {
			t.Fatalf("%s pass: %v", name, err)
		}
		for _, p := range pkgs {
			if skippedByPolicy(p) {
				t.Errorf("%s pass reported tmp package %q", name, p)
			}
		}
		for _, f := range CheckMatrix(m, edges, pkgs) {
			if skippedByPolicy(f.Path) {
				t.Errorf("%s pass reported tmp finding: %v", name, f)
			}
		}
	}
}

func TestExceptionsApply(t *testing.T) {
	ex, err := LoadExceptions()
	if err != nil {
		t.Fatal(err)
	}
	fs := []Finding{{Rule: "CAP-EXEC-IN-DOMAIN", Path: "internal/domain/plant.go", Line: 3}}
	if got := ex.Apply(fs, "."); len(got) != 1 {
		t.Fatalf("empty tables changed findings: %v", got)
	}

	bad := Exceptions{Baseline: []BaselineEntry{{Rule: "R", Path: "p"}}}
	if got := bad.Apply(nil, "."); !HasRule(got, "CAP-BASELINE-EXCEEDED") || !HasRule(got, "CAP-BASELINE-STALE") {
		t.Fatalf("baseline ratchet and liveness not enforced: %v", got)
	}
}
