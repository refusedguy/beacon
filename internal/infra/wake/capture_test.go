package wake

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeRunner records the argument vector of every call and returns a scripted
// output or error for that call index.
type fakeRunner struct {
	calls [][]string
	outs  [][]byte
	errs  []error
}

func (f *fakeRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	i := len(f.calls) - 1
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	if i < len(f.outs) {
		return f.outs[i], nil
	}
	return nil, nil
}

func TestCapturePaneReturnsOutput(t *testing.T) {
	want := "first line\nsecond line\n"
	r := &fakeRunner{outs: [][]byte{[]byte(want)}}

	got, err := CapturePane(context.Background(), r, "%42")
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if got != want {
		t.Fatalf("CapturePane output = %q, want %q", got, want)
	}

	wantCalls := [][]string{{"capture-pane", "-p", "-t", "%42"}}
	if !reflect.DeepEqual(r.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", r.calls, wantCalls)
	}
}

func TestPasteTwoStepIsTwoCalls(t *testing.T) {
	r := &fakeRunner{}

	if err := PasteTwoStep(context.Background(), r, "%42", "hello\nworld"); err != nil {
		t.Fatalf("PasteTwoStep: %v", err)
	}

	wantCalls := [][]string{
		{"send-keys", "-t", "%42", "-l", "--", "hello\nworld"},
		{"send-keys", "-t", "%42", "Enter"},
	}
	if !reflect.DeepEqual(r.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", r.calls, wantCalls)
	}
}

func TestPasteTwoStepStopsAfterTextFailure(t *testing.T) {
	boom := errors.New("send failed")
	r := &fakeRunner{errs: []error{boom}}

	err := PasteTwoStep(context.Background(), r, "%42", "text")
	if !errors.Is(err, boom) {
		t.Fatalf("PasteTwoStep error = %v, want %v", err, boom)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls = %v, want only the text step", r.calls)
	}
}

func TestSubmitOnlySendsEnter(t *testing.T) {
	r := &fakeRunner{}

	if err := SubmitOnly(context.Background(), r, "%42"); err != nil {
		t.Fatalf("SubmitOnly: %v", err)
	}

	wantCalls := [][]string{{"send-keys", "-t", "%42", "Enter"}}
	if !reflect.DeepEqual(r.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", r.calls, wantCalls)
	}
}

func TestCapturePaneRejectsMissingRunnerAndTarget(t *testing.T) {
	if _, err := CapturePane(context.Background(), nil, "%42"); !errors.Is(err, ErrNoPaneRunner) {
		t.Fatalf("nil runner error = %v, want ErrNoPaneRunner", err)
	}

	r := &fakeRunner{}
	if _, err := CapturePane(context.Background(), r, ""); !errors.Is(err, ErrNoPaneTarget) {
		t.Fatalf("empty target error = %v, want ErrNoPaneTarget", err)
	}
}
