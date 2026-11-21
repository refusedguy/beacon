// Package domain holds the Beacon entities, status enums and port interfaces.
//
// This file models the four OpenRig export boundaries named in T3.344. A
// scanner runs before content crosses one of them. The model keeps the two
// kinds of checks apart: structural checks always run, and the term check runs
// only when a caller supplies local rules. The domain layer stays on the
// standard library so the wake adapter can use it without an infra dependency.
package domain

import "strings"

// ExportBoundary names a surface where internal content can leave Beacon and
// enter a public artifact. The four values follow the OpenRig gates in T3.344.
type ExportBoundary string

const (
	BoundaryContextPack  ExportBoundary = "context-pack"
	BoundarySkillMirror  ExportBoundary = "skill-mirror"
	BoundaryBuildPackage ExportBoundary = "build-package"
	BoundaryReleaseGate  ExportBoundary = "release-substance-gate"
)

// Known reports whether b is one of the four export boundaries. An unknown
// boundary is a finding, not an empty scan: no content may cross a surface
// the scanner does not recognize.
func (b ExportBoundary) Known() bool {
	switch b {
	case BoundaryContextPack, BoundarySkillMirror, BoundaryBuildPackage, BoundaryReleaseGate:
		return true
	default:
		return false
	}
}

// LeakRule is one local term rule. Term matches as a case-insensitive
// substring of a line. AllowedContextLines lists lines where the term may
// appear, for example a glossary entry that has to name the term.
type LeakRule struct {
	Term                string
	AllowedContextLines []string
}

// ExportPolicy configures ScanExport. Terms is the local rule set; an empty
// set skips the term check. PrivatePathPrefixes adds caller-specific paths to
// the structural rules, which run even when Terms is empty.
type ExportPolicy struct {
	Terms               []LeakRule
	PrivatePathPrefixes []string
}

// LeakFindingKind separates the always-on structural checks from the local
// term check, so a caller can tell which rule rejected a line.
type LeakFindingKind string

const (
	LeakUnknownBoundary LeakFindingKind = "unknown-boundary"
	LeakPrivatePath     LeakFindingKind = "private-path"
	LeakTerm            LeakFindingKind = "term"
)

// LeakFinding is one line rejected at an export boundary. Line is one-based;
// it is zero when the boundary itself is unknown. Rule names the matched term
// or path prefix, or the unknown boundary value.
type LeakFinding struct {
	Boundary ExportBoundary
	Line     int
	Kind     LeakFindingKind
	Rule     string
}

// defaultPrivatePathPrefixes are structural rules. They run with an empty
// policy so a private path never reaches a skill or a bundle just because no
// local term rule was configured.
var defaultPrivatePathPrefixes = []string{"/home/", "/root/", "/mnt/projects/", "/Users/"}

// ScanExport checks lines before they cross b. It returns every finding, and
// an empty result means the content may cross the boundary. The structural
// checks run first and always. The term check runs only when policy has at
// least one rule, and a rule's AllowedContextLines exempts exact matches.
func ScanExport(b ExportBoundary, lines []string, policy ExportPolicy) []LeakFinding {
	var findings []LeakFinding
	if !b.Known() {
		findings = append(findings, LeakFinding{Boundary: b, Kind: LeakUnknownBoundary, Rule: string(b)})
	}

	seen := make(map[LeakFinding]struct{})
	add := func(f LeakFinding) {
		if _, ok := seen[f]; ok {
			return
		}
		seen[f] = struct{}{}
		findings = append(findings, f)
	}

	prefixes := append([]string{}, defaultPrivatePathPrefixes...)
	for _, extra := range policy.PrivatePathPrefixes {
		if extra != "" {
			prefixes = append(prefixes, extra)
		}
	}

	for i, line := range lines {
		lineNo := i + 1
		for _, prefix := range prefixes {
			if strings.Contains(line, prefix) {
				add(LeakFinding{Boundary: b, Line: lineNo, Kind: LeakPrivatePath, Rule: prefix})
			}
		}
		if len(policy.Terms) == 0 {
			continue
		}
		lower := strings.ToLower(line)
		for _, rule := range policy.Terms {
			if rule.Term == "" || !strings.Contains(lower, strings.ToLower(rule.Term)) {
				continue
			}
			if allowedContext(line, rule.AllowedContextLines) {
				continue
			}
			add(LeakFinding{Boundary: b, Line: lineNo, Kind: LeakTerm, Rule: rule.Term})
		}
	}
	return findings
}

// allowedContext compares trimmed lines so an indented glossary entry still
// counts as an allowed line.
func allowedContext(line string, allowed []string) bool {
	trimmed := strings.TrimSpace(line)
	for _, candidate := range allowed {
		if trimmed == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}
