package domain

import "testing"

func TestScanExportRecognizesTerm(t *testing.T) {
	policy := ExportPolicy{Terms: []LeakRule{{Term: "internal-token"}}}
	lines := []string{"first line", "an INTERNAL-TOKEN here", "clean line"}

	got := ScanExport(BoundarySkillMirror, lines, policy)
	if len(got) != 1 {
		t.Fatalf("ScanExport returned %d findings, want 1: %+v", len(got), got)
	}
	want := LeakFinding{Boundary: BoundarySkillMirror, Line: 2, Kind: LeakTerm, Rule: "internal-token"}
	if got[0] != want {
		t.Fatalf("finding = %+v, want %+v", got[0], want)
	}
}

func TestScanExportHonorsAllowedContextLines(t *testing.T) {
	allowed := "glossary: internal-token marks an internal detail"
	policy := ExportPolicy{Terms: []LeakRule{{Term: "internal-token", AllowedContextLines: []string{"  " + allowed + "  "}}}}
	lines := []string{allowed, "leaked internal-token in a skill"}

	got := ScanExport(BoundaryContextPack, lines, policy)
	if len(got) != 1 {
		t.Fatalf("ScanExport returned %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Line != 2 || got[0].Kind != LeakTerm {
		t.Fatalf("finding = %+v, want a term finding on line 2", got[0])
	}
}

func TestScanExportChecksBoundaries(t *testing.T) {
	boundaries := []ExportBoundary{
		BoundaryContextPack,
		BoundarySkillMirror,
		BoundaryBuildPackage,
		BoundaryReleaseGate,
	}
	for _, b := range boundaries {
		if !b.Known() {
			t.Errorf("%q is not a known boundary", b)
		}
		if got := ScanExport(b, []string{"clean line"}, ExportPolicy{}); len(got) != 0 {
			t.Errorf("clean scan on %q returned %+v", b, got)
		}
	}

	unknown := ExportBoundary("invented-boundary")
	if unknown.Known() {
		t.Fatalf("%q reported as known", unknown)
	}
	got := ScanExport(unknown, []string{"clean line"}, ExportPolicy{})
	want := LeakFinding{Boundary: unknown, Kind: LeakUnknownBoundary, Rule: string(unknown)}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("unknown boundary findings = %+v, want [%+v]", got, want)
	}
}

func TestScanExportBlocksPrivatePathsInSkillsAndBundles(t *testing.T) {
	line := "checkout lives in /home/nbook/Projects/beacon"
	for _, b := range []ExportBoundary{BoundarySkillMirror, BoundaryBuildPackage} {
		got := ScanExport(b, []string{line}, ExportPolicy{})
		if len(got) != 1 {
			t.Fatalf("%s: got %d findings, want 1: %+v", b, len(got), got)
		}
		want := LeakFinding{Boundary: b, Line: 1, Kind: LeakPrivatePath, Rule: "/home/"}
		if got[0] != want {
			t.Fatalf("%s: finding = %+v, want %+v", b, got[0], want)
		}
	}
}

func TestScanExportWithoutTermRulesSkipsTermCheckButKeepsStructuralChecks(t *testing.T) {
	policy := ExportPolicy{}
	lines := []string{"the word internal-token is only here", "key at /root/.ssh/id_rsa"}

	got := ScanExport(BoundarySkillMirror, lines, policy)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	want := LeakFinding{Boundary: BoundarySkillMirror, Line: 2, Kind: LeakPrivatePath, Rule: "/root/"}
	if got[0] != want {
		t.Fatalf("finding = %+v, want %+v", got[0], want)
	}

	unknown := ScanExport(ExportBoundary("unconfigured"), []string{"clean"}, policy)
	if len(unknown) != 1 || unknown[0].Kind != LeakUnknownBoundary {
		t.Fatalf("unknown boundary findings = %+v, want one unknown-boundary finding", unknown)
	}
}
