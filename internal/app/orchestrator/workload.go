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
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
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
// Watching the ones still in progress. ByAttention splits NeedsAttention by
// typed reason. The other maps group the same Watches by verdict, subscriber
// role and project. Registered counts the new orchestrator subscriptions
// installed during the pass. SlotsTotal is the configured parallel-slot
// budget; SlotsUsed is the number of active slots in the same snapshot.
// Skipped counts units a bounded pass did not start after a failure.
type Workload struct {
	Total          int               `json:"total"`
	Ready          int               `json:"ready"`
	NeedsAttention int               `json:"needsAttention"`
	Watching       int               `json:"watching"`
	Registered     int               `json:"registered"`
	SlotsTotal     int               `json:"slotsTotal"`
	SlotsUsed      int               `json:"slotsUsed"`
	Skipped        int               `json:"skipped"`
	RateLimit      WorkloadRateLimit `json:"rateLimit"`
	ByVerdict      map[string]int    `json:"byVerdict"`
	ByRole         map[string]int    `json:"byRole"`
	ByProject      map[string]int    `json:"byProject"`
	ByAttention    map[string]int    `json:"byAttention"`
}

// WorkloadStore is the port the command needs from Beacon state: read the
// active Watches and register a subscriber on one of them. The composition
// root adapts the domain store and the watch_pr registration handler to this
// port, so tests replace a single fake.
type WorkloadStore interface {
	ActiveWorkloadWatches(ctx context.Context) ([]WorkloadWatch, error)
	Subscribe(ctx context.Context, w WorkloadWatch, sub WorkloadSubscriber) error
}

// SlotBudgetStore is an optional capability for stores that can resolve the
// configured parallel-slot budget. Keeping it separate from WorkloadStore
// leaves the daemon adapter unchanged: a store without this capability still
// compiles and reports the budget derived from the watch snapshot.
type SlotBudgetStore interface {
	SlotBudget(ctx context.Context) (total int, used int, err error)
}

// Rate-limit statuses mirrored as plain strings so the app layer names no
// domain type. The values match the domain RateLimitStatus constants.
const (
	workloadRateAllowed  = "allowed"
	workloadRateWarning  = "allowed_warning"
	workloadRateRejected = "rejected"
)

// WorkloadRateLimit is the app-layer projection of a rate-limit event.
// Utilization is the used share of the window in [0, 1]. ResetsAt is the
// refill time; the zero time means the producer did not supply one. The
// composition root projects the domain event into this shape because ADR 0005
// forbids internal/app -> internal/domain.
type WorkloadRateLimit struct {
	Status      string    `json:"status"`
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

// RateLimitStore is an optional capability for stores that can read the latest
// rate-limit event. A store without it still compiles and reports no budget
// signal, so the daemon adapter stays unchanged.
type RateLimitStore interface {
	RateLimit(ctx context.Context) (WorkloadRateLimit, error)
}

// SkippedStore is an optional capability for stores that expose the number of
// units the last bounded pass did not start after a failure. A store without
// it still compiles and the workload report shows zero.
type SkippedStore interface {
	SkippedUnits(ctx context.Context) (int, error)
}

// SpawnPlan is the planner's answer for one pass over pending spawns. Allowed
// and Deferred split the new spawns. RejectedCalls stays zero: the planner
// holds new work only and never cancels a call already running. HTTP 429 is a
// separate call error and is not counted here.
type SpawnPlan struct {
	Allowed       []string
	Deferred      []string
	RejectedCalls int
	Until         time.Time
	Reason        string
}

// SpawnPlanner turns a rate-limit event into a spawn decision. It holds no
// domain type and reads only the event projected by the composition root.
type SpawnPlanner struct {
	limit WorkloadRateLimit
}

// NewSpawnPlanner creates a planner for one rate-limit event.
func NewSpawnPlanner(limit WorkloadRateLimit) SpawnPlanner {
	return SpawnPlanner{limit: limit}
}

// PlanSpawns splits pending spawn ids into ones that start now and ones that
// wait. A warning defers every new spawn until ResetsAt and leaves running
// calls alone. Rejected holds every new spawn. Allowed and unrecognised
// statuses let the batch through. A warning whose reset time has already
// passed also lets the batch through.
func (p SpawnPlanner) PlanSpawns(now time.Time, pending []string) SpawnPlan {
	plan := SpawnPlan{}
	switch normalizeToken(p.limit.Status) {
	case workloadRateWarning:
		if p.limit.ResetsAt.After(now) {
			plan.Until = p.limit.ResetsAt
			plan.Reason = "rate limit warning"
			plan.Deferred = append([]string(nil), pending...)
			return plan
		}
	case workloadRateRejected:
		plan.Reason = "rate limit rejected"
		plan.Deferred = append([]string(nil), pending...)
		return plan
	case workloadRateAllowed:
	}
	plan.Allowed = append([]string(nil), pending...)
	return plan
}

// normalizeToken folds case and surrounding space so verdicts and roles from
// different producers land in one bucket.
func normalizeToken(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// workloadAttention is the app-layer mirror of domain.Attention. The layer
// matrix forbids internal/app -> internal/domain (ADR 0005), so the values are
// plain strings here. An empty Reason means no known trigger, not proof that
// the Watch is clear.
type workloadAttention struct {
	Level      string
	Reason     string
	NextAction string
}

// NeedsAction reports whether an agent has to act on the Watch.
func (a workloadAttention) NeedsAction() bool {
	return a.Level == "action"
}

// attentionForVerdict classifies a verdict into the typed attention model.
// Retry, rebase, keep_working and timeout need an agent. Ready, watching and
// unrecognised sources get no reason, which is not proof that the Watch is
// clear.
func attentionForVerdict(verdict string) workloadAttention {
	switch normalizeToken(verdict) {
	case VerdictRetry:
		return workloadAttention{Level: "action", Reason: "retry", NextAction: "rerun the failed checks"}
	case workloadVerdictRebase:
		return workloadAttention{Level: "action", Reason: "rebase", NextAction: "rebase onto origin/main"}
	case workloadVerdictKeepWorking:
		return workloadAttention{Level: "action", Reason: "keep_working", NextAction: "resume the failed checks"}
	case workloadVerdictTimeout:
		return workloadAttention{Level: "action", Reason: "timeout", NextAction: "inspect the Watch and decide whether to continue"}
	}
	return workloadAttention{}
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

// BuildWorkload groups watches by verdict, subscriber role, project and typed
// attention reason, and counts the three headline buckets. It is pure: it never
// writes to the store.
// Each role is counted at most once per Watch, so a Watch with two executors
// still contributes one to byRole["executor"].
func BuildWorkload(watches []WorkloadWatch) Workload {
	out := Workload{
		ByVerdict:   make(map[string]int),
		ByRole:      make(map[string]int),
		ByProject:   make(map[string]int),
		ByAttention: make(map[string]int),
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
		case verdict == workloadVerdictWatching:
			out.Watching++
		}
		if attention := attentionForVerdict(verdict); attention.NeedsAction() {
			out.NeedsAttention++
			out.ByAttention[attention.Reason]++
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
	// One active Watch holds one orchestrator slot, so the snapshot's length is
	// the default used count. A store that implements SlotBudgetStore can
	// override this with a source closer to the live agent inventory.
	out.SlotsUsed = len(watches)
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

	if bs, ok := st.(SlotBudgetStore); ok {
		total, used, err := bs.SlotBudget(ctx)
		if err != nil {
			return Workload{}, fmt.Errorf("workload: slot budget: %w", err)
		}
		report.SlotsTotal = total
		if used >= 0 {
			report.SlotsUsed = used
		}
	}

	if rs, ok := st.(RateLimitStore); ok {
		limit, err := rs.RateLimit(ctx)
		if err != nil {
			return Workload{}, fmt.Errorf("workload: rate limit: %w", err)
		}
		limit.Status = normalizeToken(limit.Status)
		report.RateLimit = limit
	}

	if ss, ok := st.(SkippedStore); ok {
		skipped, err := ss.SkippedUnits(ctx)
		if err != nil {
			return Workload{}, fmt.Errorf("workload: skipped units: %w", err)
		}
		report.Skipped = skipped
	}

	errs := &observe.ErrorCollector{}
	for _, w := range watches {
		if hasWorkloadRole(w, RoleOrchestrator) {
			continue
		}
		sub := WorkloadSubscriber{ID: OrchestratorSubscriberID, Role: RoleOrchestrator}
		if err := st.Subscribe(ctx, w, sub); err != nil {
			errs.Addf("workload: subscribe watch %s: %w", w.ID, err)
			continue
		}
		report.Registered++
	}
	return report, errs.Error()
}
