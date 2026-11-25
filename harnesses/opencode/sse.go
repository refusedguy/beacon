package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// sseFrame is one parsed server-sent event from the OpenCode event stream.
type sseFrame struct {
	event string
	data  string
}

// WatchIdle starts the idle stream watcher in the background.
func (h *Harness) WatchIdle(ctx context.Context) {
	h.init()
	h.watchOnce.Do(func() { go h.watchLoop(ctx) })
}

// RunIdleWatch runs the idle stream watcher until ctx is cancelled.
func (h *Harness) RunIdleWatch(ctx context.Context) {
	h.init()
	h.watchLoop(ctx)
}

// watchLoop reconnects to the idle stream with exponential backoff.
func (h *Harness) watchLoop(ctx context.Context) {
	delay := h.ReconnectMin
	timer := time.NewTimer(delay)
	timer.Stop()
	defer timer.Stop()
	for ctx.Err() == nil {
		_ = h.streamIdle(ctx)
		if ctx.Err() != nil {
			return
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if delay < h.ReconnectMax {
			delay *= 2
			if delay > h.ReconnectMax {
				delay = h.ReconnectMax
			}
		}
	}
}

// streamClient returns a client without a request timeout for long-lived streams.
func (h *Harness) streamClient() *http.Client {
	if h.HTTP == nil || h.HTTP.Timeout == 0 {
		return h.HTTP
	}
	return &http.Client{
		Transport:     h.HTTP.Transport,
		CheckRedirect: h.HTTP.CheckRedirect,
		Jar:           h.HTTP.Jar,
	}
}

// streamIdle reads the OpenCode event stream and emits idle events per session.
func (h *Harness) streamIdle(ctx context.Context) error {
	h.init()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.Endpoint+"/api/event", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if h.Token != "" {
		ApplyAuth(req, h.Token)
	}
	client := h.streamClient()
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("opencode event stream: status %d", resp.StatusCode)
	}
	return readSSE(resp.Body, func(f sseFrame) {
		if f.event != "session.idle" {
			return
		}
		session := sessionIDFromData(f.data)
		if session == "" {
			return
		}
		run, ok := h.runFor(session)
		if !ok {
			return
		}
		select {
		case h.events <- core.HarnessEvent{RunID: run, SessionID: session, Kind: "idle"}:
		case <-ctx.Done():
		}
	})
}

// readSSE parses server-sent event frames from r and calls onFrame for each.
func readSSE(r io.Reader, onFrame func(sseFrame)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var frame sseFrame
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if frame.event != "" || frame.data != "" {
				onFrame(frame)
			}
			frame = sseFrame{}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value := line, ""
		if i := strings.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], strings.TrimPrefix(line[i+1:], " ")
		}
		switch field {
		case "event":
			frame.event = value
		case "data":
			if frame.data != "" {
				frame.data += "\n"
			}
			frame.data += value
		}
	}
	if frame.event != "" || frame.data != "" {
		onFrame(frame)
	}
	return scanner.Err()
}

// sessionIDFromData extracts a session id from an event payload.
func sessionIDFromData(data string) string {
	data = strings.TrimSpace(data)
	if data == "" {
		return ""
	}
	var value any
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return data
	}
	if s, ok := value.(string); ok {
		return s
	}
	var found string
	var walk func(any)
	walk = func(v any) {
		if found != "" {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				name := strings.ToLower(k)
				if name == "sessionid" || name == "session_id" {
					if s, ok := val.(string); ok && s != "" {
						found = s
						return
					}
				}
			}
			for _, val := range t {
				walk(val)
				if found != "" {
					return
				}
			}
		case []any:
			for _, val := range t {
				walk(val)
				if found != "" {
					return
				}
			}
		}
	}
	walk(value)
	if found == "" {
		found = findAnyID(value)
	}
	return found
}

// findAnyID searches a decoded payload for the first string id field.
func findAnyID(v any) string {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if strings.EqualFold(k, "id") {
				if s, ok := val.(string); ok && s != "" {
					return s
				}
			}
		}
		for _, val := range t {
			if s := findAnyID(val); s != "" {
				return s
			}
		}
	case []any:
		for _, val := range t {
			if s := findAnyID(val); s != "" {
				return s
			}
		}
	}
	return ""
}
