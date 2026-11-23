package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/shared"
)

// InferenceOutcome names the three ways a bounded model call ends. A result
// carries model text. A refusal carries the reason and the file summary. A
// manual outcome carries the value a human entered after a refusal.
type InferenceOutcome string

const (
	OutcomeResult  InferenceOutcome = "result"
	OutcomeRefusal InferenceOutcome = "refusal"
	OutcomeManual  InferenceOutcome = "manual"
)

const (
	// DefaultInferenceLimit matches the 15 second cap dmux puts on a model
	// call before it falls back to manual input.
	DefaultInferenceLimit = 15 * time.Second
	// DefaultInferenceInputLimit matches the 5000 character diff cap dmux
	// applies before sending a prompt.
	DefaultInferenceInputLimit = 5000

	inferenceTruncationMarker = "\n[truncated]"
)

// ErrInferenceRefused reports a model that declines the request. A caller can
// wrap it with a reason and still use errors.Is on the result.
var ErrInferenceRefused = errors.New("session: inference refused")

// InferenceProvider is the model call. The caller supplies it, so this package
// owns no client and performs no network I/O. A provider must return when its
// context is done.
type InferenceProvider interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// InferenceFunc adapts a function to InferenceProvider.
type InferenceFunc func(ctx context.Context, prompt string) (string, error)

// Complete calls f.
func (f InferenceFunc) Complete(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt)
}

// Clock returns the current time. A caller injects it so elapsed time in an
// answer is reproducible. A nil clock uses time.Now.
type Clock func() time.Time

// ManualInput asks a human for a value. The caller owns the form rendering and
// the input source. This package only passes the refusal reason and files.
type ManualInput func(ctx context.Context, form InferenceForm) (string, error)

// InferenceRequest configures one bounded model call. Zero Limit and
// InputLimit take the dmux defaults.
type InferenceRequest struct {
	Prompt     string
	Files      []string
	Limit      time.Duration
	InputLimit int
}

// InferenceForm is the information a human gets when the model cannot answer.
type InferenceForm struct {
	Reason string
	Files  []string
}

// Summary renders the reason and the changed-file list. A refusal with no
// files still shows the reason.
func (f InferenceForm) Summary() string {
	if len(f.Files) == 0 {
		return f.Reason
	}
	return f.Reason + "\nchanged files: " + strings.Join(f.Files, ", ")
}

// InferenceAnswer is the outcome of BoundInference. Files is a sorted copy of
// the request files. ModelCalls counts one attempt; a manual answer never
// calls the model a second time.
type InferenceAnswer struct {
	Outcome         InferenceOutcome
	Text            string
	Reason          string
	Files           []string
	PromptTruncated bool
	ModelCalls      int
}

// BoundInference runs one model call under a time limit and an input cap. It
// truncates an overlong prompt with a marker, then maps the call to one of the
// three outcomes. A timeout, an error, an empty response or an explicit
// refusal hands an InferenceForm to manual when one is supplied. Manual
// returns OutcomeManual with the entered value and makes no second model call.
func BoundInference(ctx context.Context, provider InferenceProvider, now Clock, manual ManualInput, req InferenceRequest) InferenceAnswer {
	if now == nil {
		now = time.Now
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultInferenceLimit
	}
	inputLimit := req.InputLimit
	if inputLimit <= 0 {
		inputLimit = DefaultInferenceInputLimit
	}
	prompt, truncated := truncateInferenceInput(req.Prompt, inputLimit)
	answer := InferenceAnswer{
		Files:           uniqueInferenceFiles(req.Files),
		PromptTruncated: truncated,
	}
	if provider == nil {
		return refuseInference(ctx, answer, "no inference provider", manual)
	}

	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	started := now()
	type completion struct {
		text string
		err  error
	}
	done := make(chan completion, 1)
	go func() {
		text, err := provider.Complete(callCtx, prompt)
		done <- completion{text: text, err: err}
	}()
	answer.ModelCalls = 1

	select {
	case <-callCtx.Done():
		reason := fmt.Sprintf("model call timed out after %s", now().Sub(started))
		return refuseInference(ctx, answer, reason, manual)
	case got := <-done:
		// The deadline and the provider may finish together. Treat a call
		// whose context has expired as a timeout, never as a late result.
		if callCtx.Err() != nil {
			reason := fmt.Sprintf("model call timed out after %s", now().Sub(started))
			return refuseInference(ctx, answer, reason, manual)
		}
		if got.err != nil {
			return refuseInference(ctx, answer, got.err.Error(), manual)
		}
		text := strings.TrimSpace(got.text)
		if text == "" {
			return refuseInference(ctx, answer, "empty model response", manual)
		}
		answer.Outcome = OutcomeResult
		answer.Text = text
		return answer
	}
}

// refuseInference records the reason and, when a human is available, returns
// the entered value. Without manual input the outcome stays a refusal with
// the file summary attached.
func refuseInference(ctx context.Context, answer InferenceAnswer, reason string, manual ManualInput) InferenceAnswer {
	answer.Outcome = OutcomeRefusal
	answer.Reason = reason
	if manual == nil {
		return answer
	}
	form := InferenceForm{Reason: reason, Files: append([]string(nil), answer.Files...)}
	value, err := manual(ctx, form)
	if err != nil {
		answer.Reason = fmt.Sprintf("%s; manual input failed: %v", reason, err)
		return answer
	}
	answer.Outcome = OutcomeManual
	answer.Text = value
	return answer
}

// truncateInferenceInput keeps the marker inside the cap so the prompt handed
// to the model never exceeds InputLimit.
func truncateInferenceInput(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	if limit <= len(inferenceTruncationMarker) {
		return text[:limit], true
	}
	return text[:limit-len(inferenceTruncationMarker)] + inferenceTruncationMarker, true
}

// uniqueInferenceFiles trims, deduplicates and sorts the file summary.
func uniqueInferenceFiles(files []string) []string {
	trimmed := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f != "" {
			trimmed = append(trimmed, f)
		}
	}
	out := shared.DistinctBy(trimmed, func(f string) string { return f })
	sort.Strings(out)
	return out
}
