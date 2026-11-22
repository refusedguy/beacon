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
	"crypto/sha256"
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

// TokenCategory names one bucket of a child's input context. The five values
// mirror the /tokens breakdown in aider (aider/commands.py:445-532): the
// system prompt, the dialog history, the repository map, files the child may
// edit and files it may only read.
type TokenCategory string

const (
	TokenSystem   TokenCategory = "system"
	TokenHistory  TokenCategory = "history"
	TokenRepoMap  TokenCategory = "repo-map"
	TokenEditable TokenCategory = "editable"
	TokenReadOnly TokenCategory = "read-only"

	// TokenWarnThreshold is the remaining-window size below which a context
	// is considered tight. aider warns here and suggests /drop or /clear
	// (aider/commands.py:538-551).
	TokenWarnThreshold = 1024
)

// TokenCategories lists the buckets in report order. A context may leave any
// bucket unset; unset buckets count as zero.
var TokenCategories = []TokenCategory{
	TokenSystem, TokenHistory, TokenRepoMap, TokenEditable, TokenReadOnly,
}

// TokenUsage is the accounted breakdown of one context snapshot. Total is the
// sum of the per-category counts. Remaining is MaxInput minus Total, clamped
// at zero. Warning is non-empty exactly when a positive MaxInput leaves fewer
// than TokenWarnThreshold tokens. Cost values the whole input at the price
// carried by the snapshot.
type TokenUsage struct {
	Counts    map[TokenCategory]int
	Total     int
	MaxInput  int
	Remaining int
	Cost      float64
	Warning   string
}

// ContextSnapshot is the data a parent passes to a spawned child. The map is
// copied on construction and on read, so a caller cannot change a snapshot
// after it has been handed to a child. The zero value is an empty snapshot.
// ranked holds an optional ranked repository map (CA-01). attached holds files
// the parent confirmed from a mention (CA-10). ignored holds mention keys the
// parent refused, so a later turn does not ask for them again. reflected is the
// message sent back to the parent on the next turn. The token fields carry the
// per-category accounting (CA-07) and budget is the requested token budget for
// the map (CA-02). All are copied on write and owned by the snapshot, like
// values; the token fields never appear as MCP fields.
type ContextSnapshot struct {
	values       map[string]string
	ranked       string
	attached     map[string]string
	ignored      map[string]bool
	reflected    string
	tokens       map[TokenCategory]int
	maxInput     int
	costPerToken float64
	budget       int
}

// NewContext copies values into a snapshot. A nil map gives an empty snapshot.
func NewContext(values map[string]string) ContextSnapshot {
	cp := make(map[string]string, len(values))
	for k, v := range values {
		cp[k] = v
	}
	return ContextSnapshot{
		values:   cp,
		attached: make(map[string]string),
		ignored:  make(map[string]bool),
	}
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

// WithTokenCount returns a copy of c recording count tokens for category.
// Negative counts clamp to zero. The receiver is not changed.
func (c ContextSnapshot) WithTokenCount(category TokenCategory, count int) ContextSnapshot {
	if count < 0 {
		count = 0
	}
	if c.tokens == nil {
		c.tokens = make(map[TokenCategory]int, len(TokenCategories))
	}
	c.tokens[category] = count
	return c
}

// WithMaxInputTokens returns a copy of c carrying the model input window. A
// non-positive value means the window is unknown. The receiver is not changed.
func (c ContextSnapshot) WithMaxInputTokens(max int) ContextSnapshot {
	if max < 0 {
		max = 0
	}
	c.maxInput = max
	return c
}

// WithInputCostPerToken returns a copy of c carrying the price used to value
// the input. A negative price clamps to zero. The receiver is not changed.
func (c ContextSnapshot) WithInputCostPerToken(price float64) ContextSnapshot {
	if price < 0 {
		price = 0
	}
	c.costPerToken = price
	return c
}

// TokenUsage computes the per-category token breakdown for c. Total is the
// sum of every category, Remaining is the unused window under max_input_tokens
// and Warning fires below TokenWarnThreshold. It never mutates c.
func (c ContextSnapshot) TokenUsage() TokenUsage {
	u := TokenUsage{Counts: make(map[TokenCategory]int, len(TokenCategories))}
	for _, category := range TokenCategories {
		n := c.tokens[category]
		if n < 0 {
			n = 0
		}
		u.Counts[category] = n
		u.Total += n
	}
	u.MaxInput = c.maxInput
	if c.maxInput > 0 {
		u.Remaining = c.maxInput - u.Total
		if u.Remaining < 0 {
			u.Remaining = 0
		}
		if u.Remaining < TokenWarnThreshold {
			u.Warning = fmt.Sprintf(
				"warning: %d tokens remaining (threshold %d); use /drop or /clear",
				u.Remaining, TokenWarnThreshold,
			)
		}
	}
	u.Cost = float64(u.Total) * c.costPerToken
	return u
}

// WithAttachedFiles returns a copy of c with paths attached. Each path is
// stored under its normalized mention key, so a later mention of the same file
// does not ask for it twice. Empty paths are ignored.
func (c ContextSnapshot) WithAttachedFiles(paths []string) ContextSnapshot {
	attached := make(map[string]string, len(c.attached)+len(paths))
	for k, v := range c.attached {
		attached[k] = v
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		attached[mentionKey(p)] = filepath.ToSlash(p)
	}
	c.attached = attached
	return c
}

// AttachedFiles returns the confirmed file paths sorted by value. The slice is
// a copy, so a caller cannot mutate the snapshot through it.
func (c ContextSnapshot) AttachedFiles() []string {
	out := make([]string, 0, len(c.attached))
	for _, v := range c.attached {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// WithIgnoredMentions returns a copy of c that remembers keys as refused. A
// refused key is filtered out of later mention requests.
func (c ContextSnapshot) WithIgnoredMentions(keys []string) ContextSnapshot {
	ignored := make(map[string]bool, len(c.ignored)+len(keys))
	for k, v := range c.ignored {
		ignored[k] = v
	}
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			ignored[k] = true
		}
	}
	c.ignored = ignored
	return c
}

// IgnoredMentions returns the refused mention keys sorted by value.
func (c ContextSnapshot) IgnoredMentions() []string {
	out := make([]string, 0, len(c.ignored))
	for k := range c.ignored {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// WithReflected returns a copy of c carrying text as the message shown to the
// parent on the next turn. An empty text clears it.
func (c ContextSnapshot) WithReflected(text string) ContextSnapshot {
	c.reflected = text
	return c
}

// Reflected returns the message queued for the parent on the next turn.
func (c ContextSnapshot) Reflected() string { return c.reflected }

// mentionKey normalizes a path or identifier to the key used for matching. It
// takes the base name, drops the extension and lowercases the result, so
// "delegation.go", "pkg/delegation.go" and "delegation" all resolve alike.
func mentionKey(token string) string {
	base := filepath.Base(filepath.ToSlash(strings.TrimSpace(token)))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return strings.ToLower(base)
}

// mentionTokenRE matches file paths and identifiers of at least five
// characters, the same shape aider uses when it scans an answer.
var mentionTokenRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_./-]{4,}`)

// ParseMentions returns the path and identifier tokens found in text, in
// first-seen order and without duplicates. A trailing dot or slash from prose
// is trimmed. Tokens shorter than five characters are ignored.
func ParseMentions(text string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, raw := range mentionTokenRE.FindAllString(text, -1) {
		token := strings.TrimRight(raw, "./-")
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

// MatchMentionFiles resolves mention tokens to repository files by name
// without extension. files are repository-relative paths. A key shared by
// more than one file is ambiguous and is skipped. The result keeps mention
// order and contains no duplicate paths.
func MatchMentionFiles(mentions, files []string) []string {
	index := make(map[string]string, len(files))
	ambiguous := make(map[string]bool)
	for _, f := range files {
		path := filepath.ToSlash(f)
		key := mentionKey(path)
		if prev, ok := index[key]; ok && prev != path {
			ambiguous[key] = true
			continue
		}
		index[key] = path
	}
	seen := make(map[string]bool)
	var out []string
	for _, m := range mentions {
		key := mentionKey(m)
		if ambiguous[key] {
			continue
		}
		path, ok := index[key]
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

// MentionChoice is the parent's answer for one resolved mention file.
type MentionChoice struct {
	Path   string
	Accept bool
}

// reflectedMessage builds the message returned to the parent after a mention
// decision. It names the attached paths and the refused keys so the next turn
// can explain both outcomes.
func reflectedMessage(attached, ignored []string) string {
	var b strings.Builder
	if len(attached) > 0 {
		paths := append([]string(nil), attached...)
		sort.Strings(paths)
		b.WriteString("attached mentioned files: " + strings.Join(paths, ", "))
	}
	if len(ignored) > 0 {
		keys := append([]string(nil), ignored...)
		sort.Strings(keys)
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString("ignored mentions (no repeat request): " + strings.Join(keys, ", "))
	}
	return b.String()
}

// WithMapBudget returns a copy of c carrying a requested token budget for the
// ranked map. A non-positive budget clears the override. The receiver is not
// changed.
func (c ContextSnapshot) WithMapBudget(budget int) ContextSnapshot {
	if budget < 0 {
		budget = 0
	}
	c.budget = budget
	return c
}

// MapBudget returns the requested token budget for the ranked map. Zero means
// the CA-01 default applies.
func (c ContextSnapshot) MapBudget() int { return c.budget }

// ContextTokens returns the full token cost of the snapshot and the child
// history. The caller compares it with the model's max_input_tokens before
// handing the context to a child. The budget is not counted: only the text
// the child receives costs tokens.
func (c ContextSnapshot) ContextTokens(history []Message) int {
	n := CountTokens(c.ranked)
	for _, value := range c.values {
		n += CountTokens(value)
	}
	return n + TokensOf(history)
}

// merged returns a new snapshot with other's entries written over c's. The
// ranked map follows the same rule: a non-empty map from other replaces the
// inherited one, and an empty one leaves the parent's map intact. Token counts
// merge by category, while the window, price and a positive map budget come
// from other when set. Attached files and ignored mentions are unioned. A
// non-empty reflected message from other replaces the inherited one.
func (c ContextSnapshot) merged(other ContextSnapshot) ContextSnapshot {
	out := c.Entries()
	for k, v := range other.values {
		out[k] = v
	}
	ranked := c.ranked
	if other.ranked != "" {
		ranked = other.ranked
	}
	tokens := make(map[TokenCategory]int, len(TokenCategories))
	for _, category := range TokenCategories {
		tokens[category] = c.tokens[category]
		if n, ok := other.tokens[category]; ok {
			tokens[category] = n
		}
	}
	maxInput := c.maxInput
	if other.maxInput > 0 {
		maxInput = other.maxInput
	}
	cost := c.costPerToken
	if other.costPerToken > 0 {
		cost = other.costPerToken
	}
	attached := make(map[string]string, len(c.attached)+len(other.attached))
	for k, v := range c.attached {
		attached[k] = v
	}
	for k, v := range other.attached {
		attached[k] = v
	}
	ignored := make(map[string]bool, len(c.ignored)+len(other.ignored))
	for k, v := range c.ignored {
		ignored[k] = v
	}
	for k, v := range other.ignored {
		ignored[k] = v
	}
	reflected := c.reflected
	if other.reflected != "" {
		reflected = other.reflected
	}
	budget := c.budget
	if other.budget > 0 {
		budget = other.budget
	}
	return ContextSnapshot{
		values:       out,
		ranked:       ranked,
		attached:     attached,
		ignored:      ignored,
		reflected:    reflected,
		tokens:       tokens,
		maxInput:     maxInput,
		costPerToken: cost,
		budget:       budget,
	}
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
// budget is the requested token budget for the rendered map. A new child has
// an empty chat, so MapBudgetFor applies the wider empty-chat budget. Errors
// from SpawnWith are passed through.
func (r *Registry) SpawnWithRankedMap(parentID, childID ThreadID, name, root, taskText string, budget int) (Thread, error) {
	return r.SpawnWithRankedMapInContext(parentID, childID, name, root, taskText, budget, 0)
}

// SpawnWithRankedMapInContext is SpawnWithRankedMap with an explicit context
// window. The window caps the empty-chat budget at window-4096. A non-positive
// window leaves the budget uncapped.
func (r *Registry) SpawnWithRankedMapInContext(parentID, childID ThreadID, name, root, taskText string, budget, contextWindow int) (Thread, error) {
	effective := MapBudgetFor(budget, contextWindow, true)
	extra := NewContext(nil).WithMapBudget(effective)
	if effective > 0 {
		extra = extra.WithRankedMap(BuildRankedMap(root, taskText, effective))
	}
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

// PendingMentionFiles returns the repository files mentioned in text that the
// thread has neither attached nor refused. text is the card text or the
// executor answer; files is the repository file list. An unknown thread
// returns ErrUnknownThread. The result keeps first-seen mention order.
func (r *Registry) PendingMentionFiles(id ThreadID, text string, files []string) ([]string, error) {
	t, ok := r.threads[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownThread, id)
	}
	var out []string
	for _, path := range MatchMentionFiles(ParseMentions(text), files) {
		key := mentionKey(path)
		if t.Context.ignored[key] {
			continue
		}
		if _, attached := t.Context.attached[key]; attached {
			continue
		}
		out = append(out, path)
	}
	return out, nil
}

// ApplyMentionChoices records the parent's decision on the mention files of a
// thread. Accepted paths are attached to the child's context; refused paths
// are remembered so they are not offered again. The returned message is the
// reflected message queued for the next turn. An unknown thread returns
// ErrUnknownThread.
func (r *Registry) ApplyMentionChoices(id ThreadID, choices []MentionChoice) (ContextSnapshot, string, error) {
	t, ok := r.threads[id]
	if !ok {
		return ContextSnapshot{}, "", fmt.Errorf("%w: %s", ErrUnknownThread, id)
	}
	var attached, ignored []string
	for _, c := range choices {
		if c.Accept {
			attached = append(attached, c.Path)
		} else {
			ignored = append(ignored, mentionKey(c.Path))
		}
	}
	message := reflectedMessage(attached, ignored)
	t.Context = t.Context.WithAttachedFiles(attached).WithIgnoredMentions(ignored).WithReflected(message)
	r.threads[id] = t
	return t.Context, message, nil
}

// ParentNotice is the projection of a child's question into the parent chat.
// ChildName is included so the parent sees which child asked.
// ReflectedMessage carries the result of the last mention decision into the
// next turn (CA-10).
type ParentNotice struct {
	ParentID         ThreadID `json:"parentID"`
	ChildID          ThreadID `json:"childID"`
	ChildName        string   `json:"childName"`
	Question         string   `json:"question"`
	ReflectedMessage string   `json:"reflectedMessage,omitempty"`
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
		ParentID:         child.ParentID,
		ChildID:          child.ID,
		ChildName:        child.Name,
		Question:         question,
		ReflectedMessage: child.Context.Reflected(),
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

// Budget rules from aider/repomap.py:122-132 and :666-703. An empty chat gets
// a wider map because no history competes with it for the context window.
const (
	rankedMapEmptyMultiplier = 8
	rankedMapReserve         = 4096
	rankedMapTolerance       = 0.15
	rankedMapSampleThreshold = 200
	rankedMapSampleEvery     = 100
	rankedMapMaxTags         = 5
)

// MapBudgetFor returns the ranked-map token budget. A chat with history keeps
// the requested budget. An empty chat gets min(requested*8, window-4096).
// The reserve caps both cases when the caller supplies a context window, so
// the map cannot crowd out the model request. A non-positive request falls
// back to RankedMapTokenBudget. A window of 4096 or less leaves no room and
// returns zero.
func MapBudgetFor(requested, contextWindow int, emptyChat bool) int {
	if requested <= 0 {
		requested = RankedMapTokenBudget
	}
	budget := requested
	if emptyChat {
		budget = requested * rankedMapEmptyMultiplier
	}
	if contextWindow > 0 {
		room := contextWindow - rankedMapReserve
		if room <= 0 {
			return 0
		}
		if budget > room {
			budget = room
		}
	}
	return budget
}

// EstimateMapTokens estimates the token cost of a rendered map. Maps of at
// least 200 bytes are sampled every 100th line and the sampled token count is
// scaled by the byte-length ratio, as in aider/repomap.py:89-101. Shorter maps
// are counted in full. The estimate guides the tag search; the final check
// uses the full count.
func EstimateMapTokens(text string) int {
	if text == "" {
		return 0
	}
	if len(text) < rankedMapSampleThreshold {
		return CountTokens(text)
	}
	lines := strings.Split(text, "\n")
	sampledTokens, sampledBytes := 0, 0
	for i, line := range lines {
		if i%rankedMapSampleEvery != 0 {
			continue
		}
		sampledTokens += CountTokens(line)
		sampledBytes += len(line) + 1
	}
	if sampledBytes == 0 {
		return CountTokens(text)
	}
	estimate := sampledTokens * len(text) / sampledBytes
	if estimate < 1 {
		return 1
	}
	return estimate
}

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

// BuildRankedMap renders a ranked repository map for taskText within budget.
// The first section lists tagged files by descending rank, then files with no
// tags. A tag-count binary search and a rank-ordered line cut keep the map
// inside the requested token budget (CA-02).
func BuildRankedMap(root, taskText string, budget int) string {
	g := newRankedGraph(root, taskText)
	if len(g.files) == 0 {
		return ""
	}
	if budget <= 0 {
		budget = RankedMapTokenBudget
	}
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
	return rankedMapForBudget(g, order, mentioned, budget)
}

// renderRankedMap renders order with at most tagLimit tags per file and at
// most fileLimit lines. Tagged files come first, then files with no tags.
// rankedTags puts mentioned definitions ahead of the rest, so a smaller
// tagLimit drops the lowest-ranked tags first.
func renderRankedMap(g *rankedGraph, order []int, mentioned map[string]bool, tagLimit, fileLimit int) string {
	var b strings.Builder
	written := 0
	for pass := 0; pass < 2 && written < fileLimit; pass++ {
		for _, i := range order {
			if written >= fileLimit {
				break
			}
			rf := g.files[i]
			if (len(rf.defs) == 0) != (pass == 1) {
				continue
			}
			line := rf.rel
			tags := rankedTags(rf.defs, mentioned)
			if tagLimit < len(tags) {
				tags = tags[:tagLimit]
			}
			if len(tags) > 0 {
				line += ": " + strings.Join(tags, ", ")
			}
			b.WriteString(line)
			b.WriteByte('\n')
			written++
		}
	}
	return b.String()
}

// rankedMapForBudget picks the largest tag count that fits the budget, then
// drops the lowest-ranked files if the path lines alone are too large. It
// binary searches the tag list as in aider/repomap.py:666-703 and checks the
// full token count against a 15% error (repomap.py:689-696). The sampled
// estimate only guides the search, so a sampling error cannot push the map
// past the upper bound.
func rankedMapForBudget(g *rankedGraph, order []int, mentioned map[string]bool, budget int) string {
	all := len(order)

	lo, hi := 0, rankedMapMaxTags
	for lo <= hi {
		mid := (lo + hi) / 2
		if EstimateMapTokens(renderRankedMap(g, order, mentioned, mid, all)) <= budget {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	tags := hi
	if tags < 0 {
		tags = 0
	}

	files := all
	if EstimateMapTokens(renderRankedMap(g, order, mentioned, 0, all)) > budget {
		fLo, fHi := 0, all
		for fLo <= fHi {
			fMid := (fLo + fHi) / 2
			if EstimateMapTokens(renderRankedMap(g, order, mentioned, 0, fMid)) <= budget {
				fLo = fMid + 1
			} else {
				fHi = fMid - 1
			}
		}
		files = fHi
		if files < 0 {
			files = 0
		}
	}

	text := renderRankedMap(g, order, mentioned, tags, files)
	limit := int(float64(budget) * (1 + rankedMapTolerance))
	for CountTokens(text) > limit && tags > 0 {
		tags--
		text = renderRankedMap(g, order, mentioned, tags, files)
	}
	for CountTokens(text) > limit && files > 0 {
		files--
		text = renderRankedMap(g, order, mentioned, tags, files)
	}
	return text
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

// Role is the author of one dialog message. The assistant role marks a valid
// cut point for head/tail compaction (CA-06): a split never lands inside an
// assistant turn.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in a child's dialog history. Text is copied by value,
// so a caller cannot mutate history through a slice element after the fact.
type Message struct {
	Role Role
	Text string
}

// CountTokens estimates how many model tokens text costs. It is a pure
// heuristic: every whitespace word costs one token and each extra four bytes
// cost one more. Real tokenization belongs at the model boundary; this
// counter only has to be deterministic so budget checks are reproducible.
func CountTokens(text string) int {
	n := 0
	for _, word := range strings.Fields(text) {
		n += 1 + len(word)/4
	}
	return n
}

// TokensOf sums the estimated token cost of a message slice.
func TokensOf(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += CountTokens(m.Text)
	}
	return n
}

// HistoryFingerprint identifies the exact contents of a history at one point
// in time. Compaction captures it before the (possibly slow) head summarizing
// and checks it again before applying the result.
type HistoryFingerprint [sha256.Size]byte

// Fingerprint hashes role and text of every message in order. Length prefixes
// keep two different message sequences from colliding.
func Fingerprint(msgs []Message) HistoryFingerprint {
	h := sha256.New()
	for _, m := range msgs {
		fmt.Fprintf(h, "%d:%s%d:%s", len(m.Role), m.Role, len(m.Text), m.Text)
	}
	var fp HistoryFingerprint
	copy(fp[:], h.Sum(nil))
	return fp
}

// CompressFunc summarizes the head of a history into text within a token
// budget. The caller supplies the model call; this package stays pure and
// performs no network or store I/O.
type CompressFunc func(ctx context.Context, prefix string, head []Message, budget int) (string, error)

// CompactOptions configures a budgeted compaction. Zero fields take the
// defaults below. Budget is the only required field; without it a compaction
// is a no-op that copies the history.
type CompactOptions struct {
	Budget       int
	TailMessages int
	TailShare    float64
	Reserve      int
	MaxDepth     int
	Prefix       string
}

const (
	defaultTailMessages = 10
	defaultTailShare    = 0.5
	defaultReserve      = 512
	defaultMaxDepth     = 3
)

func (o CompactOptions) normalized() CompactOptions {
	if o.TailMessages <= 0 {
		o.TailMessages = defaultTailMessages
	}
	if o.TailShare <= 0 || o.TailShare >= 1 {
		o.TailShare = defaultTailShare
	}
	if o.Reserve < 0 {
		o.Reserve = defaultReserve
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = defaultMaxDepth
	}
	return o
}

// Compaction is a prepared result. It is not applied to any history until
// Apply verifies that the history still matches the fingerprint taken when
// the compaction was prepared.
type Compaction struct {
	fp     HistoryFingerprint
	result []Message
	// Tokens is the estimated token cost of result.
	Tokens int
	// Depth is how many times the head was summarized, at most MaxDepth.
	Depth int
}

// assistantBoundary moves start back to the nearest assistant message so a
// cut never lands inside an assistant turn. When no assistant message exists
// at or before start, the original start is kept.
func assistantBoundary(msgs []Message, start int) int {
	if start <= 0 || start >= len(msgs) {
		return start
	}
	for i := start; i >= 0; i-- {
		if msgs[i].Role == RoleAssistant {
			return i
		}
	}
	return start
}

// PrepareCompaction builds a compaction for msgs. It preserves the trailing
// messages verbatim, moves the cut to an assistant boundary, summarizes the
// head within the budget minus tail and reserve, and repeats summarization at
// most MaxDepth times. It does not modify msgs.
func PrepareCompaction(ctx context.Context, msgs []Message, opt CompactOptions, summarize CompressFunc) (Compaction, error) {
	fp := Fingerprint(msgs)
	opt = opt.normalized()
	c := Compaction{fp: fp}

	total := TokensOf(msgs)
	if opt.Budget <= 0 || total <= opt.Budget {
		c.result = append([]Message(nil), msgs...)
		c.Tokens = total
		return c, nil
	}

	tailStart := len(msgs) - opt.TailMessages
	if tailStart < 0 {
		tailStart = 0
	}
	tailStart = assistantBoundary(msgs, tailStart)
	tail := append([]Message(nil), msgs[tailStart:]...)

	// The tail owns at most TailShare of the budget. Drop its oldest entries
	// first so the most recent messages always survive verbatim.
	tailBudget := int(float64(opt.Budget) * opt.TailShare)
	for TokensOf(tail) > tailBudget && len(tail) > 0 {
		tail = tail[1:]
	}
	if len(tail) == 0 {
		return c, nil
	}

	head := msgs[:tailStart]
	headBudget := opt.Budget - TokensOf(tail) - opt.Reserve
	if headBudget <= 0 || len(head) == 0 || summarize == nil {
		c.result = tail
		c.Tokens = TokensOf(tail)
		return c, nil
	}

	summary := ""
	current := head
	for depth := 0; depth < opt.MaxDepth; depth++ {
		text, err := summarize(ctx, opt.Prefix, current, headBudget)
		if err != nil {
			return Compaction{}, err
		}
		summary = text
		c.Depth = depth + 1
		if CountTokens(summary) <= headBudget {
			break
		}
		current = []Message{{Role: RoleSystem, Text: summary}}
	}

	result := make([]Message, 0, len(tail)+1)
	if summary != "" && CountTokens(summary)+TokensOf(tail) <= opt.Budget {
		result = append(result, Message{Role: RoleSystem, Text: summary})
	}
	result = append(result, tail...)
	c.result = result
	c.Tokens = TokensOf(result)
	return c, nil
}

// Apply commits a prepared compaction. It returns the compacted history and
// ok=true only when current still matches the fingerprint captured at
// PrepareCompaction time. A changed history returns ok=false and leaves the
// caller's data untouched, so a slow summarization cannot overwrite newer
// turns.
func (c Compaction) Apply(current []Message) ([]Message, bool) {
	if Fingerprint(current) != c.fp {
		return nil, false
	}
	return append([]Message(nil), c.result...), true
}

// TokenBreakdown renders the diagnostic /tokens report for c. It prints every
// category with its token count and cost, then the total, the input window,
// the remaining tokens and the warning when the window is tight. It is a pure
// formatter: a CLI command can print the returned text, and nothing here
// performs I/O or changes any declared MCP field.
func TokenBreakdown(c ContextSnapshot) string {
	u := c.TokenUsage()
	var b strings.Builder
	for _, category := range TokenCategories {
		n := u.Counts[category]
		fmt.Fprintf(&b, "%s: %d tokens (%.4f)\n", category, n, float64(n)*c.costPerToken)
	}
	fmt.Fprintf(&b, "total: %d tokens (%.4f)\n", u.Total, u.Cost)
	if u.MaxInput > 0 {
		fmt.Fprintf(&b, "max_input_tokens: %d\n", u.MaxInput)
		fmt.Fprintf(&b, "remaining: %d tokens\n", u.Remaining)
	}
	if u.Warning != "" {
		b.WriteString(u.Warning)
		b.WriteByte('\n')
	}
	return b.String()
}
