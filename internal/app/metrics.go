package app

import (
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// criticalPathSeconds is the span from the first check completion to the last
// one. Checks missing either endpoint are ignored. A staged pipeline pays this
// span in sequence, so it explains the CI wait that remains after the first
// check finishes. It is zero when no check reports both endpoints.
func criticalPathSeconds(checks []core.CheckResult) int64 {
	var first, last time.Time
	for _, c := range checks {
		if c.StartedAt.IsZero() || c.CompletedAt.IsZero() || c.CompletedAt.Before(c.StartedAt) {
			continue
		}
		if first.IsZero() || c.CompletedAt.Before(first) {
			first = c.CompletedAt
		}
		if last.IsZero() || c.CompletedAt.After(last) {
			last = c.CompletedAt
		}
	}
	if first.IsZero() {
		return 0
	}
	return int64(last.Sub(first).Seconds())
}

// ciCycle is the part of a run's CI wait the current timeline event belongs
// to. A run starts in its initial cycle. A rebase or an agent fix starts a new
// cycle that keeps its name until the next transition.
type ciCycle int

const (
	ciInitial ciCycle = iota
	ciRebase
	ciFix
)

func cycleForState(state core.RunState, current ciCycle) ciCycle {
	switch state {
	case core.RunNeedsRebase:
		return ciRebase
	case core.RunNeedsAgentFix:
		return ciFix
	default:
		return current
	}
}

func addCycleDuration(out *Durations, cycle ciCycle, seconds int64) {
	switch cycle {
	case ciRebase:
		out.WaitingCIRebase += seconds
	case ciFix:
		out.WaitingCIFix += seconds
	default:
		out.WaitingCIInitial += seconds
	}
}

// deriveDurations measures each state interval. A live run has no closing
// event, so its last interval runs until now and the panel sees a ticking
// timer. Terminal runs keep the fixed UpdatedAt boundary. Waiting CI is also
// split by the cycle the timeline was in at each interval.
func deriveDurations(run *core.Run, events []RunEvent, now time.Time) Durations {
	var out Durations
	endOfRun := run.UpdatedAt
	if !run.State.Terminal() && now.After(endOfRun) {
		endOfRun = now
	}
	cycle := ciInitial
	for i, event := range events {
		state := core.RunState(event.State)
		cycle = cycleForState(state, cycle)
		end := endOfRun
		if i+1 < len(events) {
			end = events[i+1].At
		}
		if end.After(event.At) {
			seconds := int64(end.Sub(event.At).Seconds())
			addStateDuration(&out, state, cycle, seconds)
		}
	}
	// Older state databases have no events. Use the run's own window so the
	// panel still shows an elapsed time.
	if len(events) == 0 && endOfRun.After(run.CreatedAt) {
		seconds := int64(endOfRun.Sub(run.CreatedAt).Seconds())
		addStateDuration(&out, run.State, cycleForState(run.State, cycle), seconds)
	}
	return out
}

// addStateDuration adds one interval to every duration bucket the state owns.
// Waiting CI feeds both its total and the current cycle split from that same
// interval, so both call sites share one state-to-bucket mapping.
func addStateDuration(out *Durations, state core.RunState, cycle ciCycle, seconds int64) {
	switch state {
	case core.RunWaitingCI:
		out.WaitingCI += seconds
		addCycleDuration(out, cycle, seconds)
	case core.RunRunning:
		out.Running += seconds
	case core.RunBlocked:
		out.Blocked += seconds
	}
}

func makeMetrics(
	active, recent []RunSnapshot,
	timeline map[string][]RunEvent,
	histories map[string][]Decision,
	counts []DecisionCount,
) Metrics {
	m := Metrics{Skips: map[string]int{}, Blocked: map[string]int{}, LaunchSources: map[string]int{}}
	merged := make(map[string]bool)
	blocked := make(map[string]bool)
	addSnapshotMetrics(&m, active, recent, merged, blocked)
	for _, events := range timeline {
		for _, event := range events {
			if event.Kind == "merged" {
				merged[event.RunID] = true
			}
		}
	}
	addDecisionMetrics(&m, histories, merged, blocked)
	addCountMetrics(&m, counts)
	m.MergeCount = len(merged)
	return m
}

func addCountMetrics(m *Metrics, counts []DecisionCount) {
	for _, c := range counts {
		action := core.ActionForKind(c.Kind)
		if action.Qualifier != "" && c.Action != action.Qualifier {
			continue
		}
		switch action.Counter {
		case core.CounterDispatchCycles:
			m.DispatchCycles += c.Count
		case core.CounterSkip:
			m.SkipCount += c.Count
			m.Skips[c.Verdict] += c.Count
		case core.CounterDispatch:
			m.DispatchCount += c.Count
		case core.CounterRebase:
			m.RebaseCount += c.Count
		case core.CounterFix:
			m.FixCount += c.Count
		}
	}
}

// blockedReasonCategory reduces a free-form blocked reason to one of a small
// fixed set of keys. Reasons carry SHAs, provider errors and command output, so
// keying the metric by the raw text would grow the map without bound. Anything
// outside the known categories lands in "other".
func blockedReasonCategory(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.HasPrefix(reason, "gate failed"):
		return "gate failed"
	case strings.HasPrefix(reason, "base_red"):
		return "base red"
	case strings.HasPrefix(reason, "rebase attempts"):
		return "rebase attempts"
	case strings.HasPrefix(reason, "fix attempts"):
		return "fix attempts"
	case strings.HasPrefix(reason, "idle"):
		return "idle"
	default:
		return "other"
	}
}

// addSnapshotMetrics folds the durations already computed for each run snapshot
// into the totals and counts each run by its launch source in one pass. It never
// walks a timeline again, so each timeline is measured exactly once by
// makeRunSnapshot. A run with no recorded source came from an automatic path and
// counts as daemon.
func addSnapshotMetrics(m *Metrics, active, recent []RunSnapshot, merged, blocked map[string]bool) {
	for _, group := range [][]RunSnapshot{active, recent} {
		for _, snap := range group {
			m.CIWaitSeconds += snap.Durations.WaitingCI
			m.CIWaitInitial += snap.Durations.WaitingCIInitial
			m.CIWaitRebase += snap.Durations.WaitingCIRebase
			m.CIWaitFix += snap.Durations.WaitingCIFix
			id := snap.ID
			if snap.State == string(core.RunMerged) {
				merged[id] = true
			}
			if snap.State == string(core.RunBlocked) && !blocked[id] {
				blocked[id] = true
				m.Blocked[blockedReasonCategory(snap.Reason)]++
				m.BlockedCount++
			}
			source := core.ParseLaunchSource(snap.LaunchSource)
			m.LaunchSources[string(source)]++
		}
	}
	m.ActiveCount = len(active)
	m.RecentCount = len(recent)
}

func addDecisionMetrics(m *Metrics, histories map[string][]Decision, merged, blocked map[string]bool) {
	for _, decisions := range histories {
		for _, decision := range decisions {
			switch decision.Kind {
			case "transition.merge":
				merged[decision.RunID] = true
			case "reconcile.decision":
				if decision.Verdict == string(core.VerdictBlocked) && !blocked[decision.RunID] {
					blocked[decision.RunID] = true
					m.Blocked[blockedReasonCategory(decision.Reason)]++
					m.BlockedCount++
				}
			}
		}
	}
}
