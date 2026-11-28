package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/refusedguy/beacon/pkg/core"
)

const (
	operatorNotifyTimeout = 10 * time.Second
	parentNotifyTimeout   = 30 * time.Second

	// parentNotifyInlineWindow lets an in-memory harness finish before the
	// caller returns. A slow endpoint hands off to a goroutine after the
	// window, so it cannot hold up a reconcile tick.
	parentNotifyInlineWindow = 2 * time.Millisecond
)

// ErrParentNotifyUnsupported reports a harness with no parent inbox. Such a
// harness cannot deliver the notice, so the decision log must not claim it was
// sent. Harnesses that support delivery return nil or a real transport error.
var ErrParentNotifyUnsupported = errors.New("parent notification unsupported")

// parentNotifySupport is the optional capability a harness implements to
// declare whether it can deliver a message to an orchestrator session.
type parentNotifySupport interface {
	ParentNotifySupported() bool
}

// notify sends one operator notice per run and subject. The delivery claim is
// taken before the send, so repeated reconcile ticks cannot repeat the same
// notice. A failed send releases the claim for a later retry.
func (r *Reconciler) notify(run *core.Run, subject, body string) error {
	if r.Notifier == nil {
		return nil
	}
	key := "notify:" + string(run.ID) + ":" + subject
	if r.Store != nil {
		ok, err := r.Store.ClaimDelivery(context.Background(), key)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), operatorNotifyTimeout)
	defer cancel()
	err := r.Notifier.Notify(ctx, core.Notification{RunID: run.ID, Subject: subject, Body: compact(body)})
	if err != nil && r.Store != nil {
		if releaseErr := r.Store.ReleaseDelivery(context.Background(), key); releaseErr != nil {
			slog.Error("release notify delivery", "run", run.ID, "subject", subject, "error", releaseErr)
		}
	}
	return err
}

// notifyParent claims the terminal parent notice before handing the post to a
// goroutine. The claim is what prevents a forced reconcile and the daemon tick
// from posting the same notice twice. Text comes from the persisted run, so an
// orphan with no pull request never reports PR #0.
func (r *Reconciler) notifyParent(ctx context.Context, run *core.Run, pr int, reason string) {
	if run.ParentSessionID == "" || r.Harness == nil {
		return
	}
	key := "parent-notify:" + string(run.ID) + ":" + reason
	if r.Store != nil {
		ok, err := r.Store.ClaimDelivery(ctx, key)
		if err != nil {
			slog.Error("claim parent notify delivery", "run", run.ID, "error", err)
			return
		}
		if !ok {
			return
		}
	}
	if support, ok := r.Harness.(parentNotifySupport); ok && !support.ParentNotifySupported() {
		r.recordParentNotify(ctx, run, pr, key, ErrParentNotifyUnsupported)
		return
	}
	fresh := run
	if r.Store != nil {
		if stored, err := r.Store.Get(ctx, run.ID); err == nil && stored != nil {
			fresh = stored
		}
	}
	text := parentNoticeText(fresh, pr, reason)
	session := run.ParentSessionID
	done := make(chan error, 1)
	go func() {
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), parentNotifyTimeout)
		defer cancel()
		done <- r.Harness.NotifyParent(sendCtx, session, text)
	}()
	select {
	case err := <-done:
		r.recordParentNotify(ctx, run, pr, key, err)
	case <-time.After(parentNotifyInlineWindow):
		// The post is still in flight. Record only that it was queued, never
		// that it was delivered, and let the sender release a failed claim.
		r.Decisions.logTransitionParentNotify(ctx, run.ID, pr, "queued")
		go func() {
			err := <-done
			r.finishParentNotify(context.WithoutCancel(ctx), run, pr, key, err)
		}()
	}
}

// parentNoticeText builds the notice from the run's persisted state. A zero PR
// is omitted, and the caller's reason is kept alongside the recorded reason so
// a blocked run is not described as a state of its own.
func parentNoticeText(run *core.Run, pr int, reason string) string {
	var b strings.Builder
	b.WriteString("Beacon: ")
	if pr > 0 {
		fmt.Fprintf(&b, "PR #%d ", pr)
	}
	fmt.Fprintf(&b, "run %s state %s", run.ID, run.State)
	if run.Reason != "" {
		fmt.Fprintf(&b, " reason %s", run.Reason)
	}
	if reason != "" && reason != run.Reason && !strings.Contains(run.Reason, reason) {
		fmt.Fprintf(&b, " (%s)", reason)
	}
	return b.String()
}

func (r *Reconciler) recordParentNotify(ctx context.Context, run *core.Run, pr int, key string, err error) {
	result := "notified"
	if err != nil {
		result = "failed"
		if errors.Is(err, ErrParentNotifyUnsupported) || strings.Contains(err.Error(), "unsupported") {
			result = "unsupported"
		}
	}
	r.Decisions.logTransitionParentNotify(ctx, run.ID, pr, result)
	r.finishParentNotify(ctx, run, pr, key, err)
}

// finishParentNotify releases a failed claim so a later path can retry. A
// terminal run is no longer returned by FindByPR, so its failure stays as the
// recorded decision rather than an unreachable retry.
func (r *Reconciler) finishParentNotify(ctx context.Context, run *core.Run, pr int, key string, err error) {
	if err == nil || r.Store == nil || run.State.Terminal() {
		return
	}
	if releaseErr := r.Store.ReleaseDelivery(ctx, key); releaseErr != nil {
		slog.Error("release parent notify delivery", "run", run.ID, "pr", pr, "error", releaseErr)
	}
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

// BeaconBranchPrefix is the namespace every Beacon branch lives under.
const BeaconBranchPrefix = "beacon/"

// epicBranchPrefix keeps the former name for callers that still use it.
const epicBranchPrefix = BeaconBranchPrefix

// unsafeBranchChars matches characters a git ref may not contain. Every
// generated ref passes through one sanitizer so the names stay byte identical.
var unsafeBranchChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// BranchSanitize sets the sanitize scope for BeaconBranch. Epic callers build a
// raw ref and rely on the workspace to sanitize it before publishing, so the
// scope has to be explicit at each call site.
type BranchSanitize bool

const (
	// RawBranch keeps the id verbatim.
	RawBranch BranchSanitize = false
	// SafeBranch replaces characters a git ref rejects with dashes.
	SafeBranch BranchSanitize = true
)

// BeaconBranch prefixes id with beacon/ and, when the scope is SafeBranch,
// sanitizes id. The prefix is added here so no caller can forget it.
func BeaconBranch(id string, sanitize BranchSanitize) string {
	if sanitize == SafeBranch {
		id = unsafeBranchChars.ReplaceAllString(id, "-")
	}
	return BeaconBranchPrefix + id
}

// IsEpicBranch reports whether branch names a Beacon epic or child ref. Tasks
// on the configured base carry no prefix and need no branch publish.
func IsEpicBranch(branch string) bool {
	return strings.HasPrefix(strings.TrimSpace(branch), BeaconBranchPrefix)
}

func epicBranch(id core.TaskID) string { return BeaconBranch(string(id), RawBranch) }

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

// tickCacheKey carries one tick's reads through context. Reconciliation asks
// the same questions many times on a single tick: which epic owns a task,
// what branch a run targets and whether the base is red. The cache lets every
// path on the tick share one source read and one read per distinct base branch.
type tickCacheKey struct{}

type tickCache struct {
	tree      core.TaskTree
	treeReady bool
	base      map[string]core.BaseStatus
}

func withTickCache(ctx context.Context) context.Context {
	if cacheFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, tickCacheKey{}, &tickCache{base: make(map[string]core.BaseStatus)})
}

func cacheFrom(ctx context.Context) *tickCache {
	c, _ := ctx.Value(tickCacheKey{}).(*tickCache)
	return c
}

// taskTree indexes one source read. Every relation question in reconciliation
// goes through it, so the parent field is read once instead of scanning IDs.
// A tick cache turns the read into one per tick rather than one per question.
func (r *Reconciler) taskTree(ctx context.Context) core.TaskTree {
	if c := cacheFrom(ctx); c != nil && c.treeReady {
		return c.tree
	}
	tree := r.loadTaskTree(ctx)
	if c := cacheFrom(ctx); c != nil {
		c.tree, c.treeReady = tree, true
	}
	return tree
}

func (r *Reconciler) loadTaskTree(ctx context.Context) core.TaskTree {
	if r.Source == nil {
		return core.NewTaskTree(nil)
	}
	tasks, err := r.Source.ListAvailable(ctx)
	if err != nil {
		return core.NewTaskTree(nil)
	}
	return core.NewTaskTree(tasks)
}

// baseStatus returns the status of branch, reading SCM at most once per
// distinct branch on a tick. checkBaseRed and checkBaseRecovery often inspect
// the same branch during one tick, so the second read reuses the first result.
func (r *Reconciler) baseStatus(ctx context.Context, branch string) (core.BaseStatus, error) {
	if c := cacheFrom(ctx); c != nil {
		if status, ok := c.base[branch]; ok {
			return status, nil
		}
	}
	status, err := r.SCM.BaseBranchStatus(ctx, branch)
	if err != nil {
		return core.BaseStatus{}, err
	}
	if c := cacheFrom(ctx); c != nil {
		c.base[branch] = status
	}
	return status, nil
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
	epic, isEpic := r.epicTask(ctx, run.TaskID)
	return r.guardEpicReviewTask(ctx, epic, isEpic, run, obs)
}

// guardEpicReviewTask is the review gate for a caller that already looked up
// the epic on the shared tree. It avoids a second lookup on the merge path.
func (r *Reconciler) guardEpicReviewTask(ctx context.Context, epic core.Task, isEpic bool, run *core.Run, obs core.Observation) (bool, error) {
	if !isEpic {
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
	epic, isEpic := r.epicTask(ctx, run.TaskID)
	ok, err := r.guardEpicReviewTask(ctx, epic, isEpic, run, obs)
	if err != nil {
		return decision, err
	}
	if !ok {
		return core.MergeDecision{Verdict: core.VerdictWait, Reason: "epic review pending"}, nil
	}
	return decision, r.merge(ctx, run, obs, isEpic)
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
