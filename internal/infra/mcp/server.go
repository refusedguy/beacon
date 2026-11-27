// Package mcp exposes worker and orchestrator tools to agents over MCP.
package mcp

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unsafe"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
	"github.com/refusedguy/beacon/pkg/core/redact"
)

// NewServer builds the MCP server and registers every tool in the catalog.
func NewServer(sv Services) *msdk.Server {
	s := msdk.NewServer(&msdk.Implementation{Name: "beacon", Version: "v1.0.0"}, nil)
	for _, register := range toolRegistrations {
		register(s, sv)
	}
	return s
}

func registerTaskCreate(s *msdk.Server, sv Services) {
	// Journal operation ids so an interrupted call can be retried without
	// writing the same cards twice. The create wrapper also accepts epic fields.
	journal := newOpJournal()
	msdk.AddTool(s, catalogTool("beacon_task_create"), func(ctx context.Context, req *msdk.CallToolRequest, a epicCreateArgs) (*msdk.CallToolResult, any, error) {
		if err := requireOrchestrator(req); err != nil {
			return nil, nil, err
		}
		if err := requireService("task creator", sv.TaskCreator); err != nil {
			return nil, nil, err
		}
		return mutationTool(ctx, journal, a.OperationID, func(ctx context.Context) (any, error) {
			return createTasks(ctx, sv.TaskCreator, a)
		})
	})
}

func createTasks(ctx context.Context, creator *app.TaskCreator, a epicCreateArgs) (any, error) {
	batch := len(a.Tasks) > 0
	if batch && hasSingleCreateFields(a) {
		return nil, core.Errorf(core.CodeInvalidInput, "tasks cannot be combined with single card fields")
	}
	inputs := []app.CreateTaskInput{createInput(a)}
	if batch {
		inputs = inputs[:0]
		for _, item := range a.Tasks {
			inputs = append(inputs, batchInput(item))
		}
	}
	created, err := creator.CreateBatch(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if !batch {
		return created[0], nil
	}
	out := app.CreateBatchResult{Created: make([]app.CreatedCard, 0, len(created)), Pushed: true}
	for _, item := range created {
		out.Created = append(out.Created, app.CreatedCard{ID: item.ID, Path: item.Path})
	}
	return out, nil
}

// hasSingleCreateFields reports whether a create call mixes a batch with the
// fields of one card. A mixed call cannot be honoured without discarding one
// shape, so it is rejected before any card is written.
func hasSingleCreateFields(a epicCreateArgs) bool {
	return a.Title != "" || a.Body != "" || len(a.Scope) > 0 || len(a.Depends) > 0 ||
		a.Priority != 0 || a.ID != "" || a.Kind != "" || a.BaseBranch != "" ||
		a.RequireReview != "" || len(a.Plan) > 0
}

func registerTaskUpdate(s *msdk.Server, sv Services) {
	journal := newOpJournal()
	msdk.AddTool(s, catalogTool("beacon_task_update"), func(ctx context.Context, req *msdk.CallToolRequest, a updateTaskArgs) (*msdk.CallToolResult, any, error) {
		if err := requireOrchestrator(req); err != nil {
			return nil, nil, err
		}
		if err := requireService("task updater", sv.TaskUpdater); err != nil {
			return nil, nil, err
		}
		return mutationTool(ctx, journal, a.OperationID, func(ctx context.Context) (any, error) {
			return updateTasks(ctx, sv.TaskUpdater, a)
		})
	})
}

func updateTasks(ctx context.Context, updater *app.TaskUpdater, a updateTaskArgs) (any, error) {
	for _, item := range a.Updates {
		if item.Status != nil {
			return nil, core.Errorf(core.CodeInvalidInput, "status cannot be set through task update")
		}
	}
	updates := make([]app.UpdateInput, 0, len(a.Updates))
	for _, item := range a.Updates {
		updates = append(updates, updateInput(item))
	}
	return updater.Update(ctx, updates)
}

func registerTaskDelete(s *msdk.Server, sv Services) {
	journal := newOpJournal()
	msdk.AddTool(s, catalogTool("beacon_task_delete"), func(ctx context.Context, req *msdk.CallToolRequest, a deleteTaskArgs) (*msdk.CallToolResult, any, error) {
		if err := requireOrchestrator(req); err != nil {
			return nil, nil, err
		}
		if err := requireService("task deleter", sv.TaskDeleter); err != nil {
			return nil, nil, err
		}
		return mutationTool(ctx, journal, a.OperationID, func(ctx context.Context) (any, error) {
			return deleteTasks(ctx, sv.TaskDeleter, a)
		})
	})
}

func deleteTasks(ctx context.Context, deleter *app.TaskDeleter, a deleteTaskArgs) (any, error) {
	return deleter.Delete(ctx, a.IDs)
}

// operationJournalLimit bounds the completed mutations kept for idempotency.
const operationJournalLimit = 128

// maxOperationIDLength keeps a caller-supplied key from growing the map without bound.
const maxOperationIDLength = 256

type journalEntry struct{ result any }

// opJournal stores the result of each completed mutation by operation id. A
// single-flight channel keeps concurrent calls with the same id from writing
// twice. Entries are bounded and the oldest are evicted first. Recency is an
// LRU list, and index maps an id to its list element so touch stays O(1).
type opJournal struct {
	mu       sync.Mutex
	done     map[string]journalEntry
	order    *list.List
	index    map[string]*list.Element
	inflight map[string]chan struct{}
}

func newOpJournal() *opJournal {
	return &opJournal{
		done:     make(map[string]journalEntry),
		order:    list.New(),
		index:    make(map[string]*list.Element),
		inflight: make(map[string]chan struct{}),
	}
}

// claim returns a nil wait when the caller owns the operation. A done entry
// also yields no owner; the caller reads it with cached instead.
func (j *opJournal) claim(id string) (<-chan struct{}, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.done[id]; ok {
		j.touch(id)
		return nil, false
	}
	if wait, ok := j.inflight[id]; ok {
		return wait, false
	}
	wait := make(chan struct{})
	j.inflight[id] = wait
	return wait, true
}

// touch moves id to the most-recent end of the LRU in constant time. Callers
// hold mu.
func (j *opJournal) touch(id string) {
	if elem, ok := j.index[id]; ok {
		j.order.MoveToBack(elem)
		return
	}
	j.index[id] = j.order.PushBack(id)
}

func (j *opJournal) finish(id string, result any, err error) {
	j.mu.Lock()
	wait := j.inflight[id]
	delete(j.inflight, id)
	if err == nil {
		j.done[id] = journalEntry{result: result}
		j.touch(id)
		for len(j.done) > operationJournalLimit {
			oldest := j.order.Front()
			if oldest == nil {
				break
			}
			key := oldest.Value.(string)
			j.order.Remove(oldest)
			delete(j.index, key)
			delete(j.done, key)
		}
	}
	j.mu.Unlock()
	close(wait)
}

func (j *opJournal) cached(id string) (any, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	entry, ok := j.done[id]
	if ok {
		j.touch(id)
	}
	return entry.result, ok
}

// run applies fn once per operation id. Repeats and concurrent callers reuse
// the stored result with a duplicate status. A caller cancelled after its
// commit records a recovered status, since the push finished in a detached
// context. A cancelled waiter returns ctx.Err. A panic in fn is recovered into
// an error and still releases every waiter, so a later call can retry.
func (j *opJournal) run(ctx context.Context, id string, fn func(context.Context) (any, error)) (out any, err error) {
	if id == "" {
		return fn(ctx)
	}
	if len(id) > maxOperationIDLength {
		return nil, core.Errorf(core.CodeInvalidInput, "operation_id is too long")
	}
	wait, owner := j.claim(id)
	if !owner {
		if wait != nil {
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if result, ok := j.cached(id); ok {
			return withMeta(result, id, "duplicate"), nil
		}
		return nil, core.Errorf(core.CodeUnavailable, "operation %q did not complete", id)
	}
	var result any
	defer func() {
		if rec := recover(); rec != nil {
			err = core.Errorf(core.CodeUnavailable, "operation %q panicked: %v", id, rec)
			j.finish(id, nil, err)
			return
		}
		j.finish(id, result, err)
	}()
	result, err = fn(ctx)
	if err != nil {
		return nil, err
	}
	status := "applied"
	if ctx.Err() != nil {
		status = "recovered"
	}
	return withMeta(result, id, status), nil
}

func mutationTool(ctx context.Context, journal *opJournal, id string, fn func(context.Context) (any, error)) (*msdk.CallToolResult, any, error) {
	result, err := journal.run(ctx, id, fn)
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(result), nil, nil
}

// withMeta sets idempotency metadata through the shared result contract. It
// does not enumerate result types, so a new mutation response needs no change
// here as long as it embeds app.OperationMeta and implements WithMeta.
func withMeta(v any, id, status string) any {
	if result, ok := v.(app.OperationResult); ok {
		return result.WithMeta(id, status)
	}
	return v
}

// epicCreateArgs is the input schema of beacon_task_create. It extends the
// basic create arguments with the fields an epic card needs.
type epicCreateArgs struct {
	Title         string          `json:"title,omitempty"`
	Body          string          `json:"body,omitempty"`
	Scope         []string        `json:"scope,omitempty"`
	Depends       []string        `json:"depends,omitempty"`
	Priority      int             `json:"priority,omitempty"`
	ID            string          `json:"id,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	BaseBranch    string          `json:"base_branch,omitempty"`
	RequireReview string          `json:"require_review,omitempty"`
	Plan          []epicChildArg  `json:"plan,omitempty"`
	Tasks         []batchTaskArgs `json:"tasks,omitempty"`
	OperationID   string          `json:"operation_id,omitempty"`
}

// batchTaskArgs is one entry of the optional tasks array in beacon_task_create.
type batchTaskArgs struct {
	Title         string         `json:"title,omitempty"`
	Body          string         `json:"body,omitempty"`
	Scope         []string       `json:"scope,omitempty"`
	Depends       []string       `json:"depends,omitempty"`
	Priority      int            `json:"priority,omitempty"`
	ID            string         `json:"id,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	BaseBranch    string         `json:"base_branch,omitempty"`
	RequireReview string         `json:"require_review,omitempty"`
	Plan          []epicChildArg `json:"plan,omitempty"`
}

type epicChildArg struct {
	Title    string   `json:"title"`
	Body     string   `json:"body,omitempty"`
	Scope    []string `json:"scope,omitempty"`
	Depends  []string `json:"depends,omitempty"`
	Priority int      `json:"priority,omitempty"`
}

func createInput(a epicCreateArgs) app.CreateTaskInput {
	return buildCreateInput(a.Title, a.Body, a.Scope, a.Depends, a.Priority,
		a.ID, a.Kind, a.BaseBranch, a.RequireReview, a.Plan)
}

func batchInput(a batchTaskArgs) app.CreateTaskInput {
	return buildCreateInput(a.Title, a.Body, a.Scope, a.Depends, a.Priority,
		a.ID, a.Kind, a.BaseBranch, a.RequireReview, a.Plan)
}

func buildCreateInput(title, body string, scope, depends []string, priority int,
	id, kind, baseBranch, review string, plan []epicChildArg) app.CreateTaskInput {
	children := make([]core.ChildSpec, 0, len(plan))
	for _, spec := range plan {
		children = append(children, core.ChildSpec{
			Title: spec.Title, Body: spec.Body, Scope: specScopes(spec.Scope),
			Depends: specDeps(spec.Depends), Priority: spec.Priority,
		})
	}
	return app.CreateTaskInput{
		Title: title, Body: body, Scope: scope, Depends: depends, Priority: priority,
		ID: id, Kind: core.TaskKind(kind), BaseBranch: baseBranch,
		RequireReview: core.ReviewMode(review), Plan: children,
	}
}

// updateTaskArgs is the input schema of beacon_task_update. A nil field is
// omitted from the call and leaves that card field unchanged.
type updateTaskArgs struct {
	Updates     []updateOneArgs `json:"updates"`
	OperationID string          `json:"operation_id,omitempty"`
}

type updateOneArgs struct {
	ID            string          `json:"id"`
	Title         *string         `json:"title,omitempty"`
	Body          *string         `json:"body,omitempty"`
	Scope         *[]string       `json:"scope,omitempty"`
	Depends       *[]string       `json:"depends,omitempty"`
	Priority      *int            `json:"priority,omitempty"`
	Kind          *string         `json:"kind,omitempty"`
	RequireReview *string         `json:"require_review,omitempty"`
	BaseBranch    *string         `json:"base_branch,omitempty"`
	Plan          *[]epicChildArg `json:"plan,omitempty"`
	Status        *string         `json:"status,omitempty"` // rejected by updateTasks: task flow belongs to Beacon
}

func updateInput(a updateOneArgs) app.UpdateInput {
	out := app.UpdateInput{
		ID: a.ID, Title: a.Title, Body: a.Body, Scope: a.Scope, Depends: a.Depends,
		Priority: a.Priority, Kind: a.Kind, RequireReview: a.RequireReview, BaseBranch: a.BaseBranch,
	}
	if a.Plan != nil {
		plan := make([]core.ChildSpec, 0, len(*a.Plan))
		for _, spec := range *a.Plan {
			plan = append(plan, core.ChildSpec{
				Title: spec.Title, Body: spec.Body, Scope: specScopes(spec.Scope),
				Depends: specDeps(spec.Depends), Priority: spec.Priority,
			})
		}
		out.Plan = &plan
	}
	return out
}

// deleteTaskArgs is the input schema of beacon_task_delete.
type deleteTaskArgs struct {
	IDs         []string `json:"ids"`
	OperationID string   `json:"operation_id,omitempty"`
}

// reconcileArgs is the input schema of beacon_reconcile.
type reconcileArgs struct {
	PR int `json:"pr"`
}

func registerReconcile(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_reconcile"), func(ctx context.Context, req *msdk.CallToolRequest, a reconcileArgs) (*msdk.CallToolResult, any, error) {
		if err := requireOrchestrator(req); err != nil {
			return nil, nil, err
		}
		if err := requireService("reconcile service", sv.Reconcile); err != nil {
			return nil, nil, err
		}
		result := sv.Reconcile.Tick(ctx, a.PR)
		out := jsonResult(result)
		if result.Error != "" {
			slog.WarnContext(ctx, "beacon_reconcile", "pr", a.PR, "error", redact.Redact(result.Error))
			out.IsError = true
		}
		return out, nil, nil
	})
}

func specScopes(values []string) []core.ScopePath {
	out := make([]core.ScopePath, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
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

func specDeps(values []string) []core.TaskID {
	out := make([]core.TaskID, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, core.TaskID(value))
		}
	}
	return out
}

func textResult(s string) *msdk.CallToolResult {
	return &msdk.CallToolResult{Content: []msdk.Content{&msdk.TextContent{Text: s}}}
}

func jsonResult(v any) *msdk.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return textResult(fmt.Sprintf("{\"error\":%q}", err.Error()))
	}
	// The SDK stores tool text as a string, but json.Marshal hands us a byte
	// slice we own. Expose those bytes as a string view instead of copying them
	// into a second allocation. b is never mutated, and the returned string
	// keeps the backing array alive, so the wire bytes stay identical.
	return &msdk.CallToolResult{
		Content: []msdk.Content{&msdk.TextContent{Text: unsafe.String(unsafe.SliceData(b), len(b))}},
	}
}

// ServeStdio connects the server to a stdio transport and blocks until the session ends.
func ServeStdio(s *msdk.Server) error {
	sess, err := s.Connect(context.Background(), &msdk.StdioTransport{}, nil)
	if err != nil {
		return err
	}
	return sess.Wait()
}
