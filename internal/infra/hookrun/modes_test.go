package hookrun

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeProcess struct {
	awaitFn     func(context.Context) ([]byte, error)
	watchFn     func(context.Context, func(string)) ([]byte, error)
	terminateFn func() error
	forgetFn    func()
}

func (f *fakeProcess) Await(ctx context.Context) ([]byte, error) {
	if f.awaitFn != nil {
		return f.awaitFn(ctx)
	}
	return nil, nil
}

func (f *fakeProcess) Watch(ctx context.Context, onLine func(string)) ([]byte, error) {
	if f.watchFn != nil {
		return f.watchFn(ctx, onLine)
	}
	return nil, nil
}

func (f *fakeProcess) Terminate() error {
	if f.terminateFn != nil {
		return f.terminateFn()
	}
	return nil
}

func (f *fakeProcess) Forget() {
	if f.forgetFn != nil {
		f.forgetFn()
	}
}

type fakeRunner struct {
	startFn func(path, stdin string) (Process, error)
}

func (f fakeRunner) Start(path, stdin string) (Process, error) {
	if f.startFn != nil {
		return f.startFn(path, stdin)
	}
	return &fakeProcess{}, nil
}

func TestNotifyReturnsBeforeScriptFinishes(t *testing.T) {
	scriptDone := make(chan struct{})
	p := &fakeProcess{
		awaitFn: func(ctx context.Context) ([]byte, error) {
			<-scriptDone
			return nil, nil
		},
		watchFn: func(ctx context.Context, onLine func(string)) ([]byte, error) {
			<-scriptDone
			return nil, nil
		},
	}
	r := fakeRunner{startFn: func(string, string) (Process, error) { return p, nil }}

	done := make(chan Result, 1)
	go func() { done <- Execute(r, Notify, Spec{Path: "hook"}) }()
	select {
	case res := <-done:
		if !res.Detached || !res.Success || res.Mode != Notify {
			t.Fatalf("unexpected result: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("Notify waited for the script")
	}
	close(scriptDone)
}

func TestSyncTimeout(t *testing.T) {
	p := &fakeProcess{
		awaitFn: func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		terminateFn: func() error { return nil },
	}
	r := fakeRunner{startFn: func(string, string) (Process, error) { return p, nil }}

	start := time.Now()
	res := Execute(r, Sync, Spec{Path: "hook", Timeout: 200 * time.Millisecond})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Sync returned after %s, want under 1s", elapsed)
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", res.Err)
	}
	if !res.TimedOut || !res.Terminated || res.Success {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestSyncReturnsCombinedOutput(t *testing.T) {
	p := &fakeProcess{
		awaitFn: func(context.Context) ([]byte, error) { return []byte("out\nerr\n"), nil },
	}
	r := fakeRunner{startFn: func(string, string) (Process, error) { return p, nil }}

	res := Execute(r, Sync, Spec{Path: "hook", Timeout: time.Second})
	if !res.Success || string(res.Output) != "out\nerr\n" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.TimedOut || res.Terminated || res.Detached {
		t.Fatalf("Sync set timeout flags: %+v", res)
	}
}

func TestGateStreamsLinesInOrderAndTerminatesOnce(t *testing.T) {
	var seen []string
	termCalls := 0
	p := &fakeProcess{
		watchFn: func(ctx context.Context, onLine func(string)) ([]byte, error) {
			for _, line := range []string{"one", "two", "three"} {
				onLine(line)
			}
			<-ctx.Done()
			return []byte("one\ntwo\nthree\n"), ctx.Err()
		},
		terminateFn: func() error {
			termCalls++
			return nil
		},
	}
	r := fakeRunner{startFn: func(string, string) (Process, error) { return p, nil }}

	res := Execute(r, Gate, Spec{
		Path:    "hook",
		Timeout: 50 * time.Millisecond,
		OnLine:  func(line string) { seen = append(seen, line) },
	})
	want := []string{"one", "two", "three"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("lines = %v, want %v", seen, want)
	}
	if termCalls != 1 {
		t.Fatalf("Terminate calls = %d, want 1", termCalls)
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) || !res.TimedOut || !res.Terminated {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestGateCompletesWithoutTerminate(t *testing.T) {
	var seen []string
	termCalls := 0
	p := &fakeProcess{
		watchFn: func(ctx context.Context, onLine func(string)) ([]byte, error) {
			onLine("a")
			onLine("b")
			return []byte("a\nb\n"), nil
		},
		terminateFn: func() error {
			termCalls++
			return nil
		},
	}
	r := fakeRunner{startFn: func(string, string) (Process, error) { return p, nil }}

	res := Execute(r, Gate, Spec{
		Path:    "hook",
		Timeout: time.Second,
		OnLine:  func(line string) { seen = append(seen, line) },
	})
	if want := []string{"a", "b"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("lines = %v, want %v", seen, want)
	}
	if !res.Success || res.TimedOut || res.Terminated || termCalls != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestExecuteRejectsInvalidInput(t *testing.T) {
	r := fakeRunner{startFn: func(string, string) (Process, error) { return &fakeProcess{}, nil }}
	if res := Execute(r, Mode("bogus"), Spec{Path: "hook"}); res.Err == nil {
		t.Fatal("unknown mode must return an error")
	}
	if res := Execute(r, Sync, Spec{Path: "hook"}); res.Err == nil {
		t.Fatal("missing timeout must return an error")
	}
}

func TestExecuteReportsStartError(t *testing.T) {
	wantErr := errors.New("start failed")
	r := fakeRunner{startFn: func(string, string) (Process, error) { return nil, wantErr }}
	res := Execute(r, Sync, Spec{Path: "hook", Timeout: time.Second})
	if !errors.Is(res.Err, wantErr) || res.Success {
		t.Fatalf("unexpected result: %+v", res)
	}
}
