package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
	"github.com/refusedguy/beacon/internal/infra/worktree"
)

// indexRunner is the dispatch seam for the index commands. The switch in
// cmd/beacon/main.go belongs to another card's OwnerFiles, so wiring installs
// the final dispatch case without editing this file.
var indexRunner = func(c *CLI, args []string) int { return runIndex(c, args) }

// RegisterIndex installs the index command implementation.
func RegisterIndex(fn func(*CLI, []string) int) { indexRunner = fn }

func runIndex(c *CLI, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.errOut, "beacon index: подкоманда не задана")
		return 2
	}
	switch args[0] {
	case "build":
		return runIndexBuild(c, args[1:])
	case "ignore-check":
		return runIndexIgnoreCheck(c, args[1:])
	case "cache-check":
		return runIndexCacheCheck(c, args[1:])
	default:
		fmt.Fprintf(c.errOut, "beacon index: неизвестная подкоманда %q\n", args[0])
		return 2
	}
}

// runIndexBuild пересобирает SQLite-индекс из карточек. Чтение и запись файлов
// остаются в taskstore: cmd только выбирает каталоги и печатает итог.
func runIndexBuild(c *CLI, args []string) int {
	fs := flag.NewFlagSet("index build", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	root := fs.String("root", c.root, "корень проекта с docs/tasks")
	if !parseFlags("index build", args, c.errOut, fs) {
		return 2
	}

	projectRoot := *root
	if projectRoot == "" {
		projectRoot = c.root
	}

	dir, err := stateDir(projectRoot)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon index build: %v\n", err)
		return 2
	}

	// CA-18: prime the versioned parse cache for the cards directory. A broken
	// or unwritable cache never fails the build: the cache degrades to memory.
	cardsDir := filepath.Join(projectRoot, "docs", "tasks")
	pc := newParseCache(dir)
	primeParseCache(pc, cardsDir)

	idx, err := taskstore.OpenIndex(taskstore.IndexPath(dir, projectRoot))
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon index build: %v\n", err)
		return 2
	}
	defer func() { _ = idx.Close() }()

	// CA-12: consult the ignore spec before the rebuild. The pre-pass never
	// opens file contents, so the rebuild keeps its own single read per card.
	spec := worktree.NewIgnoreSpec(filepath.Join(projectRoot, ".beaconignore"))
	ignored := spec.CountIgnored(cardsDir)

	stats, err := idx.RebuildDirTimed(cardsDir)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon index build: %v\n", err)
		return 2
	}

	pc.save()
	fmt.Fprintf(c.out, "beacon index build: cards=%d deps=%d owners=%d duration_ms=%d ignored=%d cache_parses=%d cache_hits=%d cache_memory=%t\n",
		stats.Cards, stats.Deps, stats.Owners, stats.Duration.Milliseconds(), ignored,
		pc.parses, pc.hits, !pc.diskOK)
	return 0
}

// runIndexIgnoreCheck implements the CA-12 check: walk a directory more than
// once and print the counters. The first pass fills the cache, so every later
// pass over an unchanged tree must leave content_reads at its first-pass
// value. A file above IgnoreMaxContentBytes never becomes a content read.
func runIndexIgnoreCheck(c *CLI, args []string) int {
	fs := flag.NewFlagSet("index ignore-check", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	root := fs.String("root", c.root, "каталог для обхода")
	rules := fs.String("rules", "", "файл правил игнорирования")
	passes := fs.Int("passes", 2, "число обходов")
	if !parseFlags("index ignore-check", args, c.errOut, fs) {
		return 2
	}
	if *passes < 1 {
		fmt.Fprintln(c.errOut, "beacon index ignore-check: passes должно быть >= 1")
		return 2
	}

	spec := worktree.NewIgnoreSpec(*rules)
	previous := int64(0)
	for i := 0; i < *passes; i++ {
		kept, err := spec.Walk(*root)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon index ignore-check: обход %s: %v\n", *root, err)
			return 2
		}
		st := spec.Stats()
		fmt.Fprintf(c.out, "pass=%d kept=%d new_reads=%d content_reads=%d cache_hits=%d rule_loads=%d\n",
			i+1, len(kept), st.ContentReads-previous, st.ContentReads, st.CacheHits, st.RuleLoads)
		previous = st.ContentReads
	}
	return 0
}

// --- CA-18: versioned parse cache -------------------------------------------

// parseCacheVersion is the schema version. It is part of the on-disk directory
// name, so a future format change cannot reinterpret an old cache (the same
// layout aider uses for its versioned SQLite directory).
const parseCacheVersion = 1

const (
	parseCacheDirName  = "repomap-cache"
	parseCacheFileName = "entries.json"
)

// File-system seams keep cmd/beacon free of direct capability calls and let
// tests simulate a corrupt or unwritable cache (ADR 0005).
var (
	indexCacheReadFile   = os.ReadFile
	indexCacheWriteFile  = os.WriteFile
	indexCacheMkdirAll   = os.MkdirAll
	indexCacheStat       = os.Stat
	indexCacheRename     = os.Rename
	indexCacheCreateTemp = os.CreateTemp
	indexCacheRemove     = os.Remove
	indexCacheChtimes    = os.Chtimes
)

type parseCacheEntry struct {
	Version string   `json:"version"`
	MTimeNS int64    `json:"mtime_ns"`
	Size    int64    `json:"size"`
	Tokens  []string `json:"tokens"`
}

type parseCacheFile struct {
	Version int                        `json:"version"`
	Entries map[string]parseCacheEntry `json:"entries"`
	Renders map[string]string          `json:"renders"`
}

// parseCache holds parsed cards and rendered views. Entries are keyed by the
// file path and validated against mtime + size, so a changed file is re-parsed.
// Renders are keyed by relative path + mtime + sorted lines of interest.
// When the disk store fails twice in a row the cache stays in memory only and
// the command keeps running.
type parseCache struct {
	dir          string
	file         string
	diskOK       bool
	failures     int
	entries      map[string]parseCacheEntry
	renders      map[string]string
	parses       int64
	hits         int64
	renderMisses int64
	renderHits   int64
}

func newParseCache(stateDirPath string) *parseCache {
	dir := filepath.Join(stateDirPath, parseCacheDirName, fmt.Sprintf("v%d", parseCacheVersion))
	pc := &parseCache{
		dir:     dir,
		file:    filepath.Join(dir, parseCacheFileName),
		diskOK:  true,
		entries: make(map[string]parseCacheEntry),
		renders: make(map[string]string),
	}
	_ = pc.load()
	return pc
}

func (pc *parseCache) load() error {
	raw, err := indexCacheReadFile(pc.file)
	if err != nil {
		return err
	}
	var data parseCacheFile
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	if data.Version != parseCacheVersion {
		return fmt.Errorf("parse cache version %d != %d", data.Version, parseCacheVersion)
	}
	if data.Entries != nil {
		pc.entries = data.Entries
	}
	if data.Renders != nil {
		pc.renders = data.Renders
	}
	return nil
}

// save persists the cache with an atomic rename. A corrupt file is simply
// overwritten on the first save (recreate); repeated failures switch to the
// in-memory fallback. It never panics and never reports to the caller as an
// error that would break the command.
func (pc *parseCache) save() {
	if !pc.diskOK {
		return
	}
	if err := indexCacheMkdirAll(pc.dir, 0o755); err != nil {
		pc.fail(err)
		return
	}
	data := parseCacheFile{Version: parseCacheVersion, Entries: pc.entries, Renders: pc.renders}
	raw, err := json.Marshal(data)
	if err != nil {
		pc.fail(err)
		return
	}
	tmp, err := indexCacheCreateTemp(pc.dir, "entries-*.tmp")
	if err != nil {
		pc.fail(err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = indexCacheRemove(tmpName)
		pc.fail(err)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = indexCacheRemove(tmpName)
		pc.fail(err)
		return
	}
	if err := indexCacheRename(tmpName, pc.file); err != nil {
		_ = indexCacheRemove(tmpName)
		pc.fail(err)
		return
	}
	pc.failures = 0
}

func (pc *parseCache) fail(err error) {
	pc.failures++
	// The first failure still gets a recreate attempt on the next save. Two
	// consecutive failures mean the store is unavailable: use memory only.
	if pc.failures >= 2 {
		pc.diskOK = false
	}
}

// get returns the parsed tokens for path. The mtime and size come from the
// caller's FileInfo so callers stat once and the cache validates freshness.
func (pc *parseCache) get(path string, info os.FileInfo) ([]string, bool) {
	mtime := info.ModTime().UnixNano()
	size := info.Size()
	key := path
	if e, ok := pc.entries[key]; ok &&
		e.Version == fmt.Sprintf("v%d", parseCacheVersion) &&
		e.MTimeNS == mtime && e.Size == size {
		pc.hits++
		return e.Tokens, true
	}
	tokens, err := parseCard(path)
	if err != nil {
		return nil, false
	}
	pc.entries[key] = parseCacheEntry{
		Version: fmt.Sprintf("v%d", parseCacheVersion),
		MTimeNS: mtime,
		Size:    size,
		Tokens:  tokens,
	}
	pc.parses++
	return tokens, false
}

// renderKey combines the relative path, mtime and sorted interests. Changing
// any of them produces a different key, so stale renders are never served.
func renderKey(rel string, mtime int64, interests []string) string {
	keys := append([]string(nil), interests...)
	sort.Strings(keys)
	return fmt.Sprintf("%s\x00%d\x00%s", rel, mtime, strings.Join(keys, "\x00"))
}

func (pc *parseCache) render(rel string, mtime int64, interests, tokens []string) (string, bool) {
	key := renderKey(rel, mtime, interests)
	if out, ok := pc.renders[key]; ok {
		pc.renderHits++
		return out, true
	}
	want := make(map[string]bool, len(interests))
	for _, s := range interests {
		want[s] = true
	}
	lines := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if want[strings.TrimSuffix(tok, ":")] {
			lines = append(lines, tok)
		}
	}
	out := strings.Join(lines, "\n")
	pc.renders[key] = out
	pc.renderMisses++
	return out, false
}

// parseCard extracts whitespace-separated tokens from a file. It is the unit
// of work the cache avoids repeating on an unchanged tree.
func parseCard(path string) ([]string, error) {
	raw, err := indexCacheReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(raw)), nil
}

// primeParseCache walks a cards directory once through the cache so build
// runs on an unchanged tree stop re-reading card contents after the first.
func primeParseCache(pc *parseCache, root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(root, e.Name())
		info, err := indexCacheStat(path)
		if err != nil {
			continue
		}
		pc.get(path, info)
	}
}

// runIndexCacheCheck is the CA-18 measurement: repeated passes over an
// unchanged tree must parse zero files after the first; touching a file
// invalidates just that entry; an unavailable cache file keeps passes working
// in memory. Flags drive each of those three conditions.
func runIndexCacheCheck(c *CLI, args []string) int {
	fs := flag.NewFlagSet("index cache-check", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	root := fs.String("root", c.root, "каталог с файлами для разбора")
	passes := fs.Int("passes", 3, "число проходов")
	state := fs.String("state", "", "каталог состояния кэша (по умолчанию stateDir)")
	touch := fs.Bool("touch", false, "изменить mtime первого файла после первого прохода")
	breakCache := fs.Bool("break-cache", false, "сделать хранилище кэша недоступным")
	interests := fs.String("interests", "Status,Цель,Владелец", "строки интереса через запятую")
	if !parseFlags("index cache-check", args, c.errOut, fs) {
		return 2
	}
	if *passes < 1 {
		fmt.Fprintln(c.errOut, "beacon index cache-check: passes должно быть >= 1")
		return 2
	}
	stateDirPath := *state
	if stateDirPath == "" {
		var err error
		stateDirPath, err = stateDir(*root)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon index cache-check: %v\n", err)
			return 2
		}
	}
	if *breakCache {
		cacheRoot := filepath.Join(stateDirPath, parseCacheDirName)
		blocker := filepath.Join(cacheRoot, fmt.Sprintf("v%d", parseCacheVersion))
		_ = indexCacheMkdirAll(cacheRoot, 0o755)
		_ = indexCacheRemove(blocker)
		// A regular file where the versioned cache directory should be makes
		// every MkdirAll fail, forcing the in-memory fallback.
		_ = indexCacheWriteFile(blocker, []byte("not a directory"), 0o644)
	}

	pc := newParseCache(stateDirPath)
	interestsList := strings.Split(*interests, ",")
	entries, err := os.ReadDir(*root)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon index cache-check: %v\n", err)
		return 2
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, filepath.Join(*root, e.Name()))
		}
	}
	touched := false
	for pass := 1; pass <= *passes; pass++ {
		passParses, passHits := pc.parses, pc.hits
		for _, path := range files {
			info, err := indexCacheStat(path)
			if err != nil {
				continue
			}
			tokens, _ := pc.get(path, info)
			pc.render(filepath.Base(path), info.ModTime().UnixNano(), interestsList, tokens)
		}
		pc.save()
		fmt.Fprintf(c.out, "pass=%d parses=%d hits=%d renders=%d render_hits=%d memory=%t\n",
			pass, pc.parses-passParses, pc.hits-passHits, pc.renderMisses, pc.renderHits, !pc.diskOK)
		if *touch && pass == 1 && len(files) > 0 {
			future := time.Now().Add(time.Hour)
			if err := indexCacheChtimes(files[0], future, future); err != nil {
				fmt.Fprintf(c.errOut, "beacon index cache-check: touch %s: %v\n", files[0], err)
				return 2
			}
			touched = true
		}
	}
	if *touch && !touched {
		fmt.Fprintln(c.errOut, "beacon index cache-check: не удалось изменить mtime: нет файлов")
		return 2
	}
	return 0
}
