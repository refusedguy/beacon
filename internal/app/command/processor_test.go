package command

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/store"
)

var _ Outbox = (*store.OutboxStore)(nil)

// commitCall is one observed call into the fake outbox.
type commitCall struct {
	key     string
	kind    string
	payload string
}

type fakeOutbox struct {
	created []bool
	err     []error
	calls   []commitCall
	log     *[]string
}

func (f *fakeOutbox) Commit(_ context.Context, key, kind string, payload []byte) (bool, error) {
	f.calls = append(f.calls, commitCall{key: key, kind: kind, payload: string(payload)})
	i := len(f.calls) - 1
	created := false
	if i < len(f.created) {
		created = f.created[i]
	}
	var err error
	if i < len(f.err) {
		err = f.err[i]
	}
	if f.log != nil {
		*f.log = append(*f.log, "commit")
	}
	return created, err
}

func TestProcessCommitsOutboxAndRunsEffect(t *testing.T) {
	var log []string
	ob := &fakeOutbox{created: []bool{true}, log: &log}
	sink := func(_ context.Context, in Effect) error {
		log = append(log, "effect:"+in.Key)
		return nil
	}
	p := New(ob, sink)

	res, err := p.Process(context.Background(), Effect{
		Key: "cmd-1", Kind: "notify", Payload: []byte("hello"),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !res.Created || res.Replayed {
		t.Fatalf("result = %+v, want Created", res)
	}
	want := []commitCall{{key: "cmd-1", kind: "notify", payload: "hello"}}
	if !reflect.DeepEqual(ob.calls, want) {
		t.Fatalf("commits = %+v, want %+v", ob.calls, want)
	}
	wantLog := []string{"commit", "effect:cmd-1"}
	if !reflect.DeepEqual(log, wantLog) {
		t.Fatalf("order = %v, want %v", log, wantLog)
	}
}

func TestProcessRollbackRunsNoEffect(t *testing.T) {
	boom := errors.New("tx rolled back")
	ob := &fakeOutbox{created: []bool{false}, err: []error{boom}}
	called := false
	p := New(ob, func(context.Context, Effect) error {
		called = true
		return nil
	})

	res, err := p.Process(context.Background(), Effect{Key: "cmd-1", Kind: "notify"})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if res != (Result{}) {
		t.Fatalf("result = %+v, want zero", res)
	}
	if called {
		t.Fatal("sink ran after a rolled-back Commit")
	}
}

func TestProcessRepeatIsIdempotent(t *testing.T) {
	ob := &fakeOutbox{created: []bool{true, false}}
	effects := 0
	p := New(ob, func(context.Context, Effect) error {
		effects++
		return nil
	})
	in := Effect{Key: "cmd-1", Kind: "notify", Payload: []byte("hello")}

	first, err := p.Process(context.Background(), in)
	if err != nil || !first.Created {
		t.Fatalf("first = %+v, %v; want Created", first, err)
	}
	second, err := p.Process(context.Background(), in)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if !second.Replayed || second.Created {
		t.Fatalf("repeat result = %+v, want Replayed", second)
	}
	if effects != 1 {
		t.Fatalf("effects = %d, want 1", effects)
	}
	if len(ob.calls) != 2 {
		t.Fatalf("commits = %d, want 2 (repeat still consults the ledger)", len(ob.calls))
	}
}

func TestProcessEffectNeverPrecedesCommit(t *testing.T) {
	var log []string
	ob := &fakeOutbox{created: []bool{true}, log: &log}
	p := New(ob, func(context.Context, Effect) error {
		log = append(log, "effect")
		return nil
	})
	if _, err := p.Process(context.Background(), Effect{Key: "cmd-1", Kind: "notify"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(log) != 2 || log[0] != "commit" || log[1] != "effect" {
		t.Fatalf("order = %v, want [commit effect]", log)
	}
}

func TestProcessRejectsEmptyIdentity(t *testing.T) {
	ob := &fakeOutbox{created: []bool{true}}
	p := New(ob, nil)
	for _, in := range []Effect{
		{Key: "  ", Kind: "notify"},
		{Key: "cmd-1", Kind: " "},
	} {
		if _, err := p.Process(context.Background(), in); err == nil {
			t.Fatalf("Process(%+v) accepted empty identity", in)
		}
	}
	if len(ob.calls) != 0 {
		t.Fatalf("commits = %d, want 0 after validation failures", len(ob.calls))
	}
}
