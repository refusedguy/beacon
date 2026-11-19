package mcp

import (
	"encoding/json"
	"errors"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// sessionIDMetaKey is the reserved _meta key that carries the caller's
	// opaque session identifier. The value is a wire contract, not knowledge
	// of any particular client: the core never interprets it (ADR 0001).
	sessionIDMetaKey = "ai.opencode/sessionID"

	// chunkMetaKey carries one numbered chunk of a Beacon event stream in a
	// tool result's _meta (ADR 0031 §1, Q 1.2 mcp-partials).
	chunkMetaKey = "ai.beacon/streamChunk"

	// finalMetaKey carries the terminal stream counters and the complete
	// result in a tool result's _meta.
	finalMetaKey = "ai.beacon/streamFinal"
)

// SessionIDFromRequest reads the caller's session identifier from the tool
// request's _meta and returns it as an opaque string. It returns nil when the
// request has no _meta, the key is missing, or the value is empty. The value
// is passed through verbatim: no format parsing, no validation and no effect
// on access (WORKER-PROMPT §2).
func SessionIDFromRequest(req *mcpsdk.CallToolRequest) *string {
	if req == nil || req.Params == nil || req.Params.Meta == nil {
		return nil
	}
	raw, ok := req.Params.Meta[sessionIDMetaKey]
	if !ok {
		return nil
	}
	value, ok := raw.(string)
	if !ok || value == "" {
		return nil
	}
	return &value
}

// PendingApproval is one decision the server is waiting on. It comes from the
// server's own store, never from a client snapshot. DecisionID is the ID a
// reviewer must present to approve it, ReviewerSessionID is the opaque session
// authorized to decide it and RunID ties the decision to one orchestration run
// (ADR 0034 §2).
type PendingApproval struct {
	DecisionID        string
	ReviewerSessionID string
	RunID             string
}

// PendingApprovalStore is the narrow read port for the authoritative pending
// list. The core reads it fresh on every authentication, so a client cannot
// approve against a stale or fabricated snapshot (ADR 0034 §2).
type PendingApprovalStore interface {
	PendingApprovals() ([]PendingApproval, error)
}

// ApprovalSnapshot is application state a client sends alongside an approval.
// It is untrusted saved data: neither its reviewer nor its decision ID can
// authorize an approval. Authenticate deliberately ignores it (ADR 0034 §2).
type ApprovalSnapshot struct {
	ReviewerSessionID string
	DecisionID        string
	RunID             string
}

// ApprovalAuthenticator authenticates an approval before it is applied. It
// verifies the reviewer through the session ID carried in the request _meta
// and checks the decision ID against the server's pending list. It never uses
// a client snapshot as a source of truth (ADR 0034 §2).
type ApprovalAuthenticator struct {
	store PendingApprovalStore
}

// NewApprovalAuthenticator builds an authenticator over the server's pending
// approval store.
func NewApprovalAuthenticator(store PendingApprovalStore) *ApprovalAuthenticator {
	return &ApprovalAuthenticator{store: store}
}

// Authenticate verifies the reviewer and the decision ID against the server's
// pending decisions and returns the matching record. The reviewer is the
// session ID from req's _meta, not the one in snapshot; the pending list comes
// from the store, not from snapshot. A foreign reviewer or a decision ID that
// is not pending is rejected. The snapshot argument is accepted so the
// untrusted data is explicit at the boundary, but it is never read.
func (a *ApprovalAuthenticator) Authenticate(req *mcpsdk.CallToolRequest, decisionID string, _ ApprovalSnapshot) (PendingApproval, error) {
	if a == nil || a.store == nil {
		return PendingApproval{}, domain.NewAppError(domain.CodePersistFailed, "ack: pending approval store is nil")
	}
	sessionID := SessionIDFromRequest(req)
	if sessionID == nil {
		return PendingApproval{}, domain.NewAppError(domain.CodeInvalidInput, "ack: approval carries no reviewer sessionID")
	}
	if decisionID == "" {
		return PendingApproval{}, domain.NewAppError(domain.CodeInvalidInput, "ack: approval carries no decisionID")
	}
	pending, err := a.store.PendingApprovals()
	if err != nil {
		return PendingApproval{}, err
	}
	for _, p := range pending {
		if p.DecisionID == decisionID && p.ReviewerSessionID == *sessionID {
			return p, nil
		}
	}
	return PendingApproval{}, domain.NewAppError(domain.CodeInvalidInput, "ack: approval rejected: reviewer is not authorized or decisionID is not pending")
}

// SetChunkMeta attaches one numbered stream chunk to result's _meta so a
// client can restore order by seq and spot gaps without parsing the body.
func SetChunkMeta(result *mcpsdk.CallToolResult, ev domain.StreamEvent) error {
	return setStreamMeta(result, chunkMetaKey, ev)
}

// ChunkMetaFromResult reads a numbered stream chunk from result's _meta. The
// bool is false when the key is absent or the value cannot be decoded.
func ChunkMetaFromResult(result *mcpsdk.CallToolResult) (domain.StreamEvent, bool) {
	var ev domain.StreamEvent
	return ev, getStreamMeta(result, chunkMetaKey, &ev)
}

// SetFinalMeta attaches the terminal stream counters and the complete result
// to result's _meta.
func SetFinalMeta(result *mcpsdk.CallToolResult, final domain.StreamFinal) error {
	return setStreamMeta(result, finalMetaKey, final)
}

// FinalMetaFromResult reads the terminal stream counters from result's _meta.
func FinalMetaFromResult(result *mcpsdk.CallToolResult) (domain.StreamFinal, bool) {
	var final domain.StreamFinal
	return final, getStreamMeta(result, finalMetaKey, &final)
}

// setStreamMeta stores value as raw JSON in the result's _meta map. Raw JSON
// keeps the value well-formed on the wire while getStreamMeta can decode it
// whether the map holds our RawMessage or a map decoded from the wire.
func setStreamMeta(result *mcpsdk.CallToolResult, key string, value any) error {
	if result == nil {
		return errors.New("mcp: nil tool result")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if result.Meta == nil {
		result.Meta = mcpsdk.Meta{}
	}
	result.Meta[key] = json.RawMessage(b)
	return nil
}

func getStreamMeta(result *mcpsdk.CallToolResult, key string, out any) bool {
	if result == nil || result.Meta == nil {
		return false
	}
	raw, ok := result.Meta[key]
	if !ok {
		return false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, out) == nil
}

// ---- ADR 0058 п1 / T3.128: двухуровневая шина сигнал и журнал ----

// WakeSignal is the cheap level-1 notification. It carries only Head, the
// journal position that changed, and never a message body. Its only job is to
// wake a sleeping agent so it goes and reads the journal (ADR 0058 §1).
type WakeSignal struct {
	Head uint64 `json:"head"`
}

// JournalRecord is one level-2 entry. Seq is a gapless 1-based position, so it
// is also the cursor value an agent saves and later presents on reconnect.
// Payload is opaque to the bus: it is copied on write and on read, so neither
// a producer nor a consumer can change a committed entry through a shared
// slice.
type JournalRecord struct {
	Seq     uint64 `json:"seq"`
	Payload []byte `json:"payload,omitempty"`
}

// DualLevelBus keeps the two levels of ADR 0058 §1 apart. Level 1 is the cheap
// WakeSignal: it wakes an agent and carries a pointer only. Level 2 is the
// append-only journal: it guarantees delivery. An agent saves the cursor of
// the last record it processed and, after a reconnect, reads strictly after
// that cursor, so a missed record is never lost and a committed record is
// never delivered twice. The cursor lives with the journal, not in an MCP
// status response as in ADR 0025: this is two levels with different roles, not
// one status-and-cursor pair. The mutex serializes publish, read and cursor
// commit so positions stay gapless and a cursor cannot move backwards under
// concurrent calls in one process.
type DualLevelBus struct {
	mu      sync.Mutex
	journal []JournalRecord
	cursors map[string]uint64
}

// NewDualLevelBus builds an empty two-level bus.
func NewDualLevelBus() *DualLevelBus {
	return &DualLevelBus{cursors: make(map[string]uint64)}
}

// Publish appends payload to the journal and returns the cheap wake signal
// carrying the new Head. The signal never contains payload: the message is
// delivered only through ReadAfter.
func (b *DualLevelBus) Publish(payload []byte) WakeSignal {
	if b == nil {
		return WakeSignal{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.journal = append(b.journal, JournalRecord{
		Seq:     uint64(len(b.journal)) + 1,
		Payload: append([]byte(nil), payload...),
	})
	return WakeSignal{Head: uint64(len(b.journal))}
}

// Head returns the newest committed journal position.
func (b *DualLevelBus) Head() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return uint64(len(b.journal))
}

// ReadAfter returns the journal records with Seq greater than cursor, in
// ascending order. This is the level-2 delivery path: the caller passes the
// cursor it saved before the disconnect. Records at or below the cursor are
// never returned, so a reconnect cannot redeliver them; every record above it
// is returned, so none is lost. Neither the journal nor any saved cursor is
// changed by a read: the agent commits its cursor only after it processes the
// records.
func (b *DualLevelBus) ReadAfter(cursor uint64) []JournalRecord {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if cursor >= uint64(len(b.journal)) {
		return nil
	}
	out := make([]JournalRecord, 0, uint64(len(b.journal))-cursor)
	for _, rec := range b.journal[cursor:] {
		out = append(out, JournalRecord{Seq: rec.Seq, Payload: append([]byte(nil), rec.Payload...)})
	}
	return out
}

// Cursor returns the cursor saved for subscriber. Zero means the subscriber
// has not committed any record yet and will read the journal from its start.
func (b *DualLevelBus) Cursor(subscriber string) uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cursors[subscriber]
}

// CommitCursor saves seq as the last record subscriber processed and returns
// the effective saved cursor. It is monotonic: an older cursor cannot move the
// position back and redeliver records, and a cursor past Head is clamped to
// Head. Committing is what makes a delivered record stay delivered once across
// a reconnect.
func (b *DualLevelBus) CommitCursor(subscriber string, seq uint64) uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cursors == nil {
		b.cursors = make(map[string]uint64)
	}
	head := uint64(len(b.journal))
	if seq > head {
		seq = head
	}
	if seq > b.cursors[subscriber] {
		b.cursors[subscriber] = seq
	}
	return b.cursors[subscriber]
}
