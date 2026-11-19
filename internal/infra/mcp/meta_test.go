package mcp

import (
	"encoding/json"
	"errors"
	"reflect"
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
