package webhook

import (
	"strings"
	"sync"
)

// Decision is the filter's verdict for one delivery. DecisionAccept is the
// only verdict that authorizes queueing work. Every other verdict drops the
// delivery: no hint, no outbox row, only the returned audit reason survives.
type Decision string

const (
	DecisionAccept            Decision = "accept"
	DecisionRejectEmpty       Decision = "reject_empty"
	DecisionRejectEvent       Decision = "reject_event"
	DecisionRejectBranch      Decision = "reject_branch"
	DecisionRejectAuthor      Decision = "reject_author"
	DecisionDuplicateDelivery Decision = "duplicate_delivery"
	DecisionDuplicateHeadSHA  Decision = "duplicate_head_sha"

	reasonAccepted = "accepted"
)

// Publish reports whether the decision allows an outbox row. A filtered
// delivery must never reach the queue, so callers check Publish before
// writing anything durable (BS.28, ADR 0193).
func (d Decision) Publish() bool { return d == DecisionAccept }

// FilterConfig declares the deliveries one bridge cares about. An empty
// allowlist accepts any non-empty value for that field; a non-empty allowlist
// is a closed, exact set.
type FilterConfig struct {
	Events   []string
	Branches []string
	Authors  []string
}

// Filter classifies deliveries for one bridge and owns the dedupe index.
// It performs no I/O: no GitHub call, no CI read, no outbox write. The verdict
// and its reason are returned so the caller can record them in the audit log.
// The decision logic is pure; the dedupe index is the only mutable state and
// it converges under concurrent calls.
type Filter struct {
	events   map[string]struct{}
	branches map[string]struct{}
	authors  map[string]struct{}

	mu         sync.Mutex
	deliveries map[string]struct{}
	headSHAs   map[string]struct{}
}

// NewFilter builds a filter with empty dedupe indexes.
func NewFilter(cfg FilterConfig) *Filter {
	return &Filter{
		events:     keySet(cfg.Events),
		branches:   keySet(cfg.Branches),
		authors:    keySet(cfg.Authors),
		deliveries: make(map[string]struct{}),
		headSHAs:   make(map[string]struct{}),
	}
}

// Restore seeds one stored delivery into the dedupe index. Startup calls it
// for every row in the journal so a restart keeps the delivery IDs and head
// SHAs the previous process already handled. Empty keys are ignored. Values
// are trimmed to match the inputs Filter takes.
func (f *Filter) Restore(deliveryID, headSHA string) {
	if f == nil {
		return
	}
	deliveryID = strings.TrimSpace(deliveryID)
	headSHA = strings.TrimSpace(headSHA)
	if deliveryID == "" && headSHA == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if deliveryID != "" {
		if f.deliveries == nil {
			f.deliveries = make(map[string]struct{})
		}
		f.deliveries[deliveryID] = struct{}{}
	}
	if headSHA != "" {
		if f.headSHAs == nil {
			f.headSHAs = make(map[string]struct{})
		}
		f.headSHAs[headSHA] = struct{}{}
	}
}

// ClaimDelivery reports whether a delivery ID is new and records it when so.
// It is the fast in-memory gate for the receiver. The restored index makes a
// repeat after a restart fail this check, so no second effect is queued. The
// durable journal keeps its own idempotency; this method only avoids the
// write for a delivery the process already knows.
func (f *Filter) ClaimDelivery(deliveryID string) bool {
	if f == nil {
		return false
	}
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, seen := f.deliveries[deliveryID]; seen {
		return false
	}
	if f.deliveries == nil {
		f.deliveries = make(map[string]struct{})
	}
	f.deliveries[deliveryID] = struct{}{}
	return true
}

func keySet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

// Filter decides whether one delivery may be queued. Inputs are trimmed before
// comparison. All five fields must be present: a delivery without a routing
// key or a dedupe key cannot be handled safely. Field filters run first, then
// delivery dedupe, then head-SHA dedupe. A delivery enters the index only when
// it is accepted, so a filtered event can be retried with corrected fields and
// a rejected event can never suppress a later, distinct delivery.
func (f *Filter) Filter(event, branch, author, deliveryID, headSHA string) (Decision, string) {
	event = strings.TrimSpace(event)
	branch = strings.TrimSpace(branch)
	author = strings.TrimSpace(author)
	deliveryID = strings.TrimSpace(deliveryID)
	headSHA = strings.TrimSpace(headSHA)

	f.mu.Lock()
	defer f.mu.Unlock()
	_, seenDelivery := f.deliveries[deliveryID]
	_, seenSHA := f.headSHAs[headSHA]
	decision, reason := decide(f.events, f.branches, f.authors,
		event, branch, author, deliveryID, headSHA, seenDelivery, seenSHA)

	if decision == DecisionAccept {
		if f.deliveries == nil {
			f.deliveries = make(map[string]struct{})
		}
		if f.headSHAs == nil {
			f.headSHAs = make(map[string]struct{})
		}
		f.deliveries[deliveryID] = struct{}{}
		f.headSHAs[headSHA] = struct{}{}
	}
	return decision, reason
}

// decide is the pure core of Filter. It touches no state and performs no I/O:
// the caller supplies the dedupe lookups and records an accepted delivery.
func decide(events, branches, authors map[string]struct{},
	event, branch, author, deliveryID, headSHA string,
	seenDelivery, seenSHA bool,
) (Decision, string) {
	for _, field := range []struct{ name, value string }{
		{"event", event},
		{"branch", branch},
		{"author", author},
		{"deliveryID", deliveryID},
		{"headSHA", headSHA},
	} {
		if field.value == "" {
			return DecisionRejectEmpty, "webhook: missing " + field.name
		}
	}
	if !allowed(events, event) {
		return DecisionRejectEvent, "webhook: event " + event + " not accepted"
	}
	if !allowed(branches, branch) {
		return DecisionRejectBranch, "webhook: branch " + branch + " not accepted"
	}
	if !allowed(authors, author) {
		return DecisionRejectAuthor, "webhook: author " + author + " not accepted"
	}
	if seenDelivery {
		return DecisionDuplicateDelivery, "webhook: delivery " + deliveryID + " already handled"
	}
	if seenSHA {
		return DecisionDuplicateHeadSHA, "webhook: head SHA " + headSHA + " already handled"
	}
	return DecisionAccept, reasonAccepted
}

func allowed(set map[string]struct{}, value string) bool {
	if len(set) == 0 {
		return true
	}
	_, ok := set[value]
	return ok
}
