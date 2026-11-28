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
	available, existing, results, err := c.existingBatchCards(ctx, writer, ids)
	if err != nil {
		return nil, err
	}
	graph, groups := batchGraph(resolved, ids, existing)
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

	c.fillBatchResults(results, groups, existing, written)
	c.Cache.RememberBatch(ids, results)
	return results, nil
}

// batchGraph builds the cards to write and records which slice of the written
// graph belongs to each input. Inputs the source already knows contribute no
// cards and an empty group.
func batchGraph(resolved []CreateTaskInput, ids []core.TaskID, existing []bool) ([]core.Task, []batchGroup) {
	graph := make([]core.Task, 0, len(resolved))
	groups := make([]batchGroup, len(resolved))
	for i, in := range resolved {
		if existing[i] {
			continue
		}
		group := batchGroupTasks(in, ids[i])
		groups[i] = batchGroup{start: len(graph), end: len(graph) + len(group)}
		graph = append(graph, group...)
	}
	return graph, groups
}

func (c *TaskCreator) fillBatchResults(results []CreatedTask, groups []batchGroup, existing []bool, written []core.Task) {
	for i, group := range groups {
		if existing[i] {
			continue
		}
		results[i] = c.result(written[group.start:group.end])
	}
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
			value, err := resolveLocalRef(dep, i, ids)
			if err != nil {
				return nil, err
			}
			deps = append(deps, value)
		}
		in.Depends = deps
		body, err := resolveBodyRefs(in.Body, i, ids)
		if err != nil {
			return nil, err
		}
		in.Body = body
		plan, err := resolvePlanRefs(in.Plan, i, ids)
		if err != nil {
			return nil, err
		}
		in.Plan = plan
		resolved[i] = in
	}
	return resolved, nil
}

// resolveLocalRef resolves one trimmed value against the earlier cards in a
// batch. A value that is not a local ref passes through unchanged. A value that
// starts with @ but is not an accepted @N is an error, so @0 and @01 cannot
// escape as dangling IDs.
func resolveLocalRef(value string, index int, ids []core.TaskID) (string, error) {
	ref := strings.TrimSpace(value)
	if !strings.HasPrefix(ref, "@") {
		return ref, nil
	}
	n, ok := localRefIndex(ref)
	if !ok {
		return "", core.Errorf(core.CodeInvalidInput, "invalid local reference %q", ref)
	}
	if n < 1 || n > index {
		return "", core.Errorf(core.CodeInvalidInput,
			"local reference %q must name an earlier task in this batch", ref)
	}
	return string(ids[n-1]), nil
}

// resolveBodyRefs rewrites @N tokens in a card body the same way depends are
// rewritten. It reports the first bad reference and still returns the text.
func resolveBodyRefs(body string, index int, ids []core.TaskID) (string, error) {
	var firstErr error
	resolved := bodyRefPattern.ReplaceAllStringFunc(body, func(token string) string {
		out, err := resolveLocalRef(token, index, ids)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return out
	})
	return resolved, firstErr
}

// resolvePlanRefs rewrites @N in every plan child's dependencies. The index is
// the epic's position in the batch, so a child can name an earlier batch card.
func resolvePlanRefs(plan []core.ChildSpec, index int, ids []core.TaskID) ([]core.ChildSpec, error) {
	out := make([]core.ChildSpec, len(plan))
	for j, spec := range plan {
		out[j] = spec
		if len(spec.Depends) == 0 {
			continue
		}
		deps := make([]core.TaskID, len(spec.Depends))
		for k, dep := range spec.Depends {
			value, err := resolveLocalRef(string(dep), index, ids)
			if err != nil {
				return nil, err
			}
			deps[k] = core.TaskID(value)
		}
		out[j].Depends = deps
	}
	return out, nil
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

// existingBatchCards resolves all already known cards in one source read. A
// failed read is returned to the caller so no card is written on stale data.
func (c *TaskCreator) existingBatchCards(ctx context.Context, writer CardWriter, ids []core.TaskID) ([]core.Task, []bool, []CreatedTask, error) {
	batch := c.Cache.Batch(ids)

	available, err := writer.Available(ctx)
	if err != nil {
		return nil, nil, nil, err
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
	return available, exists, results, nil
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

// singleCardID returns the id CreateBatch assigns to a one-card batch with the
// same content. A plain create adopts this id so a retry finds the card.
func (c *TaskCreator) singleCardID(in CreateTaskInput) core.TaskID {
	return c.ids().ForBatch(batchSeed([]CreateTaskInput{in}), 0)
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
			for _, dep := range child.Depends {
				b.WriteString(string(dep))
				b.WriteByte('\x1f')
			}
			b.WriteByte('\x1e')
		}
		b.WriteByte('\x1e')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

var localRefPattern = regexp.MustCompile(`^@([1-9][0-9]*)$`)

// bodyRefPattern finds @N-shaped tokens in free text so references in a card
// body get the same check as references in depends. The word boundary stops a
// token like @1abc from being rewritten partially.
var bodyRefPattern = regexp.MustCompile(`@[0-9]+\b`)

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
