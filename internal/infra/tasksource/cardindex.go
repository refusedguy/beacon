package tasksource

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

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
// in-place edits are handled by refreshIndexedCard one card at a time. A
// removal is detected by statting indexed paths, so a filesystem that does not
// move the directory mtime on delete still drops the missing card.
func withCardIndex(dir string, fn func(*cardIndex) error) error {
	key := cardIndexKey(dir)
	cardIndexCache.mu.Lock()
	defer cardIndexCache.mu.Unlock()
	idx, ok := cardIndexCache.byDir[key]
	if !ok || !cardModTime(dir).Equal(idx.dirMod) || hasMissingCard(idx) {
		var err error
		idx, err = buildCardIndex(dir)
		if err != nil {
			return err
		}
		cardIndexCache.byDir[key] = idx
	}
	return fn(idx)
}

// hasMissingCard reports whether any indexed card file has been removed. It
// checks paths only, independent of the directory mtime.
func hasMissingCard(idx *cardIndex) bool {
	for path := range idx.byPath {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return true
		}
	}
	return false
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
	t, parsed, hasFM := parseCardFields(content, path)
	if !parsed {
		return
	}
	noteParsedCard(dir, path, content, t, hasFM)
}

// noteParsedCard folds a write back into the index using a task the caller has
// already parsed. Status patches use it: the text before the write was decoded
// once, and a status-only rewrite cannot change any other field, so decoding
// the rewritten text again would repeat the same YAML work. The caller passes
// that task with its status set to the value written to disk.
func noteParsedCard(dir, path, content string, t core.Task, hasFM bool) {
	cardIndexCache.mu.Lock()
	defer cardIndexCache.mu.Unlock()
	idx, ok := cardIndexCache.byDir[cardIndexKey(dir)]
	if !ok {
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
	var entry indexedCard
	var path, updated string
	err := withCardIndex(s.Dir, func(idx *cardIndex) error {
		p, ok := idx.byID[id]
		if !ok {
			return os.ErrNotExist
		}
		entry, ok = refreshIndexedCard(idx, p)
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
	// rewriteStatus only touches the status line, so the task parsed before
	// the write stays valid once its status is replaced. This avoids a second
	// YAML decode of the rewritten text.
	entry.task.Status = normalizeStatus(status)
	noteParsedCard(s.Dir, path, updated, entry.task, entry.hasFM)
	return nil
}
