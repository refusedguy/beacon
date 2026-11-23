// Package orchestrator implements the automatic merge loop over active Beacon
// Watches. The core is pure over five narrow ports: Store reads active
// Watches, remembers completed merges and closes Watches; Merger performs the
// hosting-service merge; DependencyResolver resolves free-form waitFor and
// waitForMain references; Waker leaves a retry/fail/wait Watch for its
// subscriber; Source supplies the file list a rules check reads, from either a
// PR diff or a git ref. Concrete adapters live in this package and in cmd/beacon.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	pathpkg "path"
	"strings"
	"sync"
	"time"

	// ARDH-15: observe is the documented cross-cutting exception. Metrics are
	// emitted at the decision sites; a port through every call would add noise
	// without moving an adapter.
	"github.com/refusedguy/beacon/internal/infra/ghci"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// The verdict strings below mirror internal/domain/types.go. ADR 0005 forbids
// app from importing domain, so a test in this package checks the values
// against domain.Verdicts().
const (
	// VerdictReady is the only verdict that permits a merge.
	VerdictReady = "ready"
	// VerdictWait is the in-process hold decision when a dependency is not
	// satisfied. It carries a reason in the Action recorded for the Watch.
	VerdictWait = "wait"
	// VerdictWaitDependency is the persisted hold status. It is written to the
	// Watch so beacon_list_running and beacon inspect can show why a merge is
	// held, and so the next tick re-checks the dependency instead of dropping
	// the Watch. The reason follows the colon in the persisted verdict.
	VerdictWaitDependency = "wait_dependency"
	// VerdictRetry and VerdictFail are left for the subscriber's wake. Wake
	// delivery belongs to another card. VerdictFail is an orchestrator-internal
	// decision, not a domain.Verdict: the bridge never writes it to the store,
	// so verdict_published_total never carries a fail label.
	VerdictRetry = "retry"
	VerdictFail  = "fail"
)

// OrchestratorVerdicts returns the verdict strings the merge loop can emit.
func OrchestratorVerdicts() []string {
	return []string{VerdictReady, VerdictWait, VerdictWaitDependency, VerdictRetry, VerdictFail}
}

// Action kinds recorded in Result.Actions.
const (
	ActionMerge       = "merge"
	ActionSkip        = "skip"
	ActionDryRun      = "dry-run"
	ActionNeedsRebase = "needs-rebase"
	ActionWake        = "wake"
	ActionWait        = "wait"
	// ActionBlockedFixAgent records one FIX agent request for a blocked Watch.
	ActionBlockedFixAgent = "blocked-fix-agent"
	// ActionBlockEscalated records a blocked Watch that exhausted its FIX budget.
	ActionBlockEscalated = "block-escalated"
	// ActionApprovalFirst is the approval request for the original attempt,
	// which runs with a narrow set of extra permissions.
	ActionApprovalFirst = "approval-first"
	// ActionApprovalRetry is the approval request for the retry after a
	// sandbox denial. It runs escalated and carries no extra permissions.
	ActionApprovalRetry = "approval-retry"
	// ActionSandboxDenied records one operation refused by the selected
	// sandbox mode. The Message carries the deciding rule (CCx-06).
	ActionSandboxDenied = "sandbox-denied"
)

// AutoMergePolicy is the autor-merge policy of one Watch. AutoMerge is a
// pointer so the absent value means "default true": a Watch with no declared
// policy is mergeable. Any of the three hold-backs disables automatic merging.
//
// Allows is the conservative check without a resolver: a non-empty waitFor or
// waitForMain blocks there. The resolver-aware decision is Watch.canAutoMerge,
// which allows the merge once every reference is actually satisfied.
type AutoMergePolicy struct {
	AutoMerge       *bool    `json:"autoMerge,omitempty"`
	RequireApproval bool     `json:"requireApproval"`
	WaitFor         []string `json:"waitFor,omitempty"`
	WaitForMain     []string `json:"waitForMain,omitempty"`
	// Approval is the declarative approval policy. It is optional. A nil value
	// keeps the legacy RequireApproval bool as the only merge hold-back, so the
	// MCP wire contract and existing callers are unchanged. When set, it takes
	// precedence over the bool and governs all five approval categories.
	Approval *ApprovalSpec `json:"approval,omitempty"`
	// Sandbox selects the execution restriction for the run. It is optional so
	// existing config files and the MCP wire contract stay unchanged. A nil
	// value means unrestricted access (CCx-06).
	Sandbox *SandboxPolicy `json:"sandbox,omitempty"`
}

// DefaultAutoMergePolicy is the zero-configuration policy: automatic merging
// is on and no hold-back is set.
func DefaultAutoMergePolicy() AutoMergePolicy {
	return AutoMergePolicy{}
}

// Allows reports whether this policy permits an automatic merge without
// resolving dependencies. It stays conservative so a caller without a
// DependencyResolver can never merge a Watch with unresolved references.
func (p AutoMergePolicy) Allows() bool {
	if p.AutoMerge != nil && !*p.AutoMerge {
		return false
	}
	return !p.approvalRequired(ApprovalMerge) && len(p.WaitFor) == 0 && len(p.WaitForMain) == 0
}

// ---- CCx-06: sandbox modes ----
//
// Codex keeps execution restrictions in one SandboxPolicy with four variants
// (codex-rs/protocol/src/protocol.rs:1293-1341). Beacon had no such type:
// AutoMergePolicy decides only merges and git clone --reference gives repo
// isolation without an access mode. These types name the four modes and tie
// each one to a write decision and a network decision. They live in app, so
// they do not import domain and do not grow the MCP tool set.

// sandboxMergeTarget is the repo-relative ref a merge writes. A merge is a
// write operation even though it does not touch the working tree, so the
// sandbox write check runs on this path before the merger is called.
const sandboxMergeTarget = "git/refs/heads"

// SandboxMode is one named execution restriction. A run selects a mode in its
// startup config; the mode then decides write and network access.
type SandboxMode string

const (
	// SandboxDangerFullAccess allows every write and every network request.
	SandboxDangerFullAccess SandboxMode = "danger-full-access"
	// SandboxReadOnly denies every write and every network request.
	SandboxReadOnly SandboxMode = "read-only"
	// SandboxExternal hands execution to an external sandbox. Beacon itself
	// performs no direct write and no direct network request.
	SandboxExternal SandboxMode = "external-sandbox"
	// SandboxWorkspaceWrite allows writes inside the workspace roots. Network
	// access is off unless the policy overrides it.
	SandboxWorkspaceWrite SandboxMode = "workspace-write"
)

// SandboxModes returns the four modes in a stable order.
func SandboxModes() []SandboxMode {
	return []SandboxMode{
		SandboxDangerFullAccess, SandboxReadOnly, SandboxExternal, SandboxWorkspaceWrite,
	}
}

// ValidateSandboxMode rejects a name outside the four modes. An unknown mode
// must fail at config load instead of falling through to full access.
func ValidateSandboxMode(mode SandboxMode) error {
	for _, known := range SandboxModes() {
		if mode == known {
			return nil
		}
	}
	return fmt.Errorf("orchestrator: unknown sandbox mode %q", string(mode))
}

// UnmarshalJSON validates the mode while the startup config is parsed, so a
// typo cannot silently become an unrestricted run.
func (m *SandboxMode) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("orchestrator: sandbox mode: %w", err)
	}
	mode := SandboxMode(strings.TrimSpace(name))
	if err := ValidateSandboxMode(mode); err != nil {
		return err
	}
	*m = mode
	return nil
}

// SandboxPolicy is the configured restriction for one run. Mode selects the
// behaviour. WritableRoots narrows workspace-write; an empty list means the
// whole workspace. NetworkAccess overrides the mode default only when set.
// ReadOnlySubpaths and ProtectedNames stay denied even inside a writable root.
type SandboxPolicy struct {
	Mode             SandboxMode `json:"mode"`
	WritableRoots    []string    `json:"writableRoots,omitempty"`
	NetworkAccess    *bool       `json:"networkAccess,omitempty"`
	ReadOnlySubpaths []string    `json:"readOnlySubpaths,omitempty"`
	ProtectedNames   []string    `json:"protectedNames,omitempty"`
}

// DefaultSandboxPolicy keeps the historical behaviour of runs that declare no
// sandbox. A missing config must not change any existing verdict.
func DefaultSandboxPolicy() SandboxPolicy {
	return SandboxPolicy{Mode: SandboxDangerFullAccess}
}

// DefaultProtectedNames are service directory names denied in every mode.
// Codex calls these protected_metadata_names and uses them to stop a write
// from replacing its own control files (protocol.rs:1343-1359).
func DefaultProtectedNames() []string {
	return []string{".codex", ".git"}
}

// DefaultReadOnlySubpaths are repo-relative prefixes denied in every mode,
// even when a writable root contains them. Codex calls these
// read_only_subpaths (protocol.rs:1343-1359).
func DefaultReadOnlySubpaths() []string {
	return []string{".git/hooks"}
}

func (p SandboxPolicy) protectedNames() []string {
	if len(p.ProtectedNames) > 0 {
		return p.ProtectedNames
	}
	return DefaultProtectedNames()
}

func (p SandboxPolicy) readOnlySubpaths() []string {
	if len(p.ReadOnlySubpaths) > 0 {
		return p.ReadOnlySubpaths
	}
	return DefaultReadOnlySubpaths()
}

// protectedPathReason returns the deciding rule when clean must stay
// read-only. A protected name matches any path component, so a nested path
// such as .git/hooks/x cannot reach a hook through a writable root.
func (p SandboxPolicy) protectedPathReason(clean string) string {
	for _, name := range p.protectedNames() {
		for _, component := range strings.Split(clean, "/") {
			if component == name {
				return "protected path component: " + name
			}
		}
	}
	for _, sub := range p.readOnlySubpaths() {
		cleanSub, err := cleanWorkspacePath(sub)
		if err != nil || cleanSub == "." {
			continue
		}
		if clean == cleanSub || strings.HasPrefix(clean, cleanSub+"/") {
			return "read-only subpath: " + cleanSub
		}
	}
	return ""
}

// Validate checks the mode and every writable root. A root must be a clean
// relative path inside the workspace, so an escape cannot widen the mode.
func (p SandboxPolicy) Validate() error {
	if err := ValidateSandboxMode(p.Mode); err != nil {
		return err
	}
	for _, root := range p.WritableRoots {
		clean, err := cleanWorkspacePath(root)
		if err != nil {
			return fmt.Errorf("orchestrator: sandbox writable root %q: %w", root, err)
		}
		if clean == "." {
			return fmt.Errorf("orchestrator: sandbox writable root %q: use an empty list for the whole workspace", root)
		}
	}
	for _, name := range p.ProtectedNames {
		clean, err := cleanWorkspacePath(name)
		if err != nil || clean != name || strings.Contains(clean, "/") {
			return fmt.Errorf("orchestrator: sandbox protected name %q: must be a single path component", name)
		}
	}
	for _, sub := range p.ReadOnlySubpaths {
		clean, err := cleanWorkspacePath(sub)
		if err != nil {
			return fmt.Errorf("orchestrator: sandbox read-only subpath %q: %w", sub, err)
		}
		if clean == "." {
			return fmt.Errorf("orchestrator: sandbox read-only subpath %q: use an empty list for none", sub)
		}
	}
	return nil
}

// SandboxCheck is one access decision. Reason names the rule that decided it,
// so a denial can be reported without guessing which flag won.
type SandboxCheck struct {
	Operation string      `json:"operation"`
	Path      string      `json:"path,omitempty"`
	Mode      SandboxMode `json:"mode"`
	Allowed   bool        `json:"allowed"`
	Reason    string      `json:"reason"`
}

// CheckWrite decides whether path may be written under this policy. An
// absolute path, any path escaping the workspace and any protected path are
// denied in every mode, including danger-full-access, because Beacon records
// repo-relative paths and must not let a write replace its own control files.
func (p SandboxPolicy) CheckWrite(path string) SandboxCheck {
	check := SandboxCheck{Operation: "write", Path: path, Mode: p.Mode}
	clean, pathErr := cleanWorkspacePath(path)
	if pathErr != nil {
		check.Reason = "invalid path: " + pathErr.Error()
		return check
	}
	if reason := p.protectedPathReason(clean); reason != "" {
		check.Reason = reason
		return check
	}
	switch p.Mode {
	case SandboxDangerFullAccess:
		check.Allowed, check.Reason = true, "mode: danger-full-access"
	case SandboxReadOnly, SandboxExternal:
		check.Reason = "mode: " + string(p.Mode) + " denies writes"
	case SandboxWorkspaceWrite:
		if len(p.WritableRoots) == 0 {
			check.Allowed, check.Reason = true, "whole workspace"
			break
		}
		for _, root := range p.WritableRoots {
			cleanRoot, err := cleanWorkspacePath(root)
			if err != nil || cleanRoot == "." {
				continue
			}
			if clean == cleanRoot || strings.HasPrefix(clean, cleanRoot+"/") {
				check.Allowed = true
				check.Reason = "writable root: " + cleanRoot
				break
			}
		}
		if !check.Allowed {
			check.Reason = "path outside writable roots"
		}
	default:
		check.Reason = "unknown sandbox mode: " + string(p.Mode)
	}
	return check
}

// CheckNetwork decides whether a network request may be made under this
// policy. Only danger-full-access allows network by default; an explicit
// NetworkAccess overrides the mode for the other three.
func (p SandboxPolicy) CheckNetwork() SandboxCheck {
	check := SandboxCheck{Operation: "network", Mode: p.Mode}
	if ValidateSandboxMode(p.Mode) != nil {
		check.Reason = "unknown sandbox mode: " + string(p.Mode)
		return check
	}
	check.Allowed = p.Mode == SandboxDangerFullAccess
	check.Reason = "mode: " + string(p.Mode)
	if check.Allowed {
		check.Reason += " allows network"
	} else {
		check.Reason += " denies network"
	}
	if p.NetworkAccess != nil {
		check.Allowed = *p.NetworkAccess
		if check.Allowed {
			check.Reason = "networkAccess override allows network"
		} else {
			check.Reason = "networkAccess override denies network"
		}
	}
	return check
}

// ErrSandboxDenied marks a refused write or network request. Callers can use
// errors.Is to tell a sandbox refusal from a transport or merge error.
var ErrSandboxDenied = errors.New("orchestrator: sandbox denied")

// AuthorizeWrite returns nil when path may be written and wraps
// ErrSandboxDenied with the deciding reason otherwise.
func (p SandboxPolicy) AuthorizeWrite(path string) error {
	check := p.CheckWrite(path)
	if check.Allowed {
		return nil
	}
	return fmt.Errorf("%w: write %q: %s", ErrSandboxDenied, path, check.Reason)
}

// AuthorizeNetwork returns nil when a network request may be made and wraps
// ErrSandboxDenied with the deciding reason otherwise.
func (p SandboxPolicy) AuthorizeNetwork() error {
	check := p.CheckNetwork()
	if check.Allowed {
		return nil
	}
	return fmt.Errorf("%w: network: %s", ErrSandboxDenied, check.Reason)
}

func cleanWorkspacePath(path string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	if p == "" {
		return "", errors.New("empty path")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("absolute path %q", path)
	}
	clean := pathpkg.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes workspace", path)
	}
	return clean, nil
}

// ---- CCx-14: read-only from the first shell call after resume ----
//
// Codex keeps a resumed session read-only until the human approves the first
// write attempt (codex-rs/exec/tests/suite/resume.rs:746-824). Beacon kept no
// resume metadata and tied resume to no sandbox mode, so a resumed executor
// could write on its first shell call. ResumedSession pins the mode to
// read-only, applies it before each shell call, and lifts it only after
// approval. Sessions started without a resume never use ResumeSession, so
// their behaviour does not change.

// ErrReadOnlyResume marks a write refused because a resumed session has not
// been approved. It wraps ErrSandboxDenied so a caller can classify the refusal
// either as a resume gate or as a sandbox denial.
var ErrReadOnlyResume = fmt.Errorf("%w: resumed session is read-only until approval", ErrSandboxDenied)

// ResumedSession is the metadata Beacon keeps for one resumed session and the
// gate applied to its re-execution. Mode is read-only until Approve is called.
// The denials and writes counters are runtime diagnostics, not serialized
// metadata.
type ResumedSession struct {
	ID       string      `json:"id"`
	Mode     SandboxMode `json:"mode"`
	Approved bool        `json:"approved"`
	denials  int
	writes   int
}

// ResumeSession builds the metadata for a resumed session. The mode is pinned
// to read-only whatever restriction the earlier run used, so the first shell
// call of the re-execution cannot write to disk.
func ResumeSession(id string) *ResumedSession {
	return &ResumedSession{ID: id, Mode: SandboxReadOnly}
}

// ReadOnly reports whether the re-execution still refuses writes.
func (s *ResumedSession) ReadOnly() bool {
	return s.Mode == SandboxReadOnly && !s.Approved
}

// Sandbox returns the restriction applied before each shell call. Approval
// lifts the read-only window. Protected paths stay denied in both modes.
func (s *ResumedSession) Sandbox() SandboxPolicy {
	if s.Approved {
		return SandboxPolicy{Mode: SandboxDangerFullAccess}
	}
	return SandboxPolicy{Mode: s.Mode}
}

// Approve records human approval on the session metadata and lifts the
// read-only window. Writes in later shell calls are allowed.
func (s *ResumedSession) Approve() {
	s.Approved = true
}

// BeforeShellCall applies the resumed session's mode before a shell call
// writes path. While the window is active it returns ErrReadOnlyResume and
// counts one denial without writing. After approval a passing check counts one
// write. SandboxPolicy.CheckWrite still denies protected paths after approval.
func (s *ResumedSession) BeforeShellCall(path string) error {
	check := s.Sandbox().CheckWrite(path)
	if check.Allowed {
		s.writes++
		return nil
	}
	if s.ReadOnly() {
		s.denials++
		return fmt.Errorf("%w: write %q: %s", ErrReadOnlyResume, path, check.Reason)
	}
	return fmt.Errorf("%w: write %q: %s", ErrSandboxDenied, path, check.Reason)
}

// Denials returns the number of writes refused in the read-only window.
func (s *ResumedSession) Denials() int { return s.denials }

// Writes returns the number of writes that passed the gate.
func (s *ResumedSession) Writes() int { return s.writes }

// ---- CC-03: declarative approval policy ----
//
// Beacon replaces the single approval bool with five categories, a small
// declarative rule language with checked examples, and an approval cache keyed
// by (environment, category, path). The legacy RequireApproval bool remains on
// the wire for compatibility. The Approval field is the opt-in declarative
// replacement. Nothing here imports domain or grows the MCP tool set.

// ApprovalCategory names one independent approval decision. Codex keeps five
// flags in GranularApprovalConfig; Beacon keeps the five decisions it actually
// makes. Merge holds an auto-merge, action holds an executor step, rules holds
// an edit to policy files, worktree holds a checkout bind and escalation holds
// a ping to a human.
type ApprovalCategory string

const (
	ApprovalMerge          ApprovalCategory = "merge"
	ApprovalCategoryAction ApprovalCategory = "action"
	ApprovalRules          ApprovalCategory = "rules"
	ApprovalWorktree       ApprovalCategory = "worktree"
	ApprovalEscalation     ApprovalCategory = "escalation"
)

// ApprovalCategories returns all categories in a stable order.
func ApprovalCategories() []ApprovalCategory {
	return []ApprovalCategory{
		ApprovalMerge, ApprovalCategoryAction, ApprovalRules, ApprovalWorktree, ApprovalEscalation,
	}
}

func (c ApprovalCategory) known() bool {
	for _, all := range ApprovalCategories() {
		if c == all {
			return true
		}
	}
	return false
}

// RuleExample is one asserted outcome attached to a prefix rule. Allow true
// means the path is auto-approved; false means it needs approval.
type RuleExample struct {
	Path  string `json:"path"`
	Allow bool   `json:"allow"`
}

// PrefixRule is the Beacon analogue of Codex prefix_rule. A path is governed
// when it starts with one of Match and with none of NotMatch. A governed path
// is auto-approved. Source records the file and line that declared the rule,
// so a failed example points at the text to fix.
type PrefixRule struct {
	Category ApprovalCategory `json:"category"`
	Match    []string         `json:"match"`
	NotMatch []string         `json:"notMatch,omitempty"`
	Examples []RuleExample    `json:"examples,omitempty"`
	Source   string           `json:"source"`
}

// ApprovalPolicy is a parsed rule document. ParseApprovalPolicy validates every
// example before it returns a policy.
type ApprovalPolicy struct {
	Rules []PrefixRule `json:"rules"`
}

// ApprovalSpec is the opt-in declarative replacement for RequireApproval.
// Categories maps a category to whether it requires approval. An absent key
// falls back to RequireApproval for merge and to false for the other
// categories. PolicyText overrides DefaultApprovalPolicyText when set.
type ApprovalSpec struct {
	Categories    map[ApprovalCategory]bool `json:"categories,omitempty"`
	PolicyText    string                    `json:"policyText,omitempty"`
	EnvironmentID string                    `json:"environmentID,omitempty"`
}

// DefaultApprovalPolicyText is the embedded rule document. It lives here
// rather than in a new file so the change stays inside the card's OwnerFiles.
// The parser rejects the document at load time if any example disagrees with
// the rules it belongs to.
const DefaultApprovalPolicyText = `# Beacon declarative approval policy.
# category <name>: merge, action, rules, worktree, escalation.
# rule <match-prefix> [!not-match-prefix]...: a governed path is auto-approved.
# example <path> allow|deny: asserted at load; a bad example rejects the policy.

category merge
rule docs/tasks/,docs/evals/,evals/reports/
example docs/tasks/CC-03.md allow
example docs/evals/w20-policy-pinned.md allow
example evals/reports/2026/report.md allow
example internal/app/orchestrator/orchestrator.go deny

category action
rule cmd/beacon/,internal/app/orchestrator/
example cmd/beacon/orchestrate.go allow
example internal/app/orchestrator/decision.go allow
example internal/infra/mcp/tool_watch_pr.go deny

category rules
rule .beacon/config.json,.beacon/config.yaml
example .beacon/config.json allow
example .beacon/config.yaml allow
example internal/app/orchestrator/orchestrator.go deny

category worktree
rule .git/worktrees/
example .git/worktrees/cc-03-approval allow
example docs/evals/w20-policy-pinned.md deny

category escalation
rule docs/tasks/,evals/reports/
example docs/tasks/AUD-01.md allow
example evals/reports/2026/report.md allow
example cmd/beacon/orchestrate.go deny
`

// ParseApprovalPolicy parses a declarative policy and validates its examples.
// A malformed directive or a wrong example returns an error and no policy, so
// a bad document cannot reach a live merge.
func ParseApprovalPolicy(text string) (ApprovalPolicy, error) {
	policy := ApprovalPolicy{Rules: []PrefixRule{}}
	current := ApprovalCategory("")
	last := -1
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "category":
			if len(fields) != 2 {
				return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: category needs one name", lineNo)
			}
			cat := ApprovalCategory(strings.ToLower(fields[1]))
			if !cat.known() {
				return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: unknown category %q", lineNo, fields[1])
			}
			current = cat
			last = -1
		case "rule":
			if current == "" {
				return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: rule before category", lineNo)
			}
			if len(fields) < 2 {
				return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: rule needs a match prefix", lineNo)
			}
			rule := PrefixRule{Category: current, Source: fmt.Sprintf("policy:%d", lineNo), Examples: []RuleExample{}}
			for _, token := range fields[1:] {
				if strings.HasPrefix(token, "!") {
					prefix := strings.TrimSpace(strings.TrimPrefix(token, "!"))
					if prefix == "" {
						return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: empty not-match prefix", lineNo)
					}
					rule.NotMatch = append(rule.NotMatch, prefix)
					continue
				}
				for _, prefix := range strings.Split(token, ",") {
					if prefix = strings.TrimSpace(prefix); prefix != "" {
						rule.Match = append(rule.Match, prefix)
					}
				}
			}
			if len(rule.Match) == 0 {
				return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: rule needs a match prefix", lineNo)
			}
			policy.Rules = append(policy.Rules, rule)
			last = len(policy.Rules) - 1
		case "example":
			if last < 0 {
				return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: example before rule", lineNo)
			}
			if len(fields) != 3 {
				return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: example needs a path and allow or deny", lineNo)
			}
			var allow bool
			switch strings.ToLower(fields[2]) {
			case "allow":
				allow = true
			case "deny":
				allow = false
			default:
				return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: example outcome %q must be allow or deny", lineNo, fields[2])
			}
			policy.Rules[last].Examples = append(policy.Rules[last].Examples, RuleExample{Path: fields[1], Allow: allow})
		default:
			return ApprovalPolicy{}, fmt.Errorf("orchestrator: approval policy line %d: unknown directive %q", lineNo, fields[0])
		}
	}
	if err := ValidateApprovalExamples(policy); err != nil {
		return ApprovalPolicy{}, err
	}
	return policy, nil
}

// ValidateApprovalExamples runs every example through its own rule. It returns
// the first disagreement with the rule's Source and path, so a wrong example
// can be fixed at the line that declares it.
func ValidateApprovalExamples(policy ApprovalPolicy) error {
	for _, rule := range policy.Rules {
		for _, example := range rule.Examples {
			got := ruleAllows(rule, example.Path)
			if got != example.Allow {
				return fmt.Errorf("orchestrator: approval example %q in %s: got allow=%v, want %v",
					example.Path, rule.Source, got, example.Allow)
			}
		}
	}
	return nil
}

// Approve reports whether the policy auto-approves path for category. No rule
// matches means approval is required. The environment is part of the cache key
// and the audit record; rule matching itself is path based.
func Approve(policy ApprovalPolicy, category ApprovalCategory, environmentID, path string) bool {
	for _, rule := range policy.Rules {
		if rule.Category == category && ruleAllows(rule, path) {
			return true
		}
	}
	return false
}

func ruleAllows(rule PrefixRule, p string) bool {
	p = normalizeApprovalPath(p)
	matched := false
	for _, prefix := range rule.Match {
		if hasApprovalPrefix(p, normalizeApprovalPath(prefix)) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, prefix := range rule.NotMatch {
		if hasApprovalPrefix(p, normalizeApprovalPath(prefix)) {
			return false
		}
	}
	return true
}

func normalizeApprovalPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	return strings.TrimSuffix(p, "/")
}

func hasApprovalPrefix(p, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return true
	}
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// ApprovalKey identifies one approval record. Environment and category keep a
// path decision in one config from leaking into another config or category.
type ApprovalKey struct {
	EnvironmentID string
	Category      ApprovalCategory
	Path          string
}

// String returns a collision-safe key. NUL cannot occur in the fields, so no
// pair of distinct keys can share a string.
func (k ApprovalKey) String() string {
	return k.EnvironmentID + "\x00" + string(k.Category) + "\x00" + k.Path
}

// maxApprovalCache bounds the number of cached approval decisions. A decision
// older than this window is re-evaluated by Resolve. Eviction drops the oldest
// insertion first, so a key inside the window keeps its (environment, category,
// path) decision.
const maxApprovalCache = 4096

// ApprovalCache stores approval decisions by (environment, category, path).
// It is the Beacon analogue of Codex's patch approval cache key. One mutex
// guards the map because one cache is shared by concurrent Watches.
type ApprovalCache struct {
	mu      sync.Mutex
	allowed map[string]bool
	order   []string // keys in insertion order, for oldest-first eviction
}

// Lookup returns the cached decision and whether the key was present.
func (c *ApprovalCache) Lookup(key ApprovalKey) (allowed bool, ok bool) {
	if c == nil {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	allowed, ok = c.allowed[key.String()]
	return allowed, ok
}

// forgetApprovalLocked removes a key from the eviction order. The caller holds
// c.mu.
func (c *ApprovalCache) forgetApprovalLocked(key string) {
	for i, k := range c.order {
		if k == key {
			copy(c.order[i:], c.order[i+1:])
			c.order[len(c.order)-1] = ""
			c.order = c.order[:len(c.order)-1]
			return
		}
	}
}

// Remember stores one decision under key. A repeat overwrites the decision
// without moving the key in the eviction order, so re-approving one path cannot
// push other paths out of the window early.
func (c *ApprovalCache) Remember(key ApprovalKey, allowed bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.allowed == nil {
		c.allowed = map[string]bool{}
	}
	k := key.String()
	if _, ok := c.allowed[k]; !ok {
		if len(c.allowed) >= maxApprovalCache {
			oldest := c.order[0]
			delete(c.allowed, oldest)
			c.forgetApprovalLocked(oldest)
		}
		c.order = append(c.order, k)
	}
	c.allowed[k] = allowed
}

// Len reports the number of stored decisions.
func (c *ApprovalCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.allowed)
}

// Resolve returns a cached decision when present, otherwise it evaluates the
// policy and remembers the result. Two different paths produce two records.
func (c *ApprovalCache) Resolve(policy ApprovalPolicy, category ApprovalCategory, environmentID, path string) bool {
	key := ApprovalKey{EnvironmentID: environmentID, Category: category, Path: path}
	if allowed, ok := c.Lookup(key); ok {
		return allowed
	}
	allowed := Approve(policy, category, environmentID, path)
	c.Remember(key, allowed)
	return allowed
}

func (p *AutoMergePolicy) approvalRequired(category ApprovalCategory) bool {
	if p == nil {
		return false
	}
	if p.Approval != nil {
		if value, ok := p.Approval.Categories[category]; ok {
			return value
		}
		if category == ApprovalMerge {
			return p.RequireApproval
		}
		return false
	}
	return category == ApprovalMerge && p.RequireApproval
}

func (p *AutoMergePolicy) approvalEnvironment() string {
	if p != nil && p.Approval != nil {
		return p.Approval.EnvironmentID
	}
	return ""
}

func (p *AutoMergePolicy) approvalPolicy() (ApprovalPolicy, error) {
	text := DefaultApprovalPolicyText
	if p != nil && p.Approval != nil && strings.TrimSpace(p.Approval.PolicyText) != "" {
		text = p.Approval.PolicyText
	}
	return ParseApprovalPolicy(text)
}

// MergeApprovalAllowed applies the declarative policy to a merge. The legacy
// bool path always requires approval when set. The declarative path parses the
// document, validates its examples, and requires approval for any changed path
// the policy does not auto-approve. An empty change list fails closed.
func MergeApprovalAllowed(p *AutoMergePolicy, environmentID string, changedFiles []string) (bool, error) {
	if p == nil || !p.approvalRequired(ApprovalMerge) {
		return true, nil
	}
	if p.Approval == nil {
		return false, nil
	}
	policy, err := p.approvalPolicy()
	if err != nil {
		return false, err
	}
	if len(changedFiles) == 0 {
		return false, nil
	}
	for _, file := range changedFiles {
		if !Approve(policy, ApprovalMerge, environmentID, file) {
			return false, nil
		}
	}
	return true, nil
}

func approvalHoldReason(p *AutoMergePolicy) string {
	if p != nil && p.Approval != nil {
		return "require_approval_merge"
	}
	return "require_approval"
}

// DependencyResolver answers whether one free-form dependency reference is
// satisfied. A ref may name a task card (T3.123), a pull request (#12, PR#12,
// pr/12) or a file/card path that has to exist on main.
//
// An implementation must return an error instead of true when the fact cannot
// be verified. An unverifiable dependency must never allow a merge.
type DependencyResolver interface {
	Satisfied(ctx context.Context, ref string) (bool, error)
}

// Watch is the subset of a Beacon Watch the loop decides on. A nil Policy
// means the default autor-merge policy applies.
type Watch struct {
	ID       string
	Repo     string
	PR       int
	HeadSHA  string
	Verdict  string
	Revision uint64
	Policy   *AutoMergePolicy
	// Blocked carries the reason the executor blocked this Watch. Decide reads
	// it first so a stale ready verdict can never turn into a merge.
	Blocked *BlockedInfo
	// ChangedFiles lists repo-relative paths touched by the PR diff. The gate
	// adapter fills it from the PR; Decide checks it against
	// DefaultGeneratedFilePatterns so a hand-edited generated artifact can
	// never reach the merge path. A nil or empty list means no diff is known.
	ChangedFiles []string
	// Source supplies the file list the rules check reads. A nil Source falls
	// back to ChangedFiles, so callers that only know the PR diff keep working.
	// Set Source to a RefSource to check a git ref without a working copy
	// (ADR 0304).
	Source Source
}

// SandboxPolicy resolves the effective sandbox restriction for this Watch.
// A Watch without a declared policy keeps the unrestricted default.
func (w Watch) SandboxPolicy() SandboxPolicy {
	if w.Policy == nil || w.Policy.Sandbox == nil {
		return DefaultSandboxPolicy()
	}
	return *w.Policy.Sandbox
}

func (w Watch) autoMergeAllowed() bool {
	if w.Policy == nil {
		return DefaultAutoMergePolicy().Allows()
	}
	return w.Policy.Allows()
}

func (w Watch) isReady() bool {
	return strings.EqualFold(strings.TrimSpace(w.Verdict), VerdictReady)
}

// canAutoMerge is the resolver-aware merge gate. It returns VerdictReady when
// the merge is allowed, VerdictWait with a reason when a dependency holds it,
// and an empty verdict for an explicit static hold-back (autoMerge=false or
// requireApproval), which needs no wake. A resolver error never turns into a
// ready verdict: it is returned to the caller and the Watch stays put.
func (w Watch) canAutoMerge(ctx context.Context, resolver DependencyResolver) (verdict string, reason string, err error) {
	policy := DefaultAutoMergePolicy()
	if w.Policy != nil {
		policy = *w.Policy
	}
	if policy.AutoMerge != nil && !*policy.AutoMerge {
		return "", "auto_merge_disabled", nil
	}
	if policy.approvalRequired(ApprovalMerge) {
		allowed, err := MergeApprovalAllowed(&policy, policy.approvalEnvironment(), w.ChangedFiles)
		if err != nil {
			return "", "approval_policy_invalid", fmt.Errorf("orchestrator: approval policy: %w", err)
		}
		if !allowed {
			return "", approvalHoldReason(&policy), nil
		}
	}
	if len(policy.WaitFor) == 0 && len(policy.WaitForMain) == 0 {
		return VerdictReady, "", nil
	}
	if resolver == nil {
		return VerdictWait, "resolver_unavailable", nil
	}
	for _, ref := range policy.WaitFor {
		ok, err := resolver.Satisfied(ctx, ref)
		if err != nil {
			return VerdictWait, "resolve:" + ref, fmt.Errorf("orchestrator: resolve waitFor %q: %w", ref, err)
		}
		if !ok {
			return VerdictWait, "wait_for:" + ref, nil
		}
	}
	for _, ref := range policy.WaitForMain {
		ok, err := resolver.Satisfied(ctx, ref)
		if err != nil {
			return VerdictWait, "resolve_main:" + ref, fmt.Errorf("orchestrator: resolve waitForMain %q: %w", ref, err)
		}
		if !ok {
			return VerdictWait, "wait_for_main:" + ref, nil
		}
	}
	return VerdictReady, "", nil
}

// WaitDependencyVerdict builds the persisted status for one unsatisfied ref.
// Keeping the reason in the verdict makes it visible in the diagnostics that
// read Watch rows directly, without changing their projection.
func WaitDependencyVerdict(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return VerdictWaitDependency
	}
	return VerdictWaitDependency + ":" + reason
}

// isWaitDependency reports whether a persisted verdict is a dependency hold.
// Such a Watch is re-checked on the next pass: when the ref lands in main the
// same path that held the merge now merges it.
func isWaitDependency(verdict string) bool {
	v := strings.ToLower(strings.TrimSpace(verdict))
	return v == VerdictWaitDependency || strings.HasPrefix(v, VerdictWaitDependency+":")
}

// needsWake reports whether the verdict is one a subscriber must act on. The
// orchestrator does not fix these Watches; it only reports their PRs and, when
// a Waker is installed, hands the Watch over.
func needsWake(verdict string) bool {
	v := strings.ToLower(strings.TrimSpace(verdict))
	return v == VerdictRetry || v == VerdictFail
}

// Merger merges one pull request. repo names the owning repository; an
// adapter may use it to select the host but is not required to (the gh CLI
// infers the repository from the working directory).
type Merger interface {
	Merge(ctx context.Context, repo string, pr int) error
}

// MergeOutcome is the result of a checked merge. Merged is true only after the
// hosting service confirmed state=MERGED, so it is the sole permission to write
// the ledger and close the Watch. Pending means the merge command succeeded but
// the state is not observable yet: the Watch stays active and no ledger row is
// written. Reason is a short diagnostic for a PR the gate refused to merge.
type MergeOutcome struct {
	Merged  bool
	Pending bool
	Reason  string
}

// CheckedMerger is an alternate merge port for adapters that own the gh gate
// and confirmation. A checked adapter reads PR state before deciding to merge
// and after the merge command returns, then reports one of the three outcomes
// above. A caller passes it explicitly in Options.CheckedMerger; the two merge
// guarantees are never mixed by a type assertion on Merger.
type CheckedMerger interface {
	MergeChecked(ctx context.Context, repo string, pr int) (MergeOutcome, error)
}

// Store is the surface the loop needs from Beacon state: active Watches, the
// durable (watchID, revision) merge ledger and Watch cancellation.
type Store interface {
	ActiveWatches(ctx context.Context) ([]Watch, error)
	WasMerged(ctx context.Context, watchID string, revision uint64) (bool, error)
	RecordMerge(ctx context.Context, watchID string, revision uint64) error
	CloseWatch(ctx context.Context, watchID string) error
	// RecordFixAttempt persists one more FIX agent attempt on the Watch's
	// block record. The counter belongs to the block, so an unblock resets it.
	RecordFixAttempt(ctx context.Context, watchID string) error
}

// WatchCloseConflict reports that a Watch changed after the merge decision.
// The close is rejected, so the caller must not rewrite the newer record.
type WatchCloseConflict struct {
	WatchID string
	// Revision and HeadSHA are the values the close expected.
	Revision uint64
	HeadSHA  string
	// GotRevision and GotHeadSHA are the values found in the store.
	GotRevision uint64
	GotHeadSHA  string
	// GotStatus is the status found in the store. A head change closes the
	// Watch with its own reason, so a late merge must not replace that reason.
	GotStatus string
}

func (e *WatchCloseConflict) Error() string {
	return fmt.Sprintf("orchestrator: watch %s close conflict: want revision %d head %s, got revision %d head %s status %s",
		e.WatchID, e.Revision, e.HeadSHA, e.GotRevision, e.GotHeadSHA, e.GotStatus)
}

// RevisionedWatchCloser is an optional Store capability. A store that
// implements it closes a Watch only while the row still matches the revision,
// head and status the merge decision saw. A store without it keeps the plain
// CloseWatch path.
type RevisionedWatchCloser interface {
	CloseWatchAt(ctx context.Context, watchID string, revision uint64, headSHA string) error
}

// WatchHolder is an optional Store capability for persisting a dependency
// hold. A store without it still holds the merge in memory for this pass; the
// reason just is not visible in beacon_list_running / beacon inspect.
type WatchHolder interface {
	HoldWatch(ctx context.Context, watchID, verdict string) error
}

// Waker asks a subscriber to act on a retry/fail/wait Watch. Wake delivery
// lands in another card; a nil Waker leaves those Watches untouched apart from
// being reported in Result.NeedsRebase and Result.Waiting.
type Waker interface {
	Wake(ctx context.Context, watchID string, revision uint64, verdict string) error
}

// ReasonWaker is an alternate wake port for decisions that need to explain
// themselves. A wait holds on a named dependency, so the subscriber needs the
// reason, not just the verdict. A caller passes it explicitly in
// Options.ReasonWaker; the plain Waker receives only the verdict.
type ReasonWaker interface {
	WakeWithReason(ctx context.Context, watchID string, revision uint64, verdict, reason string) error
}

// Action is one decision the loop made, recorded for output and diagnostics.
// Message carries the hold reason for a wait Action.
type Action struct {
	Action   string `json:"action"`
	WatchID  string `json:"watchID"`
	PR       int    `json:"pr"`
	Revision uint64 `json:"revision"`
	Command  string `json:"command,omitempty"`
	Message  string `json:"message,omitempty"`
}

// ApprovalPermissions are the sandbox permissions an approval would grant for
// one attempt. Escalations counts RequireEscalated grants; Extra lists any
// additional permissions. An escalated retry carries one escalation and no
// Extra, so approval grants exactly what the retry actually runs with.
type ApprovalPermissions struct {
	Escalations int      `json:"escalations,omitempty"`
	Extra       []string `json:"extra,omitempty"`
}

// ApprovalAction is an approval request bound to one Watch attempt. Keeping it
// separate from Action lets a caller present the escalated retry for approval
// in place of the first attempt, which runs with narrower permissions.
type ApprovalAction struct {
	Action      string              `json:"action"`
	WatchID     string              `json:"watchID"`
	PR          int                 `json:"pr"`
	Revision    uint64              `json:"revision"`
	Permissions ApprovalPermissions `json:"permissions"`
}

// ApprovalActions returns the first-attempt and escalated-retry approval
// requests for one Watch. The first keeps the caller's narrow Extra
// permissions and asks for no escalation. After a sandbox denial the retry is
// rewritten with RequireEscalated and the extra permissions are dropped, so
// its approval carries one escalation and an empty Extra list
// (codex-rs/core/src/tools/orchestrator.rs:521-532). The same place in Codex
// decides whether the retry needs approval at all (:496-506). Beacon had only
// the single RequireApproval boolean; these two actions make the distinction
// without widening the MCP tool contract.
func ApprovalActions(w Watch, narrow []string) (first, retry ApprovalAction) {
	first = ApprovalAction{
		Action:   ActionApprovalFirst,
		WatchID:  w.ID,
		PR:       w.PR,
		Revision: w.Revision,
		Permissions: ApprovalPermissions{
			Extra: append([]string(nil), narrow...),
		},
	}
	retry = ApprovalAction{
		Action:   ActionApprovalRetry,
		WatchID:  w.ID,
		PR:       w.PR,
		Revision: w.Revision,
		Permissions: ApprovalPermissions{
			Escalations: 1,
		},
	}
	return first, retry
}

// RebaseRef addresses one rebase left for a subscriber. It carries the Watch
// and revision so a consumer can call beacon_get_result for that exact rebase
// and read its nextAction, instead of guessing from the PR number alone.
type RebaseRef struct {
	WatchID  string `json:"watchID"`
	Revision uint64 `json:"revision"`
	PR       int    `json:"pr"`
}

// Result is the outcome of one orchestrate pass. NeedsRebase lists the rebase
// refs of retry/fail Watches left for their subscribers; Waiting lists PRs
// held by an unsatisfied dependency.
type Result struct {
	DryRun      bool        `json:"dryRun"`
	Merged      []int       `json:"merged"`
	Skipped     []int       `json:"skipped,omitempty"`
	Waiting     []int       `json:"waiting"`
	NeedsRebase []RebaseRef `json:"needsRebase"`
	Actions     []Action    `json:"actions"`
	// Approvals carries the approval requests emitted on this pass. A retry
	// Watch yields an escalated-retry approval whose Extra list is empty.
	Approvals []ApprovalAction `json:"approvals,omitempty"`
}

// NetworkSpec describes the network request an operation is about to make.
// A nil spec means the operation is local, so it needs no approval. The merge
// call is the only network request in the decision path today; the caller
// supplies a spec explicitly so a Watch without one produces zero approvals.
type NetworkSpec struct {
	Method string
	Host   string
	Path   string
}

// CancelProxy aborts the in-flight network operation. The handle hands it to
// the operation before launch so cancellation is not lost when the operation
// fails. Cancel is safe to call more than once.
type CancelProxy interface {
	Cancel()
}

// ApprovalHandle finalizes one network approval. Finish records the final
// result, including an error, exactly once after the operation returns.
// Cancel proxies cancellation to the in-flight operation.
type ApprovalHandle interface {
	CancelProxy
	Finish(result error)
}

// NetworkApprover starts network approval before an operation launches and
// returns the handle that finalizes it after the result is known. An
// implementation returns a nil handle when the spec needs no approval.
type NetworkApprover interface {
	Approve(ctx context.Context, spec NetworkSpec) (ApprovalHandle, error)
}

// Options configures one pass. DryRun records the actions it would take
// without calling the Merger, writing the ledger or closing any Watch.
// Resolver supplies the dependency facts; a nil Resolver leaves every Watch
// with waitFor/waitForMain references waiting instead of merging blindly.
type Options struct {
	DryRun   bool
	Resolver DependencyResolver
	// CheckedMerger, when set, owns the mergeability gate and the post-merge
	// state confirmation. It replaces the plain Merger on the merge path. A nil
	// value keeps the trusted-on-success behaviour of Merger.
	CheckedMerger CheckedMerger
	// ReasonWaker, when set, receives the hold reason together with the wake. A
	// nil value sends only the verdict through Waker and leaves the reason on
	// the Action.
	ReasonWaker ReasonWaker
	// NetworkApprover and NetworkSpec add a network approval around the merge
	// call. Both must be set for approval to happen: an absent spec means no
	// network request and produces zero approvals. Existing callers leave both
	// nil, so their verdicts are unchanged (CCx-05).
	NetworkApprover NetworkApprover
	NetworkSpec     *NetworkSpec
	// Sandbox is the startup-selected restriction. When set, a network
	// operation is refused before approval if the mode forbids network. A nil
	// value leaves every existing call path unchanged (CCx-06).
	Sandbox *SandboxPolicy
}

// Run walks the active Watches once. Ready Watches with autor-merge enabled
// are merged at most once per (watchID, revision), then closed. Watches held
// by dependencies produce a wait with a reason and wake their subscriber.
// Blocked Watches ask for one FIX agent per pass and escalate once the cap is
// reached. Retry/fail Watches are reported and handed to the Waker, never
// repaired here. Errors from individual Watches are collected so one bad Watch
// cannot hide the rest; the returned Result still describes everything.
func Run(ctx context.Context, st Store, merger Merger, waker Waker, opts Options) (Result, error) {
	return RunWithResolver(ctx, st, merger, waker, opts.Resolver, opts)
}

// RunWithResolver is Run with an explicit dependency resolver. It is the seam
// for callers that build the resolver outside Options. Passing a nil resolver
// only allows Watches without dependency references to merge.
func RunWithResolver(ctx context.Context, st Store, merger Merger, waker Waker, resolver DependencyResolver, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := Result{
		DryRun:      opts.DryRun,
		Merged:      []int{},
		Waiting:     []int{},
		NeedsRebase: []RebaseRef{},
		Actions:     []Action{},
		Approvals:   []ApprovalAction{},
	}
	if st == nil {
		return out, errors.New("orchestrator: store is nil")
	}
	watches, err := st.ActiveWatches(ctx)
	if err != nil {
		observe.ActorError("orchestrator", "", err)
		return out, fmt.Errorf("orchestrator: active watches: %w", err)
	}
	started := time.Now()
	observe.RunStart(len(watches))
	var errs []error
	defer func() {
		stats := observe.CycleStats{
			Watches: len(watches), Merges: len(out.Merged), Wakes: len(out.Actions),
			Duration: time.Since(started),
		}
		observe.RunEnd(stats, errors.Join(errs...))
	}()

	for _, w := range watches {
		switch {
		case w.Blocked != nil:
			// Blocked wins over a stale ready verdict and over retry. Decide
			// reads the block first, so a blocked Watch can never merge here.
			handleBlocked(ctx, st, w, resolver, opts, &out, &errs)
		case w.isReady() || isWaitDependency(w.Verdict):
			// A Watch held on a previous tick is checked again with the same
			// gate. As soon as the ref is in main, this pass merges it.
			verdict, reason, err := w.canAutoMerge(ctx, resolver)
			if err != nil {
				observe.ActorError("orchestrator", w.ID, err)
				errs = append(errs, err)
				continue
			}
			switch verdict {
			case VerdictReady:
				observe.DependenciesResolved(w.ID, w.PR, nil)
				mergeOne(ctx, st, merger, opts, w, &out, &errs)
			case VerdictWait:
				// An unsatisfied dependency is nobody's fault: there is no fix
				// for an executor or a FIX agent to do, so no obligation is
				// staged and no subscriber is woken. The hold and its reason
				// are persisted instead and rechecked on the next tick.
				observe.DependencyWait(w.ID, w.PR, reason)
				out.Waiting = append(out.Waiting, w.PR)
				out.Actions = append(out.Actions, Action{
					Action: ActionWait, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
					Message: reason,
				})
				if holder, ok := st.(WatchHolder); ok && !opts.DryRun {
					if err := holder.HoldWatch(ctx, w.ID, WaitDependencyVerdict(reason)); err != nil {
						observe.ActorError("dependency-hold", w.ID, err)
						errs = append(errs, fmt.Errorf("orchestrator: hold watch %s: %w", w.ID, err))
					}
				}
			default:
				out.Actions = append(out.Actions, Action{
					Action: ActionSkip, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
					Message: reason,
				})
			}
		case needsWake(w.Verdict):
			out.NeedsRebase = append(out.NeedsRebase, RebaseRef{
				WatchID:  w.ID,
				Revision: w.Revision,
				PR:       w.PR,
			})
			out.Actions = append(out.Actions, Action{
				Action:   ActionNeedsRebase,
				WatchID:  w.ID,
				PR:       w.PR,
				Revision: w.Revision,
				Message:  "left for subscriber wake: " + w.Verdict,
			})
			// A retry is the attempt that actually runs after a denial. Its
			// approval must describe the escalated retry, not the narrow first
			// attempt (codex-rs/core/src/tools/orchestrator.rs:521-532).
			if strings.EqualFold(strings.TrimSpace(w.Verdict), VerdictRetry) {
				_, retryApproval := ApprovalActions(w, nil)
				out.Approvals = append(out.Approvals, retryApproval)
			}
			wakeOne(ctx, waker, opts.ReasonWaker, w, w.Verdict, w.Verdict, &out, &errs)
		}
	}
	return out, errors.Join(errs...)
}

// handleBlocked turns one blocked Watch into exactly one decision per pass.
// Decide owns the FIX-attempt cap: below the cap the Watch asks for another FIX
// agent; at the cap the Watch is escalated to a human. There is no direct fixer
// spawn in this process, so one BLOCKED_FIX_AGENT decision is one reported
// attempt. The attempt is persisted on the Watch's block record so Decide sees
// the budget close: without the write BlockedInfo.FixAttempts stays zero and
// the Watch asks for a FIX agent forever. The switch in RunWithResolver calls
// this once per Watch, which keeps a single decision from charging twice.
func handleBlocked(ctx context.Context, st Store, w Watch, resolver DependencyResolver, opts Options, out *Result, errs *[]error) {
	d := Decide(w, resolver, CIState{})
	if d.Kind == DecisionNotifyOrchestrator && d.Reason == ReasonEscalateHuman {
		observe.BlockEscalated(w.ID, ReasonEscalateHuman)
		out.Actions = append(out.Actions, Action{
			Action: ActionBlockEscalated, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Message: ReasonEscalateHuman,
		})
		return
	}
	if d.Kind != DecisionBlockedFixAgent {
		return
	}
	out.Actions = append(out.Actions, Action{
		Action: ActionBlockedFixAgent, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Message: d.Reason,
	})
	if opts.DryRun {
		return
	}
	// The block record is the sole budget Decide reads. Charge the attempt
	// here, where the FIX agent decision is emitted, so the third spawn closes
	// the cycle and the next pass escalates to a human. A failed write is
	// reported, not hidden: a lost charge would keep the loop spinning.
	if err := st.RecordFixAttempt(ctx, w.ID); err != nil {
		observe.ActorError("fix-attempt", w.ID, err)
		*errs = append(*errs, fmt.Errorf("orchestrator: record fix attempt %s: %w", w.ID, err))
	}
	observe.FixerSpawned(w.ID)
}

// mergeRetryMaxAttempts counts the first call and every retry. Three attempts
// keep one transient TLS failure from leaving a ready Watch untouched while
// bounding the time a single pass can spend on one PR.
const (
	mergeRetryMaxAttempts = 3
	mergeRetryBaseDelay   = 200 * time.Millisecond
	mergeRetryMaxDelay    = 2 * time.Second
)

// mergeRetrySleep is a seam for callers and tests that need a bounded pass.
// Production waits on the context so a shutdown interrupts a backoff.
var mergeRetrySleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// mergeRetryDelay returns an exponential delay with full jitter. Jitter stops
// several Watches whose merges failed in the same tick from retrying in
// lockstep and producing another burst of TLS timeouts.
func mergeRetryDelay(attempt int) time.Duration {
	d := mergeRetryBaseDelay << (attempt - 1)
	if d > mergeRetryMaxDelay {
		d = mergeRetryMaxDelay
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// retryTransient calls call until it succeeds, returns a non-transient error,
// or exhausts the bounded budget. Only errors that ghci classifies as
// transport-level are retried. A merge refusal or a conflict is never retried.
func retryTransient[T any](ctx context.Context, call func() (T, error)) (T, error) {
	var zero T
	for attempt := 1; attempt <= mergeRetryMaxAttempts; attempt++ {
		value, err := call()
		if err == nil || !ghci.IsTransientError(err) {
			return value, err
		}
		if attempt == mergeRetryMaxAttempts {
			return value, err
		}
		if sleepErr := mergeRetrySleep(ctx, mergeRetryDelay(attempt)); sleepErr != nil {
			return value, sleepErr
		}
	}
	return zero, nil
}

// withNetworkApproval runs call with approval started before launch and
// finalized after the result. Approve happens before call; Finish runs once
// on both success and error. When call fails the cancellation proxy is
// invoked so the in-flight request is aborted. A missing approver or spec is
// a no-op, which keeps every existing call path free of approvals (CCx-05).
func withNetworkApproval[T any](ctx context.Context, opts Options, call func() (T, error)) (T, error) {
	var zero T
	if opts.Sandbox != nil {
		if err := opts.Sandbox.AuthorizeNetwork(); err != nil {
			return zero, err
		}
	}
	if opts.NetworkApprover == nil || opts.NetworkSpec == nil {
		return call()
	}
	handle, err := opts.NetworkApprover.Approve(ctx, *opts.NetworkSpec)
	if err != nil {
		return zero, fmt.Errorf("orchestrator: network approval: %w", err)
	}
	if handle == nil {
		return call()
	}
	var callErr error
	defer func() { handle.Finish(callErr) }()
	value, callErr := call()
	if callErr != nil {
		handle.Cancel()
	}
	return value, callErr
}

// recordMergeFailure is the single exit for a merge that failed after its
// budget. A transient failure earns an explicit WARN event. Without it a
// Watch still marked ready looks healthy although no retry is left.
func recordMergeFailure(w Watch, command string, err error, out *Result, errs *[]error) {
	observe.MergeFailure(w.ID, w.PR, err)
	observe.ActorError("merger", w.ID, err)
	if ghci.IsTransientError(err) {
		observe.Event(observe.LevelWarn, "merge_retry_exhausted", observe.Fields{
			observe.FieldWatchID: w.ID,
			observe.FieldPR:      w.PR,
			observe.FieldError:   err.Error(),
			"attempts":           mergeRetryMaxAttempts,
		})
	}
	out.Actions = append(out.Actions, Action{
		Action: ActionMerge, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Command: command, Message: err.Error(),
	})
	*errs = append(*errs, fmt.Errorf("orchestrator: merge PR %d: %w", w.PR, err))
}

// mergeOne performs the idempotent merge branch for one ready Watch. A
// CheckedMerger passed in Options owns the gh mergeability gate and the
// post-merge state confirmation; without one the Merger is trusted on its
// success return. The caller chooses the guarantee explicitly.
func mergeOne(ctx context.Context, st Store, merger Merger, opts Options, w Watch, out *Result, errs *[]error) {
	started := time.Now()
	defer func() { observe.MergeDuration(time.Since(started)) }()
	done, err := st.WasMerged(ctx, w.ID, w.Revision)
	if err != nil {
		observe.ActorError("merge-ledger", w.ID, err)
		*errs = append(*errs, fmt.Errorf("orchestrator: merge ledger %s@%d: %w", w.ID, w.Revision, err))
		return
	}
	if done {
		observe.Event(observe.LevelInfo, "merge_skipped", observe.Fields{
			observe.FieldWatchID: w.ID, observe.FieldPR: w.PR,
			observe.FieldRevision: w.Revision, observe.FieldReason: "already merged at this revision",
		})
		out.Skipped = append(out.Skipped, w.PR)
		out.Actions = append(out.Actions, Action{
			Action: ActionSkip, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Message: "already merged at this revision",
		})
		return
	}

	command := fmt.Sprintf("gh pr merge %d --squash --delete-branch", w.PR)
	if err := w.SandboxPolicy().AuthorizeWrite(sandboxMergeTarget); err != nil {
		observe.ActorError("sandbox", w.ID, err)
		out.Actions = append(out.Actions, Action{
			Action: ActionSandboxDenied, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Message: err.Error(),
		})
		*errs = append(*errs, fmt.Errorf("orchestrator: sandbox for PR %d: %w", w.PR, err))
		return
	}
	if opts.DryRun {
		out.Actions = append(out.Actions, Action{
			Action: ActionDryRun, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Command: command,
		})
		return
	}
	if merger == nil {
		mergerErr := fmt.Errorf("orchestrator: merger is nil for PR %d", w.PR)
		observe.MergeFailure(w.ID, w.PR, mergerErr)
		*errs = append(*errs, mergerErr)
		return
	}
	observe.MergeAttempt(w.ID, w.PR)
	if checked := opts.CheckedMerger; checked != nil {
		mergeCheckedOne(ctx, st, checked, opts, w, command, out, errs)
		return
	}
	if _, err := withNetworkApproval(ctx, opts, func() (struct{}, error) {
		return retryTransient(ctx, func() (struct{}, error) {
			return struct{}{}, merger.Merge(ctx, w.Repo, w.PR)
		})
	}); err != nil {
		recordMergeFailure(w, command, err, out, errs)
		return
	}
	confirmMerge(ctx, st, w, command, out, errs)
}

// mergeCheckedOne turns one checked outcome into state changes. Only a
// confirmed MERGE closes the Watch; a held or pending PR is reported without
// an error so a dirty PR does not produce a multi-line gh hint on every pass.
func mergeCheckedOne(ctx context.Context, st Store, merger CheckedMerger, opts Options, w Watch, command string, out *Result, errs *[]error) {
	outcome, err := withNetworkApproval(ctx, opts, func() (MergeOutcome, error) {
		return retryTransient(ctx, func() (MergeOutcome, error) {
			return merger.MergeChecked(ctx, w.Repo, w.PR)
		})
	})
	if err != nil {
		log.Printf("orchestrator: merge PR %d: %v", w.PR, err)
		recordMergeFailure(w, command, err, out, errs)
		return
	}
	switch {
	case outcome.Merged:
		confirmMerge(ctx, st, w, command, out, errs)
	case outcome.Pending:
		observe.Event(observe.LevelInfo, "merge_pending", observe.Fields{
			observe.FieldWatchID: w.ID, observe.FieldPR: w.PR, observe.FieldRevision: w.Revision,
		})
		log.Printf("orchestrator: PR %d: merge command reported success, waiting for MERGE state", w.PR)
		out.Actions = append(out.Actions, Action{
			Action: ActionMerge, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Command: command, Message: "waiting for MERGE state",
		})
	default:
		reason := outcome.Reason
		if reason == "" {
			reason = "merge gate held the PR"
		}
		observe.Event(observe.LevelInfo, "merge_held", observe.Fields{
			observe.FieldWatchID: w.ID, observe.FieldPR: w.PR,
			observe.FieldRevision: w.Revision, observe.FieldReason: reason,
		})
		log.Printf("orchestrator: PR %d: merge held: %s", w.PR, reason)
		out.Skipped = append(out.Skipped, w.PR)
		out.Actions = append(out.Actions, Action{
			Action: ActionSkip, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Message: reason,
		})
	}
}

// confirmMerge writes the ledger and closes the Watch for a PR the hosting
// service reported as MERGED. It is shared by checked and plain mergers.
func confirmMerge(ctx context.Context, st Store, w Watch, command string, out *Result, errs *[]error) {
	observe.MergeSuccess(w.ID, w.PR)
	if err := st.RecordMerge(ctx, w.ID, w.Revision); err != nil {
		observe.ActorError("merge-ledger", w.ID, err)
		*errs = append(*errs, fmt.Errorf("orchestrator: record merge %s@%d: %w", w.ID, w.Revision, err))
	}
	var closeErr error
	if closer, ok := st.(RevisionedWatchCloser); ok {
		closeErr = closer.CloseWatchAt(ctx, w.ID, w.Revision, w.HeadSHA)
	} else {
		closeErr = st.CloseWatch(ctx, w.ID)
	}
	if closeErr != nil {
		observe.ActorError("watch-closer", w.ID, closeErr)
		*errs = append(*errs, fmt.Errorf("orchestrator: close watch %s: %w", w.ID, closeErr))
	}
	out.Merged = append(out.Merged, w.PR)
	out.Actions = append(out.Actions, Action{
		Action: ActionMerge, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Command: command,
	})
}

// wakeOne hands a retry/fail/wait Watch to the installed Waker. A nil Waker is
// legitimate in this card: the Watch simply stays active for later delivery.
// The reason is recorded on the Action and passed to an explicit ReasonWaker
// when the caller supplied one.
func wakeOne(ctx context.Context, waker Waker, reasonWaker ReasonWaker, w Watch, verdict, reason string, out *Result, errs *[]error) {
	if waker == nil {
		observe.WakeSkipped("", w.ID, "no waker installed")
		return
	}
	fields := observe.Fields{
		observe.FieldWatchID: w.ID, observe.FieldPR: w.PR,
		observe.FieldRevision: w.Revision, observe.FieldVerdict: verdict,
		observe.FieldReason: reason,
	}
	// Failed checks explain why the Watch came back as a retry. Writing the
	// hint only when checks exist keeps wakes without a block record unchanged.
	if w.Blocked != nil && len(w.Blocked.FailedChecks) != 0 {
		fields[observe.FieldDecisionHint] = strings.Join(w.Blocked.FailedChecks, ",")
	}
	observe.Event(observe.LevelInfo, "wake_requested", fields)
	// Only a rebase verdict asks for a rebase. A retry wake carries its own
	// verdict in wake_requested, so calling RebaseRequested here would report
	// a rebase that nobody asked for (T3.395).
	if verdict == VerdictRebase {
		observe.RebaseRequested(w.ID, w.PR)
	}
	var wakeErr error
	if reasonWaker != nil {
		wakeErr = reasonWaker.WakeWithReason(ctx, w.ID, w.Revision, verdict, reason)
	} else {
		wakeErr = waker.Wake(ctx, w.ID, w.Revision, verdict)
	}
	if wakeErr != nil {
		observe.WakeFailed("", w.ID, wakeErr)
		observe.ActorError("waker", w.ID, wakeErr)
		*errs = append(*errs, fmt.Errorf("orchestrator: wake %s: %w", w.ID, wakeErr))
	}
	out.Actions = append(out.Actions, Action{
		Action: ActionWake, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Message: reason,
	})
}
