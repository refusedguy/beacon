package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/refusedguy/beacon/pkg/core"
)

func (r *Reconciler) notify(run *core.Run, subject, body string) error {
	if r.Notifier == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.Notifier.Notify(ctx, core.Notification{RunID: run.ID, Subject: subject, Body: compact(body)})
}

func (r *Reconciler) notifyParent(ctx context.Context, run *core.Run, pr int, state string) {
	if run.ParentSessionID == "" || r.Harness == nil {
		return
	}
	text := fmt.Sprintf("Beacon: PR #%d run %s state %s", pr, run.ID, state)
	if err := r.Harness.NotifyParent(ctx, run.ParentSessionID, text); err != nil {
		slog.Error("notify parent", "run", run.ID, "error", err)
		r.Decisions.logTransitionParentNotify(ctx, run.ID, pr, "failed")
		return
	}
	r.Decisions.logTransitionParentNotify(ctx, run.ID, pr, "notified")
}

// compact trims s and caps it at 1200 runes. A byte length check handles short
// bodies without a rune slice, and long valid UTF-8 is walked to the cap.
// Invalid UTF-8 keeps the eager conversion so the output stays byte identical.
func compact(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", ""))
	if len(s) <= 1200 {
		return s
	}
	if !utf8.ValidString(s) {
		r := []rune(s)
		if len(r) <= 1200 {
			return s
		}
		return string(r[:1200]) + "\n...[truncated]"
	}
	i, n := 0, 0
	for i < len(s) && n < 1200 {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	if i == len(s) {
		return s
	}
	return s[:i] + "\n...[truncated]"
}

// ReviewGate is the part of a merge policy that decides whether an epic PR
// still needs review approval. The policy lives in an outer package, so the
// reconciler reaches it through this port instead of a concrete rule.
type ReviewGate interface {
	ReviewRequired(core.ReviewMode, core.Observation) bool
}

const epicBranchPrefix = "beacon/"

func epicBranch(id core.TaskID) string { return epicBranchPrefix + string(id) }

// parentEpicID is the ID-shape check kept for the caller that has no task
// snapshot. Reconciliation decisions come from TaskTree.Parent; this only
// recognises legacy generated IDs until that caller moves to the tree.
func parentEpicID(id core.TaskID) core.TaskID {
	s := string(id)
	i := strings.LastIndexByte(s, '-')
	if i <= 0 || len(s)-i-1 != 2 {
		return ""
	}
	for _, c := range s[i+1:] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return core.TaskID(s[:i])
}

// taskTree indexes one source read. Every relation question in reconciliation
// goes through it, so the parent field is read once instead of scanning IDs.
func (r *Reconciler) taskTree(ctx context.Context) core.TaskTree {
	if r.Source == nil {
		return core.NewTaskTree(nil)
	}
	tasks, err := r.Source.ListAvailable(ctx)
	if err != nil {
		return core.NewTaskTree(nil)
	}
	return core.NewTaskTree(tasks)
}

func (r *Reconciler) taskByID(ctx context.Context, id core.TaskID) (core.Task, bool) {
	return r.taskTree(ctx).Task(id)
}

func (r *Reconciler) epicTask(ctx context.Context, id core.TaskID) (core.Task, bool) {
	task, ok := r.taskByID(ctx, id)
	return task, ok && task.Kind == core.TaskKindEpic
}

// baseBranchFor returns the branch a run's pull request targets. Children of
// an epic target the epic branch; other runs target the configured base.
func (r *Reconciler) baseBranchFor(ctx context.Context, run *core.Run) string {
	return r.baseBranchForTree(r.taskTree(ctx), run)
}

// baseBranchForTree answers the same question from a tree the caller built
// once. Callers that inspect many runs use it to read the card source once
// instead of once per run. Duplicate IDs and missing cards resolve the same
// way as baseBranchFor because both use the same indexed snapshot.
func (r *Reconciler) baseBranchForTree(tree core.TaskTree, run *core.Run) string {
	task, ok := tree.Task(run.TaskID)
	if !ok {
		return r.BaseBranch
	}
	parent, ok := tree.Parent(task.ID)
	if !ok || parent.Kind != core.TaskKindEpic {
		return r.BaseBranch
	}
	return epicBranch(parent.ID)
}

// reconcileEpics opens a pull request for each epic whose children are done.
// The epic is ready only after all of its child tasks reached done status.
func (r *Reconciler) reconcileEpics(ctx context.Context) error {
	ready, err := r.Store.ListReadyTasks(ctx)
	if err != nil {
		return err
	}
	tree := r.taskTree(ctx)
	for _, task := range ready {
		epic, ok := tree.Task(task.ID)
		if !ok {
			epic = task
		}
		if epic.Kind != core.TaskKindEpic {
			continue
		}
		if err := r.openEpicPR(ctx, epic); err != nil {
			slog.Error("open epic PR", "task", epic.ID, "error", err)
		}
	}
	return nil
}

// openEpicPR publishes the epic branch against the main line. The delivery
// key makes repeated reconcile ticks harmless. Every path that does not finish
// binding the PR releases the key so a later tick can retry instead of stranding
// the epic behind a permanent claim.
func (r *Reconciler) openEpicPR(ctx context.Context, epic core.Task) error {
	key := "epic-pr:" + string(epic.ID)
	ok, err := r.Store.ClaimDelivery(ctx, key)
	if err != nil || !ok {
		return err
	}
	bound := false
	defer func() {
		if bound {
			return
		}
		if err := r.Store.ReleaseDelivery(ctx, key); err != nil {
			slog.Error("release epic PR delivery", "task", epic.ID, "error", err)
		}
	}()
	base := epic.BaseBranch
	if base == "" {
		base = r.BaseBranch
	}
	pr, err := r.SCM.OpenPR(ctx, epicBranch(epic.ID), base, epic.Title)
	if err != nil {
		return err
	}
	if pr <= 0 {
		return nil
	}
	if run, err := r.Store.FindActiveByTask(ctx, epic.ID); err != nil {
		return err
	} else if run != nil {
		if err := run.MarkExternal(pr); err != nil {
			return err
		}
		if run.Branch == "" {
			run.Branch = epicBranch(epic.ID)
		}
		if err := r.Store.Save(ctx, run); err != nil {
			return err
		}
		bound = true
		return nil
	}
	run := core.NewRun(core.RunID("epic-"+string(epic.ID)), epic.ID, time.Now().UTC())
	run.MaxAttempts = r.maxAttempts()
	run.Branch = epicBranch(epic.ID)
	if err := run.Apply(core.Event{Kind: core.EvStart}); err != nil {
		return err
	}
	if err := run.Apply(core.Event{Kind: core.EvPRRegistered, PRNumber: pr}); err != nil {
		return err
	}
	if err := r.Store.Save(ctx, run); err != nil {
		return err
	}
	r.signal(pr)
	bound = true
	return nil
}

// guardEpicReview applies the epic's review mode before a merge. The LLM
// reviewer starts once per observed commit and reports through approval.
func (r *Reconciler) guardEpicReview(ctx context.Context, run *core.Run, obs core.Observation) (bool, error) {
	epic, ok := r.epicTask(ctx, run.TaskID)
	if !ok {
		return true, nil
	}
	mode := epic.RequireReview
	if mode == "" {
		mode = core.ReviewModeLLM
	}
	if mode == core.ReviewModeLLM {
		r.maybeSpawnReviewer(ctx, run, obs)
	}
	return !r.Policy.ReviewRequired(mode, obs), nil
}

// maybeSpawnReviewer starts one LLM review session for an epic commit. The
// reviewer approves the run through beacon_control when the change passes.
func (r *Reconciler) maybeSpawnReviewer(ctx context.Context, run *core.Run, obs core.Observation) {
	if r.Harness == nil || obs.HeadSHA == "" {
		return
	}
	key := "llm-review:" + string(run.ID) + ":" + obs.HeadSHA
	ok, err := r.Store.ClaimDelivery(ctx, key)
	if err != nil || !ok {
		return
	}
	task := core.Task{
		ID:    core.TaskID("REVIEW-" + string(run.ID)),
		Title: fmt.Sprintf("Review epic PR #%d", obs.PRNumber),
	}
	prompt := fmt.Sprintf(
		"Review epic pull request #%d at SHA %s. If it passes, call beacon_control approve with run_id %s and sha %s.",
		obs.PRNumber, obs.HeadSHA, run.ID, obs.HeadSHA,
	)
	req := core.SpawnRequest{
		Task: task, RunID: core.RunID("review-" + string(run.ID)),
		Prompt: prompt, ParentSessionID: run.ParentSessionID,
	}
	if _, err := r.Harness.Spawn(ctx, req); err != nil {
		if releaseErr := r.Store.ReleaseDelivery(ctx, key); releaseErr != nil {
			slog.Error("release reviewer delivery", "run", run.ID, "error", releaseErr)
		}
		slog.Error("spawn epic reviewer", "run", run.ID, "error", err)
	}
}

func (r *Reconciler) mergeIfReviewed(ctx context.Context, run *core.Run, obs core.Observation, decision core.MergeDecision) (core.MergeDecision, error) {
	ok, err := r.guardEpicReview(ctx, run, obs)
	if err != nil {
		return decision, err
	}
	if !ok {
		return core.MergeDecision{Verdict: core.VerdictWait, Reason: "epic review pending"}, nil
	}
	return decision, r.merge(ctx, run, obs)
}

// finalizeExternal closes a run whose PR was merged or closed outside Beacon.
func (r *Reconciler) finalizeExternal(ctx context.Context, run *core.Run, obs core.Observation) error {
	merged := core.DecideFinalState(obs.PRState).Merged
	if err := run.ExternalFinalize(obs); err != nil {
		return err
	}
	if err := r.Store.Save(ctx, run); err != nil {
		return err
	}
	r.notifyParent(ctx, run, run.PRNumber, string(run.State))
	if merged && !run.IsFixer {
		if err := r.Source.Complete(ctx, run.TaskID); err != nil {
			slog.Error("complete externally merged task", "run", run.ID, "error", err)
		} else if err := r.Store.SetTaskStatus(ctx, run.TaskID, "done"); err != nil {
			slog.Error("set task status", "run", run.ID, "error", err)
		}
	}
	return r.cleanupRun(ctx, run)
}

func (r *Reconciler) cleanupRun(ctx context.Context, run *core.Run) error {
	if run.SessionID != "" {
		// The run is already finalized. A failed stop leaves a session that the
		// next orphan sweep can close, so cleanup stays best effort.
		bestEffort("cleanup session stop", func() error {
			return r.Harness.Stop(ctx, run.SessionID)
		}, "run", run.ID, "task", run.TaskID)
	}
	if run.WorkspacePath == "" {
		return nil
	}
	ws := core.Workspace{Path: run.WorkspacePath, Branch: run.Branch}
	clean, err := r.Workspace.Clean(ctx, ws)
	if err != nil {
		slog.Error("clean check", "run", run.ID, "error", err)
		return nil
	}
	if !clean {
		if !run.State.Terminal() {
			slog.Warn("worktree kept: uncommitted changes", "run", run.ID, "path", ws.Path)
			return nil
		}
		slog.Info("force removing worktree of terminal run", "run", run.ID, "path", ws.Path)
		bestEffort("force remove worktree", func() error {
			return r.Workspace.ForceRemove(ctx, ws)
		}, "run", run.ID)
		return nil
	}
	bestEffort("remove worktree", func() error {
		return r.Workspace.Remove(ctx, ws)
	}, "run", run.ID)
	return nil
}
