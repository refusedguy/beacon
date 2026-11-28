package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// CloseInput is one card's close request. Status must be TaskDone or
// TaskDropped, and Reason records why the card left the available set.
type CloseInput struct {
	ID     string
	Status core.TaskStatus
	Reason string
}

// ClosedTask is the per-id outcome of one card close.
type ClosedTask struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "closed", "unchanged" or "not_found"
}

// CloseResult reports every id in the batch. Refused holds the ids that still
// have an active run; when it is non-empty no file was touched and nothing was
// pushed.
type CloseResult struct {
	Results []ClosedTask `json:"results"`
	Refused []string     `json:"refused,omitempty"`
	Pushed  bool         `json:"pushed"`
	OperationMeta
}

// WithMeta returns a copy of r carrying the operation id and status.
func (r CloseResult) WithMeta(id, status string) any {
	r.Status, r.OperationID = status, id
	return r
}

// TaskCloser closes several task cards in one commit and push. A close writes
// the status and an audit reason into frontmatter, then syncs the SQLite graph
// so the dispatcher cannot claim the card again. Source is optional; when set,
// the batch rebuilds the graph in one sync.
type TaskCloser struct {
	Store     ActiveRunFinder
	Source    TaskLister
	Workspace PathCommitter
	Root      core.Workspace
}

// Close stages every card change before writing any of them. The batch is then
// applied as one transaction, synced to SQLite once and published in one
// commit. A failure at any stage restores the cards and the graph.
func (c *TaskCloser) Close(ctx context.Context, updates []CloseInput) (CloseResult, error) {
	var out CloseResult
	batch, err := normalizeCloses(updates)
	if err != nil {
		return out, err
	}
	var refused []string
	if c.Store != nil {
		ids := make([]string, len(batch))
		for i, in := range batch {
			ids[i] = in.ID
		}
		active, err := activeTaskStates(ctx, c.Store, ids)
		if err != nil {
			return out, err
		}
		for _, in := range batch {
			if _, ok := active[in.ID]; ok {
				refused = append(refused, in.ID)
			}
		}
	}
	if len(refused) > 0 {
		out.Refused = refused
		return out, nil
	}
	tx, results, err := c.stageCloses(batch)
	if err != nil {
		return out, err
	}
	out.Results = results
	if tx.empty() {
		return out, nil
	}
	g, err := loadGraph(ctx, c.Store, c.Source, func(available []core.Task) []core.Task {
		return withClosedTasks(available, batch)
	})
	if err != nil {
		return out, err
	}
	if err := finishMutation(ctx, tx, g, c.Workspace, c.Root, cardsPath, "beacon: close tasks"); err != nil {
		return CloseResult{}, err
	}
	out.Pushed = true
	return out, nil
}

// normalizeCloses validates every kept entry and first occurrence wins for
// repeated ids. Status is lowercased so "Done" and "DROPPED" are accepted.
func normalizeCloses(updates []CloseInput) ([]CloseInput, error) {
	ids := make([]string, len(updates))
	for i, in := range updates {
		ids[i] = strings.TrimSpace(in.ID)
	}
	normalized, err := normalizeIDs(ids)
	if err != nil {
		return nil, err
	}
	first := make(map[string]CloseInput, len(normalized))
	for i, id := range ids {
		if _, ok := first[id]; ok {
			continue
		}
		in := updates[i]
		in.ID = id
		in.Status = core.TaskStatus(strings.ToLower(strings.TrimSpace(string(in.Status))))
		in.Reason = strings.TrimSpace(in.Reason)
		first[id] = in
	}
	out := make([]CloseInput, 0, len(normalized))
	for _, id := range normalized {
		in := first[id]
		if in.Status != core.TaskDone && in.Status != core.TaskDropped {
			return nil, core.Errorf(core.CodeInvalidInput, "close status %q must be done or dropped", in.Status)
		}
		if in.Reason == "" {
			return nil, core.Errorf(core.CodeInvalidInput, "reason is required to close %s", in.ID)
		}
		out = append(out, in)
	}
	return out, nil
}

// stageCloses reads and stages every changed card before writing any of them.
func (c *TaskCloser) stageCloses(updates []CloseInput) (*cardTx, []ClosedTask, error) {
	tx := &cardTx{}
	var results []ClosedTask
	for _, in := range updates {
		path := cardFilePath(c.Root, in.ID)
		content, err := os.ReadFile(path)
		if status := notFoundStatus(err); status != "" {
			results = append(results, ClosedTask{ID: in.ID, Status: status})
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", path, err)
		}
		next, changed := applyCardClose(string(content), in)
		if !changed {
			results = append(results, ClosedTask{ID: in.ID, Status: "unchanged"})
			continue
		}
		tx.add(path, content, []byte(next))
		results = append(results, ClosedTask{ID: in.ID, Status: "closed"})
	}
	return tx, results, nil
}

// applyCardClose writes the status and the audit reason into frontmatter. A
// card already at the target status is left untouched, so a retry converges
// without a second commit.
func applyCardClose(content string, in CloseInput) (string, bool) {
	doc := splitCardDoc(content)
	if frontmatterValue(doc.header, "status") == string(in.Status) {
		return content, false
	}
	changed := doc.setCanonical("status", []string{"status: " + string(in.Status)})
	if doc.setCanonical("status_reason", []string{"status_reason: " + fieldValue(in.Reason)}) {
		changed = true
	}
	if !changed {
		return content, false
	}
	lines := append(append([]string{}, doc.header...), doc.body...)
	return strings.Join(lines, "\n"), true
}

// frontmatterValue returns the scalar value of key in header. Quoted values
// are unquoted so a comparison never sees the YAML delimiters.
func frontmatterValue(header []string, key string) string {
	start, end, ok := fieldRange(header, key)
	if !ok || end <= start+1 {
		return ""
	}
	line := strings.TrimSpace(header[start])
	value := strings.TrimSpace(strings.TrimPrefix(line, key+":"))
	if len(value) >= 2 && value[0] == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return strings.Trim(value, "\"'")
}

// withClosedTasks carries the batch status into the graph that is synced to
// SQLite once. Cards absent from the source graph are left out so a sync never
// invents an available task from a card the source does not list.
func withClosedTasks(tasks []core.Task, updates []CloseInput) []core.Task {
	out := make([]core.Task, len(tasks))
	copy(out, tasks)
	for i := range out {
		for _, in := range updates {
			if string(out[i].ID) == in.ID {
				out[i].Status = in.Status
				break
			}
		}
	}
	return out
}
