package cibridge

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// liveWatches reads the Watches still in the control loop. LiveWatches is
// preferred because it includes blocked Watches but excludes closed ones; the
// AllWatches fallback keeps legacy stores working.
func (b *ciRefresh) liveWatches() ([]domain.Watch, error) {
	if src, ok := b.store.(liveWatchSource); ok {
		return src.LiveWatches()
	}
	return b.store.AllWatches()
}

// ciObservation is one normalized CI fact: the PR plus its base branch. The
// batch provider supplies both; the single-PR fallback supplies the base ref
// only, which is enough for the common case where main is green.
type ciObservation struct {
	pr   domain.PRState
	base domain.BaseBranchState
}

// fetchSnapshots reads every live Watch once. A BatchCIProvider is used when
// available so one GraphQL document covers all PRs and their base branches.
// Without one, the legacy CIProvider is called per Watch. Watches parked for a
// human answer (needs_input) are skipped: a CI verdict must not wake their
// subscribers or close them before the answer resumes the same Watch (ADR 0031).
// Blocked Watches stay in the sample. The base-green resolver needs their base
// observation to release the incident, and the Watch loop keeps them parked.
func (b *ciRefresh) fetchSnapshots(ctx context.Context, watches []domain.Watch, cycleID string) (map[string]ciObservation, error) {
	out := make(map[string]ciObservation, len(watches))
	candidates := make([]domain.Watch, 0, len(watches))
	for _, w := range watches {
		if w.Status != domain.StatusClosed && w.Status != domain.StatusNeedsInput {
			candidates = append(candidates, w)
		}
	}
	if len(candidates) == 0 {
		return out, nil
	}

	if b.batch != nil {
		callStart := time.Now()
		snaps, err := b.batch.FetchBatch(ctx, candidates)
		observe.ProviderCall("batchci", "FetchBatch", "", err, time.Since(callStart))
		if err != nil {
			return nil, err
		}
		used, budget := b.batch.PointStats()
		for _, w := range candidates {
			if s, ok := snaps[w.ID]; ok {
				out[w.ID] = ciObservation{pr: s.PR, base: s.Base}
			}
		}
		b.cycleEvent(cycleID, "ci_points", cycleFields{}, observe.Fields{"used": used, "budget": budget})
		return out, nil
	}

	// The fallback reads one distinct PR per call. BoundedRun caps the calls in
	// flight; each worker writes its own index, and out is filled only after
	// every worker returns. stopOnError stays false so one bad PR cannot stop
	// the rest. The ghci provider still owns per-PR singleflight and the GitHub
	// request limits.
	const fallbackPRStateLimit = 4
	type fallbackState struct {
		state domain.PRState
		ok    bool
	}
	states := make([]fallbackState, len(candidates))
	BoundedRun(ctx, len(candidates), fallbackPRStateLimit, false, func(ctx context.Context, i int) error {
		w := candidates[i]
		callStart := time.Now()
		state, err := b.ci.PRState(ctx, w.Repo, w.PR)
		observe.ProviderCall("ghci", "PRState", w.ID, err, time.Since(callStart))
		if err != nil {
			log.Printf("cibridge: %s#%d (watch %s): %v", w.Repo, w.PR, w.ID, err)
			return err
		}
		states[i] = fallbackState{state: state, ok: true}
		return nil
	})
	for i, w := range candidates {
		if !states[i].ok {
			continue
		}
		out[w.ID] = ciObservation{
			pr:   states[i].state,
			base: domain.BaseBranchState{BaseRef: baseRefOf(w)},
		}
	}
	return out, nil
}

// baseRefOf returns the Watch base branch with "main" as the legacy default.
func baseRefOf(w domain.Watch) string {
	if strings.TrimSpace(w.BaseRef) != "" {
		return w.BaseRef
	}
	return "main"
}

// isTerminalState reports a GitHub PR state that needs no further CI cycle.
func isTerminalState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "MERGED", "CLOSED":
		return true
	}
	return false
}

// checkFacts normalizes a check map into the Rollup and Failures facts the
// verdict engine reads. It never decides a verdict: EvaluatePR owns that.
func checkFacts(checks map[string]domain.Check) (string, []string) {
	if len(checks) == 0 {
		return "SUCCESS", nil
	}
	rollup := "SUCCESS"
	failures := make([]string, 0)
	for name, c := range checks {
		switch strings.ToUpper(strings.TrimSpace(c.Conclusion)) {
		case "SUCCESS", "SKIPPED", "NEUTRAL":
			continue
		case "FAILURE", "ERROR", "CANCELLED", "CANCELED", "TIMED_OUT", "STARTUP_FAILURE":
			rollup = "FAILURE"
			failures = append(failures, name)
		default:
			if rollup != "FAILURE" {
				rollup = "PENDING"
			}
		}
	}
	sort.Strings(failures)
	return rollup, failures
}

// realFailedNames lists the checks that actually failed, with SKIPPED and
// NEUTRAL excluded. It feeds the self-teaching wake text.
func realFailedNames(checks map[string]domain.Check) []string {
	_, failures := checkFacts(checks)
	return failures
}

// snapshotFor turns one normalized CI observation into the domain Snapshot
// consumed by EvaluatePR. Suite composition was validated by the batch
// provider, so SuitesValid is true; no other fact is invented here.
func snapshotFor(w domain.Watch, obs ciObservation, now time.Time) domain.Snapshot {
	rollup, failures := checkFacts(obs.pr.Checks)
	return domain.Snapshot{
		State:            obs.pr.State,
		IsDraft:          obs.pr.IsDraft,
		HeadSHA:          obs.pr.HeadRefOid,
		WatchHeadSHA:     w.HeadSHA,
		Rollup:           rollup,
		Mergeable:        obs.pr.Mergeable,
		MergeStateStatus: obs.pr.MergeStateStatus,
		ReviewDecision:   obs.pr.ReviewDecision,
		Checks:           obs.pr.Checks,
		Failures:         failures,
		Flaky:            w.Flaky,
		At:               now,
		DeadlineAt:       w.DeadlineAt,
		NoChecksSince:    w.NoChecksSince,
		NoChecksTimeout:  w.NoChecksTimeout,
		SuitesValid:      true,
		BaseFailedChecks: obs.base.FailedChecks,
		BaseHeadSHA:      obs.base.HeadSHA,
		BaseChecksKnown:  obs.base.ChecksKnown,
	}
}

// successorWatchID mirrors the domain fan-out alias so a head-change successor
// has the same id as a fresh watch_pr registration on the new SHA.
func successorWatchID(w domain.Watch, head string) string {
	return fmt.Sprintf("watch:%s:%d:%s", w.Repo, w.PR, head)
}
