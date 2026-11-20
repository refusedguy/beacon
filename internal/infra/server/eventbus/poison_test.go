package eventbus

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
)

func TestProcessValidEvent(t *testing.T) {
	events := []RawEvent{{
		Seq:     4,
		Type:    "watch.ready",
		Payload: []byte(`{"ok":true}`),
	}}

	got := Process(Checkpoint{Watermark: 3}, events, nil)

	if len(got.Delivered) != 1 {
		t.Fatalf("delivered = %d, want 1", len(got.Delivered))
	}
	if got.Delivered[0].Seq != 4 || got.Delivered[0].Type != "watch.ready" {
		t.Fatalf("delivered[0] = %+v, want seq 4 type watch.ready", got.Delivered[0])
	}
	if got.Checkpoint.Watermark != 4 {
		t.Fatalf("watermark = %d, want 4", got.Checkpoint.Watermark)
	}
	if got.Checkpoint.LastPoison != nil {
		t.Fatalf("lastPoison = %+v, want nil", got.Checkpoint.LastPoison)
	}
	if got.Checkpoint.NextSeq() != 5 {
		t.Fatalf("NextSeq = %d, want 5", got.Checkpoint.NextSeq())
	}
}

func TestProcessUndecodableEvent(t *testing.T) {
	bad := []byte("undecodable")
	events := []RawEvent{{Seq: 7, Type: "bad", Payload: bad}}

	got := Process(Checkpoint{}, events, nil)

	if len(got.Delivered) != 0 {
		t.Fatalf("delivered = %d, want 0 for a poisoned event", len(got.Delivered))
	}
	if got.Checkpoint.Watermark != 7 {
		t.Fatalf("watermark = %d, want 7", got.Checkpoint.Watermark)
	}
	if got.Checkpoint.LastPoison == nil {
		t.Fatal("lastPoison is nil")
	}
	if got.Checkpoint.LastPoison.Seq != 7 {
		t.Fatalf("poison seq = %d, want 7", got.Checkpoint.LastPoison.Seq)
	}
	wantSHA := fmt.Sprintf("%x", sha256.Sum256(bad))
	if got.Checkpoint.LastPoison.PayloadSHA != wantSHA {
		t.Fatalf("payload_sha = %q, want %q", got.Checkpoint.LastPoison.PayloadSHA, wantSHA)
	}
	if got.Checkpoint.NextSeq() != 8 {
		t.Fatalf("NextSeq = %d, want 8", got.Checkpoint.NextSeq())
	}
}

func TestProcessContinuesAfterPoison(t *testing.T) {
	events := []RawEvent{
		{Seq: 1, Type: "tick", Payload: []byte(`{"n":1}`)},
		{Seq: 2, Type: "bad", Payload: []byte("not json")},
		{Seq: 3, Type: "tick", Payload: []byte(`{"n":3}`)},
	}

	got := Process(Checkpoint{}, events, nil)

	if len(got.Delivered) != 2 {
		t.Fatalf("delivered = %d, want 2 (seq 1 and 3)", len(got.Delivered))
	}
	wantSeq := []uint64{1, 3}
	for i, d := range got.Delivered {
		if d.Seq != wantSeq[i] {
			t.Fatalf("delivered[%d].Seq = %d, want %d", i, d.Seq, wantSeq[i])
		}
	}
	if got.Checkpoint.Watermark != 3 {
		t.Fatalf("watermark = %d, want 3", got.Checkpoint.Watermark)
	}
	if got.Checkpoint.LastPoison == nil || got.Checkpoint.LastPoison.Seq != 2 {
		t.Fatalf("lastPoison = %+v, want seq 2", got.Checkpoint.LastPoison)
	}
	if got.Checkpoint.NextSeq() != 4 {
		t.Fatalf("NextSeq = %d, want 4", got.Checkpoint.NextSeq())
	}
}

func TestProcessLastPoisonKeepsLatest(t *testing.T) {
	firstBad := []byte("first bad")
	lastBad := []byte("last bad")
	events := []RawEvent{
		{Seq: 2, Type: "bad", Payload: firstBad},
		{Seq: 5, Type: "bad", Payload: lastBad},
	}

	got := Process(Checkpoint{}, events, nil)

	if got.Checkpoint.LastPoison == nil {
		t.Fatal("lastPoison is nil")
	}
	if got.Checkpoint.LastPoison.Seq != 5 {
		t.Fatalf("poison seq = %d, want 5 (latest)", got.Checkpoint.LastPoison.Seq)
	}
	wantSHA := fmt.Sprintf("%x", sha256.Sum256(lastBad))
	if got.Checkpoint.LastPoison.PayloadSHA != wantSHA {
		t.Fatalf("payload_sha = %q, want %q for the last bad payload", got.Checkpoint.LastPoison.PayloadSHA, wantSHA)
	}
}

func TestProcessEmptyStream(t *testing.T) {
	start := Checkpoint{Watermark: 9}

	got := Process(start, nil, nil)

	if len(got.Delivered) != 0 {
		t.Fatalf("delivered = %d, want 0", len(got.Delivered))
	}
	if got.Checkpoint.Watermark != 9 {
		t.Fatalf("watermark = %d, want 9", got.Checkpoint.Watermark)
	}
	if got.Checkpoint.LastPoison != nil {
		t.Fatalf("lastPoison = %+v, want nil", got.Checkpoint.LastPoison)
	}
}

func TestProcessCustomDecoderError(t *testing.T) {
	sentinel := errors.New("cannot decode")
	decode := func(payload []byte) (any, error) {
		if string(payload) == "reject" {
			return nil, sentinel
		}
		return string(payload), nil
	}
	events := []RawEvent{
		{Seq: 1, Type: "bad", Payload: []byte("reject")},
		{Seq: 2, Type: "ok", Payload: []byte("accept")},
	}

	got := Process(Checkpoint{}, events, decode)

	if len(got.Delivered) != 1 || got.Delivered[0].Value != "accept" {
		t.Fatalf("delivered = %+v, want only the accepted value", got.Delivered)
	}
	if got.Checkpoint.LastPoison == nil || got.Checkpoint.LastPoison.Seq != 1 {
		t.Fatalf("lastPoison = %+v, want seq 1", got.Checkpoint.LastPoison)
	}
}

func TestSeededBadPayloadRegression(t *testing.T) {
	bad := []byte(`{"seq": 12, "payload": `)
	events := []RawEvent{
		{Seq: 11, Type: "tick", Payload: []byte(`{"ok":true}`)},
		{Seq: 12, Type: "broken", Payload: bad},
		{Seq: 13, Type: "tick", Payload: []byte(`{"ok":true}`)},
	}

	got := Process(Checkpoint{Watermark: 10}, events, nil)

	if len(got.Delivered) != 2 {
		t.Fatalf("delivered = %d, want 2, the bad payload stalled the stream", len(got.Delivered))
	}
	if got.Checkpoint.LastPoison == nil || got.Checkpoint.LastPoison.Seq != 12 {
		t.Fatalf("lastPoison = %+v, want seq 12", got.Checkpoint.LastPoison)
	}
	wantSHA := PayloadSHA(bad)
	if got.Checkpoint.LastPoison.PayloadSHA != wantSHA {
		t.Fatalf("payload_sha = %q, want %q", got.Checkpoint.LastPoison.PayloadSHA, wantSHA)
	}
}
