// Package wake adapts Beacon's wake pointer to the host harness. The default
// adapter uses the OpenCode HTTP API through `opencode api`, the same path the
// CLI and TUI use. It never invents a verdict and never merges a PR.
package wake

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

// OpenCodeDeliverer posts a steer message to an existing OpenCode session.
// The sessionID comes from the MCP caller's _meta, so Beacon already knows
// which agent to wake without a second registry.
type OpenCodeDeliverer struct {
	// Runner is replaceable in tests. Nil means exec.CommandContext.
	Runner func(ctx context.Context, args ...string) ([]byte, error)
}

var _ domain.WakeDeliverer = (*OpenCodeDeliverer)(nil)

// Deliver emits a <beacon-event> envelope with only identifiers. The host's
// prompt admission treats the text as untrusted input, never as a command.
func (d *OpenCodeDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	body := map[string]any{
		"text":     Envelope(p),
		"delivery": "steer",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	args := []string{"api", "POST", "/api/session/" + sessionID + "/prompt", "--data", string(raw)}
	var out []byte
	if d.Runner != nil {
		out, err = d.Runner(ctx, args...)
	} else {
		out, err = exec.CommandContext(ctx, "opencode", args...).Output()
	}
	if err != nil {
		return fmt.Errorf("wake: opencode api: %w", err)
	}
	return verifyAck(out)
}

// Envelope renders the pointer into the host's wake text. Every field is
// whitelisted: no check body or PR title is included.
func Envelope(p domain.WakePayload) string {
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

// verifyAck checks that the API admitted the prompt. An error response from
// opencode api is already an exec error; this covers a 2xx with an unexpected
// shape so a silently dropped wake is not reported as delivered.
func verifyAck(out []byte) error {
	if len(out) == 0 {
		return fmt.Errorf("wake: empty api response")
	}
	var ack struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return fmt.Errorf("wake: decode api response: %w", err)
	}
	if ack.Data.ID == "" {
		return fmt.Errorf("wake: api response has no admitted message id")
	}
	return nil
}
