package app

import (
	"context"
	"strings"
	"sync"

	"github.com/refusedguy/beacon/pkg/core"
)

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

// idempotencyKey keys the created cache by epic ID and content. The derived
// ID is content-based, so the key is stable across days; an explicit ID still
// distinguishes two epics that share a title and plan.
func idempotencyKey(epicID core.TaskID, title string, plan []core.ChildSpec) string {
	return string(epicID) + "|" + title + "|" + strings.Join(childTitles(plan), "\x00")
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
