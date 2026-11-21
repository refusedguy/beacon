package command

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// channelTestOutbox is the mock persistence port. It records the committed
// effect and flips committed only after a successful write, so the test can
// observe that the sink runs after the commit.
type channelTestOutbox struct {
	key       string
	kind      string
	payload   []byte
	created   bool
	err       error
	committed bool
	calls     int
}

func (o *channelTestOutbox) Commit(_ context.Context, key, kind string, payload []byte) (bool, error) {
	o.calls++
	o.key, o.kind, o.payload = key, kind, payload
	if o.err != nil {
		return false, o.err
	}
	o.committed = true
	return o.created, nil
}

func TestChannelSinkCommitsEffectBeforeApplying(t *testing.T) {
	outbox := &channelTestOutbox{created: true}
	var effects []Effect
	proc := New(outbox, func(_ context.Context, effect Effect) error {
		if !outbox.committed {
			t.Error("effect ran before the outbox committed")
		}
		effects = append(effects, effect)
		return nil
	})
	sink := NewChannelSink(proc)
	in := ChannelCommand{
		Actor: "ses_ch_1", Verb: "approve", Target: "w-7", Note: "looks good", RequestID: "req-9",
	}

	if err := sink.Accept(context.Background(), in); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if outbox.calls != 1 {
		t.Fatalf("outbox calls = %d, want 1", outbox.calls)
	}
	wantKey := ChannelCommandKind + ":req-9"
	if outbox.key != wantKey || outbox.kind != ChannelCommandKind {
		t.Fatalf("effect = (%q, %q), want (%q, %q)", outbox.key, outbox.kind, wantKey, ChannelCommandKind)
	}
	var stored ChannelCommand
	if err := json.Unmarshal(outbox.payload, &stored); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if stored != in {
		t.Fatalf("stored = %+v, want %+v", stored, in)
	}
	if len(effects) != 1 || effects[0].Key != wantKey || effects[0].Kind != ChannelCommandKind {
		t.Fatalf("applied effects = %+v, want one %s effect", effects, ChannelCommandKind)
	}
}

func TestChannelSinkReplayRunsNoEffect(t *testing.T) {
	outbox := &channelTestOutbox{created: false}
	applied := 0
	sink := NewChannelSink(New(outbox, func(context.Context, Effect) error {
		applied++
		return nil
	}))

	if err := sink.Accept(context.Background(), ChannelCommand{
		Actor: "ses_ch_2", Verb: "reject", Target: "w-8",
	}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if applied != 0 {
		t.Fatalf("effect ran %d times on a replay, want 0", applied)
	}
}

func TestChannelSinkKeyDoesNotDependOnNote(t *testing.T) {
	first := &channelTestOutbox{created: true}
	second := &channelTestOutbox{created: true}
	in := ChannelCommand{Actor: "ses_ch_3", Verb: "pause", Target: "w-9", Note: "first"}
	if err := NewChannelSink(New(first, nil)).Accept(context.Background(), in); err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	in.Note = "reworded"
	if err := NewChannelSink(New(second, nil)).Accept(context.Background(), in); err != nil {
		t.Fatalf("second Accept: %v", err)
	}
	if first.key != second.key {
		t.Fatalf("keys = %q and %q, want the same key for the same decision", first.key, second.key)
	}
	want := ChannelCommandKind + ":ses_ch_3:pause:w-9"
	if first.key != want {
		t.Fatalf("key = %q, want %q", first.key, want)
	}
}

func TestChannelSinkRejectsNilProcessor(t *testing.T) {
	err := NewChannelSink(nil).Accept(context.Background(), ChannelCommand{})
	if !errors.Is(err, ErrNilProcessor) {
		t.Fatalf("error = %v, want ErrNilProcessor", err)
	}
}
