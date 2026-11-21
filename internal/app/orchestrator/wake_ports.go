package orchestrator

import "context"

// WakePointer carries identifiers only. It mirrors the wake bridge fields so
// the app layer declares the shape it needs without importing internal/infra
// (DIP, ARDH-15). The adapter in cmd translates it to the concrete wake type.
type WakePointer struct {
	Adapter      string
	Target       string
	WatchID      string
	ObligationID string
	Revision     uint64
	VerdictRef   string
	Attempt      int
	Delivery     string
}

// WakeReceipt is the primitive result of one wake delivery attempt.
type WakeReceipt struct {
	ObligationID string
	Attempt      int
	Delivery     string
	Outcome      string
}

// Wake receipt outcomes. They match the wake bridge strings so the adapter in
// cmd passes outcomes through without translation.
const (
	WakeDelivered = "delivered"
	WakeQueued    = "queued"
	WakeEnqueued  = "enqueued"
	WakeErrored   = "error"
)

// WakeDefaultAdapter is the fallback name when a Watch names no adapter.
const WakeDefaultAdapter = "opencode"

// WakeBackend delivers one wake pointer. cmd/beacon owns the wake adapter that
// implements this port.
type WakeBackend interface {
	Deliver(ctx context.Context, p WakePointer) (WakeReceipt, bool)
}

// WakeDedup is the process-wide wake-identity ledger. cmd supplies the shared
// instance so the CI bridge and the daemon agree on duplicates.
type WakeDedup interface {
	Seen(obligationID string) bool
	Claim(obligationID string) bool
}
