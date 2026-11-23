package wake

import (
	"context"
	"errors"
	"testing"
)

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
			calls, target := 0, ""
			capture := func(_ context.Context, pane string) (string, error) {
				calls++
				target = pane
				return tc.frame, nil
			}
			got, err := VerifyInputBox(context.Background(), capture, "%42", tc.want)
			if err != nil {
				t.Fatalf("VerifyInputBox: %v", err)
			}
			if got != tc.state {
				t.Fatalf("state = %q, want %q (frame %q)", got, tc.state, tc.frame)
			}
			if calls != 1 || target != "%42" {
				t.Fatalf("capture calls = %d, target = %q; want 1 call on %q", calls, target, "%42")
			}
		})
	}
}

func TestVerifyInputBoxCaptureFailure(t *testing.T) {
	boom := errors.New("pane gone")
	capture := func(context.Context, string) (string, error) { return "", boom }

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
