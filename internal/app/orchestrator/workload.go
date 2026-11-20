// This file owns `beacon workload`: the orchestrator's own view of how many
// Watches it is carrying. The layer matrix (ADR 0005) forbids internal/app ->
// internal/domain, so the Watch is projected into the small shape below at the
// composition root and this package stays pure over one narrow port.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// OrchestratorSubscriberID is the stable identity used when the workload
// command enrolls itself on a Watch. Repeating the call renews this one
// subscriber instead of adding another, which keeps the registration
// idempotent even if the command runs in every pass of the daemon.
const OrchestratorSubscriberID = "beacon-workload"

// Subscriber roles mirrored as plain strings so the app layer names no domain
// type. The values match the domain Role constants, including RoleFixer.
const (
	RoleExecutor     = "executor"
	RoleOrchestrator = "orchestrator"
	RoleFixer        = "fixer"
)

// Verdicts that need a human or a rebase, beyond VerdictRetry already declared
// in orchestrator.go. They are normalized before comparison.
const (
	workloadVerdictWatching    = "watching"
	workloadVerdictRebase      = "rebase"
	workloadVerdictKeepWorking = "keep_working"
	workloadVerdictTimeout     = "timeout"
)

// unknownProject is the bucket for a Watch with neither Project nor Repo.
const unknownProject = "unknown"

// WorkloadSubscriber is one party waiting on a Watch.
type WorkloadSubscriber struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

// WorkloadWatch is the projection the workload report groups. Project is
// supplied by the composition root; when it is empty the report falls back to
// Repo, so a plain Beacon Watch still lands in a stable bucket.
type WorkloadWatch struct {
	ID          string
	Repo        string
	PR          int
	HeadSHA     string
	Project     string
	Verdict     string
	Subscribers []WorkloadSubscriber
}

// Workload is the answer printed by `beacon workload`. Ready is the number of
// Watches awaiting a merge, NeedsAttention the ones blocked on retry/rebase,
// Watching the ones still in progress. The three maps group the same Watches
// by verdict, subscriber role and project. Registered counts the new
// orchestrator subscriptions installed during the pass.
type Workload struct {
	Total          int            `json:"total"`
	Ready          int            `json:"ready"`
	NeedsAttention int            `json:"needsAttention"`
	Watching       int            `json:"watching"`
	Registered     int            `json:"registered"`
	ByVerdict      map[string]int `json:"byVerdict"`
	ByRole         map[string]int `json:"byRole"`
	ByProject      map[string]int `json:"byProject"`
}

// WorkloadStore is the port the command needs from Beacon state: read the
// active Watches and register a subscriber on one of them. The composition
// root adapts the domain store and the watch_pr registration handler to this
// port, so tests replace a single fake.
type WorkloadStore interface {
	ActiveWorkloadWatches(ctx context.Context) ([]WorkloadWatch, error)
	Subscribe(ctx context.Context, w WorkloadWatch, sub WorkloadSubscriber) error
}

// normalizeToken folds case and surrounding space so verdicts and roles from
// different producers land in one bucket.
func normalizeToken(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// needsAttention reports whether a verdict is blocked and needs an agent to
// act: retry, rebase, keep_working or a timeout. Ready and watching are
// deliberately not counted here.
func needsAttention(verdict string) bool {
	switch normalizeToken(verdict) {
	case VerdictRetry, workloadVerdictRebase, workloadVerdictKeepWorking, workloadVerdictTimeout:
		return true
	}
	return false
}

// hasWorkloadRole reports whether w already carries a subscriber with role.
// The comparison is case-insensitive and ignores blank roles.
func hasWorkloadRole(w WorkloadWatch, role string) bool {
	want := normalizeToken(role)
	for _, s := range w.Subscribers {
		if normalizeToken(s.Role) == want && want != "" {
			return true
		}
	}
	return false
}

// BuildWorkload groups watches by verdict, subscriber role and project and
// counts the three headline buckets. It is pure: it never writes to the store.
// Each role is counted at most once per Watch, so a Watch with two executors
// still contributes one to byRole["executor"].
func BuildWorkload(watches []WorkloadWatch) Workload {
	out := Workload{
		ByVerdict: make(map[string]int),
		ByRole:    make(map[string]int),
		ByProject: make(map[string]int),
	}
	for _, w := range watches {
		out.Total++
		verdict := normalizeToken(w.Verdict)
		if verdict == "" {
			verdict = workloadVerdictWatching
		}
		out.ByVerdict[verdict]++
		switch {
		case verdict == VerdictReady:
			out.Ready++
		case needsAttention(verdict):
			out.NeedsAttention++
		case verdict == workloadVerdictWatching:
			out.Watching++
		}

		project := normalizeToken(w.Project)
		if project == "" {
			project = normalizeToken(w.Repo)
		}
		if project == "" {
			project = unknownProject
		}
		out.ByProject[project]++

		seen := make(map[string]bool, len(w.Subscribers))
		for _, s := range w.Subscribers {
			role := normalizeToken(s.Role)
			if role == "" || seen[role] {
				continue
			}
			seen[role] = true
			out.ByRole[role]++
		}
	}
	return out
}

// RunWorkload reads the active Watches, enrolls the orchestrator on every
// Watch that does not yet carry it and returns the report built from the
// snapshot read before registration. Keeping the pre-registration snapshot
// makes byRole honest: it shows which Watches lacked the orchestrator instead
// of reporting every Watch as orchestrator-owned after the side effect.
//
// Registration is idempotent by role: a Watch already subscribed is skipped.
// A failed subscription is collected and does not hide the other Watches; the
// report still describes everything that was read.
func RunWorkload(ctx context.Context, st WorkloadStore) (Workload, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if st == nil {
		return Workload{}, errors.New("workload: store is nil")
	}
	watches, err := st.ActiveWorkloadWatches(ctx)
	if err != nil {
		return Workload{}, fmt.Errorf("workload: active watches: %w", err)
	}
	report := BuildWorkload(watches)

	var errs []error
	for _, w := range watches {
		if hasWorkloadRole(w, RoleOrchestrator) {
			continue
		}
		sub := WorkloadSubscriber{ID: OrchestratorSubscriberID, Role: RoleOrchestrator}
		if err := st.Subscribe(ctx, w, sub); err != nil {
			errs = append(errs, fmt.Errorf("workload: subscribe watch %s: %w", w.ID, err))
			continue
		}
		report.Registered++
	}
	return report, errors.Join(errs...)
}
