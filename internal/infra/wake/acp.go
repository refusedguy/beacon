package wake

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"

	"github.com/refusedguy/beacon/internal/domain"
)

// ACPContentBlock is one v2 prompt content block. A wake sends a single text
// block, so an event body can never arrive as an attachment or a command.
type ACPContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ACPPromptParams is the params object of an ACP session/prompt request. The
// sessionId is opaque to Beacon: the caller gets it from the host's _meta.
type ACPPromptParams struct {
	SessionID string            `json:"sessionId"`
	Prompt    []ACPContentBlock `json:"prompt"`
}

// ACPRequest is one JSON-RPC 2.0 request in the ACP wire format. A transport
// owns framing and delivery; ACPAdapter owns the wake semantics.
type ACPRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// ACPTransport carries one ACP request to an agent and returns the raw
// JSON-RPC response. Implementations must not reinterpret the method or the
// params: the wake envelope is built before the request reaches a transport.
type ACPTransport interface {
	Call(ctx context.Context, req ACPRequest) ([]byte, error)
}

// ACPAdapter implements domain.WakeDeliverer over Agent Client Protocol. Deliver
// sends Envelope(p), which contains only whitelisted identifiers, as the text
// of a session/prompt request. No harness name or command is baked into the
// adapter.
type ACPAdapter struct {
	Transport ACPTransport
	id        atomic.Int64
}

var _ domain.WakeDeliverer = (*ACPAdapter)(nil)

// Deliver inserts the wake envelope into an existing ACP session. ACP requires
// the session to be active, so the caller owns session/resume on a fresh
// connection; this prototype only sends the prompt.
func (a *ACPAdapter) Deliver(ctx context.Context, sessionID string, p domain.WakePointer) error {
	if a == nil || a.Transport == nil {
		return fmt.Errorf("wake: acp: no transport")
	}
	if sessionID == "" {
		return fmt.Errorf("wake: acp: empty session id")
	}
	req := ACPRequest{
		JSONRPC: "2.0",
		ID:      a.id.Add(1),
		Method:  "session/prompt",
		Params: ACPPromptParams{
			SessionID: sessionID,
			Prompt: []ACPContentBlock{{
				Type: "text",
				Text: Envelope(p),
			}},
		},
	}
	raw, err := a.Transport.Call(ctx, req)
	if err != nil {
		return fmt.Errorf("wake: acp session/prompt: %w", err)
	}
	return verifyACPAck(raw)
}

// verifyACPAck treats a v2 insertion receipt as the delivery proof. A JSON-RPC
// error means the message was not inserted; a missing messageId means the
// response is not an ACP v2 prompt receipt.
func verifyACPAck(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("wake: acp: empty response")
	}
	var resp struct {
		Result struct {
			MessageID string `json:"messageId"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("wake: acp: decode response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("wake: acp: json-rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.MessageID == "" {
		return fmt.Errorf("wake: acp: response has no messageId")
	}
	return nil
}

// ACPStdioTransport talks to an ACP agent launched as a child process. Each
// Call starts one process, writes one newline-delimited JSON-RPC request and
// reads one response line. Exchange replaces the process in tests.
type ACPStdioTransport struct {
	Command  string
	Args     []string
	Dir      string
	Env      []string
	Exchange func(ctx context.Context, request []byte) ([]byte, error)
}

var _ ACPTransport = (*ACPStdioTransport)(nil)

func (t *ACPStdioTransport) Call(ctx context.Context, req ACPRequest) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if t.Exchange != nil {
		return t.Exchange(ctx, raw)
	}
	if t.Command == "" {
		return nil, fmt.Errorf("wake: acp stdio: empty command")
	}
	cmd := exec.CommandContext(ctx, t.Command, t.Args...)
	cmd.Dir = t.Dir
	cmd.Env = t.Env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("wake: acp stdio: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("wake: acp stdio: stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("wake: acp stdio: start: %w", err)
	}
	if _, err := stdin.Write(raw); err != nil {
		stdin.Close()
		cmd.Wait()
		return nil, fmt.Errorf("wake: acp stdio: write: %w", err)
	}
	if err := stdin.Close(); err != nil {
		cmd.Wait()
		return nil, fmt.Errorf("wake: acp stdio: close stdin: %w", err)
	}
	line, readErr := bufio.NewReader(stdout).ReadBytes('\n')
	waitErr := cmd.Wait()
	if readErr != nil && !(readErr == io.EOF && len(line) > 0) {
		return nil, fmt.Errorf("wake: acp stdio: read: %w", readErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("wake: acp stdio: wait: %w", waitErr)
	}
	return bytes.TrimSpace(line), nil
}

// ACPHTTPTransport posts ACP requests to an HTTP endpoint. The caller picks
// the URL and the client, including any credentials for that harness.
type ACPHTTPTransport struct {
	URL    string
	Client *http.Client
	Header http.Header
}

var _ ACPTransport = (*ACPHTTPTransport)(nil)

func (t *ACPHTTPTransport) Call(ctx context.Context, req ACPRequest) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("wake: acp http: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for name, values := range t.Header {
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("wake: acp http: post: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("wake: acp http: read: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("wake: acp http: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
