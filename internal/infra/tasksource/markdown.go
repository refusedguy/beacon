// Package tasksource adapts local markdown cards and GitHub issues to core.TaskSource.
package tasksource

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
	"gopkg.in/yaml.v3"
)

// Markdown reads and writes task cards in a local directory.
type Markdown struct {
	Dir    string
	Parser core.EpicParser
}

// NewMarkdown builds a task source rooted at dir.
func NewMarkdown(dir string) *Markdown { return &Markdown{Dir: dir} }

// parser returns the configured epic parser or the default one.
func (m *Markdown) parser() core.EpicParser {
	if m.Parser != nil {
		return m.Parser
	}
	return PlanParser{}
}

// indexedCard is one parsed card together with the mtime its text was read at.
// The mtime lets a lookup detect an external edit to that one card without
// re-reading every card in the directory.
type indexedCard struct {
	card
	mod time.Time
}

// cardIndex is the in-memory index of one card directory. Lookups go through
// byID because callers name cards by parsed ID, not by filename.
type cardIndex struct {
	byPath map[string]indexedCard
	byID   map[core.TaskID]string
	paths  []string
	dirMod time.Time
}

var cardIndexCache = struct {
	mu    sync.Mutex
	byDir map[string]*cardIndex
}{byDir: make(map[string]*cardIndex)}

// cardScanCount counts full directory scans. State changes must leave it
// unchanged after the first scan, which tests and benchmarks assert.
var cardScanCount atomic.Int64

// cardIndexKey identifies a directory independently of the caller's cwd.
func cardIndexKey(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return abs
}

// cardModTime returns the mtime of path, or the zero time when it cannot be
// stated. A missing file then looks changed and is re-read on the next lookup.
func cardModTime(path string) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// buildCardIndex walks the directory once and parses every card. Unreadable or
// malformed cards are skipped, matching the old find behavior.
func buildCardIndex(dir string) (*cardIndex, error) {
	cardScanCount.Add(1)
	paths, err := (CardStore{Dir: dir}).cards()
	if err != nil {
		return nil, err
	}
	idx := &cardIndex{
		byPath: make(map[string]indexedCard, len(paths)),
		byID:   make(map[core.TaskID]string, len(paths)),
		paths:  paths,
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(b)
		t, ok, hasFM := parseCardFields(content, path)
		if !ok {
			continue
		}
		idx.byPath[path] = indexedCard{
			card: card{path: path, task: t, content: content, hasFM: hasFM},
			mod:  cardModTime(path),
		}
		// The old find returned the first match in sorted path order.
		if _, exists := idx.byID[t.ID]; !exists {
			idx.byID[t.ID] = path
		}
	}
	idx.dirMod = cardModTime(dir)
	return idx, nil
}

// withCardIndex runs fn under the cache lock with the current index for dir. A
// full scan happens on first use or when the directory gains or loses a file;
// in-place edits are handled by refreshIndexedCard one card at a time.
func withCardIndex(dir string, fn func(*cardIndex) error) error {
	key := cardIndexKey(dir)
	cardIndexCache.mu.Lock()
	defer cardIndexCache.mu.Unlock()
	idx, ok := cardIndexCache.byDir[key]
	if !ok || !cardModTime(dir).Equal(idx.dirMod) {
		var err error
		idx, err = buildCardIndex(dir)
		if err != nil {
			return err
		}
		cardIndexCache.byDir[key] = idx
	}
	return fn(idx)
}

// refreshIndexedCard re-reads path only when its mtime moved since it was
// indexed. Callers hold the cache lock.
func refreshIndexedCard(idx *cardIndex, path string) (indexedCard, bool) {
	entry, ok := idx.byPath[path]
	if !ok {
		return indexedCard{}, false
	}
	mod := cardModTime(path)
	if mod.Equal(entry.mod) {
		return entry, true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return entry, true
	}
	content := string(b)
	t, parsed, hasFM := parseCardFields(content, path)
	if !parsed {
		return entry, true
	}
	if old := entry.task.ID; old != t.ID && idx.byID[old] == path {
		delete(idx.byID, old)
	}
	entry.card = card{path: path, task: t, content: content, hasFM: hasFM}
	entry.mod = mod
	idx.byPath[path] = entry
	idx.byID[t.ID] = path
	return entry, true
}

// noteIndexedCard folds a write made through this package back into the index
// so the writer never has to rescan its own change.
func noteIndexedCard(dir, path, content string) {
	cardIndexCache.mu.Lock()
	defer cardIndexCache.mu.Unlock()
	idx, ok := cardIndexCache.byDir[cardIndexKey(dir)]
	if !ok {
		return
	}
	t, parsed, hasFM := parseCardFields(content, path)
	if !parsed {
		return
	}
	if old := idx.byPath[path]; old.task.ID != "" && old.task.ID != t.ID && idx.byID[old.task.ID] == path {
		delete(idx.byID, old.task.ID)
	}
	idx.byPath[path] = indexedCard{
		card: card{path: path, task: t, content: content, hasFM: hasFM},
		mod:  cardModTime(path),
	}
	// Leave dirMod alone. A new card may have arrived from outside this
	// package; the next lookup must rescan to discover it.
	idx.byID[t.ID] = path
}

// indexedFind locates a card by parsed ID using the index.
func (s CardStore) indexedFind(id core.TaskID) (card, error) {
	var out card
	err := withCardIndex(s.Dir, func(idx *cardIndex) error {
		path, ok := idx.byID[id]
		if !ok {
			return os.ErrNotExist
		}
		entry, ok := refreshIndexedCard(idx, path)
		if !ok {
			return os.ErrNotExist
		}
		out = entry.card
		return nil
	})
	return out, err
}

// indexedPatchStatus rewrites one card's status through the index. Claim,
// Complete and workspace marking all share this path.
func (s CardStore) indexedPatchStatus(ctx context.Context, id core.TaskID, status string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var path, updated string
	err := withCardIndex(s.Dir, func(idx *cardIndex) error {
		p, ok := idx.byID[id]
		if !ok {
			return os.ErrNotExist
		}
		entry, ok := refreshIndexedCard(idx, p)
		if !ok {
			return os.ErrNotExist
		}
		path = entry.path
		updated = rewriteStatus(entry.content, status, entry.hasFM)
		return nil
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return err
	}
	noteIndexedCard(s.Dir, path, updated)
	return nil
}

// ListAvailable returns the available cards in the directory.
func (m *Markdown) ListAvailable(ctx context.Context) ([]core.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []core.Task
	err := withCardIndex(m.Dir, func(idx *cardIndex) error {
		for _, path := range idx.paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			entry, ok := refreshIndexedCard(idx, path)
			if !ok || entry.task.Status != core.TaskAvailable {
				continue
			}
			out = append(out, entry.task)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	core.SortTasks(out)
	return out, nil
}

// cardLines normalizes line endings and splits card text once. Every parser
// below works from these lines so one card is never split twice.
func cardLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
}

// splitFrontmatterLines separates a card's YAML header from its body using
// lines that were already split from the card text.
func splitFrontmatterLines(lines []string) (yamlText, body string, ok bool) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", "", false
}

// splitFrontmatter separates a card's YAML header from its body.
func splitFrontmatter(s string) (yamlText, body string, ok bool) {
	return splitFrontmatterLines(cardLines(s))
}

// parseCard parses one card's content and records its path.
func parseCard(content, path string) (core.Task, bool) {
	t, ok, _ := parseCardFields(content, path)
	return t, ok
}

// parseCardFields parses one card and also reports whether it carried YAML
// frontmatter. Callers that need the format reuse this result instead of
// splitting the same text a second time.
func parseCardFields(content, path string) (core.Task, bool, bool) {
	dir := filepath.Dir(path)
	lines := cardLines(content)
	fmText, body, hasFM := splitFrontmatterLines(lines)
	var t core.Task
	if hasFM {
		var fm cardSchema
		if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
			slog.Warn("tasksource: malformed card frontmatter", "path", path, "error", err)
			return t, false, hasFM
		}
		t.ID = core.TaskID(firstNonEmpty(fm.TaskID, fm.ID))
		t.Title = strings.TrimSpace(fm.Title)
		t.Status = normalizeStatus(fm.Status)
		t.Parent = core.TaskID(strings.TrimSpace(fm.Parent))
		t.Priority = fm.Priority
		t.Kind = normalizeKind(fm.Kind)
		t.BaseBranch = strings.TrimSpace(fm.BaseBranch)
		t.RequireReview = normalizeReview(fm.RequireReview)
		t.WorkerAgent = strings.TrimSpace(fm.WorkerAgent)
		t.WorkerPrompt = strings.TrimSpace(fm.WorkerPrompt)
		t.Plan = childSpecs(fm.Plan, dir)
		t.Scope = scopePaths(fm.combinedScope(), dir)
		for _, d := range append(fm.Depends, fm.DependsOn...) {
			if d = strings.TrimSpace(d); d != "" {
				t.DependsOn = append(t.DependsOn, core.TaskID(d))
			}
		}
		if fm.Phase != "" {
			body = "Phase: " + fm.Phase + "\n" + body
		}
		if fm.SourceRef != "" {
			body = "SourceRef: " + fm.SourceRef + "\n" + body
		}
		t.Body = strings.TrimSpace(body)
		if t.ID == "" {
			t.ID, t.Title = headingIDLines(lines)
		}
	} else {
		t = parseLegacyLines(lines, dir)
	}
	t.SourceRef = path
	return t, t.ID != "", hasFM
}

// headingIDLines extracts an identifier and title from pre-split card lines.
func headingIDLines(lines []string) (core.TaskID, string) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		head := strings.TrimPrefix(line, "# ")
		if i := strings.Index(head, "."); i > 0 {
			return core.TaskID(strings.TrimSpace(head[:i])), strings.TrimSpace(head[i+1:])
		}
	}
	return "", ""
}

// headingID extracts an identifier and title from a card heading.
func headingID(s string) (core.TaskID, string) {
	return headingIDLines(cardLines(s))
}

// parseLegacyLines parses a card without frontmatter from pre-split lines.
func parseLegacyLines(lines []string, dir string) core.Task {
	t := core.Task{}
	t.ID, t.Title = headingIDLines(lines)
	var extra []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Статус:"):
			t.Status = normalizeStatus(strings.TrimPrefix(line, "Статус:"))
		case strings.HasPrefix(line, "Владелец файлов:"):
			for _, p := range splitList(strings.TrimPrefix(line, "Владелец файлов:")) {
				t.Scope = append(t.Scope, scopeForPath(p, dir))
			}
		case strings.HasPrefix(line, "Зависимости:"):
			for _, d := range splitList(strings.TrimPrefix(line, "Зависимости:")) {
				t.DependsOn = append(t.DependsOn, core.TaskID(d))
			}
		case strings.HasPrefix(line, "Фаза:"), strings.HasPrefix(line, "Ссылка:"):
			extra = append(extra, line)
		}
	}
	t.Body = strings.Join(extra, "\n")
	return t
}

// parseLegacy parses a card that has no YAML frontmatter.
func parseLegacy(content, dir string) core.Task {
	return parseLegacyLines(cardLines(content), dir)
}

// Children returns the child tasks declared in an epic card's plan. A card
// that is not an epic has no children and returns an empty slice.
func (m *Markdown) Children(ctx context.Context, id core.TaskID) ([]core.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := CardStore{Dir: m.Dir}.indexedFind(id)
	if err != nil {
		return nil, err
	}
	if c.task.Kind != core.TaskKindEpic {
		return nil, nil
	}
	return m.parser().Children(c.task), nil
}

// rewriteStatus replaces the status field in one card's text. It stays a
// narrow text transform so its complexity does not grow as callers deepen;
// lookup, parsing and format detection live in CardStore.
func rewriteStatus(content, status string, hasFM bool) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r", ""), "\n")
	closeAt := -1
	if hasFM {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				closeAt = i
				break
			}
		}
	}
	updated := false
	for i, line := range lines {
		if hasFM && closeAt >= 0 && i > closeAt {
			break
		}
		trim := strings.TrimSpace(line)
		if hasFM && i > 0 && i < closeAt {
			if strings.HasPrefix(trim, "status:") {
				lines[i] = "status: " + status
				updated = true
				break
			}
			continue
		}
		if strings.HasPrefix(trim, "Статус:") {
			lines[i] = "Статус: " + status
			updated = true
		}
		if updated {
			break
		}
	}
	if !updated {
		if hasFM && closeAt >= 0 {
			lines = append(lines[:closeAt], append([]string{"status: " + status}, lines[closeAt:]...)...)
		} else {
			lines = append([]string{"Статус: " + status}, lines...)
		}
	}
	return strings.Join(lines, "\n")
}

// Claim marks the card for id as in progress.
func (m *Markdown) Claim(ctx context.Context, id core.TaskID) error {
	return CardStore{Dir: m.Dir}.indexedPatchStatus(ctx, id, "in_progress")
}

// Complete marks the card for id as done.
func (m *Markdown) Complete(ctx context.Context, id core.TaskID) error {
	return CardStore{Dir: m.Dir}.indexedPatchStatus(ctx, id, "done")
}

// MarkInWorkspace rewrites the card inside a worker workspace so the status change
// travels in the worker pull request.
func (m *Markdown) MarkInWorkspace(ctx context.Context, id core.TaskID, workspacePath, status string) error {
	return CardStore{Dir: filepath.Join(workspacePath, "docs", "tasks")}.indexedPatchStatus(ctx, id, status)
}

// Create writes a new card with available status. It refuses to overwrite an
// existing card with the same ID.
func (m *Markdown) Create(ctx context.Context, task core.Task) (core.Task, error) {
	if err := ctx.Err(); err != nil {
		return core.Task{}, err
	}
	fields, err := marshalFields(cardFields{
		ID:       string(task.ID),
		Title:    task.Title,
		Status:   string(core.TaskAvailable),
		Scope:    scopeValues(task.Scope),
		Depends:  dependencyValues(task.DependsOn),
		Priority: task.Priority,
	})
	if err != nil {
		return core.Task{}, err
	}
	extra, err := yaml.Marshal(epicFields{
		Kind:          string(task.Kind),
		BaseBranch:    task.BaseBranch,
		RequireReview: string(task.RequireReview),
		Plan:          planSpecs(task.Plan),
	})
	if err != nil {
		return core.Task{}, err
	}
	var content strings.Builder
	content.WriteString("---\n")
	content.WriteString(fields)
	if text := strings.TrimSpace(string(extra)); text != "" && text != "{}" {
		content.WriteString(text)
		content.WriteString("\n")
	}
	content.WriteString("---\n\n")
	if body := strings.TrimSpace(task.Body); body != "" {
		content.WriteString(body)
		content.WriteString("\n")
	}
	path := filepath.Join(m.Dir, string(task.ID)+".md")
	if err := ctx.Err(); err != nil {
		return core.Task{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return core.Task{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return core.Task{}, err
	}
	if _, err := f.WriteString(content.String()); err != nil {
		_ = f.Close()
		return core.Task{}, err
	}
	if err := f.Close(); err != nil {
		return core.Task{}, err
	}
	created := task
	created.Status = core.TaskAvailable
	created.SourceRef = path
	noteIndexedCard(m.Dir, path, content.String())
	return created, nil
}
