package tasksource

import (
	"context"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// cardFingerprint identifies the bytes of one card. Size alone misses an
// equal-length edit and mtime alone misses a rewrite that restores the old
// timestamp, so a lookup compares both size and a content hash.
type cardFingerprint struct {
	size int64
	hash uint64
}

func fingerprint(b []byte) cardFingerprint {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return cardFingerprint{size: int64(len(b)), hash: h.Sum64()}
}

// indexedCard is one parsed card together with the fingerprint of the text it
// was read from. The fingerprint lets a lookup detect an in-place edit without
// re-parsing every card in the directory.
type indexedCard struct {
	card
	fp cardFingerprint
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
// malformed cards are skipped, matching the old find behavior. A duplicate ID
// is rejected as well: the index answers by ID and SQLite stores one row per
// ID, so two cards claiming the same ID cannot both be served.
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
		if other, exists := idx.byID[t.ID]; exists {
			slog.Warn("tasksource: duplicate task id skipped", "id", t.ID, "path", path, "kept", other)
			continue
		}
		idx.byPath[path] = indexedCard{
			card: card{path: path, task: t, content: content, hasFM: hasFM},
			fp:   fingerprint(b),
		}
		idx.byID[t.ID] = path
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

// removeIndexedPath drops a card from both index maps. Callers hold the cache
// lock, so no reader sees a half-removed entry.
func removeIndexedPath(idx *cardIndex, path string) {
	entry, ok := idx.byPath[path]
	if !ok {
		return
	}
	if id := entry.task.ID; id != "" && idx.byID[id] == path {
		delete(idx.byID, id)
	}
	delete(idx.byPath, path)
}

// refreshIndexedCard re-reads path when its bytes moved since the index was
// built. A missing or unreadable file is dropped, so no caller can find a
// deleted card through a cached entry. Callers hold the cache lock.
func refreshIndexedCard(idx *cardIndex, path string) (indexedCard, bool) {
	entry, ok := idx.byPath[path]
	if !ok {
		return indexedCard{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		removeIndexedPath(idx, path)
		return indexedCard{}, false
	}
	fp := fingerprint(b)
	if fp == entry.fp {
		return entry, true
	}
	content := string(b)
	t, parsed, hasFM := parseCardFields(content, path)
	if !parsed {
		removeIndexedPath(idx, path)
		return indexedCard{}, false
	}
	removeIndexedPath(idx, path)
	if other, exists := idx.byID[t.ID]; exists && other != path {
		slog.Warn("tasksource: duplicate task id skipped", "id", t.ID, "path", path, "kept", other)
		return indexedCard{}, false
	}
	entry.card = card{path: path, task: t, content: content, hasFM: hasFM}
	entry.fp = fp
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
	removeIndexedPath(idx, path)
	idx.byPath[path] = indexedCard{
		card: card{path: path, task: t, content: content, hasFM: hasFM},
		fp:   fingerprint([]byte(content)),
	}
	// Leave dirMod alone. A new card may have arrived from outside this
	// package; the next lookup must rescan to discover it.
	idx.byID[t.ID] = path
}

// noteCardDirMod records the directory mtime after a write through this
// package. A rename creates and removes a temporary file, which moves the
// directory mtime; without this update the next lookup would rescan for a
// change this package already folded into the index.
func noteCardDirMod(dir string) {
	cardIndexCache.mu.Lock()
	defer cardIndexCache.mu.Unlock()
	if idx, ok := cardIndexCache.byDir[cardIndexKey(dir)]; ok {
		idx.dirMod = cardModTime(dir)
	}
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

// writeCardAtomic writes content through a temporary file in the same
// directory and renames it into place. A reader never observes a truncated
// card, which matters because status writes can race with a snapshot read.
func writeCardAtomic(path, content string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".beacon-card-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	if err := writeCardAtomic(path, updated); err != nil {
		return err
	}
	// Re-read the renamed file so the index holds the bytes actually on disk.
	// A rename replaces the inode, so the old fingerprint no longer applies.
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	noteIndexedCard(s.Dir, path, string(b))
	noteCardDirMod(s.Dir)
	return nil
}
