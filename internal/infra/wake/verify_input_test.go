package wake

import (
	"context"
	"errors"
	"testing"
)

// fakeInputPaneCapture returns scripted pane snapshots. It never starts tmux.
type fakeInputPaneCapture struct {
	frames []string
	err    error
	calls  int
	target string
}

func (f *fakeInputPaneCapture) Capture(ctx context.Context, target string) (string, error) {
	f.calls++
	f.target = target
	if f.err != nil {
		return "", f.err
	}
	i := f.calls - 1
	if i >= len(f.frames) {
		i = len(f.frames) - 1
	}
	if i < 0 {
		return "", nil
	}
	return f.frames[i], nil
}

func TestVerifyInputBoxStates(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		want  string
		state InputBoxState
	}{
		{
			name:  "ready with empty box and block cursor",
			frame: "Claude Code\n> █\n",
			state: InputReady,
		},
		{
			name:  "ready with codex prompt glyph",
			frame: "Codex\n❯ █\n",
			state: InputReady,
		},
		{
			name:  "ready with ansi styling stripped",
			frame: "x\n\x1b[36m❯\x1b[0m \x1b[7m█\x1b[0m\n",
			state: InputReady,
		},
		{
			name:  "ready with an empty box after submit",
			frame: "> ship\n> █\n",
			state: InputReady,
		},
		{
			name:  "not ready when box still holds a draft",
			frame: "> draft█\n",
			state: InputNotReady,
		},
		{
			name:  "not ready without a visible cursor",
			frame: "> draft\n",
			state: InputNotReady,
		},
		{
			name:  "not ready with no prompt line",
			frame: "compiling ...\n",
			state: InputNotReady,
		},
		{
			name:  "ready when box holds the exact word",
			frame: "> ship█\n",
			want:  "ship",
			state: InputReady,
		},
		{
			name:  "ready when word is bounded by punctuation",
			frame: "> (ship)█\n",
			want:  "ship",
			state: InputReady,
		},
		{
			name:  "not ready when word is only a word substring",
			frame: "> relationship█\n",
			want:  "ship",
			state: InputNotReady,
		},
		{
			name:  "not ready when expected text is absent",
			frame: "> draft█\n",
			want:  "ship",
			state: InputNotReady,
		},
		{
			name:  "not ready when text matches but cursor is gone",
			frame: "> ship\n",
			want:  "ship",
			state: InputNotReady,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := &fakeInputPaneCapture{frames: []string{tc.frame}}
			got, err := VerifyInputBox(context.Background(), capture, "%42", tc.want)
			if err != nil {
				t.Fatalf("VerifyInputBox: %v", err)
			}
			if got != tc.state {
				t.Fatalf("state = %q, want %q (frame %q)", got, tc.state, tc.frame)
			}
			if capture.calls != 1 || capture.target != "%42" {
				t.Fatalf("capture calls = %d, target = %q; want 1 call on %q", capture.calls, capture.target, "%42")
			}
		})
	}
}

func TestVerifyInputBoxCaptureFailure(t *testing.T) {
	boom := errors.New("pane gone")
	capture := &fakeInputPaneCapture{err: boom}

	got, err := VerifyInputBox(context.Background(), capture, "%42", "")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if got != InputNotReady {
		t.Fatalf("state = %q, want %q", got, InputNotReady)
	}
}

func TestVerifyInputBoxNilCapturer(t *testing.T) {
	got, err := VerifyInputBox(context.Background(), nil, "%42", "")
	if !errors.Is(err, ErrNoPaneCapturer) {
		t.Fatalf("error = %v, want ErrNoPaneCapturer", err)
	}
	if got != InputNotReady {
		t.Fatalf("state = %q, want %q", got, InputNotReady)
	}
}

func TestPaneCaptureFuncAdapter(t *testing.T) {
	called := false
	capture := PaneCaptureFunc(func(ctx context.Context, target string) (string, error) {
		called = true
		if target != "%7" {
			t.Fatalf("target = %q, want %%7", target)
		}
		return "> █\n", nil
	})

	got, err := VerifyInputBox(context.Background(), capture, "%7", "")
	if err != nil {
		t.Fatalf("VerifyInputBox: %v", err)
	}
	if !called {
		t.Fatal("adapted capture function was not called")
	}
	if got != InputReady {
		t.Fatalf("state = %q, want %q", got, InputReady)
	}
}
