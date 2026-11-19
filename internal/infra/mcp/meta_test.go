package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

func requestWithMeta(meta mcpsdk.Meta) *mcpsdk.CallToolRequest {
	return &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Meta: meta}}
}

func TestSessionIDFromRequestReadsKeyValueVerbatim(t *testing.T) {
	const value = "sess/with:symbols#and-dashes"
	got := SessionIDFromRequest(requestWithMeta(mcpsdk.Meta{sessionIDMetaKey: value}))
	if got == nil {
		t.Fatalf("SessionIDFromRequest() = nil, want %q", value)
	}
	if *got != value {
		t.Errorf("SessionIDFromRequest() = %q, want %q", *got, value)
	}
}

func TestSessionIDFromRequestMissingKeyReturnsNil(t *testing.T) {
	got := SessionIDFromRequest(requestWithMeta(mcpsdk.Meta{"other/key": "value"}))
	if got != nil {
		t.Errorf("SessionIDFromRequest() = %q, want nil when the key is absent", *got)
	}
}

func TestSessionIDFromRequestEmptyValueReturnsNil(t *testing.T) {
	got := SessionIDFromRequest(requestWithMeta(mcpsdk.Meta{sessionIDMetaKey: ""}))
	if got != nil {
		t.Errorf("SessionIDFromRequest() = %q, want nil for an empty value", *got)
	}
}

func TestStreamMetaRoundTrip(t *testing.T) {
	result := &mcpsdk.CallToolResult{}

	ev := domain.StreamEvent{Seq: 7, WatchID: "w1", Payload: []byte("chunk")}
	if err := SetChunkMeta(result, ev); err != nil {
		t.Fatalf("SetChunkMeta: %v", err)
	}
	gotEv, ok := ChunkMetaFromResult(result)
	if !ok {
		t.Fatal("ChunkMetaFromResult: chunk meta not found")
	}
	if gotEv.Seq != 7 || gotEv.WatchID != "w1" || string(gotEv.Payload) != "chunk" {
		t.Errorf("chunk meta = %+v, want seq 7, watchID w1, payload chunk", gotEv)
	}

	final := domain.StreamFinal{
		Chunks:     7,
		Missing:    []uint64{2, 5},
		Duplicates: 3,
		Streamed:   true,
	}
	if err := SetFinalMeta(result, final); err != nil {
		t.Fatalf("SetFinalMeta: %v", err)
	}
	gotFinal, ok := FinalMetaFromResult(result)
	if !ok {
		t.Fatal("FinalMetaFromResult: final meta not found")
	}
	if gotFinal.Chunks != 7 || gotFinal.Duplicates != 3 || !gotFinal.Streamed {
		t.Errorf("final meta = %+v, want chunks 7, duplicates 3, streamed true", gotFinal)
	}
	if !reflect.DeepEqual(gotFinal.Missing, []uint64{2, 5}) {
		t.Errorf("final missing = %v, want [2 5]", gotFinal.Missing)
	}
}

func TestStreamMetaSurvivesResultJSONRoundTrip(t *testing.T) {
	result := &mcpsdk.CallToolResult{}
	ev := domain.StreamEvent{Seq: 2, WatchID: "w2"}
	if err := SetChunkMeta(result, ev); err != nil {
		t.Fatalf("SetChunkMeta: %v", err)
	}

	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded mcpsdk.CallToolResult
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	got, ok := ChunkMetaFromResult(&decoded)
	if !ok {
		t.Fatal("chunk meta lost across the wire")
	}
	if got.Seq != 2 || got.WatchID != "w2" {
		t.Errorf("chunk meta after wire = %+v, want seq 2, watchID w2", got)
	}
}

func TestStreamMetaAbsentAndInvalid(t *testing.T) {
	if _, ok := ChunkMetaFromResult(nil); ok {
		t.Error("nil result must not carry chunk meta")
	}
	if _, ok := FinalMetaFromResult(&mcpsdk.CallToolResult{}); ok {
		t.Error("result without _meta must not carry final meta")
	}
	if err := SetChunkMeta(nil, domain.StreamEvent{}); err == nil {
		t.Error("SetChunkMeta on a nil result must fail")
	}

	invalid := &mcpsdk.CallToolResult{Meta: mcpsdk.Meta{chunkMetaKey: "not an object"}}
	if _, ok := ChunkMetaFromResult(invalid); ok {
		t.Error("a malformed chunk meta must not decode")
	}
}

func TestSessionIDFromRequestAbsentMetaDoesNotBreak(t *testing.T) {
	cases := map[string]*mcpsdk.CallToolRequest{
		"nil request":     nil,
		"nil params":      {},
		"nil meta":        {Params: &mcpsdk.CallToolParamsRaw{}},
		"non-string meta": requestWithMeta(mcpsdk.Meta{sessionIDMetaKey: 42}),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SessionIDFromRequest(req); got != nil {
				t.Errorf("SessionIDFromRequest() = %q, want nil", *got)
			}
		})
	}
}

// memPendingApprovalStore is the authoritative pending list the server holds.
// calls records how many times the core read it, so a test can prove the read
// happens on every authentication instead of trusting a cached value.
type memPendingApprovalStore struct {
	pending []PendingApproval
	err     error
	calls   int
}

func (s *memPendingApprovalStore) PendingApprovals() ([]PendingApproval, error) {
	s.calls++
	return s.pending, s.err
}

func requestFromSession(sessionID string) *mcpsdk.CallToolRequest {
	return requestWithMeta(mcpsdk.Meta{sessionIDMetaKey: sessionID})
}

// TestAuthenticateApprovalAcceptsAuthorizedReviewer proves that the matching
// reviewer and decision ID return the pending record, including its RunID.
func TestAuthenticateApprovalAcceptsAuthorizedReviewer(t *testing.T) {
	store := &memPendingApprovalStore{pending: []PendingApproval{
		{DecisionID: "d1", ReviewerSessionID: "alice", RunID: "run-1"},
		{DecisionID: "d2", ReviewerSessionID: "bob", RunID: "run-2"},
	}}
	auth := NewApprovalAuthenticator(store)

	got, err := auth.Authenticate(requestFromSession("alice"), "d1", ApprovalSnapshot{})
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got.DecisionID != "d1" || got.ReviewerSessionID != "alice" || got.RunID != "run-1" {
		t.Errorf("Authenticate() = %+v, want the pending record for d1", got)
	}
	if store.calls != 1 {
		t.Errorf("store reads = %d, want 1", store.calls)
	}
}

// TestAuthenticateApprovalRejectsForeignReviewer is the acceptance test: an
// approval from a session that is not authorized for the decision is rejected
// and no pending approval is returned.
func TestAuthenticateApprovalRejectsForeignReviewer(t *testing.T) {
	store := &memPendingApprovalStore{pending: []PendingApproval{
		{DecisionID: "d1", ReviewerSessionID: "alice", RunID: "run-1"},
	}}
	auth := NewApprovalAuthenticator(store)

	got, err := auth.Authenticate(requestFromSession("mallory"), "d1", ApprovalSnapshot{})
	if err == nil {
		t.Fatalf("foreign approval was accepted: %+v", got)
	}
	assertCode(t, err, domain.CodeInvalidInput)
	if got != (PendingApproval{}) {
		t.Errorf("foreign approval returned a pending record: %+v", got)
	}
}

// TestAuthenticateApprovalRejectsUnknownDecisionID proves the decision ID is
// checked against the pending list, not taken from the client at face value.
func TestAuthenticateApprovalRejectsUnknownDecisionID(t *testing.T) {
	store := &memPendingApprovalStore{pending: []PendingApproval{
		{DecisionID: "d1", ReviewerSessionID: "alice", RunID: "run-1"},
	}}
	auth := NewApprovalAuthenticator(store)

	if _, err := auth.Authenticate(requestFromSession("alice"), "d-forbidden", ApprovalSnapshot{}); err == nil {
		t.Error("an unknown decisionID was accepted")
	} else {
		assertCode(t, err, domain.CodeInvalidInput)
	}
}

// TestAuthenticateApprovalIgnoresClientSnapshot proves the untrusted snapshot
// cannot grant an approval. It forges the authorized reviewer and a pending
// decision, yet the real request session is foreign, so the approval is
// rejected. The authoritative reviewer comes from the request _meta.
func TestAuthenticateApprovalIgnoresClientSnapshot(t *testing.T) {
	store := &memPendingApprovalStore{pending: []PendingApproval{
		{DecisionID: "d1", ReviewerSessionID: "alice", RunID: "run-1"},
	}}
	auth := NewApprovalAuthenticator(store)
	forged := ApprovalSnapshot{ReviewerSessionID: "alice", DecisionID: "d1", RunID: "run-1"}

	if _, err := auth.Authenticate(requestFromSession("mallory"), "d1", forged); err == nil {
		t.Error("a forged client snapshot granted an approval")
	} else {
		assertCode(t, err, domain.CodeInvalidInput)
	}
}

// TestAuthenticateApprovalRequiresReviewerAndDecisionID covers missing
// authoritative inputs before any pending lookup can succeed.
func TestAuthenticateApprovalRequiresReviewerAndDecisionID(t *testing.T) {
	store := &memPendingApprovalStore{pending: []PendingApproval{
		{DecisionID: "d1", ReviewerSessionID: "alice", RunID: "run-1"},
	}}
	auth := NewApprovalAuthenticator(store)

	if _, err := auth.Authenticate(nil, "d1", ApprovalSnapshot{}); err == nil {
		t.Error("an approval with no reviewer sessionID was accepted")
	} else {
		assertCode(t, err, domain.CodeInvalidInput)
	}
	if _, err := auth.Authenticate(requestFromSession("alice"), "", ApprovalSnapshot{}); err == nil {
		t.Error("an approval with no decisionID was accepted")
	} else {
		assertCode(t, err, domain.CodeInvalidInput)
	}
}

// TestAuthenticateApprovalStoreError proves a failed pending read is surfaced
// instead of being mistaken for an absent decision.
func TestAuthenticateApprovalStoreError(t *testing.T) {
	want := errors.New("store unavailable")
	auth := NewApprovalAuthenticator(&memPendingApprovalStore{err: want})

	if _, err := auth.Authenticate(requestFromSession("alice"), "d1", ApprovalSnapshot{}); !errors.Is(err, want) {
		t.Errorf("Authenticate() error = %v, want the store error", err)
	}
}

// TestAuthenticateApprovalReadsPendingOnEveryCall proves the pending list is
// read from the store each time, so a decision removed since the last call is
// no longer approvable.
func TestAuthenticateApprovalReadsPendingOnEveryCall(t *testing.T) {
	store := &memPendingApprovalStore{pending: []PendingApproval{
		{DecisionID: "d1", ReviewerSessionID: "alice", RunID: "run-1"},
	}}
	auth := NewApprovalAuthenticator(store)

	if _, err := auth.Authenticate(requestFromSession("alice"), "d1", ApprovalSnapshot{}); err != nil {
		t.Fatalf("first Authenticate() error = %v", err)
	}
	store.pending = nil
	if _, err := auth.Authenticate(requestFromSession("alice"), "d1", ApprovalSnapshot{}); err == nil {
		t.Error("a decision removed from the pending list was still approvable")
	}
	if store.calls != 2 {
		t.Errorf("store reads = %d, want 2", store.calls)
	}
}

// --- ADR 0058 п1 / T3.128: двухуровневая шина сигнал и журнал ---

// TestPublishSignalIsOnlyAPointer proves level 1 wakes without delivering:
// the signal carries just the journal head, while the message lives only in
// level 2 and is reachable through ReadAfter.
func TestPublishSignalIsOnlyAPointer(t *testing.T) {
	bus := NewDualLevelBus()
	sig := bus.Publish([]byte("wake me, the body is in the journal"))

	if sig.Head != 1 {
		t.Fatalf("signal head = %d, want 1", sig.Head)
	}
	if bus.Head() != 1 {
		t.Errorf("journal head = %d, want 1", bus.Head())
	}

	b, err := json.Marshal(sig)
	if err != nil {
		t.Fatalf("marshal signal: %v", err)
	}
	if string(b) != `{"head":1}` {
		t.Errorf("signal wire form = %s, want only the head pointer", b)
	}

	// The signal's own head is already consumed: it points at the record, it
	// does not carry the record. Reading after it returns nothing; reading
	// from the saved cursor before it returns the message.
	if got := bus.ReadAfter(sig.Head); len(got) != 0 {
		t.Errorf("signal delivered %d records, want 0", len(got))
	}
	got := bus.ReadAfter(0)
	if len(got) != 1 || string(got[0].Payload) != "wake me, the body is in the journal" {
		t.Fatalf("journal after cursor 0 = %+v, want one message", got)
	}
}

// TestReadAfterCursorDeliversEachRecordOnce proves a 1-based gapless journal
// and that a saved cursor is the delivery boundary: records above it arrive
// once, records at or below it never arrive again.
func TestReadAfterCursorDeliversEachRecordOnce(t *testing.T) {
	bus := NewDualLevelBus()
	for _, msg := range []string{"a", "b", "c"} {
		bus.Publish([]byte(msg))
	}

	got := bus.ReadAfter(0)
	if len(got) != 3 {
		t.Fatalf("first read = %d records, want 3", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Seq != uint64(i+1) || string(got[i].Payload) != want {
			t.Errorf("record %d = %+v, want seq %d payload %q", i, got[i], i+1, want)
		}
	}
	if again := bus.ReadAfter(0); len(again) != 3 {
		t.Errorf("replay from the same cursor = %d records, want 3 (read is pure)", len(again))
	}
	if after := bus.ReadAfter(3); len(after) != 0 {
		t.Errorf("read after cursor 3 = %d records, want 0", len(after))
	}

	bus.Publish([]byte("d"))
	after := bus.ReadAfter(3)
	if len(after) != 1 || after[0].Seq != 4 || string(after[0].Payload) != "d" {
		t.Fatalf("read after cursor 3 = %+v, want only record 4", after)
	}
	if after := bus.ReadAfter(4); len(after) != 0 {
		t.Errorf("read after head = %d records, want 0", len(after))
	}
	if after := bus.ReadAfter(99); len(after) != 0 {
		t.Errorf("read after a cursor past head = %d records, want 0", len(after))
	}
}

// TestReconnectResumesFromSavedCursor is the acceptance test: an agent commits
// the cursor of what it processed, a new message arrives while it is away, and
// after reconnect it gets exactly that new message, once. The earlier message
// is not redelivered and the new one is not lost.
func TestReconnectResumesFromSavedCursor(t *testing.T) {
	bus := NewDualLevelBus()
	const subscriber = "agent-1"

	sig := bus.Publish([]byte("first"))
	first := bus.ReadAfter(bus.Cursor(subscriber))
	if len(first) != 1 || first[0].Seq != sig.Head {
		t.Fatalf("first delivery = %+v, want one record at signal head %d", first, sig.Head)
	}
	// The agent processes the record, then saves its cursor.
	if got := bus.CommitCursor(subscriber, first[0].Seq); got != 1 {
		t.Fatalf("saved cursor = %d, want 1", got)
	}

	// Disconnect. A new message is published and a wake signal is emitted while
	// the agent is away. On reconnect the saved cursor is the only state used.
	sig = bus.Publish([]byte("second while away"))
	if sig.Head != 2 {
		t.Fatalf("wake signal head = %d, want 2", sig.Head)
	}
	if saved := bus.Cursor(subscriber); saved != 1 {
		t.Fatalf("cursor after reconnect = %d, want the saved 1", saved)
	}
	redelivered := bus.ReadAfter(bus.Cursor(subscriber))
	if len(redelivered) != 1 {
		t.Fatalf("delivery after reconnect = %d records, want 1", len(redelivered))
	}
	if redelivered[0].Seq != 2 || string(redelivered[0].Payload) != "second while away" {
		t.Errorf("delivery after reconnect = %+v, want only the new record 2", redelivered[0])
	}

	bus.CommitCursor(subscriber, redelivered[0].Seq)
	if again := bus.ReadAfter(bus.Cursor(subscriber)); len(again) != 0 {
		t.Errorf("reconnect after committing cursor 2 delivered %d records, want 0", len(again))
	}
}

// TestCommitCursorIsMonotonic guards the no-duplicate half of the contract:
// an old cursor cannot rewind delivery, and a cursor past head is clamped.
func TestCommitCursorIsMonotonic(t *testing.T) {
	bus := NewDualLevelBus()
	bus.Publish([]byte("one"))
	bus.Publish([]byte("two"))

	if got := bus.CommitCursor("a", 2); got != 2 {
		t.Errorf("commit 2 = %d, want 2", got)
	}
	if got := bus.CommitCursor("a", 1); got != 2 {
		t.Errorf("commit 1 after 2 = %d, want 2 (cursor must not rewind)", got)
	}
	if got := bus.CommitCursor("b", 99); got != 2 {
		t.Errorf("commit 99 with head 2 = %d, want 2 (clamped to head)", got)
	}
	if got := bus.Cursor("a"); got != 2 {
		t.Errorf("cursor a = %d, want 2", got)
	}
	if got := bus.Cursor("unknown"); got != 0 {
		t.Errorf("cursor of an unknown subscriber = %d, want 0", got)
	}
	if got := bus.ReadAfter(bus.Cursor("a")); len(got) != 0 {
		t.Errorf("read after a committed cursor = %d records, want 0", len(got))
	}
}

// TestJournalOwnsPayload proves the journal is authoritative: a producer
// reusing its buffer after publish, or a consumer editing a delivered copy,
// cannot change what the next reconnect reads.
func TestJournalOwnsPayload(t *testing.T) {
	bus := NewDualLevelBus()
	payload := []byte("original")
	bus.Publish(payload)
	payload[0] = 'X'

	got := bus.ReadAfter(0)
	if len(got) != 1 || string(got[0].Payload) != "original" {
		t.Fatalf("journal changed with the producer buffer: %+v", got)
	}
	got[0].Payload[0] = 'Y'
	again := bus.ReadAfter(0)
	if len(again) != 1 || string(again[0].Payload) != "original" {
		t.Fatalf("journal changed with a consumer copy: %+v", again)
	}
}

// TestNilBusIsSafe matches the defensive style of the meta helpers: a nil bus
// wakes nobody and delivers nothing instead of panicking.
func TestNilBusIsSafe(t *testing.T) {
	var bus *DualLevelBus
	if sig := bus.Publish([]byte("x")); sig.Head != 0 {
		t.Errorf("nil bus signal = %+v, want zero", sig)
	}
	if bus.Head() != 0 || bus.Cursor("a") != 0 || bus.CommitCursor("a", 5) != 0 {
		t.Error("nil bus must report zero head and cursor")
	}
	if got := bus.ReadAfter(0); got != nil {
		t.Errorf("nil bus delivered %+v, want nothing", got)
	}
}

// TestConcurrentPublishKeepsGaplessSequence proves the journal positions stay
// gapless and unique when several producers publish at once, so a cursor is a
// stable boundary under concurrent calls in one process.
func TestConcurrentPublishKeepsGaplessSequence(t *testing.T) {
	const n = 64
	bus := NewDualLevelBus()
	heads := make(chan uint64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			heads <- bus.Publish([]byte(fmt.Sprintf("m%d", i))).Head
		}(i)
	}
	wg.Wait()
	close(heads)

	seen := make(map[uint64]bool, n)
	for head := range heads {
		if seen[head] {
			t.Errorf("duplicate journal head %d", head)
		}
		seen[head] = true
	}
	if len(seen) != n {
		t.Fatalf("distinct heads = %d, want %d", len(seen), n)
	}
	for i := uint64(1); i <= n; i++ {
		if !seen[i] {
			t.Errorf("journal head %d missing from the sequence", i)
		}
	}

	records := bus.ReadAfter(0)
	if len(records) != n {
		t.Fatalf("journal records = %d, want %d", len(records), n)
	}
	for i, rec := range records {
		if rec.Seq != uint64(i+1) {
			t.Fatalf("record %d has seq %d, want %d", i, rec.Seq, i+1)
		}
	}
}
