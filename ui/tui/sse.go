package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Event is the monitor's copy of the daemon's loopback wire event. It is a
// notification only; the client re-reads runs after receiving one.
type Event struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	RunID string `json:"run_id"`
	At    string `json:"at"`
}

// sseFrame is one parsed server-sent event from the daemon.
type sseFrame struct {
	id    string
	event string
	data  string
}

// streamMinDelay and streamMaxDelay bound the stream reconnect backoff.
const (
	streamMinDelay = 200 * time.Millisecond
	streamMaxDelay = 5 * time.Second
)

// streamHTTPClient builds a timeout-free client for the SSE stream.
func (c *Client) streamHTTPClient() *http.Client {
	// SSE streams are long-lived, so this client must have no request timeout.
	// Build a fresh client rather than copying c.HTTP: copying a client after
	// use is unsafe and would carry its timeout into the stream.
	streamClient := &http.Client{}
	if base := c.HTTP; base != nil {
		streamClient.Transport = base.Transport
	}
	return streamClient
}

// Stream keeps the event stream open, calling onEvent for each event.
func (c *Client) Stream(ctx context.Context, onEvent func(Event)) error {
	delay := streamMinDelay
	for ctx.Err() == nil {
		err := c.streamOnce(ctx, onEvent)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			delay = streamMinDelay
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < streamMaxDelay {
			delay *= 2
			if delay > streamMaxDelay {
				delay = streamMaxDelay
			}
		}
	}
	return nil
}

// streamOnce connects once to the event stream and decodes its events.
func (c *Client) streamOnce(ctx context.Context, onEvent func(Event)) error {
	base, err := c.endpoint()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+apiEventsPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.streamHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tui: event stream: status %d", resp.StatusCode)
	}
	return readSSE(resp.Body, func(f sseFrame) {
		if f.data == "" {
			return
		}
		var ev Event
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			return
		}
		if ev.Type == "" {
			ev.Type = f.event
		}
		onEvent(ev)
	})
}

// readSSE parses server-sent event frames from r and calls onFrame for each.
func readSSE(r io.Reader, onFrame func(sseFrame)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), responseLimit)
	var frame sseFrame
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if frame.event != "" || frame.data != "" || frame.id != "" {
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
		case "id":
			frame.id = value
		case "event":
			frame.event = value
		case "data":
			if frame.data != "" {
				frame.data += "\n"
			}
			frame.data += value
		}
	}
	if frame.event != "" || frame.data != "" || frame.id != "" {
		onFrame(frame)
	}
	return scanner.Err()
}
