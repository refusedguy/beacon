package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeObligationLister is an in-memory read port. It records every requested
// watchID so a test can prove the handler never asks for another Watch.
type fakeObligationLister struct {
	byWatch map[string][]domain.Obligation
	calls   []string
	err     error
}

func (f *fakeObligationLister) WatchObligations(watchID string) ([]domain.Obligation, error) {
	f.calls = append(f.calls, watchID)
	if f.err != nil {
		return nil, f.err
	}
	return append([]domain.Obligation(nil), f.byWatch[watchID]...), nil
}

// fakeObligationNotFound is the missing-row sentinel returned by the fake
// watch reader. It satisfies resultStoreNotFound through IsNotFound.
type fakeObligationNotFound struct{}

func (*fakeObligationNotFound) Error() string { return "watch not found" }

type fakeObligationWatchReader struct {
	watches map[string]domain.Watch
	calls   []string
	err     error
}

func (f *fakeObligationWatchReader) Watch(id string) (domain.Watch, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return domain.Watch{}, f.err
	}
	w, ok := f.watches[id]
	if !ok {
		return domain.Watch{}, &fakeObligationNotFound{}
	}
	return w, nil
}

func (*fakeObligationWatchReader) IsNotFound(err error) bool {
	var missing *fakeObligationNotFound
	return errors.As(err, &missing)
}

var obligationsTestTime = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

// obligationsFixture builds three obligations: two on revision 3 and one on
// revision 2, each for a different subscriber (AT-04 verification).
func obligationsFixture() []domain.Obligation {
	return []domain.Obligation{
		{
			ID:             "w-3-ses_a",
			WatchID:        "w",
			Revision:       3,
			SubscriberID:   "ses_a",
			VerdictPointer: domain.VerdictReady,
			StagedAt:       obligationsTestTime,
			ClaimedAt:      obligationsTestTime.Add(1 * time.Minute),
			ReadAt:         obligationsTestTime.Add(2 * time.Minute),
			DeliveredAt:    obligationsTestTime.Add(3 * time.Minute),
		},
		{
			ID:             "w-3-ses_b",
			WatchID:        "w",
			Revision:       3,
			SubscriberID:   "ses_b",
			VerdictPointer: domain.VerdictKeepWorking,
			StagedAt:       obligationsTestTime.Add(4 * time.Minute),
		},
		{
			ID:             "w-2-ses_c",
			WatchID:        "w",
			Revision:       2,
			SubscriberID:   "ses_c",
			VerdictPointer: domain.VerdictWatching,
		},
	}
}

func newObligationsFixture() (*ObligationsHandler, *fakeObligationLister, *fakeObligationWatchReader) {
	lister := &fakeObligationLister{byWatch: map[string][]domain.Obligation{
		"w": obligationsFixture(),
	}}
	watches := &fakeObligationWatchReader{watches: map[string]domain.Watch{
		"w": {ID: "w", Revision: 3},
	}}
	return NewObligationsHandler(watches, lister), lister, watches
}

func TestObligationsNoFilterReturnsAllRows(t *testing.T) {
	handler, lister, watches := newObligationsFixture()

	out, err := handler.List(context.Background(), ObligationsInput{WatchID: "w"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !out.Found {
		t.Error("Found = false, want true for a Watch with obligations")
	}
	if out.WatchID != "w" || out.Revision != 3 {
		t.Errorf("WatchID/Revision = %q/%d, want w/3", out.WatchID, out.Revision)
	}
	if len(out.Obligations) != 3 {
		t.Fatalf("obligations = %d, want 3", len(out.Obligations))
	}
	first := out.Obligations[0]
	if first.ObligationID != "w-3-ses_a" || first.SubscriberID != "ses_a" || first.Revision != 3 {
		t.Errorf("first row = %+v, want w-3-ses_a/ses_a/rev3", first)
	}
	if first.VerdictPointer != string(domain.VerdictReady) {
		t.Errorf("VerdictPointer = %q, want %q", first.VerdictPointer, domain.VerdictReady)
	}
	if first.ClaimedAt != obligationsTestTime.Add(1*time.Minute).Format(time.RFC3339Nano) {
		t.Errorf("ClaimedAt = %q, want the recorded timestamp", first.ClaimedAt)
	}
	if first.ReadAt != obligationsTestTime.Add(2*time.Minute).Format(time.RFC3339Nano) {
		t.Errorf("ReadAt = %q, want the recorded timestamp", first.ReadAt)
	}
	if len(lister.calls) != 1 || lister.calls[0] != "w" {
		t.Errorf("lister calls = %v, want [w]", lister.calls)
	}
	if len(watches.calls) != 1 || watches.calls[0] != "w" {
		t.Errorf("watch calls = %v, want [w]", watches.calls)
	}
}

func TestObligationsSubscriberFilter(t *testing.T) {
	handler, _, _ := newObligationsFixture()

	out, err := handler.List(context.Background(), ObligationsInput{WatchID: "w", SubscriberID: "ses_b"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !out.Found {
		t.Error("Found = false, want true when the filter matches")
	}
	if len(out.Obligations) != 1 {
		t.Fatalf("obligations = %d, want 1", len(out.Obligations))
	}
	if out.Obligations[0].SubscriberID != "ses_b" {
		t.Errorf("SubscriberID = %q, want ses_b", out.Obligations[0].SubscriberID)
	}
}

func TestObligationsFilterNoMatchIsEmptySuccess(t *testing.T) {
	handler, _, _ := newObligationsFixture()

	out, err := handler.List(context.Background(), ObligationsInput{WatchID: "w", SubscriberID: "ses_unknown"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if out.Found {
		t.Error("Found = true, want false when the filter matches nothing")
	}
	if len(out.Obligations) != 0 {
		t.Errorf("obligations = %d, want 0", len(out.Obligations))
	}
}

func TestObligationsMissingWatchIsEmptySuccess(t *testing.T) {
	handler, _, _ := newObligationsFixture()

	out, err := handler.List(context.Background(), ObligationsInput{WatchID: "missing"})
	if err != nil {
		t.Fatalf("List for a missing Watch: %v", err)
	}
	if out.Found {
		t.Error("Found = true, want false for an empty selection")
	}
	if len(out.Obligations) != 0 {
		t.Errorf("obligations = %d, want 0", len(out.Obligations))
	}
	if out.Revision != 0 {
		t.Errorf("Revision = %d, want 0 for a missing Watch", out.Revision)
	}
}

func TestObligationsDoesNotStampFacts(t *testing.T) {
	handler, lister, _ := newObligationsFixture()
	before := obligationsFixture()

	if _, err := handler.List(context.Background(), ObligationsInput{WatchID: "w"}); err != nil {
		t.Fatalf("first List: %v", err)
	}
	if _, err := handler.List(context.Background(), ObligationsInput{WatchID: "w"}); err != nil {
		t.Fatalf("second List: %v", err)
	}

	after := lister.byWatch["w"]
	if len(after) != len(before) {
		t.Fatalf("obligation count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		if !after[i].ClaimedAt.Equal(before[i].ClaimedAt) || !after[i].ReadAt.Equal(before[i].ReadAt) {
			t.Errorf("row %d: claimedAt/readAt changed from %v/%v to %v/%v",
				i, before[i].ClaimedAt, before[i].ReadAt, after[i].ClaimedAt, after[i].ReadAt)
		}
	}
}

func TestObligationsTruncatesAtRecordCeiling(t *testing.T) {
	many := make([]domain.Obligation, obligationsMaxRecords+5)
	for i := range many {
		many[i] = domain.Obligation{ID: "o", WatchID: "w", SubscriberID: "ses", Revision: 3}
	}
	handler := NewObligationsHandler(
		&fakeObligationWatchReader{watches: map[string]domain.Watch{"w": {ID: "w", Revision: 3}}},
		&fakeObligationLister{byWatch: map[string][]domain.Obligation{"w": many}},
	)

	out, err := handler.List(context.Background(), ObligationsInput{WatchID: "w"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out.Obligations) != obligationsMaxRecords {
		t.Errorf("obligations = %d, want %d", len(out.Obligations), obligationsMaxRecords)
	}
	if !out.Truncated {
		t.Error("Truncated = false, want true when rows exceed the ceiling")
	}
}

func TestObligationsEmptyWatchIDIsRejected(t *testing.T) {
	handler, _, _ := newObligationsFixture()

	_, err := handler.List(context.Background(), ObligationsInput{})
	assertCode(t, err, domain.CodeInvalidInput)
}

func TestObligationsNilListerIsAPersistError(t *testing.T) {
	handler := NewObligationsHandler(nil, nil)

	_, err := handler.List(context.Background(), ObligationsInput{WatchID: "w"})
	assertCode(t, err, domain.CodePersistFailed)
}

func TestObligationsWatchStorageErrorIsReturned(t *testing.T) {
	handler := NewObligationsHandler(
		&fakeObligationWatchReader{err: errors.New("db offline")},
		&fakeObligationLister{byWatch: map[string][]domain.Obligation{"w": obligationsFixture()}},
	)

	_, err := handler.List(context.Background(), ObligationsInput{WatchID: "w"})
	assertCode(t, err, domain.CodePersistFailed)
}

func TestObligationsOutputSchemaIsDerived(t *testing.T) {
	schema, err := ObligationsOutputSchema()
	if err != nil {
		t.Fatalf("ObligationsOutputSchema: %v", err)
	}
	if schema == nil {
		t.Fatal("ObligationsOutputSchema = nil, want a derived schema")
	}
}
