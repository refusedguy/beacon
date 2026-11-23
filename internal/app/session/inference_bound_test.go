package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBoundInferenceReturnsModelText(t *testing.T) {
	provider := InferenceFunc(func(_ context.Context, prompt string) (string, error) {
		if prompt != "write a message" {
			t.Errorf("provider prompt = %q, want the request prompt", prompt)
		}
		return "  a commit message\n", nil
	})
	answer := BoundInference(context.Background(), provider, time.Now, nil, InferenceRequest{
		Prompt: "write a message",
		Limit:  2 * time.Second,
	})
	if answer.Outcome != OutcomeResult {
		t.Fatalf("outcome = %q, want result", answer.Outcome)
	}
	if answer.Text != "a commit message" {
		t.Fatalf("text = %q, want trimmed model text", answer.Text)
	}
	if answer.ModelCalls != 1 {
		t.Fatalf("model calls = %d, want 1", answer.ModelCalls)
	}
	if answer.PromptTruncated {
		t.Fatal("short prompt was marked truncated")
	}
}

func TestBoundInferenceStopsHangingProvider(t *testing.T) {
	const limit = 100 * time.Millisecond
	provider := InferenceFunc(func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	base := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	var ticks int
	clock := func() time.Time {
		t := base.Add(time.Duration(ticks) * time.Second)
		ticks++
		return t
	}

	start := time.Now()
	answer := BoundInference(context.Background(), provider, clock, nil, InferenceRequest{
		Prompt: "write a message",
		Files:  []string{"a.go"},
		Limit:  limit,
	})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("hanging provider blocked for %s, limit was %s", elapsed, limit)
	}
	if answer.Outcome != OutcomeRefusal {
		t.Fatalf("outcome = %q, want refusal", answer.Outcome)
	}
	if !strings.Contains(answer.Reason, "timed out after 1s") {
		t.Fatalf("reason = %q, want the injected clock to report 1s", answer.Reason)
	}
	if answer.ModelCalls != 1 {
		t.Fatalf("model calls = %d, want 1", answer.ModelCalls)
	}
	if len(answer.Files) != 1 || answer.Files[0] != "a.go" {
		t.Fatalf("files = %v, want [a.go]", answer.Files)
	}
}

func TestBoundInferenceTruncatesLongPrompt(t *testing.T) {
	var seen string
	provider := InferenceFunc(func(_ context.Context, prompt string) (string, error) {
		seen = prompt
		return "done", nil
	})
	answer := BoundInference(context.Background(), provider, time.Now, nil, InferenceRequest{
		Prompt:     strings.Repeat("x", 6000),
		InputLimit: DefaultInferenceInputLimit,
		Limit:      2 * time.Second,
	})
	if !answer.PromptTruncated {
		t.Fatal("long prompt was not marked truncated")
	}
	if len(seen) != DefaultInferenceInputLimit {
		t.Fatalf("provider prompt length = %d, want %d", len(seen), DefaultInferenceInputLimit)
	}
	if !strings.HasSuffix(seen, inferenceTruncationMarker) {
		t.Fatal("truncated prompt has no marker")
	}
	if answer.Outcome != OutcomeResult {
		t.Fatalf("outcome = %q, want result", answer.Outcome)
	}
}

func TestBoundInferenceEmptyResponseShowsFiles(t *testing.T) {
	provider := InferenceFunc(func(_ context.Context, _ string) (string, error) {
		return "  \n", nil
	})
	var forms []InferenceForm
	manual := func(_ context.Context, form InferenceForm) (string, error) {
		forms = append(forms, form)
		return "typed by human", nil
	}
	answer := BoundInference(context.Background(), provider, time.Now, manual, InferenceRequest{
		Prompt: "write a message",
		Files:  []string{"b.go", "a.go", "b.go", ""},
		Limit:  2 * time.Second,
	})
	if answer.Outcome != OutcomeManual {
		t.Fatalf("outcome = %q, want manual", answer.Outcome)
	}
	if answer.Text != "typed by human" {
		t.Fatalf("text = %q, want the manual value", answer.Text)
	}
	if answer.ModelCalls != 1 {
		t.Fatalf("model calls = %d, want 1", answer.ModelCalls)
	}
	if len(forms) != 1 {
		t.Fatalf("manual calls = %d, want 1", len(forms))
	}
	if forms[0].Reason != "empty model response" {
		t.Fatalf("form reason = %q, want empty model response", forms[0].Reason)
	}
	if got, want := forms[0].Summary(), "empty model response\nchanged files: a.go, b.go"; got != want {
		t.Fatalf("form summary = %q, want %q", got, want)
	}
}

func TestBoundInferenceRefusalUsesManualOnce(t *testing.T) {
	boom := fmt.Errorf("provider declined: %w", ErrInferenceRefused)
	provider := InferenceFunc(func(_ context.Context, _ string) (string, error) {
		return "", boom
	})
	var calls int
	manual := func(_ context.Context, form InferenceForm) (string, error) {
		calls++
		if form.Reason != boom.Error() {
			t.Errorf("form reason = %q, want %q", form.Reason, boom.Error())
		}
		return "edited by human", nil
	}
	answer := BoundInference(context.Background(), provider, time.Now, manual, InferenceRequest{
		Prompt: "write a message",
		Files:  []string{"a.go"},
		Limit:  2 * time.Second,
	})
	if answer.Outcome != OutcomeManual {
		t.Fatalf("outcome = %q, want manual", answer.Outcome)
	}
	if answer.Text != "edited by human" {
		t.Fatalf("text = %q, want the entered value", answer.Text)
	}
	if calls != 1 || answer.ModelCalls != 1 {
		t.Fatalf("manual calls = %d, model calls = %d, want 1 and 1", calls, answer.ModelCalls)
	}
}

func TestBoundInferenceRefusalWithoutManualKeepsFiles(t *testing.T) {
	boom := errors.New("model unavailable")
	provider := InferenceFunc(func(_ context.Context, _ string) (string, error) {
		return "", boom
	})
	answer := BoundInference(context.Background(), provider, time.Now, nil, InferenceRequest{
		Prompt: "write a message",
		Files:  []string{"a.go", "b.go"},
		Limit:  2 * time.Second,
	})
	if answer.Outcome != OutcomeRefusal {
		t.Fatalf("outcome = %q, want refusal", answer.Outcome)
	}
	if answer.Reason != boom.Error() {
		t.Fatalf("reason = %q, want %q", answer.Reason, boom.Error())
	}
	if answer.Text != "" {
		t.Fatalf("text = %q, want empty", answer.Text)
	}
	if len(answer.Files) != 2 {
		t.Fatalf("files = %v, want two entries", answer.Files)
	}
}

func TestBoundInferenceManualFailureStaysRefusal(t *testing.T) {
	provider := InferenceFunc(func(_ context.Context, _ string) (string, error) {
		return "", ErrInferenceRefused
	})
	manual := func(_ context.Context, _ InferenceForm) (string, error) {
		return "", errors.New("form closed")
	}
	answer := BoundInference(context.Background(), provider, time.Now, manual, InferenceRequest{
		Prompt: "write a message",
		Limit:  2 * time.Second,
	})
	if answer.Outcome != OutcomeRefusal {
		t.Fatalf("outcome = %q, want refusal", answer.Outcome)
	}
	if !strings.Contains(answer.Reason, "manual input failed: form closed") {
		t.Fatalf("reason = %q, want the manual error", answer.Reason)
	}
}

func TestBoundInferenceNilProvider(t *testing.T) {
	answer := BoundInference(context.Background(), nil, nil, nil, InferenceRequest{Prompt: "x"})
	if answer.Outcome != OutcomeRefusal {
		t.Fatalf("outcome = %q, want refusal", answer.Outcome)
	}
	if answer.ModelCalls != 0 {
		t.Fatalf("model calls = %d, want 0", answer.ModelCalls)
	}
	if answer.Reason != "no inference provider" {
		t.Fatalf("reason = %q, want no inference provider", answer.Reason)
	}
}

func TestInferenceFormSummaryWithoutFiles(t *testing.T) {
	form := InferenceForm{Reason: "empty model response"}
	if got := form.Summary(); got != "empty model response" {
		t.Fatalf("summary = %q, want the reason alone", got)
	}
}

func TestUniqueInferenceFilesTrimsDeduplicatesAndSorts(t *testing.T) {
	got := strings.Join(uniqueInferenceFiles([]string{" b.go ", "a.go", " b.go", "  "}), ",")
	if want := "a.go,b.go"; got != want {
		t.Fatalf("uniqueInferenceFiles = %q, want %q", got, want)
	}
	if got := uniqueInferenceFiles(nil); len(got) != 0 {
		t.Fatalf("uniqueInferenceFiles(nil) = %q, want empty", got)
	}
}
