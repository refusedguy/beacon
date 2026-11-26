package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// TaskGraphStore is the store capability TaskCreator uses to sync the task graph.
type TaskGraphStore interface {
	SyncTasks(ctx context.Context, tasks []core.Task) error
}

// TaskRemover is the optional source capability that deletes one card by ID.
type TaskRemover interface {
	Remove(ctx context.Context, id core.TaskID) error
}

// TaskBatchCreator is the optional source capability that writes several cards in one call.
type TaskBatchCreator interface {
	CreateMany(ctx context.Context, tasks []core.Task) ([]core.Task, error)
}

// CardWriter is the source role TaskCreator uses for cards. It writes a whole
// graph in one call and lists what the source already knows.
type CardWriter interface {
	Available(ctx context.Context) ([]core.Task, error)
	Write(ctx context.Context, tasks []core.Task) ([]core.Task, error)
	Remove(ctx context.Context, ids ...core.TaskID)
}

// sourceCardWriter adapts core.TaskSource and its optional batch and remove
// capabilities to CardWriter. Removal is best effort: a failed rollback must
// not hide the original create error.
type sourceCardWriter struct{ Source core.TaskSource }

// Available returns the cards the source lists as available.
func (w sourceCardWriter) Available(ctx context.Context) ([]core.Task, error) {
	return w.Source.ListAvailable(ctx)
}

// Write creates the cards through the source's batch capability, or one by one.
func (w sourceCardWriter) Write(ctx context.Context, tasks []core.Task) ([]core.Task, error) {
	if batch, ok := w.Source.(TaskBatchCreator); ok {
		return batch.CreateMany(ctx, tasks)
	}
	out := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		created, err := w.Source.Create(ctx, task)
		if err != nil {
			return out, err
		}
		out = append(out, created)
	}
	return out, nil
}

// Remove deletes each card through the source's remove capability when it has one.
func (w sourceCardWriter) Remove(ctx context.Context, ids ...core.TaskID) {
	remover, ok := w.Source.(TaskRemover)
	if !ok {
		return
	}
	for _, id := range ids {
		_ = remover.Remove(ctx, id)
	}
}

// CreateTaskInput is the JSON body accepted by the task-create operation.
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

// TaskWorkspace is the workspace surface TaskCreator uses. It composes the
// commit capability with branch creation, so an epic can publish its base
// branch without discovering the capability by assertion at use.
type TaskWorkspace interface {
	PathCommitter
	BranchEnsurer
}

// TaskCreator writes cards through the source, then commits and pushes them.
// An epic with a plan expands into deterministic child cards before the epic.
type TaskCreator struct {
	Source    core.TaskSource
	Workspace TaskWorkspace
	Root      core.Workspace
	Store     TaskGraphStore
	// Date overrides the date used by generated random IDs. Derived epic and
	// batch IDs ignore it, so setting it cannot change a content-derived ID.
	Date string

	Writer    CardWriter
	Publisher CardPublisher
	IDs       IDFactory
	Cache     IdempotencyCache
}

func (c *TaskCreator) writer() CardWriter {
	if c.Writer != nil {
		return c.Writer
	}
	return sourceCardWriter{Source: c.Source}
}

func (c *TaskCreator) publisher() CardPublisher {
	if c.Publisher != nil {
		return c.Publisher
	}
	return pathCardPublisher{ws: c.Workspace, root: c.Root}
}

func (c *TaskCreator) ids() IDFactory {
	if c.IDs != nil {
		return c.IDs
	}
	return defaultIDFactory{Date: c.Date}
}

// Create writes one card, or an epic with its planned children, syncs the task
// graph and publishes the cards in one commit. An epic call with a known ID
// returns the cached result instead of writing the cards again.
func (c *TaskCreator) Create(ctx context.Context, in CreateTaskInput) (CreatedTask, error) {
	var out CreatedTask
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return out, core.Errorf(core.CodeInvalidInput, "task title is required")
	}
	kind := core.NormalizeTaskKind(in.Kind)
	epic := kind == core.TaskKindEpic
	id := core.TaskID(strings.TrimSpace(in.ID))
	if id == "" && epic {
		id = c.ids().ForEpic(title, in.Plan)
	}
	if id == "" {
		var err error
		if id, err = c.ids().New(); err != nil {
			return out, err
		}
	}
	children := epicChildren(id, in.Plan)
	key := idempotencyKey(id, title, in.Plan)
	writer := c.writer()
	if epic {
		if existing, ok := lookupCreated(ctx, writer, &c.Cache, key, id, children); ok {
			return c.result(existing), nil
		}
		// Children fetch this branch before the epic opens a PR, so publish it
		// before any card is written. A failure leaves no half-created epic.
		if err := ensureEpicBranch(ctx, c.Workspace, epicBranch(id), publishBase(c.Root)); err != nil {
			return out, fmt.Errorf("create epic branch: %w", err)
		}
	}

	task := core.Task{
		ID: id, Title: title, Body: strings.TrimSpace(in.Body),
		Scope: taskScopes(in.Scope), DependsOn: taskDependencies(in.Depends),
		Priority: in.Priority, Status: core.TaskAvailable,
		Kind: kind, BaseBranch: strings.TrimSpace(in.BaseBranch),
		RequireReview: core.NormalizeReviewMode(in.RequireReview), Plan: in.Plan,
		SourceRef: cardPath(id),
	}
	if epic {
		for _, child := range children {
			task.DependsOn = append(task.DependsOn, child.ID)
		}
	}
	graph := append(append([]core.Task{}, children...), task)

	created, err := writer.Write(ctx, graph)
	if err != nil {
		rollbackCards(ctx, writer, created)
		return out, err
	}
	if c.Store != nil {
		available, err := writer.Available(ctx)
		if err != nil {
			rollbackCards(ctx, writer, created)
			return out, err
		}
		if err := c.Store.SyncTasks(ctx, withCreated(available, created)); err != nil {
			rollbackCards(ctx, writer, created)
			return out, err
		}
	}
	if err := c.publisher().Publish(ctx, cardsPath, "beacon: create task "+string(id)); err != nil {
		rollbackCards(ctx, writer, created)
		return out, err
	}
	if epic {
		c.Cache.RememberCreated(key, created)
	}
	return c.result(created), nil
}

// ensureEpicBranch publishes an epic base branch through the composed workspace
// injected into TaskCreator. Tasks on the configured base need no branch work.
func ensureEpicBranch(ctx context.Context, ws TaskWorkspace, branch, base string) error {
	branch = strings.TrimSpace(branch)
	if !strings.HasPrefix(branch, epicBranchPrefix) {
		return nil
	}
	return ws.EnsureBranch(ctx, branch, base)
}

// epicChildren gives an epic's plan the branch that its children branch from.
// The expansion itself lives in core and is shared with the card parser.
func epicChildren(id core.TaskID, plan []core.ChildSpec) []core.Task {
	return core.SetParent(core.ExpandChildren(core.Task{ID: id, Plan: plan}, epicBranch(id)), id)
}

// rollbackCards removes the cards written before a single create failed.
func rollbackCards(ctx context.Context, writer CardWriter, tasks []core.Task) {
	if len(tasks) == 0 {
		return
	}
	opCtx, cancel := detachedContext(ctx)
	defer cancel()
	ids := make([]core.TaskID, 0, len(tasks))
	for i := len(tasks) - 1; i >= 0; i-- {
		ids = append(ids, tasks[i].ID)
	}
	writer.Remove(opCtx, ids...)
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
