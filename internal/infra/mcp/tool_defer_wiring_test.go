package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeDeferStore is the mock DeferWriter used by these tests. It enforces the
// same (watchID, reason) idempotency key as the production adapter and counts
// writes so a repeat can be proven not to reach the store.
type fakeDeferStore struct {
	mu     sync.Mutex
	cards  map[string]DeferredWiringCard
	writes int
	err    error
}

var _ DeferWriter = (*fakeDeferStore)(nil)

func newFakeDeferStore() *fakeDeferStore {
	return &fakeDeferStore{cards: make(map[string]DeferredWiringCard)}
}

func (s *fakeDeferStore) DeferWiring(_ context.Context, card DeferredWiringCard) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	s.writes++
	key := deferWiringKey(card.WatchID, card.Reason)
	if _, ok := s.cards[key]; ok {
		return false, nil
	}
	s.cards[key] = card
	return true, nil
}

func (s *fakeDeferStore) DeferredWirings(_ context.Context) ([]DeferredWiringCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make([]DeferredWiringCard, 0, len(s.cards))
	for _, c := range s.cards {
		out = append(out, c)
	}
	return out, nil
}

func (s *fakeDeferStore) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

func TestDeferWiringCreatesCard(t *testing.T) {
	store := newFakeDeferStore()
	h := NewDeferWiringHandler(store)
	in := DeferWiringInput{
		WatchID:    "ses_executor",
		PR:         42,
		Title:      "Wiring for contract",
		Reason:     "eighth tool needs a follow-up PR",
		OwnerFiles: []string{"internal/infra/mcp/server.go", "cmd/beacon/deferred.go"},
	}

	out, err := h.Defer(context.Background(), in)
	if err != nil {
		t.Fatalf("Defer: %v", err)
	}
	if out.CardID != "WIRING-42" {
		t.Errorf("cardID = %q, want WIRING-42", out.CardID)
	}
	if !out.Created {
		t.Error("created = false on the first call, want true")
	}
	if out.Status != DeferredStatusDeferred {
		t.Errorf("status = %q, want %q", out.Status, DeferredStatusDeferred)
	}
	for _, want := range []string{
		"# WIRING-42. Wiring for contract",
		"Владелец файлов: internal/infra/mcp/server.go, cmd/beacon/deferred.go",
		"Причина: eighth tool needs a follow-up PR",
		"Приёмка: Wiring для PR #42 выполнен, карточка WIRING-42 закрыта.",
	} {
		if !strings.Contains(out.CardMarkdown, want) {
			t.Errorf("cardMarkdown missing %q:\n%s", want, out.CardMarkdown)
		}
	}
	if store.writeCount() != 1 {
		t.Errorf("store writes = %d, want 1", store.writeCount())
	}
}

func TestDeferWiringIdempotentByWatchAndReason(t *testing.T) {
	store := newFakeDeferStore()
	h := NewDeferWiringHandler(store)
	first := DeferWiringInput{WatchID: "ses_a", PR: 7, Title: "first title", Reason: "same reason", OwnerFiles: []string{"a.go"}}

	one, err := h.Defer(context.Background(), first)
	if err != nil {
		t.Fatalf("first Defer: %v", err)
	}
	second := first
	second.Title = "changed title"
	second.OwnerFiles = []string{"b.go"}
	two, err := h.Defer(context.Background(), second)
	if err != nil {
		t.Fatalf("second Defer: %v", err)
	}
	if two.Created {
		t.Error("repeat call reported created=true, want false")
	}
	if two.CardID != one.CardID || two.CardMarkdown != one.CardMarkdown {
		t.Errorf("repeat card changed: first=%+v second=%+v", one, two)
	}
	if store.writeCount() != 1 {
		t.Errorf("store writes = %d after repeat, want 1", store.writeCount())
	}

	other := first
	other.Reason = "different reason"
	three, err := h.Defer(context.Background(), other)
	if err != nil {
		t.Fatalf("third Defer: %v", err)
	}
	if !three.Created {
		t.Error("a different reason must create a new card")
	}
	if store.writeCount() != 2 {
		t.Errorf("store writes = %d after second reason, want 2", store.writeCount())
	}
}

func TestDeferWiringValidatesInput(t *testing.T) {
	invalid := []DeferWiringInput{
		{},
		{WatchID: "ses_a", PR: 0, Reason: "r"},
		{WatchID: "ses_a", PR: 1, Reason: ""},
	}
	for i, in := range invalid {
		_, err := NewDeferWiringHandler(newFakeDeferStore()).Defer(context.Background(), in)
		var appErr *domain.AppError
		if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
			t.Errorf("case %d: err = %v, want INVALID_INPUT", i, err)
		}
	}

	_, err := NewDeferWiringHandler(nil).Defer(context.Background(), DeferWiringInput{WatchID: "ses_a", PR: 1, Reason: "r"})
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != domain.CodePersistFailed {
		t.Errorf("nil store: err = %v, want PERSIST_FAILED", err)
	}

	store := newFakeDeferStore()
	store.err = errors.New("disk is full")
	_, err = NewDeferWiringHandler(store).Defer(context.Background(), DeferWiringInput{WatchID: "ses_a", PR: 1, Reason: "r"})
	if !errors.As(err, &appErr) || appErr.Code != domain.CodePersistFailed {
		t.Errorf("store error: err = %v, want PERSIST_FAILED", err)
	}
}

func TestDeferWiringToolThroughServer(t *testing.T) {
	store := newFakeDeferStore()
	session := connectTestSession(t, Dependencies{DeferWiring: store})

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "beacon_defer_wiring",
		Arguments: map[string]any{
			"watchID":    "ses_executor",
			"pr":         9,
			"title":      "Test card",
			"reason":     "handoff",
			"ownerFiles": []any{"server.go"},
		},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_defer_wiring: %v", err)
	}
	if result.IsError {
		t.Fatalf("beacon_defer_wiring returned isError: %v", result.Content)
	}
	out := decodeStructured[DeferWiringOutput](t, result)
	if out.CardID != "WIRING-9" || !out.Created || out.Status != DeferredStatusDeferred {
		t.Errorf("output = %+v, want WIRING-9/created/deferred", out)
	}
	if !strings.Contains(out.CardMarkdown, "WIRING-9") {
		t.Errorf("cardMarkdown missing card id: %s", out.CardMarkdown)
	}
}

func TestDeferWiringOutputSchema(t *testing.T) {
	schemas, err := AllOutputSchemas()
	if err != nil {
		t.Fatalf("AllOutputSchemas: %v", err)
	}
	schema, ok := schemas["beacon_defer_wiring"]
	if !ok {
		t.Fatal("AllOutputSchemas missing beacon_defer_wiring")
	}
	assertSchemaFromType[DeferWiringOutput](t, schema)

	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve schema: %v", err)
	}
	raw, err := json.Marshal(DeferWiringOutput{
		CardID: "WIRING-1", Created: true, Status: DeferredStatusDeferred,
		CardMarkdown: "# WIRING-1. Title\n",
	})
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if err := resolved.Validate(instance); err != nil {
		t.Errorf("real response does not validate against its output schema: %v", err)
	}
}

func TestRenderDeferred(t *testing.T) {
	store := newFakeDeferStore()
	for _, card := range []DeferredWiringCard{
		{CardID: "WIRING-9", WatchID: "ses_b", PR: 9, Title: "Second", Reason: "b", Status: DeferredStatusDeferred},
		{CardID: "WIRING-2", WatchID: "ses_a", PR: 2, Title: "First", Reason: "a", Status: DeferredStatusDeferred},
	} {
		if _, err := store.DeferWiring(context.Background(), card); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}
	cards, err := store.DeferredWirings(context.Background())
	if err != nil {
		t.Fatalf("DeferredWirings: %v", err)
	}

	table, err := RenderDeferred("table", cards)
	if err != nil {
		t.Fatalf("RenderDeferred(table): %v", err)
	}
	if !strings.Contains(table, "CARDID") || !strings.Contains(table, "WIRING-2") || !strings.Contains(table, "WIRING-9") {
		t.Errorf("table output missing cards:\n%s", table)
	}
	if strings.Index(table, "WIRING-2") > strings.Index(table, "WIRING-9") {
		t.Errorf("table output is not sorted by cardID:\n%s", table)
	}

	encoded, err := RenderDeferred("json", cards)
	if err != nil {
		t.Fatalf("RenderDeferred(json): %v", err)
	}
	var decoded []DeferredWiringCard
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("invalid JSON output %q: %v", encoded, err)
	}
	if len(decoded) != 2 || decoded[0].CardID != "WIRING-2" || decoded[1].CardID != "WIRING-9" {
		t.Errorf("JSON cards = %+v, want two sorted cards", decoded)
	}

	if _, err := RenderDeferred("yaml", cards); err == nil {
		t.Error("unknown format must fail")
	}
}
