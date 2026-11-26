package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// ActiveRunFinder is the part of the run store needed to block deletion of a
// task that still has work in flight.
type ActiveRunFinder interface {
	FindActiveByTask(ctx context.Context, task core.TaskID) (*core.Run, error)
}

// TaskLister is the part of a task source needed to rebuild the SQLite graph
// once per batch. It is optional: without it the dispatcher's next sync owns
// the graph refresh.
type TaskLister interface {
	ListAvailable(ctx context.Context) ([]core.Task, error)
}

// DeletedTask is the per-id outcome of one card deletion.
type DeletedTask struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "deleted" or "not_found"
}

// DeleteResult reports every id in the batch. Refused holds the ids that still
// have an active run; when it is non-empty no file was touched and nothing was
// pushed.
type DeleteResult struct {
	Results []DeletedTask `json:"results"`
	Refused []string      `json:"refused,omitempty"`
	Pushed  bool          `json:"pushed"`
	OperationMeta
}

// WithMeta returns a copy of r carrying the operation id and status.
func (r DeleteResult) WithMeta(id, status string) any {
	r.Status, r.OperationID = status, id
	return r
}

// TaskDeleter removes several task cards in one commit and push. Missing cards
// are reported as not_found so a repeated delete is a no-op. Source is optional;
// when set, the batch rebuilds the SQLite graph in one sync.
type TaskDeleter struct {
	Store     ActiveRunFinder
	Source    TaskLister
	Workspace PathCommitter
	Root      core.Workspace
}

// cardChange is one staged mutation of a card file. A nil before means the
// file did not exist. A nil after means the file must be removed.
type cardChange struct {
	path   string
	before []byte
	after  []byte
}

// cardTx is the one transaction shared by batch deletion and batch update.
// Every file is staged before anything is written, applied in a single pass,
// then rolled back in reverse order if a later step fails.
type cardTx struct {
	changes []cardChange
	applied int
}

func (tx *cardTx) add(path string, before, after []byte) {
	tx.changes = append(tx.changes, cardChange{path: path, before: before, after: after})
}

func (tx *cardTx) empty() bool { return len(tx.changes) == 0 }

// apply writes every staged change. A failure rolls back the changes already
// applied and joins every rollback error with the original failure.
func (tx *cardTx) apply() error {
	for i, change := range tx.changes {
		tx.applied = i + 1
		var err error
		if change.after == nil {
			err = os.Remove(change.path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		} else {
			if err = os.MkdirAll(filepath.Dir(change.path), 0o755); err == nil {
				err = os.WriteFile(change.path, change.after, 0o644)
			}
		}
		if err != nil {
			return joinRollback(fmt.Errorf("write %s: %w", change.path, err), tx.rollback())
		}
	}
	return nil
}

// rollback restores every applied change in reverse order. Each failure names
// the affected card and its path, and all failures are joined so none is hidden.
func (tx *cardTx) rollback() error {
	var errs []error
	for i := tx.applied - 1; i >= 0; i-- {
		change := tx.changes[i]
		var err error
		if change.before == nil {
			err = os.Remove(change.path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		} else {
			err = os.WriteFile(change.path, change.before, 0o644)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("rollback card %s (%s): %w", cardIDFromPath(change.path), change.path, err))
		}
	}
	return errors.Join(errs...)
}

// cardIDFromPath returns the card id encoded in a docs/tasks/<id>.md path. It
// is used only to make rollback errors name the affected card.
func cardIDFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

// joinRollback keeps the original failure as the primary error and appends the
// rollback failure so a caller sees both.
func joinRollback(primary, rollback error) error {
	if rollback == nil {
		return primary
	}
	return fmt.Errorf("%w; rollback: %w", primary, rollback)
}

// mutationGraph holds the graph before a batch and the graph the batch should
// leave behind. A nil lister or store makes the sync a no-op.
type mutationGraph struct {
	before []core.Task
	next   []core.Task
	store  TaskGraphStore
}

func loadGraph(ctx context.Context, store ActiveRunFinder, source TaskLister, next func([]core.Task) []core.Task) (mutationGraph, error) {
	var g mutationGraph
	if source == nil {
		return g, nil
	}
	available, err := source.ListAvailable(ctx)
	if err != nil {
		return g, err
	}
	g.before = available
	g.next = next(available)
	if graphStore, ok := store.(TaskGraphStore); ok {
		g.store = graphStore
	}
	return g, nil
}

// restore puts the pre-batch graph back after a failed sync or publish. Its
// error names the graph as the affected resource.
func (g mutationGraph) restore(ctx context.Context) error {
	if g.store == nil {
		return nil
	}
	if err := g.store.SyncTasks(ctx, g.before); err != nil {
		return fmt.Errorf("restore task graph: %w", err)
	}
	return nil
}

func (g mutationGraph) sync(ctx context.Context) error {
	if g.store == nil {
		return nil
	}
	return g.store.SyncTasks(ctx, g.next)
}

// finishMutation applies the staged transaction, syncs SQLite once and
// publishes once. Any failure rolls the files and the graph back together.
func finishMutation(ctx context.Context, tx *cardTx, g mutationGraph, ws PathCommitter, root core.Workspace, pathspec, message string) error {
	if err := tx.apply(); err != nil {
		return err
	}
	if err := g.sync(ctx); err != nil {
		return joinRollback(err, errors.Join(tx.rollback(), g.restore(ctx)))
	}
	if err := publishPath(ctx, ws, root, pathspec, message); err != nil {
		return joinRollback(err, errors.Join(tx.rollback(), g.restore(ctx)))
	}
	return nil
}

// Delete removes docs/tasks/<id>.md for every id. It refuses the whole batch
// when any id has an active run, and restores removed cards if publishing fails.
func (d *TaskDeleter) Delete(ctx context.Context, ids []string) (DeleteResult, error) {
	var out DeleteResult
	normalized, err := normalizeIDs(ids)
	if err != nil {
		return out, err
	}
	refused, err := d.refused(ctx, normalized)
	if err != nil {
		return out, err
	}
	if len(refused) > 0 {
		out.Refused = refused
		return out, nil
	}
	tx, results, err := d.stageDeletes(normalized)
	if err != nil {
		return out, err
	}
	out.Results = results
	if tx.empty() {
		return out, nil
	}
	g, err := loadGraph(ctx, d.Store, d.Source, func(available []core.Task) []core.Task {
		return withoutTasks(available, normalized)
	})
	if err != nil {
		return out, err
	}
	if err := finishMutation(ctx, tx, g, d.Workspace, d.Root, cardsPath, "beacon: delete tasks"); err != nil {
		return DeleteResult{}, err
	}
	out.Pushed = true
	return out, nil
}

// refused checks every id before the tree is touched so a refusal changes nothing.
func (d *TaskDeleter) refused(ctx context.Context, ids []string) ([]string, error) {
	var out []string
	if d.Store == nil {
		return out, nil
	}
	for _, id := range ids {
		run, err := d.Store.FindActiveByTask(ctx, core.TaskID(id))
		if err != nil {
			return nil, err
		}
		if run != nil {
			out = append(out, id)
		}
	}
	return out, nil
}

// stageDeletes reads and stages every card before changing the tree. No file is
// removed here, so a read failure needs no rollback.
func (d *TaskDeleter) stageDeletes(ids []string) (*cardTx, []DeletedTask, error) {
	tx := &cardTx{}
	var results []DeletedTask
	for _, id := range ids {
		path := cardFilePath(d.Root, id)
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			results = append(results, DeletedTask{ID: id, Status: "not_found"})
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", path, err)
		}
		tx.add(path, content, nil)
		results = append(results, DeletedTask{ID: id, Status: "deleted"})
	}
	return tx, results, nil
}

func cardFilePath(root core.Workspace, id string) string {
	dir := root.Path
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "docs", "tasks", id+".md")
}

func withoutTasks(tasks []core.Task, ids []string) []core.Task {
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	out := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		if !drop[string(task.ID)] {
			out = append(out, task)
		}
	}
	return out
}

// normalizeIDs is the one id rule shared by task deletion and task update. It
// trims space, drops blanks and repeats, and rejects any id that cannot name a
// card. The first occurrence of an id wins.
func normalizeIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		if !validTaskID(id) {
			return nil, core.Errorf(core.CodeInvalidInput, "invalid task id %q", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func validTaskID(id string) bool {
	return id != "." && id != ".." && !strings.ContainsAny(id, `/\\`)
}
