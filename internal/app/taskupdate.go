package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// UpdateInput is one card's changes; a nil field leaves that field as is.
type UpdateInput struct {
	ID            string
	Title         *string
	Body          *string
	Scope         *[]string
	Depends       *[]string
	Priority      *int
	Kind          *string
	RequireReview *string
	BaseBranch    *string
	Plan          *[]core.ChildSpec
}

// UpdatedTask is the per-id outcome of one card update.
type UpdatedTask struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "updated", "unchanged" or "not_found"
}

// UpdateResult reports every id; Refused lists ids with an active run.
type UpdateResult struct {
	Results []UpdatedTask `json:"results"`
	Refused []string      `json:"refused,omitempty"`
	Pushed  bool          `json:"pushed"`
	OperationMeta
}

// WithMeta returns a copy of r carrying the operation id and status.
func (r UpdateResult) WithMeta(id, status string) any {
	r.Status, r.OperationID = status, id
	return r
}

// TaskUpdater edits several task cards in one commit and push. Source is
// optional; when set, the batch rebuilds the SQLite graph in one sync.
type TaskUpdater struct {
	Store     ActiveRunFinder
	Source    TaskLister
	Workspace PathCommitter
	Root      core.Workspace
}

// Update stages every card change before writing any of them. The whole batch
// is then applied as one transaction, synced to SQLite once and published in
// one commit. A failure at any stage restores the cards and the graph.
func (u *TaskUpdater) Update(ctx context.Context, updates []UpdateInput) (UpdateResult, error) {
	var out UpdateResult
	batch, err := normalizeUpdates(updates)
	if err != nil {
		return out, err
	}
	var refused []string
	if u.Store != nil {
		for _, in := range batch {
			run, err := u.Store.FindActiveByTask(ctx, core.TaskID(in.ID))
			if err != nil {
				return out, err
			}
			if run != nil {
				refused = append(refused, in.ID)
			}
		}
	}
	if len(refused) > 0 {
		out.Refused = refused
		return out, nil
	}
	tx, results, err := u.stageUpdates(batch)
	if err != nil {
		return out, err
	}
	out.Results = results
	if tx.empty() {
		return out, nil
	}
	g, err := loadGraph(ctx, u.Store, u.Source, func(available []core.Task) []core.Task {
		return withUpdatedTasks(available, batch)
	})
	if err != nil {
		return out, err
	}
	if err := finishMutation(ctx, tx, g, u.Workspace, u.Root, cardsPath, "beacon: update tasks"); err != nil {
		return UpdateResult{}, err
	}
	out.Pushed = true
	return out, nil
}

func normalizeUpdates(updates []UpdateInput) ([]UpdateInput, error) {
	out := make([]UpdateInput, 0, len(updates))
	seen := make(map[string]bool, len(updates))
	for _, in := range updates {
		id := strings.TrimSpace(in.ID)
		if id == "" || seen[id] {
			continue
		}
		if !validTaskID(id) {
			return nil, core.Errorf(core.CodeInvalidInput, "invalid task id %q", id)
		}
		seen[id] = true
		in.ID = id
		out = append(out, in)
	}
	return out, nil
}

// stageUpdates reads and stages every changed card before writing any of them,
// so the batch lands as one transaction.
func (u *TaskUpdater) stageUpdates(updates []UpdateInput) (*cardTx, []UpdatedTask, error) {
	tx := &cardTx{}
	var results []UpdatedTask
	for _, in := range updates {
		path := cardFilePath(u.Root, in.ID)
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			results = append(results, UpdatedTask{ID: in.ID, Status: "not_found"})
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", path, err)
		}
		next, changed := applyCardUpdate(string(content), in)
		if !changed {
			results = append(results, UpdatedTask{ID: in.ID, Status: "unchanged"})
			continue
		}
		tx.add(path, content, []byte(next))
		results = append(results, UpdatedTask{ID: in.ID, Status: "updated"})
	}
	return tx, results, nil
}

// withUpdatedTasks carries the batch changes into the graph that is synced to
// SQLite once. Cards absent from the source graph are left out so a sync never
// invents an available task from a card the source does not list.
func withUpdatedTasks(tasks []core.Task, updates []UpdateInput) []core.Task {
	out := make([]core.Task, len(tasks))
	copy(out, tasks)
	for i := range out {
		for _, in := range updates {
			if string(out[i].ID) == in.ID {
				out[i] = applyGraphUpdate(out[i], in)
				break
			}
		}
	}
	return out
}

func applyGraphUpdate(task core.Task, in UpdateInput) core.Task {
	if in.Title != nil {
		task.Title = strings.TrimSpace(*in.Title)
	}
	if in.Body != nil {
		task.Body = strings.TrimSpace(*in.Body)
	}
	if in.Scope != nil {
		task.Scope = taskScopes(cleanList(*in.Scope))
	}
	if in.Depends != nil {
		task.DependsOn = taskDependencies(cleanList(*in.Depends))
	}
	if in.Priority != nil {
		task.Priority = *in.Priority
	}
	if in.Kind != nil {
		task.Kind = core.NormalizeTaskKind(core.TaskKind(*in.Kind))
	}
	if in.RequireReview != nil {
		task.RequireReview = core.NormalizeReviewMode(core.ReviewMode(*in.RequireReview))
	}
	if in.BaseBranch != nil {
		task.BaseBranch = strings.TrimSpace(*in.BaseBranch)
	}
	if in.Plan != nil {
		task.Plan = *in.Plan
	}
	task.SourceRef = cardPath(task.ID)
	return task
}

// cardDoc splits frontmatter from body so unrelated lines stay as is.
type cardDoc struct {
	header []string
	body   []string
}

func splitCardDoc(s string) cardDoc {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				return cardDoc{header: lines[:i+1], body: lines[i+1:]}
			}
		}
	}
	return cardDoc{header: []string{"---", "---"}, body: lines}
}

// fieldRange returns the line range owned by key, including indented children.
func fieldRange(header []string, key string) (int, int, bool) {
	for i := 1; i < len(header)-1; i++ {
		if !strings.HasPrefix(strings.TrimSpace(header[i]), key+":") {
			continue
		}
		end := i + 1
		for end < len(header)-1 {
			line := header[end]
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				break
			}
			end++
		}
		return i, end, true
	}
	return 0, 0, false
}

func (d *cardDoc) setField(key string, lines []string) {
	if start, end, ok := fieldRange(d.header, key); ok {
		next := make([]string, 0, len(d.header)-(end-start)+len(lines))
		next = append(next, d.header[:start]...)
		next = append(next, lines...)
		next = append(next, d.header[end:]...)
		d.header = next
		return
	}
	at := len(d.header) - 1
	next := make([]string, 0, len(d.header)+len(lines))
	next = append(next, d.header[:at]...)
	next = append(next, lines...)
	next = append(next, d.header[at:]...)
	d.header = next
}

// setCanonical replaces a field only when its content differs.
func (d *cardDoc) setCanonical(key string, lines []string) bool {
	if start, end, ok := fieldRange(d.header, key); ok && canonical(d.header[start:end]) == canonical(lines) {
		return false
	}
	d.setField(key, lines)
	return true
}

func canonical(lines []string) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(strings.Join(strings.Fields(line), ""))
	}
	return b.String()
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.Trim(value, "` "))
		if value != "" && value != "нет" && value != "-" {
			out = append(out, value)
		}
	}
	return out
}

func fieldValue(value string) string {
	if value == "" || strings.Contains(value, ": ") || strings.Contains(value, " #") ||
		strings.ContainsAny(value, "\n\r\"'") {
		return strconv.Quote(value)
	}
	return value
}

func listLines(key string, values []string) []string {
	if len(values) == 0 {
		return []string{key + ": []"}
	}
	lines := []string{key + ":"}
	for _, value := range values {
		lines = append(lines, "  - "+fieldValue(value))
	}
	return lines
}

func applyCardUpdate(content string, in UpdateInput) (string, bool) {
	doc := splitCardDoc(content)
	changed := applyFrontmatter(&doc, in)
	if in.Body != nil {
		want := strings.TrimSpace(*in.Body)
		if strings.TrimSpace(strings.Join(doc.body, "\n")) != want {
			if want == "" {
				doc.body = nil
			} else {
				doc.body = []string{want}
			}
			changed = true
		}
	}
	if !changed {
		return content, false
	}
	lines := append(append([]string{}, doc.header...), doc.body...)
	return strings.Join(lines, "\n"), true
}

func applyFrontmatter(doc *cardDoc, in UpdateInput) bool {
	changed := false
	mark := func(v bool) {
		if v {
			changed = true
		}
	}
	if in.Title != nil {
		mark(doc.setCanonical("title", []string{"title: " + fieldValue(strings.TrimSpace(*in.Title))}))
	}
	if in.BaseBranch != nil {
		mark(doc.setCanonical("base_branch", []string{"base_branch: " + fieldValue(strings.TrimSpace(*in.BaseBranch))}))
	}
	if in.Kind != nil {
		kind := string(core.NormalizeTaskKind(core.TaskKind(*in.Kind)))
		mark(doc.setCanonical("kind", []string{"kind: " + kind}))
	}
	if in.RequireReview != nil {
		review := string(core.NormalizeReviewMode(core.ReviewMode(*in.RequireReview)))
		mark(doc.setCanonical("require_review", []string{"require_review: " + review}))
	}
	if in.Priority != nil {
		mark(doc.setCanonical("priority", []string{"priority: " + strconv.Itoa(*in.Priority)}))
	}
	if in.Scope != nil {
		mark(doc.setCanonical("scope", listLines("scope", cleanList(*in.Scope))))
	}
	if in.Depends != nil {
		mark(doc.setCanonical("depends", listLines("depends", cleanList(*in.Depends))))
	}
	if in.Plan != nil {
		mark(doc.setCanonical("plan", planLines(planSpecValues(*in.Plan))))
	}
	return changed
}

type planSpecValue struct {
	Title    string
	Scope    []string
	Depends  []string
	Priority int
}

func plainValues[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		text := string(value)
		if len(text) > 1 && text[1] == ':' {
			text = text[2:]
		}
		out = append(out, text)
	}
	return out
}

func planSpecValues(specs []core.ChildSpec) []planSpecValue {
	out := make([]planSpecValue, 0, len(specs))
	for _, spec := range specs {
		out = append(out, planSpecValue{
			Title:    strings.TrimSpace(spec.Title),
			Scope:    cleanList(plainValues(spec.Scope)),
			Depends:  cleanList(plainValues(spec.Depends)),
			Priority: spec.Priority,
		})
	}
	return out
}

func planLines(specs []planSpecValue) []string {
	if len(specs) == 0 {
		return []string{"plan: []"}
	}
	nested := func(key string, values []string) []string {
		if len(values) == 0 {
			return []string{"    " + key + ": []"}
		}
		lines := []string{"    " + key + ":"}
		for _, value := range values {
			lines = append(lines, "      - "+fieldValue(value))
		}
		return lines
	}
	lines := []string{"plan:"}
	for _, spec := range specs {
		lines = append(lines, "  - title: "+fieldValue(spec.Title))
		lines = append(lines, nested("scope", spec.Scope)...)
		lines = append(lines, nested("depends", spec.Depends)...)
		lines = append(lines, "    priority: "+strconv.Itoa(spec.Priority))
	}
	return lines
}
