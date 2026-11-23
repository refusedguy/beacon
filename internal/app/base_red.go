package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func failedNames(checks []core.CheckResult) []string {
	var names []string
	for _, c := range checks {
		if c.State == core.CheckFailure {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return names
}

func intersects(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		if set[v] {
			return true
		}
	}
	return false
}

func signature(repo, base string, names []string) string {
	return repo + "|" + base + "|" + strings.Join(names, ",")
}

func (r *Reconciler) checkBaseRed(ctx context.Context, run *core.Run, obs core.Observation, reason string) error {
	baseBranch := r.baseBranchFor(ctx, run)
	if baseBranch == "" {
		return nil
	}
	base, err := r.SCM.BaseBranchStatus(ctx, baseBranch)
	if err != nil {
		return err
	}
	if !base.Red() {
		return nil
	}
	prFailed := failedNames(obs.Checks)
	if !intersects(prFailed, failedNames(base.FailedChecks)) {
		return nil
	}
	in, err := r.Store.CreateIncident(ctx, &core.BaseIncident{
		Repo: r.Repo, BaseBranch: baseBranch,
		Signature:        signature(r.Repo, baseBranch, prFailed),
		FailedChecksJSON: sqliteChecks(prFailed),
	})
	if err != nil {
		return err
	}
	active, err := r.Store.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, other := range active {
		if other.IsFixer || other.PRNumber == 0 {
			continue
		}
		if other.State != core.RunWaitingCI && other.State != core.RunNeedsAgentFix {
			continue
		}
		if r.baseBranchFor(ctx, other) != baseBranch {
			continue
		}
		otherObs, err := r.SCM.Observe(ctx, other.PRNumber)
		if err != nil || !intersects(failedNames(otherObs.Checks), failedNames(base.FailedChecks)) {
			continue
		}
		if err := other.Apply(core.Event{Kind: core.EvBlock, Reason: "base_red"}); err != nil {
			continue
		}
		other.BlockedIncidentID = in.ID
		if err := r.Store.Save(ctx, other); err != nil {
			return err
		}
	}
	if strings.EqualFold(r.BaseRedMode, "notify_orchestrator") {
		return r.notify(run, "base_red", "base branch is red: "+reason)
	}
	return r.ensureFixer(ctx, in, base, run)
}

func sqliteChecks(names []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf("%q", n))
	}
	b.WriteByte(']')
	return b.String()
}

func (r *Reconciler) ensureFixer(ctx context.Context, in *core.BaseIncident, base core.BaseStatus, origin *core.Run) error {
	if in.FixerRunID != "" {
		live, err := r.Store.Get(ctx, in.FixerRunID)
		if err != nil {
			return err
		}
		if live != nil && !live.State.Terminal() {
			return nil
		}
	}
	max := r.MaxFixTries
	if max <= 0 {
		max = 2
	}
	if in.Attempts >= max {
		return r.notify(origin, "fixer exhausted", "base branch fix attempts exhausted")
	}
	attempt := in.Attempts + 1
	id := core.RunID(fmt.Sprintf("fix-%s-%d", in.ID, attempt))
	task := core.Task{
		ID:    core.TaskID(fmt.Sprintf("FIX-%s-%d", in.ID, attempt)),
		Title: "Fix red base branch " + base.BaseBranch,
		Scope: []core.ScopePath{core.ScopeAll()},
	}
	ws, err := r.Workspace.Create(ctx, task)
	if err != nil {
		return err
	}
	run := core.NewRun(id, task.ID, time.Now().UTC())
	run.IsFixer = true
	run.Scope = task.Scope
	run.WorkspacePath = ws.Path
	run.Branch = ws.Branch
	run.ParentSessionID = r.ParentSessionID
	prompt := fmt.Sprintf("Base branch %s is red. Failed checks: %s\n%s\nFix the base branch and open a PR.",
		base.BaseBranch, in.FailedChecksJSON, compact(base.FailureExcerpt))
	session, err := r.Harness.Spawn(ctx, core.SpawnRequest{
		Task: task, Workspace: ws, RunID: run.ID, Prompt: prompt,
		ParentSessionID: run.ParentSessionID, IsFixer: true,
	})
	if err != nil {
		_ = r.Workspace.Remove(ctx, ws)
		return err
	}
	run.SessionID = session.ID
	if session.PR != 0 {
		run.PRNumber = session.PR
	}
	if err := run.Apply(core.Event{Kind: core.EvStart, Reason: "base_red"}); err != nil {
		return err
	}
	if err := r.Store.Save(ctx, run); err != nil {
		return err
	}
	in.FixerRunID = run.ID
	in.Attempts++
	return r.Store.UpdateIncident(ctx, in)
}

func (r *Reconciler) checkBaseRecovery(ctx context.Context) error {
	incidents, err := r.Store.ListOpenIncidents(ctx)
	if err != nil {
		return err
	}
	for _, in := range incidents {
		base, err := r.SCM.BaseBranchStatus(ctx, in.BaseBranch)
		if err != nil {
			slog.Error("base status", "incident", in.ID, "error", err)
			continue
		}
		if base.Red() {
			continue
		}
		in.Status = "resolved"
		if err := r.Store.UpdateIncident(ctx, in); err != nil {
			return err
		}
		runs, err := r.Store.ListByIncident(ctx, in.ID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if err := r.unblockWithRebase(ctx, run); err != nil {
				slog.Error("unblock run", "run", run.ID, "error", err)
			}
		}
	}
	return nil
}

func (r *Reconciler) unblockWithRebase(ctx context.Context, run *core.Run) error {
	if err := run.Apply(core.Event{Kind: core.EvUnblock, Reason: "base green"}); err != nil {
		return err
	}
	run.BlockedIncidentID = ""
	run.RebaseAttempts = 0
	run.ObservedSHA = ""
	if run.PRNumber > 0 {
		newSHA, err := r.SCM.UpdateBranch(ctx, run.PRNumber, run.HeadSHA)
		if err != nil {
			_ = run.Apply(core.Event{Kind: core.EvBehind, Reason: "base recovered"})
			_ = run.Apply(core.Event{Kind: core.EvRebaseFailed, Reason: err.Error()})
			return r.Store.Save(ctx, run)
		}
		if err := run.Apply(core.Event{Kind: core.EvBehind, Reason: "base recovered"}); err == nil {
			_ = run.Apply(core.Event{Kind: core.EvRebased, HeadSHA: newSHA, Reason: "base recovered"})
		}
	}
	return r.Store.Save(ctx, run)
}
