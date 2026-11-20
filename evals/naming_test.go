//go:build eval

package main

import (
	"strings"
	"testing"
)

func TestValidBeaconSlug(t *testing.T) {
	valid := []string{"ci", "night-shift", "red-verdict-triage", "gate-result", "w01", "a1-b2"}
	for _, s := range valid {
		if !validBeaconSlug(s) {
			t.Errorf("validBeaconSlug(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "Night-Shift", "night_shift", "night--shift", "-night", "night-", "a b", "123"}
	for _, s := range invalid {
		if validBeaconSlug(s) {
			t.Errorf("validBeaconSlug(%q) = true, want false", s)
		}
	}
}

func TestCheckArtifactNameKinds(t *testing.T) {
	cases := []struct {
		name string
		kind artifactKind
		ok   bool
	}{
		{".github/workflows/ci.yml", artifactWorkflow, true},
		{".github/workflows/gates-binary.yml", artifactWorkflow, true},
		{".opencode/skills/night-shift/SKILL.md", artifactKnowledge, true},
		{".opencode/skills/night-shift/references/comment-rules.md", artifactKnowledge, true},
		{"AGENTS.md", artifactRules, true},
		{"docs/rules/conventions.md", artifactRules, true},
		{"README.md", "", false},
		{"aif-action.md", "", false},
	}
	for _, tc := range cases {
		kind, ok := checkArtifactName(tc.name)
		if kind != tc.kind || ok != tc.ok {
			t.Errorf("checkArtifactName(%q) = %q/%t, want %q/%t", tc.name, kind, ok, tc.kind, tc.ok)
		}
	}
}

func TestCheckArtifactNameRejectsArbitraryNames(t *testing.T) {
	bad := []string{
		"",
		"   ",
		"notes.txt",
		"docs/adrs/0001-example.md",
		"internal/app/orchestrator/checkall.go",
		".github/workflows/sub/ci.yml",
		".github/workflows/CI.yml",
		".opencode/skills/Night-Shift/SKILL.md",
		".opencode/skills/night-shift/README.md",
		"/etc/passwd",
		"../.github/workflows/ci.yml",
	}
	for _, name := range bad {
		_, ok := checkArtifactName(name)
		if ok {
			t.Errorf("checkArtifactName(%q) accepted an arbitrary name", name)
		}
	}
}

func TestCheckArtifactNamesReportsMismatch(t *testing.T) {
	cases := []artifactNameCase{
		{".github/workflows/ci.yml", artifactWorkflow, true},
		{"README.md", "", false},
	}
	actual, pass := checkArtifactNames(cases)
	if !pass || actual != "matched=2/2" {
		t.Errorf("checkArtifactNames() = %q/%t, want matched=2/2/true", actual, pass)
	}

	cases[1] = artifactNameCase{"README.md", artifactWorkflow, true}
	actual, pass = checkArtifactNames(cases)
	if pass || !strings.Contains(actual, "README.md") {
		t.Errorf("checkArtifactNames() = %q/%t, want mismatch with the bad name", actual, pass)
	}
}

func TestArtifactEvalsPass(t *testing.T) {
	checks := []struct {
		name string
		run  func() (string, bool)
	}{
		{"N01", evalN01WorkflowArtifactName},
		{"N02", evalN02SkillArtifactName},
		{"N03", evalN03RuleArtifactName},
		{"N04", evalN04ArbitraryNameRejected},
	}
	for _, tc := range checks {
		actual, pass := tc.run()
		if !pass {
			t.Errorf("%s failed: %s", tc.name, actual)
		}
	}
}
