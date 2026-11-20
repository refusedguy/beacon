// Package wake delivers Beacon wake pointers to an OpenCode session through
// the `opencode api` CLI. The CLI reaches the same server and uses the same
// credentials as the TUI, so no plugin and no separate HTTP client are needed.
// The package never invents a verdict and never merges a PR.
package wake

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// connectedEvent is the first frame a correct SSE stream sends. Anything
	// before it is a replay of past events and must not reach the subscriber.
	connectedEvent = "server.connected"

	// defaultEventTypes are the facts a wake subscriber cares about.
	defaultEventTypes = "session.inbox.delivered,session.execution.started"
)

// PromptSyntax selects how session.prompt is addressed on the CLI. Both forms
// are accepted by `opencode api` and must produce the same admitted message.
type PromptSyntax int

const (
	// PromptRPC uses the operation name and a --param for the session id.
	PromptRPC PromptSyntax = iota
	// PromptPath uses the POST /api/session/{id}/prompt path form.
	PromptPath
)

// CLI runs `opencode api` commands. Server is an optional --server URL; when
// set, every command carries `--server <Server>` right after `api`. Runner is
// replaceable in tests; nil means exec.CommandContext with the real binary.
type CLI struct {
	Server string
	Runner func(ctx context.Context, args ...string) ([]byte, error)
}

// Envelope renders a pointer as untrusted wake text. Only identifiers cross
// the boundary: no check body, PR title, or verdict detail is interpolated.
func Envelope(p domain.Pointer) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"watchID":      p.WatchID,
		"obligationID": p.ObligationID,
		"revision":     p.Revision,
	})
	return "<beacon-event>" + strings.TrimSpace(buf.String()) + "</beacon-event>"
}

func promptData(text string) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"text": text, "delivery": "steer"})
	return strings.TrimSpace(buf.String())
}

func (c *CLI) baseArgs() []string {
	args := []string{"api"}
	if c.Server != "" {
		args = append(args, "--server", c.Server)
	}
	return args
}

// OpenAPIArgs builds the discovery command `opencode api GET /openapi.json`.
func (c *CLI) OpenAPIArgs() []string {
	return append(c.baseArgs(), "GET", "/openapi.json")
}

// PromptArgs builds the prompt command in either syntax. The --data body is
// identical for both, so both forms admit the same steer message.
func (c *CLI) PromptArgs(syntax PromptSyntax, sessionID, text string) []string {
	args := c.baseArgs()
	data := promptData(text)
	switch syntax {
	case PromptRPC:
		return append(args, "session.prompt", "--param", "sessionID="+sessionID, "--data", data)
	default:
		return append(args, "POST", "/api/session/"+sessionID+"/prompt", "--data", data)
	}
}

// EventArgs builds `opencode api GET /api/event` restricted to the given
// types. With no types it subscribes to the default wake facts.
func (c *CLI) EventArgs(types ...string) []string {
	if len(types) == 0 {
		types = strings.Split(defaultEventTypes, ",")
	}
	path := "/api/event?types=" + strings.Join(types, ",")
	return append(c.baseArgs(), "GET", path)
}

func (c *CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Runner != nil {
		return c.Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).Output()
}

// OpenAPI fetches the live OpenAPI document. It is the discovery step that
// proves the CLI and its authorization are usable before any wake is sent.
func (c *CLI) OpenAPI(ctx context.Context) ([]byte, error) {
	return c.run(ctx, c.OpenAPIArgs()...)
}

func admittedID(out []byte) (string, error) {
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

// Prompt wakes a session with the chosen syntax and returns the admitted
// message id from data.id. Both syntaxes are accepted so the caller can verify
// they produce the same result.
func (c *CLI) Prompt(ctx context.Context, syntax PromptSyntax, sessionID, text string) (string, error) {
	if !strings.HasPrefix(sessionID, "ses_") {
		return "", fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	out, err := c.run(ctx, c.PromptArgs(syntax, sessionID, text)...)
	if err != nil {
		return "", fmt.Errorf("wake: opencode api: %w", err)
	}
	return admittedID(out)
}

// Deliver is the path-form convenience that wakes a session with a pointer.
func (c *CLI) Deliver(ctx context.Context, sessionID string, p domain.Pointer) error {
	_, err := c.Prompt(ctx, PromptPath, sessionID, Envelope(p))
	return err
}

// SSEFrame is one parsed server-sent event.
type SSEFrame struct {
	Event string
	Data  string
}

// Frames parses raw SSE bytes into frames. Comment and heartbeat lines that
// start with ':' are ignored, and unterminated final frames are still kept.
func Frames(out []byte) []SSEFrame {
	var frames []SSEFrame
	var cur SSEFrame
	have := false
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			if have {
				frames = append(frames, cur)
				cur, have = SSEFrame{}, false
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			field, value = line, ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			cur.Event = value
			have = true
		case "data":
			cur.Data = value
			have = true
		}
	}
	if have {
		frames = append(frames, cur)
	}
	return frames
}

// Events drops frames that precede server.connected. A subscription must not
// surface past events that the server may replay before the connection frame.
func Events(frames []SSEFrame) []SSEFrame {
	for i, f := range frames {
		if f.Event == connectedEvent {
			return frames[i:]
		}
	}
	return nil
}

// Subscribe opens the event stream for the given types and returns frames
// from the connection point onward. The first returned frame is always
// server.connected; an absent connection frame is an error, not a silent
// empty stream.
func (c *CLI) Subscribe(ctx context.Context, types ...string) ([]SSEFrame, error) {
	out, err := c.run(ctx, c.EventArgs(types...)...)
	if err != nil {
		return nil, fmt.Errorf("wake: opencode api event: %w", err)
	}
	frames := Events(Frames(out))
	if len(frames) == 0 || frames[0].Event != connectedEvent {
		return nil, fmt.Errorf("wake: event stream must start with %s", connectedEvent)
	}
	return frames, nil
}
