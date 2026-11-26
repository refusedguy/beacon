package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

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
