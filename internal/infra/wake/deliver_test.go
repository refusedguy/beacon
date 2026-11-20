package wake

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

func testRequest(id, key string, body []byte) WakeRequest {
	return WakeRequest{
		DeliveryID:     id,
		IdempotencyKey: key,
		Target:         "ses_test",
		Kind:           KindSteer,
		Body:           body,
	}
}

func TestWakeRequestFromWebhook(t *testing.T) {
	h := http.Header{}
	h.Set(HeaderWebhookID, "wh_123")
	h.Set(HeaderIdempotencyKey, "key-1")
	req, err := WakeRequestFromWebhook(h, "ses_test", KindSteer, []byte(`{"kind":"done"}`))
	if err != nil {
		t.Fatalf("WakeRequestFromWebhook: %v", err)
	}
	if req.DeliveryID != "wh_123" || req.IdempotencyKey != "key-1" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Target != "ses_test" || req.Kind != KindSteer || req.At.IsZero() {
		t.Fatalf("unexpected request fields: %+v", req)
	}
}

func TestWakeRequestFromWebhookRequiresID(t *testing.T) {
	_, err := WakeRequestFromWebhook(http.Header{}, "ses_test", KindSteer, nil)
	if !errors.Is(err, ErrDeliveryIDRequired) {
		t.Fatalf("err = %v, want ErrDeliveryIDRequired", err)
	}
}

func TestDuplicateDeliveryStartsNoSecondJob(t *testing.T) {
	var calls int32
	d := &Deliverer{
		Journal: NewDeliveryJournal(),
		Transport: func(context.Context, WakeRequest) (DeliveryStatus, error) {
			atomic.AddInt32(&calls, 1)
			return StatusStarted, nil
		},
	}
	req := testRequest("wh_1", "", []byte("body"))
	first, err := d.Deliver(context.Background(), req)
	if err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	if first.Status != StatusStarted || first.Coalesced {
		t.Fatalf("first = %+v, want started", first)
	}
	second, err := d.Deliver(context.Background(), req)
	if err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if second.Status != StatusDuplicate || !second.Coalesced {
		t.Fatalf("second = %+v, want duplicate/coalesced", second)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("transport calls = %d, want 1 (duplicate must not start a second job)", got)
	}
	if d.Journal.Len() != 1 {
		t.Fatalf("journal len = %d, want 1", d.Journal.Len())
	}
}

func TestIdempotencyKeyConflictRejected(t *testing.T) {
	var calls int32
	d := &Deliverer{
		Journal: NewDeliveryJournal(),
		Transport: func(context.Context, WakeRequest) (DeliveryStatus, error) {
			atomic.AddInt32(&calls, 1)
			return StatusDelivered, nil
		},
	}
	if _, err := d.Deliver(context.Background(), testRequest("wh_1", "key-1", []byte("body-A"))); err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	_, err := d.Deliver(context.Background(), testRequest("wh_2", "key-1", []byte("body-B")))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestIdempotencyKeySameBodyIsDuplicate(t *testing.T) {
	var calls int32
	d := &Deliverer{
		Journal: NewDeliveryJournal(),
		Transport: func(context.Context, WakeRequest) (DeliveryStatus, error) {
			atomic.AddInt32(&calls, 1)
			return StatusDelivered, nil
		},
	}
	first, _ := d.Deliver(context.Background(), testRequest("wh_1", "key-1", []byte("body")))
	second, err := d.Deliver(context.Background(), testRequest("wh_2", "key-1", []byte("body")))
	if err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if second.Status != StatusDuplicate || !second.Coalesced {
		t.Fatalf("second = %+v, want duplicate", second)
	}
	if second.DeliveryID != first.DeliveryID {
		t.Fatalf("duplicate deliveryId = %q, want %q", second.DeliveryID, first.DeliveryID)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestDeliveryIDReuseWithDifferentBodyRejected(t *testing.T) {
	d := &Deliverer{
		Journal:   NewDeliveryJournal(),
		Transport: func(context.Context, WakeRequest) (DeliveryStatus, error) { return StatusDelivered, nil },
	}
	if _, err := d.Deliver(context.Background(), testRequest("wh_1", "", []byte("A"))); err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	_, err := d.Deliver(context.Background(), testRequest("wh_1", "", []byte("B")))
	if !errors.Is(err, ErrDeliveryIDReuse) {
		t.Fatalf("err = %v, want ErrDeliveryIDReuse", err)
	}
}

func TestKeyFixedBeforeExternalCall(t *testing.T) {
	d := &Deliverer{Journal: NewDeliveryJournal()}
	d.Transport = func(_ context.Context, req WakeRequest) (DeliveryStatus, error) {
		got, ok := d.Journal.Get(req.DeliveryID)
		if !ok {
			return StatusStarted, errors.New("journal entry missing before external call")
		}
		if got.Status != StatusQueued {
			return StatusStarted, errors.New("journal status not staged before external call")
		}
		return StatusDelivered, nil
	}
	res, err := d.Deliver(context.Background(), testRequest("wh_1", "key-1", []byte("body")))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != StatusDelivered {
		t.Fatalf("status = %q, want delivered", res.Status)
	}
	after, _ := d.Journal.Get("wh_1")
	if after.Status != StatusDelivered {
		t.Fatalf("journal status after = %q, want delivered", after.Status)
	}
}

func TestConcurrentDuplicatesStartOneJob(t *testing.T) {
	var calls int32
	d := &Deliverer{
		Journal: NewDeliveryJournal(),
		Transport: func(context.Context, WakeRequest) (DeliveryStatus, error) {
			atomic.AddInt32(&calls, 1)
			return StatusStarted, nil
		},
	}
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.Deliver(context.Background(), testRequest("wh_1", "", []byte("body"))); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Deliver: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("transport calls = %d, want 1 under concurrency", got)
	}
}

func TestMissingDeliveryIDRejected(t *testing.T) {
	d := &Deliverer{
		Journal:   NewDeliveryJournal(),
		Transport: func(context.Context, WakeRequest) (DeliveryStatus, error) { return StatusStarted, nil },
	}
	_, err := d.Deliver(context.Background(), testRequest("", "", nil))
	if !errors.Is(err, ErrDeliveryIDRequired) {
		t.Fatalf("err = %v, want ErrDeliveryIDRequired", err)
	}
}
