package app

import "github.com/refusedguy/beacon/pkg/core"

// sameObservation reports whether the run already recorded the exact head and
// base SHA pair from obs. Reconcile uses it to skip duplicate rebase and wake
// work when the PR has not actually changed.
func sameObservation(run *core.Run, obs core.Observation) bool {
	return run.ObservedSHA == obs.HeadSHA && run.ObservedBaseSHA == obs.BaseSHA
}

// recordObservation stores the head and base SHA pair the reconciler has just
// acted on, so the next poll can skip an identical observation.
func recordObservation(run *core.Run, headSHA, baseSHA string) {
	run.ObservedSHA = headSHA
	run.ObservedBaseSHA = baseSHA
}
