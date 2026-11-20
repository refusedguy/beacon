package store

import (
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// sampleDelivery builds a well-formed delivery for the tests below. Only the
// fields a test cares about are changed afterwards.
func sampleDelivery(id string) WebhookDelivery {
	return WebhookDelivery{
		DeliveryID: id,
		Event:      "pull_request",
		Action:     "opened",
		Repo:       "refusedguy/beacon",
		PR:         375,
		HeadSHA:    "abc123",
		WatchID:    "watch-1",
		Revision:   7,
		Status:     "done",
		ReceivedAt: 1000,
		Outcome:    "applied",
	}
}

// TestWebhookDeliveryKeyIsNamespaced pins the idempotency key from ADR 0150.
// The same raw delivery ID must not collide with a task start, a client
// request or a completion notice.
func TestWebhookDeliveryKeyIsNamespaced(t *testing.T) {
	const raw = "delivery-abc"
	key := WebhookDeliveryKey(raw)
	if want := domain.IdempotencyKey(domain.GitHubDeliveryNamespace, raw); key != want {
		t.Fatalf("WebhookDeliveryKey(%q) = %q, want %q", raw, key, want)
	}
	if key == raw {
		t.Fatal("WebhookDeliveryKey returned the raw delivery id")
	}
	if key != WebhookDeliveryKey(raw) {
		t.Fatal("WebhookDeliveryKey is not deterministic")
	}
	if key == domain.IdempotencyKey(domain.StartNamespace, raw) ||
		key == domain.IdempotencyKey(completionNotificationNamespace, raw) {
		t.Fatal("webhook delivery key collides with a start or completion key")
	}
}

// TestDeliverySeenEmptyDB proves an untouched file has no phantom row and a
// lookup does not need the journal table to exist yet.
func TestDeliverySeenEmptyDB(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	got, seen, err := d.DeliverySeen("delivery-absent")
	if err != nil {
		t.Fatalf("DeliverySeen: %v", err)
	}
	if seen {
		t.Fatalf("DeliverySeen on empty DB = true, want false")
	}
	if got != (WebhookDelivery{}) {
		t.Fatalf("DeliverySeen on empty DB returned %+v, want zero value", got)
	}
	count, err := d.WebhookDeliveryCount()
	if err != nil {
		t.Fatalf("WebhookDeliveryCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("empty DB count = %d, want 0", count)
	}
}

// TestRecordDeliveryRepeat proves a redelivery of the same delivery_id writes
// no second row and does not roll a finished status back.
func TestRecordDeliveryRepeat(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	first, err := d.RecordDelivery(sampleDelivery("delivery-1"))
	if err != nil {
		t.Fatalf("first RecordDelivery: %v", err)
	}
	if !first.Created || first.Duplicate || first.Conflict {
		t.Fatalf("first RecordDelivery = %+v, want Created only", first)
	}

	// Same delivery ID and body, only a later arrival: a pure duplicate.
	repeat := sampleDelivery("delivery-1")
	repeat.ReceivedAt = 2000
	second, err := d.RecordDelivery(repeat)
	if err != nil {
		t.Fatalf("repeat RecordDelivery: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("repeat RecordDelivery = %+v, want Duplicate", second)
	}
	if second.Created || second.Conflict {
		t.Fatalf("repeat RecordDelivery = %+v, want Duplicate only", second)
	}

	// A redelivery carrying a stale status must not roll the first row back.
	stale := sampleDelivery("delivery-1")
	stale.Status = "pending"
	stale.Outcome = "ignored"
	third, err := d.RecordDelivery(stale)
	if err != nil {
		t.Fatalf("stale RecordDelivery: %v", err)
	}
	if !third.Duplicate || !third.Conflict {
		t.Fatalf("stale RecordDelivery = %+v, want Duplicate and Conflict", third)
	}

	// The first row wins: status and outcome are unchanged.
	if got := third.Delivery.Status; got != "done" {
		t.Errorf("stored status after repeat = %q, want done", got)
	}
	if got := third.Delivery.Outcome; got != "applied" {
		t.Errorf("stored outcome after repeat = %q, want applied", got)
	}
	if got := third.Delivery.ReceivedAt; got != 1000 {
		t.Errorf("stored received_at after repeat = %d, want 1000", got)
	}

	count, err := d.WebhookDeliveryCount()
	if err != nil {
		t.Fatalf("WebhookDeliveryCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count after repeat = %d, want 1", count)
	}
}

// TestRecordDeliveryConflictingBody proves a conflicting redelivery returns
// the stored outcome instead of overwriting the journal row.
func TestRecordDeliveryConflictingBody(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	firstIn := sampleDelivery("delivery-2")
	if _, err := d.RecordDelivery(firstIn); err != nil {
		t.Fatalf("first RecordDelivery: %v", err)
	}

	conflict := sampleDelivery("delivery-2")
	conflict.Event = "check_run"
	conflict.Action = "completed"
	conflict.Repo = "someone/else"
	conflict.PR = 999
	conflict.HeadSHA = "deadbeef"
	conflict.Status = "failed"
	conflict.Outcome = "rejected"
	got, err := d.RecordDelivery(conflict)
	if err != nil {
		t.Fatalf("conflicting RecordDelivery: %v", err)
	}
	if !got.Duplicate || !got.Conflict {
		t.Fatalf("conflicting RecordDelivery = %+v, want Duplicate and Conflict", got)
	}
	if got.Created {
		t.Fatal("conflicting RecordDelivery reported Created")
	}
	if outcome := got.Delivery.Outcome; outcome != firstIn.Outcome {
		t.Fatalf("stored outcome = %q, want first outcome %q", outcome, firstIn.Outcome)
	}
	if event := got.Delivery.Event; event != firstIn.Event {
		t.Fatalf("stored event = %q, want first event %q", event, firstIn.Event)
	}

	// DeliverySeen agrees with what RecordDelivery returned: nothing changed.
	seen, ok, err := d.DeliverySeen("delivery-2")
	if err != nil {
		t.Fatalf("DeliverySeen: %v", err)
	}
	if !ok {
		t.Fatal("DeliverySeen = false after a recorded delivery")
	}
	if seen != got.Delivery {
		t.Fatalf("DeliverySeen = %+v, want %+v", seen, got.Delivery)
	}
	count, err := d.WebhookDeliveryCount()
	if err != nil {
		t.Fatalf("WebhookDeliveryCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count after conflict = %d, want 1", count)
	}
}

// TestDeliverySeenAfterRestart proves a second store handle opened over the
// same file sees the committed delivery and another redelivery stays a no-op.
func TestDeliverySeenAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	in := sampleDelivery("delivery-3")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := first.RecordDelivery(in); err != nil {
		first.Close()
		t.Fatalf("RecordDelivery before restart: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// A fresh handle over the same file, as a restarted process would hold.
	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("restarted Open: %v", err)
	}
	defer restarted.Close()

	seen, ok, err := restarted.DeliverySeen("delivery-3")
	if err != nil {
		t.Fatalf("DeliverySeen after restart: %v", err)
	}
	if !ok {
		t.Fatal("DeliverySeen = false after restart")
	}
	if seen.Outcome != in.Outcome || seen.Status != in.Status {
		t.Fatalf("after restart got status=%q outcome=%q, want %q/%q",
			seen.Status, seen.Outcome, in.Status, in.Outcome)
	}

	// The restarted handle must not write a second row on redelivery.
	again, err := restarted.RecordDelivery(sampleDelivery("delivery-3"))
	if err != nil {
		t.Fatalf("RecordDelivery after restart: %v", err)
	}
	if !again.Duplicate || again.Created {
		t.Fatalf("RecordDelivery after restart = %+v, want Duplicate only", again)
	}
	count, err := restarted.WebhookDeliveryCount()
	if err != nil {
		t.Fatalf("WebhookDeliveryCount after restart: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count after restart = %d, want 1", count)
	}
}

// TestRecordDeliveryRejectsEmptyID pins the boundary check: an empty delivery
// header cannot produce a journal row.
func TestRecordDeliveryRejectsEmptyID(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for _, id := range []string{"", "   "} {
		in := sampleDelivery(id)
		if _, err := d.RecordDelivery(in); err == nil {
			t.Errorf("RecordDelivery with delivery id %q succeeded, want invalid input", id)
		}
		if _, _, err := d.DeliverySeen(id); err == nil {
			t.Errorf("DeliverySeen with delivery id %q succeeded, want invalid input", id)
		}
	}
	count, err := d.WebhookDeliveryCount()
	if err != nil {
		t.Fatalf("WebhookDeliveryCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("row count after invalid input = %d, want 0", count)
	}
}
