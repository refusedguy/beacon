// Package server projects the durable event journal into the server transport
// as SSE frames. Replay reads through a minimal EventReader port, so this
// package never imports store/events.go (BS.11) or app/event (BS.16) and never
// holds a lock that an appending writer needs.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Envelope is one versioned event returned by the journal. Its fields mirror
// the events table without importing the store package that owns that table.
type Envelope struct {
	Seq           uint64          `json:"seq"`
	Scope         string          `json:"scope,omitempty"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	Payload       json.RawMessage `json:"payload"`
}

// EventReader is the minimal journal port the projection needs. ReadAfter
// returns at most limit envelopes with seq greater than cursor in ascending
// seq order. An implementation must read a snapshot: a replay may never block
// a writer appending a new event, and events appended mid-page appear on the
// next call.
type EventReader interface {
	ReadAfter(ctx context.Context, cursor uint64, limit int) ([]Envelope, error)
}

const (
	// PageSize is the replay page size from BS.17.
	PageSize = 500

	// SchemaVersion is stamped on envelopes that reach the projection without
	// one. Clients switch payload decoding on this number (ADR 0012).
	SchemaVersion = 1
)

// ErrInvalidCursor marks a Last-Event-ID or replay value that is not a seq.
var ErrInvalidCursor = errors.New("server: invalid cursor")

// Frame is one SSE-compatible record. ID is the envelope seq and doubles as
// the Last-Event-ID a client sends on reconnect.
type Frame struct {
	ID    uint64
	Event string
	Data  []byte
}

// Bytes renders the frame in the SSE wire format.
func (f Frame) Bytes() []byte {
	var b bytes.Buffer
	_, _ = fmt.Fprintf(&b, "id: %d\nevent: %s\ndata: %s\n\n", f.ID, sanitizeEventName(f.Event), f.Data)
	return b.Bytes()
}

// wireEnvelope is the stable, versioned JSON carried in an SSE data line.
type wireEnvelope struct {
	Seq           uint64          `json:"seq"`
	Scope         string          `json:"scope,omitempty"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	Payload       json.RawMessage `json:"payload"`
}

// Projection turns journal pages into SSE frames.
type Projection struct {
	reader   EventReader
	pageSize int
}

// NewProjection builds a projection that reads pages of PageSize envelopes.
func NewProjection(reader EventReader) *Projection {
	return &Projection{reader: reader, pageSize: PageSize}
}

// ReadAfter reads one page after cursor. A nil reader is reported as an error
// rather than a panic so bad wiring cannot take the server down mid-stream.
func (p *Projection) ReadAfter(ctx context.Context, cursor uint64) ([]Envelope, error) {
	if p == nil || p.reader == nil {
		return nil, errors.New("server: nil event reader")
	}
	envs, err := p.reader.ReadAfter(ctx, cursor, p.pageLimit())
	if err != nil {
		return nil, fmt.Errorf("server: read events after seq %d: %w", cursor, err)
	}
	return envs, nil
}

// Frames reads one page after cursor and renders each envelope as an SSE
// frame. Envelopes at or below cursor are skipped, so a reader returning a
// stale row cannot replay a seq twice.
func (p *Projection) Frames(ctx context.Context, cursor uint64) ([]Frame, error) {
	envs, err := p.ReadAfter(ctx, cursor)
	if err != nil {
		return nil, err
	}
	frames := make([]Frame, 0, len(envs))
	for _, env := range envs {
		if env.Seq <= cursor {
			continue
		}
		frames = append(frames, frameFor(env))
	}
	return frames, nil
}

// ReplayAndLive fuses replay pages with events buffered by a live
// subscription. The caller must subscribe before calling this method: the
// channel accumulates events appended while pages are read. Pages are limited
// to PageSize and every seq is emitted once, whether it came from a page or
// from the live channel (BS.17).
func (p *Projection) ReplayAndLive(ctx context.Context, cursor uint64, live <-chan Envelope) ([]Frame, error) {
	frames := make([]Frame, 0)
	for {
		page, err := p.ReadAfter(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		advanced := false
		for _, env := range page {
			if env.Seq <= cursor {
				continue
			}
			frames = append(frames, frameFor(env))
			cursor = env.Seq
			advanced = true
		}
		// A page that cannot advance the cursor would spin forever. Stop and
		// let a later request retry instead of blocking the writer.
		if !advanced || len(page) < p.pageLimit() {
			break
		}
	}
	for {
		select {
		case env, ok := <-live:
			if !ok {
				return frames, nil
			}
			if env.Seq <= cursor {
				continue
			}
			frames = append(frames, frameFor(env))
			cursor = env.Seq
		default:
			return frames, nil
		}
	}
}

// CursorFromRequest resolves the replay cursor for an SSE request. The
// Last-Event-ID header wins over the replay query parameter (BS.17). A
// malformed header is an error: falling back to the query could resend events
// under an id the client has already passed.
func CursorFromRequest(r *http.Request) (uint64, error) {
	if r == nil {
		return 0, fmt.Errorf("%w: nil request", ErrInvalidCursor)
	}
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		return parseCursor(raw)
	}
	if r.URL == nil {
		return 0, nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get("replay"))
	if raw == "" {
		return 0, nil
	}
	return parseCursor(raw)
}

func parseCursor(raw string) (uint64, error) {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrInvalidCursor, raw, err)
	}
	return n, nil
}

func (p *Projection) pageLimit() int {
	if p == nil || p.pageSize <= 0 {
		return PageSize
	}
	return p.pageSize
}

func frameFor(env Envelope) Frame {
	version := env.SchemaVersion
	if version <= 0 {
		version = SchemaVersion
	}
	wire := wireEnvelope{
		Seq:           env.Seq,
		Scope:         env.Scope,
		Type:          env.Type,
		SchemaVersion: version,
		Payload:       normalizePayload(env.Payload),
	}
	data, err := json.Marshal(wire)
	if err != nil {
		// normalizePayload keeps wire valid JSON, so this is unreachable. Keep
		// the version in the fallback so a client still rejects an old schema.
		data = []byte(`{"schema_version":` + strconv.Itoa(version) + `,"payload":null}`)
	}
	return Frame{ID: env.Seq, Event: env.Type, Data: data}
}

func normalizePayload(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return json.RawMessage("null")
	}
	return trimmed
}

func sanitizeEventName(name string) string {
	if name == "" {
		return "message"
	}
	for i, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '.' || r == '-'
		if !valid {
			if i == 0 {
				return "message"
			}
			return name[:i]
		}
	}
	return name
}
