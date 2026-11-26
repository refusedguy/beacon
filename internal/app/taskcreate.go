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

// cardsPath is the repository pathspec for task cards. Creation and completion
// share it so no caller hardcodes where cards live.
const cardsPath = "docs/tasks"

// cardPath is the repository path of one card. It is the single place that
// turns a task ID into a file path.
func cardPath(id core.TaskID) string { return cardsPath + "/" + string(id) + ".md" }

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

// CardPublisher commits one pathspec. The workspace and root are bound at
// construction, so a caller names only what changed and why.
type CardPublisher interface {
	Publish(ctx context.Context, pathspec, message string) error
}

type pathCardPublisher struct {
	ws   PathCommitter
	root core.Workspace
}

// Publish commits pathspec in the bound workspace using the given message.
func (p pathCardPublisher) Publish(ctx context.Context, pathspec, message string) error {
	return publishPath(ctx, p.ws, p.root, pathspec, message)
}

// IDFactory assigns IDs to new cards. Epic IDs stay stable for the same title
// and plan, and batch IDs depend only on the batch contents. Neither derived
// ID reads the clock, so the same content gets the same ID on any day. New
// assigns an unconstrained random ID and is the only method that uses a date.
type IDFactory interface {
	New() (core.TaskID, error)
	ForEpic(title string, plan []core.ChildSpec) core.TaskID
	ForBatch(seed string, index int) core.TaskID
}

// defaultIDFactory carries an optional explicit date for New. An empty Date
// means today. Derived IDs ignore it.
type defaultIDFactory struct{ Date string }

// New returns a random task ID dated with f.Date, or today's date when it is empty.
func (f defaultIDFactory) New() (core.TaskID, error) { return newTaskID(f.Date) }

// ForEpic derives an epic ID from the title and the planned children.
func (defaultIDFactory) ForEpic(title string, plan []core.ChildSpec) core.TaskID {
	return epicTaskID(title, plan)
}

// ForBatch derives a card ID from the batch seed and the card's index.
func (defaultIDFactory) ForBatch(seed string, index int) core.TaskID {
	return batchTaskID(seed, index)
}

// IdempotencyCache remembers completed create operations so a repeated call
// returns the same cards without writing them again.
type IdempotencyCache struct {
	mu      sync.Mutex
	created map[string][]core.Task
	batch   map[core.TaskID]CreatedTask
}

// Created returns the cards remembered for key and whether the key was present.
func (c *IdempotencyCache) Created(key string) ([]core.Task, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tasks, ok := c.created[key]
	return tasks, ok
}

// RememberCreated stores the cards produced for key.
func (c *IdempotencyCache) RememberCreated(key string, tasks []core.Task) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.created == nil {
		c.created = make(map[string][]core.Task)
	}
	c.created[key] = tasks
}

// Batch returns the remembered results for the given card IDs.
func (c *IdempotencyCache) Batch(ids []core.TaskID) map[core.TaskID]CreatedTask {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[core.TaskID]CreatedTask, len(ids))
	for _, id := range ids {
		if task, ok := c.batch[id]; ok {
			out[id] = task
		}
	}
	return out
}

// RememberBatch stores one result per ID and ignores extra results.
func (c *IdempotencyCache) RememberBatch(ids []core.TaskID, results []CreatedTask) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.batch == nil {
		c.batch = make(map[core.TaskID]CreatedTask)
	}
	for i, out := range results {
		if i >= len(ids) {
			break
		}
		c.batch[ids[i]] = out
	}
}

// NonFastForwardError marks a push rejected because the remote ref advanced.
// A caller retries the publish once after rebasing on the latest origin base.
type NonFastForwardError struct{ Err error }

// Error returns the rejection message, including the wrapped push error when set.
func (e *NonFastForwardError) Error() string {
	if e.Err == nil {
		return "push rejected as non-fast-forward"
	}
	return "push rejected as non-fast-forward: " + e.Err.Error()
}

// Unwrap returns the underlying push error.
func (e *NonFastForwardError) Unwrap() error { return e.Err }

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

// OperationMeta is the idempotency metadata shared by every mutation response.
// Embedding it keeps the status and operation_id JSON fields in one place, so
// callers can set them without knowing the concrete result type.
type OperationMeta struct {
	Status      string `json:"status,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
}

// OperationResult is any mutation response that carries shared operation
// metadata. A new result type joins by embedding OperationMeta and adding a
// WithMeta method; no caller that handles OperationResult needs to change.
type OperationResult interface {
	WithMeta(id, status string) any
}

// CreatedTask is the response for one created card, with children for an epic.
type CreatedTask struct {
	ID       string        `json:"id"`
	Path     string        `json:"path"`
	Pushed   bool          `json:"pushed"`
	IDs      []string      `json:"ids,omitempty"`
	Children []CreatedTask `json:"children,omitempty"`
	OperationMeta
}

// WithMeta returns a copy of r carrying the operation id and status.
func (r CreatedTask) WithMeta(id, status string) any {
	r.Status, r.OperationID = status, id
	return r
}

// CreatedCard is one entry in a batch create response.
type CreatedCard struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// CreateBatchResult is the response for a batch task creation.
type CreateBatchResult struct {
	Created []CreatedCard `json:"created"`
	Pushed  bool          `json:"pushed"`
	OperationMeta
}

// WithMeta returns a copy of r carrying the operation id and status.
func (r CreateBatchResult) WithMeta(id, status string) any {
	r.Status, r.OperationID = status, id
	return r
}

// detachedPushTimeout bounds a push that must finish after the caller's
// context was cancelled. A committed card must not stay unpublished.
const detachedPushTimeout = 30 * time.Second

// TaskCreator writes cards through the source, then commits and pushes them.
// An epic with a plan expands into deterministic child cards before the epic.
type TaskCreator struct {
	Source    core.TaskSource
	Workspace PathCommitter
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
		if err := ensureBranch(ctx, c.Workspace, epicBranch(id), publishBase(c.Root)); err != nil {
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

// CreateBatch writes the given cards in order and returns one result per input.
// IDs are derived from the batch contents alone, so repeating a call on any day
// returns the same cards instead of creating duplicates. A depends entry of
// the form @N resolves to the ID of the Nth card in this batch (1-based) and
// must name an earlier card.
//
// The whole batch is one operation: every file is written first, then one
// graph is synced and one commit is published. A failure at any stage removes
// every written card and restores the synced graph, so no partial batch is
// observable on disk or by the dispatcher.
func (c *TaskCreator) CreateBatch(ctx context.Context, inputs []CreateTaskInput) ([]CreatedTask, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if err := validateBatchInputs(inputs); err != nil {
		return nil, err
	}
	ids := c.batchIDs(inputs)
	resolved, err := resolveBatchInputs(inputs, ids)
	if err != nil {
		return nil, err
	}

	writer := c.writer()
	available, existing, results := c.existingBatchCards(ctx, writer, ids)
	graph := make([]core.Task, 0, len(inputs))
	groups := make([]batchGroup, len(inputs))
	for i, in := range resolved {
		if existing[i] {
			continue
		}
		group := batchGroupTasks(in, ids[i])
		groups[i] = batchGroup{start: len(graph), end: len(graph) + len(group)}
		graph = append(graph, group...)
	}
	if len(graph) == 0 {
		c.Cache.RememberBatch(ids, results)
		return results, nil
	}
	if err := c.ensureBatchBranches(ctx, resolved, ids, existing); err != nil {
		return nil, err
	}

	written, err := writer.Write(ctx, graph)
	if err != nil {
		rollbackBatch(ctx, writer, c.Store, written, available, false)
		return nil, err
	}
	if len(written) != len(graph) {
		rollbackBatch(ctx, writer, c.Store, written, available, false)
		return nil, fmt.Errorf("created %d of %d cards", len(written), len(graph))
	}

	if c.Store != nil {
		if err := c.Store.SyncTasks(ctx, withCreated(available, written)); err != nil {
			rollbackBatch(ctx, writer, c.Store, written, available, true)
			return nil, err
		}
	}
	if err := c.publisher().Publish(ctx, cardsPath, "beacon: create task "+string(ids[0])); err != nil {
		rollbackBatch(ctx, writer, c.Store, written, available, c.Store != nil)
		return nil, err
	}

	for i, group := range groups {
		if existing[i] {
			continue
		}
		results[i] = c.result(written[group.start:group.end])
	}
	c.Cache.RememberBatch(ids, results)
	return results, nil
}

// batchGroup marks the slice of the written graph that belongs to one input.
type batchGroup struct{ start, end int }

func validateBatchInputs(inputs []CreateTaskInput) error {
	for _, in := range inputs {
		if id := strings.TrimSpace(in.ID); id != "" && !validTaskID(id) {
			return core.Errorf(core.CodeInvalidInput, "invalid task id %q", id)
		}
		if strings.TrimSpace(in.Title) == "" {
			return core.Errorf(core.CodeInvalidInput, "task title is required")
		}
	}
	return nil
}

func resolveBatchInputs(inputs []CreateTaskInput, ids []core.TaskID) ([]CreateTaskInput, error) {
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
	return resolved, nil
}

// batchGroupTasks builds the cards for one input. An epic contributes its
// children followed by the epic, matching the single-card Create flow.
func batchGroupTasks(in CreateTaskInput, id core.TaskID) []core.Task {
	kind := core.NormalizeTaskKind(in.Kind)
	task := core.Task{
		ID: id, Title: strings.TrimSpace(in.Title), Body: strings.TrimSpace(in.Body),
		Scope: taskScopes(in.Scope), DependsOn: taskDependencies(in.Depends),
		Priority: in.Priority, Status: core.TaskAvailable,
		Kind: kind, BaseBranch: strings.TrimSpace(in.BaseBranch),
		RequireReview: core.NormalizeReviewMode(in.RequireReview), Plan: in.Plan,
		SourceRef: cardPath(id),
	}
	if kind != core.TaskKindEpic {
		return []core.Task{task}
	}
	children := epicChildren(id, in.Plan)
	for _, child := range children {
		task.DependsOn = append(task.DependsOn, child.ID)
	}
	return append(children, task)
}

// existingBatchCards resolves all already known cards in one source read.
func (c *TaskCreator) existingBatchCards(ctx context.Context, writer CardWriter, ids []core.TaskID) ([]core.Task, []bool, []CreatedTask) {
	batch := c.Cache.Batch(ids)

	available, err := writer.Available(ctx)
	if err != nil {
		available = nil
	}
	byID := make(map[core.TaskID]core.Task, len(available))
	for _, task := range available {
		byID[task.ID] = task
	}

	results := make([]CreatedTask, len(ids))
	exists := make([]bool, len(ids))
	for i, id := range ids {
		if out, ok := batch[id]; ok {
			results[i], exists[i] = out, true
			continue
		}
		if task, ok := byID[id]; ok {
			path := task.SourceRef
			if path == "" {
				path = cardPath(id)
			}
			results[i], exists[i] = CreatedTask{ID: string(id), Path: path, Pushed: true}, true
		}
	}
	return available, exists, results
}

func (c *TaskCreator) ensureBatchBranches(ctx context.Context, resolved []CreateTaskInput, ids []core.TaskID, existing []bool) error {
	base := publishBase(c.Root)
	seen := make(map[string]bool)
	for i, in := range resolved {
		if existing[i] || core.NormalizeTaskKind(in.Kind) != core.TaskKindEpic {
			continue
		}
		branch := epicBranch(ids[i])
		if seen[branch] {
			continue
		}
		seen[branch] = true
		if err := ensureBranch(ctx, c.Workspace, branch, base); err != nil {
			return fmt.Errorf("create epic branch: %w", err)
		}
	}
	return nil
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

// rollbackBatch removes every card written by a failed batch in one pass and
// unpublishes them from the task graph. There is no commit to undo when the
// failure happened before publish; restoring the earlier graph keeps the
// dispatcher from seeing cards whose files are gone.
func rollbackBatch(ctx context.Context, writer CardWriter, store TaskGraphStore, written, available []core.Task, synced bool) {
	if len(written) == 0 {
		return
	}
	opCtx, cancel := detachedContext(ctx)
	defer cancel()
	ids := make([]core.TaskID, 0, len(written))
	for i := len(written) - 1; i >= 0; i-- {
		ids = append(ids, written[i].ID)
	}
	writer.Remove(opCtx, ids...)
	if synced && store != nil {
		_ = store.SyncTasks(opCtx, available)
	}
}

// batchIDs assigns a stable ID to each input. An explicit ID wins. An epic
// without one keeps the derivation Create uses, so an epic made alone or in a
// batch gets the same ID. Derived IDs depend only on content, never on the day.
func (c *TaskCreator) batchIDs(inputs []CreateTaskInput) []core.TaskID {
	seed := batchSeed(inputs)
	out := make([]core.TaskID, len(inputs))
	for i, in := range inputs {
		if id := strings.TrimSpace(in.ID); id != "" {
			out[i] = core.TaskID(id)
			continue
		}
		if core.NormalizeTaskKind(in.Kind) == core.TaskKindEpic {
			out[i] = c.ids().ForEpic(in.Title, in.Plan)
			continue
		}
		out[i] = c.ids().ForBatch(seed, i)
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

// batchTaskID derives the ID from the batch seed and position alone. No clock
// is read, so the same batch on a later day keeps its IDs.
func batchTaskID(seed string, index int) core.TaskID {
	sum := sha256.Sum256([]byte(seed + "\x00" + strconv.Itoa(index)))
	return core.TaskID("T-" + hex.EncodeToString(sum[:4]))
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

func lookupCreated(ctx context.Context, writer CardWriter, cache *IdempotencyCache, key string, id core.TaskID, children []core.Task) ([]core.Task, bool) {
	tasks, ok := cache.Created(key)
	if ok {
		return tasks, true
	}
	available, err := writer.Available(ctx)
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
			out = append(out, core.Task{ID: child.ID, Parent: id, SourceRef: cardPath(child.ID)})
		}
		return append(out, core.Task{ID: id, SourceRef: cardPath(id)}), true
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

// publishPath rebases onto the configured base, then commits pathspec. A push
// rejected as non-fast-forward is retried once on the same base. Cancellation
// before the commit aborts the publish so the caller can restore the cards.
// Cancellation after the commit cannot undo it, so the retry finishes with a
// bounded context detached from the caller.
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
	return publishWithNFFRetry(ctx, ws, root, base, pathspec, message)
}

// publishWithNFFRetry commits pathspec and retries the push once when the
// remote ref advanced. Task creation and the reconciler share it. The caller
// chooses the rebase base: task creation passes publishBase(root), the
// reconciler passes the run's own branch. Keeping the base at the call site
// makes it clear why a retry rebased onto a particular ref. The retry itself
// runs under the bounded context in retryPublish.
func publishWithNFFRetry(ctx context.Context, ws PathCommitter, root core.Workspace, base, pathspec, message string) error {
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

// detachedContext returns a bounded context that keeps the caller's values but
// cannot be cancelled with it. Callers use it only after a commit is durable.
// Checking ctx.Err() once is not enough: a cancellation arriving mid-operation
// would still abort a push or rollback that must finish.
func detachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), detachedPushTimeout)
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

// epicChildren gives an epic's plan the branch that its children branch from.
// The expansion itself lives in core and is shared with the card parser.
func epicChildren(id core.TaskID, plan []core.ChildSpec) []core.Task {
	return core.SetParent(core.ExpandChildren(core.Task{ID: id, Plan: plan}, epicBranch(id)), id)
}

// idempotencyKey keys the created cache by epic ID and content. The derived
// ID is content-based, so the key is stable across days; an explicit ID still
// distinguishes two epics that share a title and plan.
func idempotencyKey(epicID core.TaskID, title string, plan []core.ChildSpec) string {
	return string(epicID) + "|" + title + "|" + strings.Join(childTitles(plan), "\x00")
}

// epicTaskID derives the ID from the title and sorted child titles alone. No
// clock is read, so creating the same epic on a later day keeps its ID.
func epicTaskID(title string, plan []core.ChildSpec) core.TaskID {
	sum := sha256.Sum256([]byte(title + "\x00" + strings.Join(childTitles(plan), "\x00")))
	return core.TaskID("T-" + hex.EncodeToString(sum[:4]))
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

// newTaskID assigns an unconstrained random ID. The date is explicit so a
// caller can pin it; an empty date means today. This is the only ID generator
// that reads the clock, and it never participates in content-derived IDs.
func newTaskID(date string) (core.TaskID, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	if date == "" {
		date = time.Now().UTC().Format("20060102")
	}
	return core.TaskID("T-" + date + "-" + hex.EncodeToString(b[:])), nil
}
