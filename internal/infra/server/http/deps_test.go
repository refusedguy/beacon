package httproute

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

type fakeWatchReader struct {
	watches []domain.Watch
	err     error
	calls   int
	ctx     context.Context
}

func (f *fakeWatchReader) Watches(ctx context.Context) ([]domain.Watch, error) {
	f.calls++
	f.ctx = ctx
	return f.watches, f.err
}

type fakeCommandSink struct {
	result CommandResult
	err    error
	got    []Command
	ctx    context.Context
}

func (f *fakeCommandSink) Apply(ctx context.Context, cmd Command) (CommandResult, error) {
	f.got = append(f.got, cmd)
	f.ctx = ctx
	return f.result, f.err
}

var (
	_ WatchReader = (*fakeWatchReader)(nil)
	_ CommandSink = (*fakeCommandSink)(nil)
)

func TestNewDeps(t *testing.T) {
	reader := &fakeWatchReader{watches: []domain.Watch{{ID: "w1"}}}
	sink := &fakeCommandSink{result: CommandResult{ID: "c1", Status: "posted"}}

	deps := NewDeps(reader, sink)
	if deps == nil {
		t.Fatal("NewDeps returned nil")
	}
	if deps.watches != reader {
		t.Fatal("watch reader was not stored")
	}
	if deps.commands != sink {
		t.Fatal("command sink was not stored")
	}
	if err := deps.Validate(); err != nil {
		t.Fatalf("Validate with both ports: %v", err)
	}

	fromFuncs := NewDeps(
		WatchReaderFunc(func(ctx context.Context) ([]domain.Watch, error) {
			return []domain.Watch{{ID: "w2"}}, nil
		}),
		CommandSinkFunc(func(ctx context.Context, cmd Command) (CommandResult, error) {
			return CommandResult{ID: "c2"}, nil
		}),
	)
	if err := fromFuncs.Validate(); err != nil {
		t.Fatalf("Validate with function adapters: %v", err)
	}

	partial := NewDeps(reader, nil)
	err := partial.Validate()
	if !errors.Is(err, ErrMissingPort) {
		t.Fatalf("Validate with one port: got %v, want ErrMissingPort", err)
	}
	var missingErr *MissingError
	if !errors.As(err, &missingErr) {
		t.Fatalf("Validate with one port: got %T, want *MissingError", err)
	}
	if !hasPort(missingErr.Ports, "commands") || hasPort(missingErr.Ports, "watches") {
		t.Fatalf("Validate named the wrong ports: %v", missingErr.Ports)
	}
}

func TestDepsWithMockPorts(t *testing.T) {
	wantWatches := []domain.Watch{{ID: "w1"}, {ID: "w2"}}
	wantResult := CommandResult{ID: "c1", Status: "posted"}
	reader := &fakeWatchReader{watches: wantWatches}
	sink := &fakeCommandSink{result: wantResult}
	deps := NewDeps(reader, sink)
	ctx := context.Background()

	gotWatches, err := deps.ReadWatches(ctx)
	if err != nil {
		t.Fatalf("ReadWatches: %v", err)
	}
	if len(gotWatches) != 2 || gotWatches[0].ID != "w1" || gotWatches[1].ID != "w2" {
		t.Fatalf("ReadWatches returned %v", gotWatches)
	}
	if reader.calls != 1 || reader.ctx != ctx {
		t.Fatalf("reader calls=%d ctx=%v", reader.calls, reader.ctx)
	}

	cmd := Command{Kind: "wake", Target: "w1", Actor: "alice"}
	gotResult, err := deps.Apply(ctx, cmd)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if gotResult != wantResult {
		t.Fatalf("Apply returned %+v, want %+v", gotResult, wantResult)
	}
	if len(sink.got) != 1 || sink.got[0] != cmd || sink.ctx != ctx {
		t.Fatalf("sink got=%v ctx=%v", sink.got, sink.ctx)
	}

	watches, result, err := readAndApply(ctx, deps)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(watches) != 2 || result != wantResult {
		t.Fatalf("handler returned watches=%v result=%+v", watches, result)
	}
	if reader.calls != 2 || len(sink.got) != 2 || sink.got[1].Target != "w1" {
		t.Fatalf("handler did not use both ports: reader=%d sink=%v", reader.calls, sink.got)
	}
}

func TestDepsPortErrorsReachHandler(t *testing.T) {
	readErr := errors.New("store read failed")
	applyErr := errors.New("sink apply failed")
	deps := NewDeps(
		&fakeWatchReader{err: readErr},
		&fakeCommandSink{err: applyErr},
	)
	ctx := context.Background()

	if _, err := deps.ReadWatches(ctx); !errors.Is(err, readErr) {
		t.Fatalf("ReadWatches: got %v, want %v", err, readErr)
	}
	if _, err := deps.Apply(ctx, Command{Kind: "wake"}); !errors.Is(err, applyErr) {
		t.Fatalf("Apply: got %v, want %v", err, applyErr)
	}
}

func TestDepsMissingPort(t *testing.T) {
	ctx := context.Background()
	deps := NewDeps(nil, nil)

	if _, err := deps.ReadWatches(ctx); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("ReadWatches with no reader: got %v, want ErrMissingPort", err)
	}
	if _, err := deps.Apply(ctx, Command{}); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("Apply with no sink: got %v, want ErrMissingPort", err)
	}
	err := deps.Validate()
	if !errors.Is(err, ErrMissingPort) {
		t.Fatalf("Validate with no ports: got %v, want ErrMissingPort", err)
	}
	var missingErr *MissingError
	if !errors.As(err, &missingErr) {
		t.Fatalf("Validate with no ports: got %T, want *MissingError", err)
	}
	if len(missingErr.Ports) != 2 || !hasPort(missingErr.Ports, "watches") || !hasPort(missingErr.Ports, "commands") {
		t.Fatalf("Validate named the wrong ports: %v", missingErr.Ports)
	}

	var nilDeps *Deps
	if err := nilDeps.Validate(); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("nil Deps Validate: got %v, want ErrMissingPort", err)
	}
	if _, err := nilDeps.ReadWatches(ctx); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("nil Deps ReadWatches: got %v, want ErrMissingPort", err)
	}
	if _, err := nilDeps.Apply(ctx, Command{}); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("nil Deps Apply: got %v, want ErrMissingPort", err)
	}
}

// readAndApply stands in for a handler. It takes only Deps and reaches the
// services through ports, so mocks replace the services in a test.
func readAndApply(ctx context.Context, deps *Deps) ([]domain.Watch, CommandResult, error) {
	watches, err := deps.ReadWatches(ctx)
	if err != nil {
		return nil, CommandResult{}, err
	}
	if len(watches) == 0 {
		return watches, CommandResult{}, nil
	}
	result, err := deps.Apply(ctx, Command{Kind: "wake", Target: watches[0].ID, Actor: "tester"})
	return watches, result, err
}

func hasPort(ports []string, want string) bool {
	for _, port := range ports {
		if port == want {
			return true
		}
	}
	return false
}
