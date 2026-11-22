// Package session holds Beacon's child-thread delegation model. It keeps the
// parent link, the lineage chain, the context a parent hands to a child, and
// the notice a child's question produces in the parent chat.
//
// The model is pure. It reads no clock, store or network. A Registry keeps
// in-memory state and the caller persists it. Delivery of a parent notice goes
// through a Notifier function supplied by the caller, so this package never
// writes to a chat or a session by itself. The server creates the thread
// references and calls into this model.
package session

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ThreadID names a thread. The server owns ID creation and this model treats
// IDs as opaque, non-empty strings, so a ses_ style value works unchanged.
type ThreadID string

// Valid reports whether id carries a non-empty value after trimming.
func (id ThreadID) Valid() bool { return strings.TrimSpace(string(id)) != "" }

// String returns the wire form of the ID.
func (id ThreadID) String() string { return string(id) }

// ContextSnapshot is the data a parent passes to a spawned child. The map is
// copied on construction and on read, so a caller cannot change a snapshot
// after it has been handed to a child. The zero value is an empty snapshot.
// ranked holds an optional ranked repository map (CA-01). It is immutable for
// the same reason as values: each snapshot owns its text.
type ContextSnapshot struct {
	values map[string]string
	ranked string
}

// NewContext copies values into a snapshot. A nil map gives an empty snapshot.
func NewContext(values map[string]string) ContextSnapshot {
	cp := make(map[string]string, len(values))
	for k, v := range values {
		cp[k] = v
	}
	return ContextSnapshot{values: cp}
}

// Get returns the value for key and whether the key exists.
func (c ContextSnapshot) Get(key string) (string, bool) {
	v, ok := c.values[key]
	return v, ok
}

// Entries returns a copy of the snapshot as a map.
func (c ContextSnapshot) Entries() map[string]string {
	cp := make(map[string]string, len(c.values))
	for k, v := range c.values {
		cp[k] = v
	}
	return cp
}

// Len reports the number of entries in the snapshot.
func (c ContextSnapshot) Len() int { return len(c.values) }

// WithRankedMap returns a copy of c carrying text as its ranked repository
// map. An empty text clears the map. The receiver is not changed.
func (c ContextSnapshot) WithRankedMap(text string) ContextSnapshot {
	c.ranked = text
	return c
}

// RankedMap returns the ranked repository map carried by the snapshot. An
// empty string means the parent did not attach a map.
func (c ContextSnapshot) RankedMap() string { return c.ranked }

// merged returns a new snapshot with other's entries written over c's. The
// ranked map follows the same rule: a non-empty map from other replaces the
// inherited one, and an empty one leaves the parent's map intact.
func (c ContextSnapshot) merged(other ContextSnapshot) ContextSnapshot {
	out := c.Entries()
	for k, v := range other.values {
		out[k] = v
	}
	ranked := c.ranked
	if other.ranked != "" {
		ranked = other.ranked
	}
	return ContextSnapshot{values: out, ranked: ranked}
}

// Thread is one node in a delegation tree. ParentID is empty for a root. A
// Thread is returned by value and its snapshot cannot be changed by the caller.
type Thread struct {
	ID       ThreadID
	Name     string
	ParentID ThreadID
	Context  ContextSnapshot
}

// IsRoot reports whether t has no parent.
func (t Thread) IsRoot() bool { return t.ParentID == "" }

// Registry is the in-memory set of threads. It owns no I/O. The caller loads
// it, calls its methods and persists the result. The zero value is ready to
// use. Do not share one Registry across goroutines without a lock.
type Registry struct {
	threads  map[ThreadID]Thread
	children map[ThreadID][]ThreadID
}

func (r *Registry) init() {
	if r.threads == nil {
		r.threads = make(map[ThreadID]Thread)
	}
	if r.children == nil {
		r.children = make(map[ThreadID][]ThreadID)
	}
}

// Root adds a thread with no parent and the given starting context. The context
// is copied into the thread. An empty ID returns ErrInvalidThread and an ID
// already in use returns ErrDuplicateThread.
func (r *Registry) Root(id ThreadID, name string, ctx ContextSnapshot) (Thread, error) {
	if !id.Valid() {
		return Thread{}, fmt.Errorf("%w: empty thread id", ErrInvalidThread)
	}
	r.init()
	if _, exists := r.threads[id]; exists {
		return Thread{}, fmt.Errorf("%w: %s", ErrDuplicateThread, id)
	}
	t := Thread{ID: id, Name: strings.TrimSpace(name), Context: ctx}
	r.threads[id] = t
	return t, nil
}

// Spawn adds childID under parentID and copies the parent's context into the
// child. The server creates thread references; this method only records the
// link. It returns ErrUnknownParent when parentID is not registered,
// ErrDuplicateThread when childID is in use, and ErrInvalidThread when an ID
// or the name is empty.
func (r *Registry) Spawn(parentID, childID ThreadID, name string) (Thread, error) {
	return r.SpawnWith(parentID, childID, name, ContextSnapshot{})
}

// SpawnWith is Spawn with extra entries merged over the parent's snapshot. An
// extra key with the same name replaces the inherited value.
func (r *Registry) SpawnWith(parentID, childID ThreadID, name string, extra ContextSnapshot) (Thread, error) {
	if !parentID.Valid() || !childID.Valid() {
		return Thread{}, fmt.Errorf("%w: empty parent or child id", ErrInvalidThread)
	}
	if strings.TrimSpace(name) == "" {
		return Thread{}, fmt.Errorf("%w: empty child name", ErrInvalidThread)
	}
	r.init()
	parent, ok := r.threads[parentID]
	if !ok {
		return Thread{}, fmt.Errorf("%w: %s", ErrUnknownParent, parentID)
	}
	if _, exists := r.threads[childID]; exists {
		return Thread{}, fmt.Errorf("%w: %s", ErrDuplicateThread, childID)
	}
	t := Thread{
		ID:       childID,
		Name:     strings.TrimSpace(name),
		ParentID: parentID,
		Context:  parent.Context.merged(extra),
	}
	r.threads[childID] = t
	r.children[parentID] = append(r.children[parentID], childID)
	return t, nil
}

// SpawnWithRankedMap adds a child under parentID and attaches a ranked map of
// the repository built from taskText. The graph is read from root/internal;
// budget is the approximate token budget for the rendered map. A non-positive
// budget uses RankedMapTokenBudget. Errors from SpawnWith are passed through.
func (r *Registry) SpawnWithRankedMap(parentID, childID ThreadID, name, root, taskText string, budget int) (Thread, error) {
	extra := NewContext(nil).WithRankedMap(BuildRankedMap(root, taskText, budget))
	return r.SpawnWith(parentID, childID, name, extra)
}

// Get returns the thread with id and whether it exists.
func (r *Registry) Get(id ThreadID) (Thread, bool) {
	t, ok := r.threads[id]
	return t, ok
}

// Children returns the direct children of id in the order they were spawned.
// An unknown id has no children.
func (r *Registry) Children(id ThreadID) []ThreadID {
	out := make([]ThreadID, len(r.children[id]))
	copy(out, r.children[id])
	return out
}

// Lineage returns the chain from the root down to and including id. An unknown
// id gives an empty slice. The walk keeps a seen set, so a malformed tree
// loaded from elsewhere cannot make it loop.
func (r *Registry) Lineage(id ThreadID) []ThreadID {
	var chain []ThreadID
	seen := make(map[ThreadID]bool)
	for id != "" {
		t, ok := r.threads[id]
		if !ok || seen[id] {
			break
		}
		seen[id] = true
		chain = append(chain, id)
		id = t.ParentID
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// ContextOf returns the context snapshot for id. An unknown id gives an empty
// snapshot.
func (r *Registry) ContextOf(id ThreadID) ContextSnapshot {
	return r.threads[id].Context
}

// Len reports the number of registered threads.
func (r *Registry) Len() int { return len(r.threads) }

// IDs returns every registered thread ID sorted by value.
func (r *Registry) IDs() []ThreadID {
	out := make([]ThreadID, 0, len(r.threads))
	for id := range r.threads {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ParentNotice is the projection of a child's question into the parent chat.
// ChildName is included so the parent sees which child asked.
type ParentNotice struct {
	ParentID  ThreadID `json:"parentID"`
	ChildID   ThreadID `json:"childID"`
	ChildName string   `json:"childName"`
	Question  string   `json:"question"`
}

// Notifier delivers a notice to a parent chat. The model calls this function
// and owns no chat or network client. A nil Notifier builds the notice without
// delivering it.
type Notifier func(ctx context.Context, notice ParentNotice) error

// Ask records a question from a child and returns the notice for its parent.
// When notifier is not nil, Ask also delivers the notice through that port.
// A nil notifier builds the notice without delivering it. An unknown child, an
// empty question or a child with no parent returns an error and no notice. A
// notifier error is returned with the notice so the caller can retry delivery.
func (r *Registry) Ask(ctx context.Context, notifier Notifier, childID ThreadID, question string) (ParentNotice, error) {
	child, ok := r.threads[childID]
	if !ok {
		return ParentNotice{}, fmt.Errorf("%w: %s", ErrUnknownThread, childID)
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return ParentNotice{}, fmt.Errorf("%w: %s", ErrEmptyQuestion, childID)
	}
	if child.ParentID == "" {
		return ParentNotice{}, fmt.Errorf("%w: %s", ErrNoParent, childID)
	}
	notice := ParentNotice{
		ParentID:  child.ParentID,
		ChildID:   child.ID,
		ChildName: child.Name,
		Question:  question,
	}
	if notifier == nil {
		return notice, nil
	}
	if err := notifier(ctx, notice); err != nil {
		return notice, err
	}
	return notice, nil
}

// Errors returned by the delegation model. Callers use errors.Is to branch on
// them.
var (
	ErrInvalidThread   = errors.New("session: invalid thread")
	ErrDuplicateThread = errors.New("session: duplicate thread")
	ErrUnknownThread   = errors.New("session: unknown thread")
	ErrUnknownParent   = errors.New("session: unknown parent")
	ErrNoParent        = errors.New("session: thread has no parent")
	ErrEmptyQuestion   = errors.New("session: empty question")
)

// RankedMapTokenBudget is the fixed starting budget for a ranked map. CA-02
// owns the adaptive budget policy; this value is the CA-01 baseline of 1024
// tokens.
const RankedMapTokenBudget = 1024

var (
	rankedIdentRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	rankedPathRE  = regexp.MustCompile(`[\w./-]+\.go`)
)

// rankedFile is one parsed Go file in the static graph.
type rankedFile struct {
	rel  string
	defs []string
	refs map[string]int
}

// rankedGraph is the definition/reference graph over internal/ Go files.
type rankedGraph struct {
	files    []rankedFile
	defFiles map[string][]int
	weights  []float64
	edges    [][]float64
	chat     []bool
}

// collectRankedFiles walks root/internal and returns the non-test .go files.
// Paths are sorted so the rendered map is stable across machines.
func collectRankedFiles(root string) []string {
	base := filepath.Join(root, "internal")
	var out []string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "testdata", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// relativeRankedPath normalizes a file path to its repository-relative form.
func relativeRankedPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// parseRankedFile extracts top-level definitions and references to
// definitions declared in other files. Declaration order is kept for stable
// tag output.
func parseRankedFile(root, path string) (*rankedFile, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	rf := &rankedFile{
		rel:  relativeRankedPath(root, path),
		refs: make(map[string]int),
	}
	defs := make(map[string]bool)
	addDef := func(name string) {
		if name == "" || defs[name] {
			return
		}
		defs[name] = true
		rf.defs = append(rf.defs, name)
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name != nil {
				addDef(d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name != nil {
						addDef(s.Name.Name)
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n != nil {
							addDef(n.Name)
						}
					}
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || id.Name == "_" || defs[id.Name] {
			return true
		}
		rf.refs[id.Name]++
		return true
	})
	return rf, nil
}

// newRankedGraph builds the graph and applies the weighting rules from
// aider/repomap.py:383, :424-445 and :487-514. Files named in the task text
// are chat files; identifiers named in the task text personalize the rank.
func newRankedGraph(root, taskText string) *rankedGraph {
	g := &rankedGraph{
		files:    make([]rankedFile, 0),
		defFiles: make(map[string][]int),
		chat:     make([]bool, 0),
	}
	for _, path := range collectRankedFiles(root) {
		rf, err := parseRankedFile(root, path)
		if err != nil || rf == nil {
			continue
		}
		g.files = append(g.files, *rf)
		g.chat = append(g.chat, false)
	}
	for i, rf := range g.files {
		for _, name := range rf.defs {
			g.defFiles[name] = append(g.defFiles[name], i)
		}
	}
	mentioned := make(map[string]bool)
	for _, m := range rankedIdentRE.FindAllString(taskText, -1) {
		mentioned[m] = true
	}
	paths := rankedPathRE.FindAllString(taskText, -1)
	n := len(g.files)
	g.weights = make([]float64, n)
	g.edges = make([][]float64, n)
	base := 100.0 / float64(n)
	for i := 0; i < n; i++ {
		g.weights[i] = base
		g.edges[i] = make([]float64, n)
	}
	for i, rf := range g.files {
		for _, p := range paths {
			p = filepath.ToSlash(filepath.Clean(p))
			if p == rf.rel || strings.HasSuffix(rf.rel, "/"+p) || strings.HasSuffix(p, "/"+rf.rel) {
				g.chat[i] = true
				break
			}
		}
	}
	for name := range mentioned {
		for _, i := range g.defFiles[name] {
			g.weights[i] += 10
		}
	}
	for i, isChat := range g.chat {
		if isChat {
			g.weights[i] += 100
		}
	}
	for from, rf := range g.files {
		for name, count := range rf.refs {
			defs := g.defFiles[name]
			if len(defs) == 0 {
				continue
			}
			w := 1.0
			if mentioned[name] {
				w *= 10
			}
			if isRankedCompound(name) {
				w *= 10
			}
			if strings.HasPrefix(name, "_") {
				w /= 10
			}
			if len(defs) > 5 {
				w /= 10
			}
			if g.chat[from] {
				w *= 50
			}
			w *= math.Sqrt(float64(count))
			share := w / float64(len(defs))
			for _, to := range defs {
				if to == from {
					continue
				}
				g.edges[from][to] += share
			}
		}
	}
	return g
}

// isRankedCompound reports whether an identifier is long enough and uses
// snake, kebab or camel casing, matching repomap.py:487-514.
func isRankedCompound(name string) bool {
	if len(name) < 8 {
		return false
	}
	if strings.ContainsAny(name, "_-") {
		return true
	}
	prevLower := false
	for _, r := range name {
		if r >= 'A' && r <= 'Z' && prevLower {
			return true
		}
		prevLower = r >= 'a' && r <= 'z'
	}
	return false
}

// ranks runs a personalized PageRank with damping 0.85 for 40 iterations.
// Dangling nodes redistribute rank using the personalization vector.
func (g *rankedGraph) ranks() []float64 {
	n := len(g.files)
	if n == 0 {
		return nil
	}
	total := 0.0
	for _, w := range g.weights {
		total += w
	}
	personalized := make([]float64, n)
	for i, w := range g.weights {
		personalized[i] = w / total
	}
	current := make([]float64, n)
	for i := range current {
		current[i] = 1.0 / float64(n)
	}
	const damping = 0.85
	for iter := 0; iter < 40; iter++ {
		next := make([]float64, n)
		for i, w := range personalized {
			next[i] = (1 - damping) * w
		}
		for from := range current {
			outgoing := 0.0
			for _, w := range g.edges[from] {
				outgoing += w
			}
			if outgoing == 0 {
				for to, w := range personalized {
					next[to] += damping * current[from] * w
				}
				continue
			}
			for to, w := range g.edges[from] {
				next[to] += damping * current[from] * (w / outgoing)
			}
		}
		sum := 0.0
		for _, w := range next {
			sum += w
		}
		if sum > 0 {
			for i := range next {
				next[i] /= sum
			}
		}
		current = next
	}
	return current
}

// BuildRankedMap renders a ranked repository map for taskText. The first
// section lists tagged files by descending rank, then files with no tags.
// Rendering stops when the approximate token budget is spent; four characters
// per token is the estimate used for the cut.
func BuildRankedMap(root, taskText string, budget int) string {
	g := newRankedGraph(root, taskText)
	if len(g.files) == 0 {
		return ""
	}
	if budget <= 0 {
		budget = RankedMapTokenBudget
	}
	limit := budget * 4
	scores := g.ranks()
	order := make([]int, len(g.files))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if scores[a] == scores[b] {
			return g.files[a].rel < g.files[b].rel
		}
		return scores[a] > scores[b]
	})
	mentioned := make(map[string]bool)
	for _, m := range rankedIdentRE.FindAllString(taskText, -1) {
		mentioned[m] = true
	}
	var b strings.Builder
	for pass := 0; pass < 2; pass++ {
		for _, i := range order {
			if b.Len() >= limit {
				return b.String()
			}
			rf := g.files[i]
			if (len(rf.defs) == 0) != (pass == 1) {
				continue
			}
			line := rf.rel
			if tags := rankedTags(rf.defs, mentioned); len(tags) > 0 {
				line += ": " + strings.Join(tags, ", ")
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// rankedTags picks up to five definitions for a file. Definitions named in
// the task come first; the rest keep declaration order so output is stable.
func rankedTags(defs []string, mentioned map[string]bool) []string {
	const max = 5
	tags := make([]string, 0, max)
	for _, d := range defs {
		if mentioned[d] && len(tags) < max {
			tags = append(tags, d)
		}
	}
	for _, d := range defs {
		if len(tags) >= max {
			break
		}
		if !mentioned[d] {
			tags = append(tags, d)
		}
	}
	return tags
}
