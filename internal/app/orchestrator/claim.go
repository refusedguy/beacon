// Package orchestrator owns the generation stamp that fences a task claim
// across a blocked -> resolved hand-over (T3.457, ADR 0189).
//
// This file is the minimal domain model behind the two facts the old code did
// not write: generation_uuid, the current generation of a task row, and
// claimed_by_generation_uuid, the generation a live claim was stamped at. A
// claim is valid only while the two match. Resolving a block rotates the
// current generation, so a claim held by the previous owner becomes stale and
// cannot continue the task. The swap that follows stamps a fresh generation for
// the new assignee, never for the actor that performed the resolve.
package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Generation is an opaque generation token. It is compared for equality only;
// its text carries no ordering the caller may rely on. It is an alias of string
// so the model satisfies the narrow generation ports declared in the infra
// packages without those packages importing the app layer (ADR 0005).
type Generation = string

// ClaimStatus is the lifecycle of one row's ownership claim.
type ClaimStatus string

const (
	// ClaimPending means the row has no live claim. A resolve leaves the row
	// here, waiting for a swap to hand it to a new owner.
	ClaimPending ClaimStatus = "pending"
	// ClaimInProgress means one owner holds a live, generation-stamped claim.
	ClaimInProgress ClaimStatus = "in_progress"
)

// ClaimRow is the durable shape of one task row's generation stamp.
type ClaimRow struct {
	// TaskID identifies the row, normally the Watch ID.
	TaskID string `json:"taskID"`
	// AssigneeID is the owner the row was handed to.
	AssigneeID string `json:"assigneeID"`
	// Generation is generation_uuid: the row's current generation. It rotates
	// on every resolve, which is what invalidates a stale claim.
	Generation Generation `json:"generation"`
	// ClaimedBy is claimed_by_generation_uuid: the generation stamped on the
	// live claim. It equals Generation while the row is in_progress and is
	// cleared when the row returns to pending.
	ClaimedBy Generation `json:"claimedBy"`
	// Status is the row's claim lifecycle.
	Status ClaimStatus `json:"status"`
}

// GenerationState is the serializable snapshot of a ledger. A store persists
// this value verbatim. Only app-layer types cross this boundary; no infra type
// is imported here (ADR 0005).
type GenerationState struct {
	Seq    uint64                `json:"seq"`
	Rows   map[string]ClaimRow   `json:"rows"`
	Owners map[string]Generation `json:"owners"`
}

// GenerationStore is the durable port for the generation ledger. It is declared
// in the app layer so neither mcp nor cibridge is imported by this package. The
// ledger calls Load once at construction and Save after every state change. An
// implementation may be backed by a file, SQLite, or an in-memory fake.
type GenerationStore interface {
	Load() (GenerationState, error)
	Save(GenerationState) error
}

// StaleClaimError reports a claim whose generation no longer matches the row.
// The claim is rejected before any state is touched, so a previous owner or a
// resolver cannot keep working on a row after its generation rotated.
type StaleClaimError struct {
	TaskID    string
	Presented Generation
	Current   Generation
}

func (e *StaleClaimError) Error() string {
	return fmt.Sprintf("claim: generation %q is stale for %q, current generation is %q",
		e.Presented, e.TaskID, e.Current)
}

// IsStaleClaim reports whether err is a stale-generation rejection.
func IsStaleClaim(err error) bool {
	var stale *StaleClaimError
	return errors.As(err, &stale)
}

// ClaimGeneration is a concurrency-safe in-memory model of task-row
// generations. Generations are issued to owners, never to the actor performing
// a transition: EnterInProgress takes only the assignee, and ResolveBlocked
// rotates the generation of the owner already on the row. The actor's identity
// and generation never enter the model, so it cannot become the owner's stamp.
type ClaimGeneration struct {
	mu      sync.Mutex
	seq     uint64
	rows    map[string]ClaimRow
	owners  map[string]Generation
	store   GenerationStore
	saveErr error
}

// NewClaimGeneration builds an empty generation ledger with no store. It keeps
// the original in-memory behaviour for callers that have not wired a port yet.
func NewClaimGeneration() *ClaimGeneration {
	return &ClaimGeneration{
		rows:   make(map[string]ClaimRow),
		owners: make(map[string]Generation),
	}
}

// NewClaimGenerationWithStore builds a ledger backed by store and restores the
// state it holds. A nil store gives the same empty ledger as NewClaimGeneration.
// A load error is returned to the caller, so a restart with an unreadable store
// fails loudly instead of silently dropping every generation.
func NewClaimGenerationWithStore(store GenerationStore) (*ClaimGeneration, error) {
	c := NewClaimGeneration()
	c.store = store
	if store == nil {
		return c, nil
	}
	state, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("orchestrator: claim generation: load state: %w", err)
	}
	c.restore(state)
	return c, nil
}

// restore applies a persisted snapshot to a freshly built ledger. Construction
// is single-threaded, so no lock is needed here.
func (c *ClaimGeneration) restore(state GenerationState) {
	if state.Seq > c.seq {
		c.seq = state.Seq
	}
	for taskID, row := range state.Rows {
		c.rows[taskID] = row
	}
	for ownerID, generation := range state.Owners {
		c.owners[ownerID] = generation
	}
}

// persistLocked writes the current state to the store. The caller holds mu, so
// the snapshot matches the mutation that just happened. A save error is recorded
// and surfaced through SaveErr: the generation-port methods return no error, so
// this is the only way to observe a failed write.
func (c *ClaimGeneration) persistLocked() {
	if c.store == nil {
		return
	}
	state := GenerationState{
		Seq:    c.seq,
		Rows:   make(map[string]ClaimRow, len(c.rows)),
		Owners: make(map[string]Generation, len(c.owners)),
	}
	for taskID, row := range c.rows {
		state.Rows[taskID] = row
	}
	for ownerID, generation := range c.owners {
		state.Owners[ownerID] = generation
	}
	if err := c.store.Save(state); err != nil {
		c.saveErr = err
	}
}

// SaveErr returns the last error returned by the store, or nil when every save
// succeeded. The mcp and cibridge ports fix the generation methods to
// error-free signatures, so persistence failures are reported here instead.
func (c *ClaimGeneration) SaveErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveErr
}

// nextLocked allocates the next generation. The caller holds the lock.
func (c *ClaimGeneration) nextLocked() Generation {
	c.seq++
	return Generation(fmt.Sprintf("generation-%d", c.seq))
}

// IssueFor allocates a fresh generation for ownerID and makes it the owner's
// current generation. Every entry into in_progress issues a fresh value, so a
// generation from a previous lease is never reused. The caller holds no lock.
func (c *ClaimGeneration) IssueFor(ownerID string) Generation {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owners == nil {
		c.owners = make(map[string]Generation)
	}
	g := c.nextLocked()
	c.owners[ownerID] = g
	c.persistLocked()
	return g
}

// GenerationFor returns the most recent generation issued to ownerID. It is
// empty when no generation was issued, so a caller can tell an owner that has
// never held a claim from one whose generation was rotated away.
func (c *ClaimGeneration) GenerationFor(ownerID string) Generation {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owners[strings.TrimSpace(ownerID)]
}

// EnterInProgress stamps a fresh generation for assigneeID on taskID and
// returns the new row. The generation is the assignee's, obtained through
// IssueFor(assigneeID); there is no actor parameter, so a resolver or fixer
// performing the hand-over cannot install its own generation as the owner's
// claim. Both generation_uuid and claimed_by_generation_uuid receive the new
// value. A retry from the same owner takes a fresh generation too: each entry
// into in_progress is a new lease, and reusing a previous one would let a
// stale claim survive the swap it was supposed to lose.
func (c *ClaimGeneration) EnterInProgress(taskID, assigneeID string) (ClaimRow, error) {
	taskID = strings.TrimSpace(taskID)
	assigneeID = strings.TrimSpace(assigneeID)
	if c == nil || taskID == "" || assigneeID == "" {
		return ClaimRow{}, fmt.Errorf("orchestrator: claim generation: taskID and assigneeID are required")
	}
	g := c.IssueFor(assigneeID)
	if g == "" {
		return ClaimRow{}, fmt.Errorf("orchestrator: claim generation: no generation for assignee %q", assigneeID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows == nil {
		c.rows = make(map[string]ClaimRow)
	}
	row := ClaimRow{
		TaskID:     taskID,
		AssigneeID: assigneeID,
		Generation: g,
		ClaimedBy:  g,
		Status:     ClaimInProgress,
	}
	c.rows[taskID] = row
	c.persistLocked()
	return row, nil
}

// ResolveBlocked rotates the generation of taskID and returns the row to
// pending. It issues a fresh generation to the owner already on the row and
// clears the claim stamp, so a claim presented at the previous generation no
// longer matches. It is idempotent: a row that is not in_progress is left
// alone and the bool is false. This is the stamp blocked:resolved was missing.
func (c *ClaimGeneration) ResolveBlocked(taskID string) (Generation, bool) {
	if c == nil {
		return "", false
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", false
	}
	c.mu.Lock()
	row, ok := c.rows[taskID]
	if !ok || row.Status != ClaimInProgress {
		c.mu.Unlock()
		return "", false
	}
	c.mu.Unlock()

	g := c.IssueFor(row.AssigneeID)

	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.rows[taskID]
	if !ok || current.Status != ClaimInProgress {
		return "", false
	}
	current.Generation = g
	current.ClaimedBy = ""
	current.Status = ClaimPending
	c.rows[taskID] = current
	c.persistLocked()
	return g, true
}

// Snapshot returns a copy of the row for taskID. ok is false when the row was
// never stamped. The copy cannot mutate ledger state.
func (c *ClaimGeneration) Snapshot(taskID string) (ClaimRow, bool) {
	if c == nil {
		return ClaimRow{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[strings.TrimSpace(taskID)]
	return row, ok
}

// Valid reports whether a claim stamped at presented is still the row's live
// claim. It is false for a pending row, an unissued generation, and any
// generation rotated away by a resolve.
func (c *ClaimGeneration) Valid(taskID string, presented Generation) bool {
	if c == nil || presented == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[strings.TrimSpace(taskID)]
	if !ok || row.Status != ClaimInProgress {
		return false
	}
	return presented == row.Generation && presented == row.ClaimedBy
}

// Claim returns the row for a presented generation, or a StaleClaimError when
// the claim is no longer current. A successful Claim is a read: it never
// mutates the row. This is the guard that stops a previous owner from
// continuing after a blocked:resolved rotated the generation.
func (c *ClaimGeneration) Claim(taskID string, presented Generation) (ClaimRow, error) {
	if c == nil {
		return ClaimRow{}, &StaleClaimError{TaskID: taskID, Presented: presented}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[strings.TrimSpace(taskID)]
	if ok && presented != "" && row.Status == ClaimInProgress &&
		presented == row.Generation && presented == row.ClaimedBy {
		return row, nil
	}
	var current Generation
	if ok {
		current = row.Generation
	}
	return ClaimRow{}, &StaleClaimError{TaskID: taskID, Presented: presented, Current: current}
}
