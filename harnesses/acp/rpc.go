package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// protocolVersion is the ACP major version this harness speaks. An agent
// that negotiates another version is closed rather than driven blindly.
const protocolVersion = 1

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcEnvelope is the union of a JSON-RPC request, response and notification.
// ID is a pointer so notifications with no ID are distinct from id 0.
type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int64     `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

// process is the ACP transport. The harness talks to an agent through this
// interface so tests can drive protocol logic without a subprocess.
type process interface {
	call(ctx context.Context, method string, params any, out any, onNote func(json.RawMessage)) error
	notify(ctx context.Context, method string, params any) error
	close() error
}

// stdioProcess hosts one ACP agent. ACP uses newline-delimited JSON-RPC over
// stdin and stdout. Calls are serialized because Beacon drives one turn at a
// time per worker session.
type stdioProcess struct {
	cmd    *exec.Cmd
	enc    *json.Encoder
	dec    *json.Decoder
	stdin  io.WriteCloser
	stdout io.ReadCloser
	mu     sync.Mutex
	next   int64
}

func startProcess(command, cwd string) (*stdioProcess, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("acp: empty command")
	}
	cmd := exec.Command(fields[0], fields[1:]...)
	cmd.Dir = cwd
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return &stdioProcess{
		cmd: cmd, enc: json.NewEncoder(stdin), dec: json.NewDecoder(stdout),
		stdin: stdin, stdout: stdout,
	}, nil
}

// call sends one request and reads ACP messages until the matching response
// arrives. Agent-to-client requests are answered inline so a permission
// prompt cannot stall the turn. Notifications go to onNote, if supplied.
func (p *stdioProcess) call(
	ctx context.Context, method string, params any, out any, onNote func(json.RawMessage),
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	id := atomic.AddInt64(&p.next, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = p.cmd.Process.Kill()
		case <-done:
		}
	}()

	if err := p.enc.Encode(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return err
	}
	for {
		var raw json.RawMessage
		if err := p.dec.Decode(&raw); err != nil {
			return err
		}
		var env rpcEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.ID != nil && *env.ID == id {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if env.Error != nil {
				return fmt.Errorf("acp %s: %s (code %d)", method, env.Error.Message, env.Error.Code)
			}
			if out != nil && len(env.Result) > 0 {
				return json.Unmarshal(env.Result, out)
			}
			return nil
		}
		if env.ID != nil && env.Method != "" {
			p.answer(env.ID, env.Method, env.Params)
			continue
		}
		if onNote != nil && env.Method != "" {
			onNote(raw)
		}
	}
}

// answer replies to an agent request. Permission prompts are approved using
// the first allow option. Other client methods are unsupported in this
// minimal harness and get a method-not-found error instead of hanging.
func (p *stdioProcess) answer(id *int64, method string, params json.RawMessage) {
	resp := rpcResponse{JSONRPC: "2.0", ID: *id}
	if method != "session/request_permission" {
		resp.Error = &rpcError{Code: -32601, Message: "unsupported client method: " + method}
	} else if optionID := firstAllowOption(params); optionID == "" {
		resp.Result = map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	} else {
		resp.Result = map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": optionID}}
	}
	_ = p.enc.Encode(resp)
}

func firstAllowOption(params json.RawMessage) string {
	var req struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return ""
	}
	fallback := ""
	for _, opt := range req.Options {
		if fallback == "" {
			fallback = opt.OptionID
		}
		if strings.HasPrefix(opt.Kind, "allow") {
			return opt.OptionID
		}
	}
	return fallback
}

func (p *stdioProcess) notify(ctx context.Context, method string, params any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return p.enc.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (p *stdioProcess) close() error {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.stdout.Close()
	_ = p.cmd.Wait()
	return nil
}

var _ process = (*stdioProcess)(nil)
