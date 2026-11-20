// Package wake encodes the delivery rules for waking a parked OpenCode
// session. It chooses steer or queue at admission, plans how an input is
// consumed at the active or idle boundary, and issues the three calls the host
// API exposes: prompt, interrupt and inbox patch. It never invents a verdict
// and never merges a PR (ADR 0074, 0075, 0076, 0077, 0078, 0079).
package wake

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DeliveryMode is the ADR 0077 choice made once at prompt admission.
type DeliveryMode string

const (
	DeliverySteer DeliveryMode = "steer"
	DeliveryQueue DeliveryMode = "queue"
)

// ExecutionState says whether the target session is running a step.
type ExecutionState string

const (
	ExecIdle   ExecutionState = "idle"
	ExecActive ExecutionState = "active"
)

// InputKind separates inputs a prompt hook may rewrite from inputs it never
// sees. ADR 0077: no hook for synthetic, shell, compaction or move.
type InputKind string

const (
	InputPrompt    InputKind = "prompt"
	InputSynthetic InputKind = "synthetic"
	InputShell     InputKind = "shell"
	InputCompact   InputKind = "compact"
	InputMove      InputKind = "move"
)

// HooksApply reports whether a prompt hook may change the delivery mode. A
// hook fires once at admission and never for a synthetic or system input.
func HooksApply(k InputKind) bool { return k == InputPrompt }

// DefaultDelivery is the admission default. Prompt, synthetic and compact
// start as steer; a hook may demote an eligible input to queue once.
func DefaultDelivery(k InputKind) DeliveryMode {
	if k == InputPrompt || k == InputSynthetic || k == InputCompact {
		return DeliverySteer
	}
	return DeliveryQueue
}

// Admission is where an admitted input is consumed.
type Admission struct {
	Mode           DeliveryMode
	SchedulesLoop  bool
	AtStepBoundary bool
	IdlePriority   bool
	Parked         bool
}

// PlanAdmission applies ADR 0077 to a delivery mode and an execution state.
// steer schedules the loop, is consumed at the next safe step boundary while
// the loop is active, and wins at idle. queue never schedules: it stays parked
// in enqueue order.
func PlanAdmission(mode DeliveryMode, state ExecutionState) Admission {
	switch mode {
	case DeliverySteer:
		a := Admission{Mode: DeliverySteer, SchedulesLoop: true, IdlePriority: true}
		if state == ExecActive {
			a.AtStepBoundary = true
		}
		return a
	case DeliveryQueue:
		return Admission{Mode: DeliveryQueue, Parked: true}
	default:
		return Admission{Mode: mode}
	}
}

// AfterInterrupt returns an input's placement after POST interrupt. The resume
// flag governs the running loop, never the inbox: a queued input stays parked
// for both resume=true and resume=false (ADR 0077, 0079).
func AfterInterrupt(mode DeliveryMode, resume bool) Admission {
	return PlanAdmission(mode, ExecActive)
}

// PendingInput is one admitted input waiting in the inbox.
type PendingInput struct {
	ID   string
	Mode DeliveryMode
}

// NextInput picks the input consumed when the session reaches idle. Steers win
// over queued items regardless of enqueue order (ADR 0077).
func NextInput(pending []PendingInput) (PendingInput, bool) {
	for _, p := range pending {
		if p.Mode == DeliverySteer {
			return p, true
		}
	}
	for _, p := range pending {
		if p.Mode == DeliveryQueue {
			return p, true
		}
	}
	return PendingInput{}, false
}

// AdmissionStatus is the classified outcome of a prompt response.
type AdmissionStatus string

const (
	StatusAdmitted    AdmissionStatus = "admitted"
	StatusDuplicateID AdmissionStatus = "duplicate-id"
	StatusError       AdmissionStatus = "error"
)

// ClassifyHTTP maps a response code to an admission outcome. ADR 0077: 409 is
// a duplicate message id, not a busy session. There is no busy-session status,
// because an active loop consumes steer at its next step boundary.
func ClassifyHTTP(code int) AdmissionStatus {
	switch code {
	case 200, 201, 202, 204:
		return StatusAdmitted
	case 409:
		return StatusDuplicateID
	default:
		return StatusError
	}
}

// BusySession reports whether a status means the session refused work as
// busy. It never does; the function exists so callers stop reading 409 that
// way (ADR 0077).
func BusySession(s AdmissionStatus) bool { return false }

// Pointer is the identifier-only payload a verdict source hands to a wake
// adapter. It never carries check bodies or PR text (ADR 0074).
type Pointer struct {
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID"`
	Revision     uint64 `json:"revision"`
	Verdict      string `json:"verdict"`
}

// Envelope renders a pointer as the untrusted wake text admitted to a session.
func Envelope(p Pointer) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"watchID":      p.WatchID,
		"obligationID": p.ObligationID,
		"revision":     p.Revision,
		"verdict":      p.Verdict,
	})
	return "<beacon-event>" + strings.TrimSpace(buf.String()) + "</beacon-event>"
}

// Runner executes an `opencode api` invocation. Tests replace it.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// API is a thin client over the opencode api CLI (ADR 0078).
type API struct {
	Runner Runner
}

func (a *API) run(ctx context.Context, args ...string) ([]byte, error) {
	if a.Runner != nil {
		return a.Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).Output()
}

// Prompt admits one session input and returns its message id. mode is the
// explicit delivery choice: steer schedules execution, queue only parks.
func (a *API) Prompt(ctx context.Context, sessionID, text string, mode DeliveryMode) (string, error) {
	if !strings.HasPrefix(sessionID, "ses_") {
		return "", fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	body := map[string]any{"text": text, "delivery": string(mode)}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	args := []string{"api", "POST", "/api/session/" + sessionID + "/prompt", "--data", string(raw)}
	out, err := a.run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("wake: opencode api prompt: %w", err)
	}
	return messageID(out)
}

// Park admits a queued input that does not schedule execution (ADR 0076).
func (a *API) Park(ctx context.Context, sessionID, text string) (string, error) {
	return a.Prompt(ctx, sessionID, text, DeliveryQueue)
}

// Interrupt posts POST /api/session/{id}/interrupt?resume=<bool>. resume=true
// stops the running loop but leaves queued inputs parked; resume=false is the
// admit-only stop (ADR 0079).
func (a *API) Interrupt(ctx context.Context, sessionID string, resume bool) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	path := "/api/session/" + sessionID + "/interrupt?resume=" + strconv.FormatBool(resume)
	if _, err := a.run(ctx, "api", "POST", path); err != nil {
		return fmt.Errorf("wake: opencode api interrupt: %w", err)
	}
	return nil
}

// Promote patches a parked inbox item to steer so the session wakes (ADR 0076).
func (a *API) Promote(ctx context.Context, sessionID, inboxID string) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	path := "/api/session/" + sessionID + "/inbox/" + inboxID
	raw := []byte(`{"delivery":"steer"}`)
	if _, err := a.run(ctx, "api", "PATCH", path, "--data", string(raw)); err != nil {
		return fmt.Errorf("wake: opencode api promote: %w", err)
	}
	return nil
}

// messageID reads data.id from a prompt response. An empty id is a silent
// drop and is never reported as admitted.
func messageID(out []byte) (string, error) {
	if len(out) == 0 {
		return "", fmt.Errorf("wake: empty api response")
	}
	var ack struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return "", fmt.Errorf("wake: decode api response: %w", err)
	}
	if ack.Data.ID == "" {
		return "", fmt.Errorf("wake: api response has no admitted message id")
	}
	return ack.Data.ID, nil
}
