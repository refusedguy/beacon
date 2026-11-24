package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// PathCommitter is the part of the workspace port that publishes a new card.
type PathCommitter interface {
	CommitPath(ctx context.Context, ws core.Workspace, pathspec, message string) error
}

// BaseRebaser is an optional workspace capability. GitWorktree implements it
// so publishing a card syncs a stale local base with origin before pushing.
// Fakes that implement it let tests observe the rebase and the push retry.
type BaseRebaser interface {
	RebaseBase(ctx context.Context, ws core.Workspace, base string) error
}

// BranchEnsurer is an optional workspace capability. GitWorktree implements it
// so an epic branch exists on origin before any child of that epic dispatches.
type BranchEnsurer interface {
	EnsureBranch(ctx context.Context, branch, base string) error
}
type TaskGraphStore interface {
	SyncTasks(ctx context.Context, tasks []core.Task) error
}
type TaskRemover interface {
	Remove(ctx context.Context, id core.TaskID) error
}
type TaskBatchCreator interface {
	CreateMany(ctx context.Context, tasks []core.Task) ([]core.Task, error)
}

// NonFastForwardError marks a push rejected because the remote ref advanced.
// A caller retries the publish once after rebasing on the latest origin base.
type NonFastForwardError struct{ Err error }

func (e *NonFastForwardError) Error() string {
	if e.Err == nil {
		return "push rejected as non-fast-forward"
	}
	return "push rejected as non-fast-forward: " + e.Err.Error()
}

func (e *NonFastForwardError) Unwrap() error { return e.Err }

type CreateTaskInput struct {
	Title         string           `json:"title"`
	Body          string           `json:"body"`
	Scope         []string         `json:"scope"`
	Depends       []string         `json:"depends"`
	Priority      int              `json:"priority"`
	ID            string           `json:"id,omitempty"`
	Kind          core.TaskKind    `json:"kind,omitempty"`
	BaseBranch    string           `json:"base_branch,omitempty"`
	RequireReview core.ReviewMode  `json:"require_review,omitempty"`
	Plan          []core.ChildSpec `json:"plan,omitempty"`
}

type CreatedTask struct {
	ID          string        `json:"id"`
	Path        string        `json:"path"`
	Pushed      bool          `json:"pushed"`
	IDs         []string      `json:"ids,omitempty"`
	Children    []CreatedTask `json:"children,omitempty"`
	Status      string        `json:"status,omitempty"`
	OperationID string        `json:"operation_id,omitempty"`
}

// CreatedCard is one entry in a batch create response.
type CreatedCard struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// CreateBatchResult is the response for a batch task creation.
type CreateBatchResult struct {
	Created     []CreatedCard `json:"created"`
	Pushed      bool          `json:"pushed"`
	Status      string        `json:"status,omitempty"`
	OperationID string        `json:"operation_id,omitempty"`
}

// detachedPushTimeout bounds a push that must finish after the caller's
// context was cancelled. A committed card must not stay unpublished.
const detachedPushTimeout = 30 * time.Second

// TaskCreator writes cards through the source, then commits and pushes them.
// An epic with a plan expands into deterministic child cards before the epic.
type TaskCreator struct {
	Source    TaskSource
	Workspace PathCommitter
	Root      core.Workspace
	Store     TaskGraphStore

	mu      sync.Mutex
	created map[string][]core.Task
	batch   map[core.TaskID]CreatedTask
}

func (c *TaskCreator) Create(ctx context.Context, in CreateTaskInput) (CreatedTask, error) {
	var out CreatedTask
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return out, core.Errorf(core.CodeInvalidInput, "task title is required")
	}
	kind := normalizeKind(in.Kind)
	epic := kind == core.TaskKindEpic
	id := core.TaskID(strings.TrimSpace(in.ID))
	if id == "" && epic {
		id = epicTaskID(title, in.Plan)
	}
	if id == "" {
		var err error
		if id, err = newTaskID(); err != nil {
			return out, err
		}
	}
	children := childTasks(id, in.Plan)
	key := idempotencyKey(id, children)
	if epic {
		if existing, ok := c.lookup(ctx, key, id, children); ok {
			return c.result(existing), nil
		}
		// Children fetch this branch before the epic opens a PR, so publish it
		// before any card is written. A failure leaves no half-created epic.
		if err := ensureBranch(ctx, c.Workspace, epicBranch(id), publishBase(c.Root)); err != nil {
			return out, fmt.Errorf("create epic branch: %w", err)
		}
	}

	task := core.Task{
		ID: id, Title: title, Body: strings.TrimSpace(in.Body),
		Scope: taskScopes(in.Scope), DependsOn: taskDependencies(in.Depends),
		Priority: in.Priority, Status: core.TaskAvailable,
		Kind: kind, BaseBranch: strings.TrimSpace(in.BaseBranch),
		RequireReview: normalizeReview(in.RequireReview), Plan: in.Plan,
		SourceRef: "docs/tasks/" + string(id) + ".md",
	}
	if epic {
		for _, child := range children {
			task.DependsOn = append(task.DependsOn, child.ID)
		}
	}
	graph := append(append([]core.Task{}, children...), task)

	created, err := c.writeTasks(ctx, graph)
	if err != nil {
		c.rollback(ctx, created)
		return out, err
	}
	if c.Store != nil {
		available, err := c.Source.ListAvailable(ctx)
		if err != nil {
			c.rollback(ctx, created)
			return out, err
		}
		if err := c.Store.SyncTasks(ctx, withCreated(available, created)); err != nil {
			c.rollback(ctx, created)
			return out, err
		}
	}
	if err := c.publish(ctx, id); err != nil {
		c.rollback(ctx, created)
		return out, err
	}
	if epic {
		c.mu.Lock()
		if c.created == nil {
			c.created = make(map[string][]core.Task)
		}
		c.created[key] = created
		c.mu.Unlock()
	}
	return c.result(created), nil
}

// CreateBatch writes the given cards in order and returns one result per input.
// IDs are derived from the batch contents, so repeating a call returns the
// same cards instead of creating duplicates. A depends entry of the form @N
// resolves to the ID of the Nth card in this batch (1-based) and must name an
// earlier card. If any step fails, every card written by this batch is removed.
func (c *TaskCreator) CreateBatch(ctx context.Context, inputs []CreateTaskInput) ([]CreatedTask, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	for _, in := range inputs {
		if id := strings.TrimSpace(in.ID); id != "" && !validTaskID(id) {
			return nil, core.Errorf(core.CodeInvalidInput, "invalid task id %q", id)
		}
	}
	ids := c.batchIDs(inputs)
	resolved := make([]CreateTaskInput, len(inputs))
	for i, in := range inputs {
		in.ID = string(ids[i])
		deps := make([]string, 0, len(in.Depends))
		for _, dep := range in.Depends {
			dep = strings.TrimSpace(dep)
			if n, ok := localRefIndex(dep); ok {
				if n < 1 || n > i {
					return nil, core.Errorf(core.CodeInvalidInput,
						"depends %q in task %d must refer to an earlier task in this batch", dep, i+1)
				}
				dep = string(ids[n-1])
			}
			deps = append(deps, dep)
		}
		in.Depends = deps
		resolved[i] = in
	}

	created := make([]CreatedTask, 0, len(inputs))
	written := make([]core.Task, 0, len(inputs))
	for i, in := range resolved {
		if existing, ok := c.existingCard(ctx, ids[i]); ok {
			created = append(created, existing)
			continue
		}
		out, err := c.Create(ctx, in)
		if err != nil {
			c.rollback(ctx, written)
			return nil, err
		}
		if len(out.IDs) == 0 {
			written = append(written, core.Task{ID: core.TaskID(out.ID), SourceRef: out.Path})
		}
		for _, id := range out.IDs {
			written = append(written, core.Task{ID: core.TaskID(id), SourceRef: "docs/tasks/" + id + ".md"})
		}
		created = append(created, out)
	}
	c.rememberBatch(ids, created)
	return created, nil
}

// batchIDs assigns a stable ID to each input. An explicit ID wins. An epic
// without one keeps the derivation Create uses, so an epic made alone or in a
// batch gets the same ID.
func (c *TaskCreator) batchIDs(inputs []CreateTaskInput) []core.TaskID {
	seed := batchSeed(inputs)
	out := make([]core.TaskID, len(inputs))
	for i, in := range inputs {
		if id := strings.TrimSpace(in.ID); id != "" {
			out[i] = core.TaskID(id)
			continue
		}
		if normalizeKind(in.Kind) == core.TaskKindEpic {
			out[i] = epicTaskID(in.Title, in.Plan)
			continue
		}
		out[i] = batchTaskID(seed, i)
	}
	return out
}

func batchSeed(inputs []CreateTaskInput) string {
	var b strings.Builder
	for _, in := range inputs {
		b.WriteString(in.Title)
		b.WriteByte('\x00')
		b.WriteString(in.Body)
		b.WriteByte('\x00')
		for _, scope := range in.Scope {
			b.WriteString(scope)
			b.WriteByte('\x1f')
		}
		b.WriteByte('\x00')
		for _, dep := range in.Depends {
			b.WriteString(dep)
			b.WriteByte('\x1f')
		}
		b.WriteByte('\x00')
		b.WriteString(strconv.Itoa(in.Priority))
		b.WriteByte('\x00')
		b.WriteString(string(in.Kind))
		b.WriteByte('\x00')
		b.WriteString(in.BaseBranch)
		b.WriteByte('\x00')
		b.WriteString(string(in.RequireReview))
		b.WriteByte('\x00')
		for _, child := range in.Plan {
			b.WriteString(child.Title)
			b.WriteByte('\x1f')
		}
		b.WriteByte('\x1e')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func batchTaskID(seed string, index int) core.TaskID {
	sum := sha256.Sum256([]byte(seed + "\x00" + strconv.Itoa(index)))
	return core.TaskID("T-" + time.Now().UTC().Format("20060102") + "-" + hex.EncodeToString(sum[:4]))
}

var localRefPattern = regexp.MustCompile(`^@([1-9][0-9]*)$`)

func localRefIndex(value string) (int, bool) {
	match := localRefPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, false
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

func (c *TaskCreator) existingCard(ctx context.Context, id core.TaskID) (CreatedTask, bool) {
	c.mu.Lock()
	out, ok := c.batch[id]
	c.mu.Unlock()
	if ok {
		return out, true
	}
	available, err := c.Source.ListAvailable(ctx)
	if err != nil {
		return CreatedTask{}, false
	}
	for _, task := range available {
		if task.ID == id {
			path := task.SourceRef
			if path == "" {
				path = "docs/tasks/" + string(id) + ".md"
			}
			return CreatedTask{ID: string(id), Path: path, Pushed: true}, true
		}
	}
	return CreatedTask{}, false
}

func (c *TaskCreator) rememberBatch(ids []core.TaskID, created []CreatedTask) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.batch == nil {
		c.batch = make(map[core.TaskID]CreatedTask)
	}
	for i, out := range created {
		if i >= len(ids) {
			break
		}
		c.batch[ids[i]] = out
	}
}

func (c *TaskCreator) publish(ctx context.Context, id core.TaskID) error {
	return publishPath(ctx, c.Workspace, c.Root, "docs/tasks", "beacon: create task "+string(id))
}

func (c *TaskCreator) writeTasks(ctx context.Context, tasks []core.Task) ([]core.Task, error) {
	if batch, ok := c.Source.(TaskBatchCreator); ok {
		return batch.CreateMany(ctx, tasks)
	}
	out := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		created, err := c.Source.Create(ctx, task)
		if err != nil {
			return out, err
		}
		out = append(out, created)
	}
	return out, nil
}

func (c *TaskCreator) rollback(ctx context.Context, tasks []core.Task) {
	remover, ok := c.Source.(TaskRemover)
	if !ok {
		return
	}
	opCtx, cancel := detachedContext(ctx)
	defer cancel()
	for i := len(tasks) - 1; i >= 0; i-- {
		_ = remover.Remove(opCtx, tasks[i].ID)
	}
}

// withCreated adds cards that the source has not listed yet to the full
// available set. SyncTasks replaces the whole graph, so dropping either side
// would hide cards from dispatch.
func withCreated(available, created []core.Task) []core.Task {
	out := make([]core.Task, 0, len(available)+len(created))
	seen := make(map[core.TaskID]bool, len(available)+len(created))
	for _, task := range available {
		if seen[task.ID] {
			continue
		}
		seen[task.ID] = true
		out = append(out, task)
	}
	for _, task := range created {
		if seen[task.ID] {
			continue
		}
		seen[task.ID] = true
		out = append(out, task)
	}
	return out
}

func (c *TaskCreator) lookup(ctx context.Context, key string, id core.TaskID, children []core.Task) ([]core.Task, bool) {
	c.mu.Lock()
	tasks, ok := c.created[key]
	c.mu.Unlock()
	if ok {
		return tasks, true
	}
	available, err := c.Source.ListAvailable(ctx)
	if err != nil {
		return nil, false
	}
	for _, task := range available {
		if task.ID != id || task.Kind != core.TaskKindEpic ||
			strings.Join(childTitles(task.Plan), "\x00") != strings.Join(taskTitles(children), "\x00") {
			continue
		}
		out := make([]core.Task, 0, len(children)+1)
		for _, child := range children {
			out = append(out, core.Task{ID: child.ID, SourceRef: "docs/tasks/" + string(child.ID) + ".md"})
		}
		return append(out, core.Task{ID: id, SourceRef: "docs/tasks/" + string(id) + ".md"}), true
	}
	return nil, false
}

func (c *TaskCreator) result(created []core.Task) CreatedTask {
	out := CreatedTask{Pushed: true, IDs: make([]string, 0, len(created))}
	for _, task := range created {
		out.IDs = append(out.IDs, string(task.ID))
	}
	if len(created) == 0 {
		return out
	}
	last := created[len(created)-1]
	out.ID, out.Path = string(last.ID), last.SourceRef
	for _, task := range created[:len(created)-1] {
		out.Children = append(out.Children, CreatedTask{ID: string(task.ID), Path: task.SourceRef})
	}
	return out
}

// publishPath rebases, commits pathspec and retries one non-fast-forward push.
// Creation, update and deletion share it. Cancellation before the commit aborts
// the publish so the caller can restore the cards. Cancellation after the
// commit cannot undo it, so the push and its retry finish with a bounded
// context detached from the caller.
func publishPath(ctx context.Context, ws PathCommitter, root core.Workspace, pathspec, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base := publishBase(root)
	if err := rebaseBase(ctx, ws, root, base); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ws.CommitPath(ctx, root, pathspec, message); err != nil {
		var nff *NonFastForwardError
		if !errors.As(err, &nff) {
			return err
		}
		return retryPublish(ctx, ws, root, pathspec, message, base)
	}
	return nil
}

func publishBase(root core.Workspace) string {
	if root.Base != "" {
		return root.Base
	}
	if root.Branch != "" {
		return root.Branch
	}
	return "main"
}

// retryPublish completes a push rejected as non-fast-forward. A cancelled
// caller must not leave a committed card unpublished, so the retry uses a
// detached bounded context.
func retryPublish(ctx context.Context, ws PathCommitter, root core.Workspace, pathspec, message, base string) error {
	opCtx, cancel := detachedContext(ctx)
	defer cancel()
	if err := rebaseBase(opCtx, ws, root, base); err != nil {
		return err
	}
	return ws.CommitPath(opCtx, root, pathspec, message)
}

// detachedContext returns the caller's context while it is live. Once it is
// cancelled, a bounded background context takes over so work that cannot be
// undone still finishes. The no-op cancel keeps callers uniform.
func detachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() != nil {
		return context.WithTimeout(context.Background(), detachedPushTimeout)
	}
	return ctx, func() {}
}

func rebaseBase(ctx context.Context, ws PathCommitter, root core.Workspace, base string) error {
	if rebaser, ok := ws.(BaseRebaser); ok {
		return rebaser.RebaseBase(ctx, root, base)
	}
	return nil
}

// ensureBranch publishes an epic base branch when the workspace supports it.
// Tasks on the configured base need no branch work, and a workspace without the
// capability keeps its previous behavior.
func ensureBranch(ctx context.Context, ws any, branch, base string) error {
	branch = strings.TrimSpace(branch)
	if !strings.HasPrefix(branch, epicBranchPrefix) {
		return nil
	}
	ensurer, ok := ws.(BranchEnsurer)
	if !ok {
		return nil
	}
	return ensurer.EnsureBranch(ctx, branch, base)
}

func childTasks(epicID core.TaskID, plan []core.ChildSpec) []core.Task {
	branch := epicBranch(epicID)
	out := make([]core.Task, 0, len(plan))
	for i, spec := range plan {
		out = append(out, core.Task{
			ID: core.ChildTaskID(epicID, i+1), Title: strings.TrimSpace(spec.Title),
			Body:  strings.TrimSpace(spec.Body),
			Scope: spec.Scope, DependsOn: append([]core.TaskID(nil), spec.Depends...),
			Priority: spec.Priority, Status: core.TaskAvailable,
			Kind: core.TaskKindTask, BaseBranch: branch,
		})
	}
	return out
}

func idempotencyKey(epicID core.TaskID, children []core.Task) string {
	return string(epicID) + "|" + strings.Join(taskTitles(children), "\x00")
}

func epicTaskID(title string, plan []core.ChildSpec) core.TaskID {
	sum := sha256.Sum256([]byte(title + "\x00" + strings.Join(childTitles(plan), "\x00")))
	return core.TaskID("T-" + time.Now().UTC().Format("20060102") + "-" + hex.EncodeToString(sum[:4]))
}

func childTitles(specs []core.ChildSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, strings.TrimSpace(spec.Title))
	}
	sort.Strings(out)
	return out
}

func taskTitles(tasks []core.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, strings.TrimSpace(task.Title))
	}
	sort.Strings(out)
	return out
}

func normalizeKind(kind core.TaskKind) core.TaskKind {
	if strings.EqualFold(strings.TrimSpace(string(kind)), string(core.TaskKindEpic)) {
		return core.TaskKindEpic
	}
	return core.TaskKindTask
}

func normalizeReview(mode core.ReviewMode) core.ReviewMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(core.ReviewModeHuman):
		return core.ReviewModeHuman
	case string(core.ReviewModeNone):
		return core.ReviewModeNone
	default:
		return core.ReviewModeLLM
	}
}

func taskScopes(values []string) []core.ScopePath {
	var out []core.ScopePath
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch {
		case strings.ContainsAny(value, "*?"):
			out = append(out, core.ScopePattern(value))
		case strings.HasSuffix(value, "/"):
			out = append(out, core.ScopeDir(value))
		default:
			out = append(out, core.ScopeFile(value))
		}
	}
	return out
}

func taskDependencies(values []string) []core.TaskID {
	var out []core.TaskID
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, core.TaskID(value))
		}
	}
	return out
}

func newTaskID() (core.TaskID, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	return core.TaskID("T-" + time.Now().UTC().Format("20060102") + "-" + hex.EncodeToString(b[:])), nil
}
