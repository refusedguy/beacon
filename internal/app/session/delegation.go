// Package session holds Beacon's child-thread delegation model. It keeps the
// parent link, the lineage chain, the context a parent hands to a child, and
// the notice a child's question produces in the parent chat.
//
// The model is pure. It reads no clock, store or network. A Registry keeps
// in-memory state and the caller persists it. Delivery of a parent notice goes
// through the Notifier port, so this package never writes to a chat or a
// session by itself. The server creates the thread references and calls into
// this model.
package session

import (
	"context"
	"errors"
	"fmt"
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
type ContextSnapshot struct {
	values map[string]string
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

// merged returns a new snapshot with other's entries written over c's.
func (c ContextSnapshot) merged(other ContextSnapshot) ContextSnapshot {
	out := c.Entries()
	for k, v := range other.values {
		out[k] = v
	}
	return ContextSnapshot{values: out}
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

// Notifier delivers a notice to a parent chat. The model calls this port and
// owns no chat or network client. The caller supplies the implementation.
type Notifier interface {
	Notify(ctx context.Context, notice ParentNotice) error
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(ctx context.Context, notice ParentNotice) error

// Notify calls f with notice.
func (f NotifierFunc) Notify(ctx context.Context, notice ParentNotice) error {
	return f(ctx, notice)
}

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
	if err := notifier.Notify(ctx, notice); err != nil {
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
