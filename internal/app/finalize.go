package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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

func compact(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", ""))
	runes := []rune(s)
	if len(runes) <= 1200 {
		return s
	}
	return string(runes[:1200]) + "\n...[truncated]"
}

// ReviewRule gates an epic PR on review approval. A missing review mode means
// the LLM reviewer, matching the default on the task card.
type ReviewRule struct{ Mode core.ReviewMode }

func (r ReviewRule) Name() string { return "review" }

func (r ReviewRule) Evaluate(o core.Observation) (core.Verdict, string) {
	if r.Mode == core.ReviewModeNone {
		return core.VerdictNoop, ""
	}
	if o.Approvals > 0 {
		return core.VerdictNoop, ""
	}
	return core.VerdictWait, "epic review approval required"
}

const epicBranchPrefix = "beacon/"

func epicBranch(id core.TaskID) string { return epicBranchPrefix + string(id) }

// parentEpicID returns the epic ID encoded in a child task ID of the form
// <epic>-NN. IDs that do not end in a two digit suffix have no parent epic.
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

func (r *Reconciler) taskByID(ctx context.Context, id core.TaskID) (core.Task, bool) {
	if r.Source == nil {
		return core.Task{}, false
	}
	tasks, err := r.Source.ListAvailable(ctx)
	if err != nil {
		return core.Task{}, false
	}
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return core.Task{}, false
}

func (r *Reconciler) epicTask(ctx context.Context, id core.TaskID) (core.Task, bool) {
	task, ok := r.taskByID(ctx, id)
	return task, ok && task.Kind == core.TaskKindEpic
}

// baseBranchFor returns the branch a run's pull request targets. Children of
// an epic target the epic branch; other runs target the configured base.
func (r *Reconciler) baseBranchFor(ctx context.Context, run *core.Run) string {
	if epicID := parentEpicID(run.TaskID); epicID != "" {
		if _, ok := r.epicTask(ctx, epicID); ok {
			return epicBranch(epicID)
		}
	}
	return r.BaseBranch
}

// reconcileEpics opens a pull request for each epic whose children are done.
// The epic is ready only after all of its child tasks reached done status.
func (r *Reconciler) reconcileEpics(ctx context.Context) error {
	ready, err := r.Store.ListReadyTasks(ctx)
	if err != nil {
		return err
	}
	for _, task := range ready {
		if task.Kind != core.TaskKindEpic {
			continue
		}
		if err := r.openEpicPR(ctx, task); err != nil {
			slog.Error("open epic PR", "task", task.ID, "error", err)
		}
	}
	return nil
}

// openEpicPR publishes the epic branch against the main line. The delivery
// key makes repeated reconcile ticks harmless.
func (r *Reconciler) openEpicPR(ctx context.Context, epic core.Task) error {
	key := "epic-pr:" + string(epic.ID)
	ok, err := r.Store.ClaimDelivery(ctx, key)
	if err != nil || !ok {
		return err
	}
	base := epic.BaseBranch
	if base == "" {
		base = r.BaseBranch
	}
	pr, err := r.SCM.OpenPR(ctx, epicBranch(epic.ID), base, epic.Title)
	if err != nil {
		_ = r.Store.ReleaseDelivery(ctx, key)
		return err
	}
	if pr <= 0 {
		return nil
	}
	if run, err := r.Store.FindActiveByTask(ctx, epic.ID); err != nil {
		return err
	} else if run != nil {
		run.PRNumber = pr
		if run.Branch == "" {
			run.Branch = epicBranch(epic.ID)
		}
		return r.Store.Save(ctx, run)
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
	verdict, _ := ReviewRule{Mode: mode}.Evaluate(obs)
	return verdict == core.VerdictNoop, nil
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
		_ = r.Store.ReleaseDelivery(ctx, key)
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
	merged := obs.PRState == "merged"
	run.State = core.RunMerged
	if !merged {
		run.State = core.RunClosed
	}
	if obs.HeadSHA != "" {
		run.HeadSHA = obs.HeadSHA
	}
	if merged {
		run.Reason = fmt.Sprintf("merged PR #%d via squash", obs.PRNumber)
	} else {
		run.Reason = "pull request " + obs.PRState
	}
	run.UpdatedAt = time.Now().UTC()
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
		_ = r.Harness.Stop(ctx, run.SessionID)
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
		if err := r.Workspace.ForceRemove(ctx, ws); err != nil {
			slog.Error("force remove worktree", "run", run.ID, "error", err)
		}
		return nil
	}
	if err := r.Workspace.Remove(ctx, ws); err != nil {
		slog.Error("remove worktree", "run", run.ID, "error", err)
	}
	return nil
}
