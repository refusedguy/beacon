package app

import (
	"context"
	"errors"
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
}

// TaskDeleter removes several task cards in one commit and push. Missing cards
// are reported as not_found so a repeated delete is a no-op.
type TaskDeleter struct {
	Store     ActiveRunFinder
	Workspace PathCommitter
	Root      core.Workspace
}

type savedCard struct {
	path    string
	content []byte
}

// Delete removes docs/tasks/<id>.md for every id. It refuses the whole batch
// when any id has an active run, and restores removed cards if publishing fails.
func (d *TaskDeleter) Delete(ctx context.Context, ids []string) (DeleteResult, error) {
	var out DeleteResult
	normalized := normalizeIDs(ids)
	if err := validateIDs(normalized); err != nil {
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
	results, saved, err := d.removeCards(normalized)
	if err != nil {
		return out, err
	}
	out.Results = results
	if len(saved) == 0 {
		return out, nil
	}
	if err := publishPath(ctx, d.Workspace, d.Root, "docs/tasks", "beacon: delete tasks"); err != nil {
		restore(saved)
		return DeleteResult{}, err
	}
	out.Pushed = true
	return out, nil
}

func validateIDs(ids []string) error {
	for _, id := range ids {
		if !validTaskID(id) {
			return core.Errorf(core.CodeInvalidInput, "invalid task id %q", id)
		}
	}
	return nil
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

func (d *TaskDeleter) removeCards(ids []string) ([]DeletedTask, []savedCard, error) {
	dir := d.Root.Path
	if dir == "" {
		dir = "."
	}
	var results []DeletedTask
	var saved []savedCard
	for _, id := range ids {
		path := filepath.Join(dir, "docs", "tasks", id+".md")
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			results = append(results, DeletedTask{ID: id, Status: "not_found"})
			continue
		}
		if err != nil {
			restore(saved)
			return nil, nil, err
		}
		if err := os.Remove(path); err != nil {
			restore(saved)
			return nil, nil, err
		}
		saved = append(saved, savedCard{path: path, content: content})
		results = append(results, DeletedTask{ID: id, Status: "deleted"})
	}
	return results, saved, nil
}

func restore(saved []savedCard) {
	for _, card := range saved {
		_ = os.WriteFile(card.path, card.content, 0o644)
	}
}

func normalizeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func validTaskID(id string) bool {
	return id != "." && id != ".." && !strings.ContainsAny(id, `/\\`)
}
