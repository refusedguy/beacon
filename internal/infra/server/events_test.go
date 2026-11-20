package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type readCall struct {
	cursor uint64
	limit  int
}

type stubReader struct {
	pages [][]Envelope
	calls []readCall
	err   error
}

func (s *stubReader) ReadAfter(_ context.Context, cursor uint64, limit int) ([]Envelope, error) {
	s.calls = append(s.calls, readCall{cursor: cursor, limit: limit})
	if s.err != nil {
		return nil, s.err
	}
	if len(s.pages) == 0 {
		return nil, nil
	}
	page := s.pages[0]
	s.pages = s.pages[1:]
	return page, nil
}

func TestReadAfterFromCursor(t *testing.T) {
	r := &stubReader{pages: [][]Envelope{{{Seq: 8}, {Seq: 9}}}}

	got, err := NewProjection(r).ReadAfter(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 8 || got[1].Seq != 9 {
		t.Fatalf("ReadAfter(7) = %+v, want seq 8, 9", got)
	}
	if len(r.calls) != 1 {
		t.Fatalf("reader calls = %d, want 1", len(r.calls))
	}
	if r.calls[0].cursor != 7 || r.calls[0].limit != PageSize {
		t.Fatalf("reader call = %+v, want cursor 7 and limit %d", r.calls[0], PageSize)
	}
}

func TestFramesEmptyBatch(t *testing.T) {
	r := &stubReader{pages: [][]Envelope{{}}}

	frames, err := NewProjection(r).Frames(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("Frames on empty page = %v, want no frames", frames)
	}
	if len(r.calls) != 1 || r.calls[0].limit != PageSize {
		t.Fatalf("reader calls = %+v, want one call with limit %d", r.calls, PageSize)
	}
}

func TestFramesSchemaVersion(t *testing.T) {
	r := &stubReader{pages: [][]Envelope{{
		{Seq: 5, Scope: "watch", Type: "watch.ready", SchemaVersion: 7,
			Payload: json.RawMessage(`{"ok":true}`)},
		{Seq: 6, Type: "watch.done"},
	}}}

	frames, err := NewProjection(r).Frames(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("Frames returned %d frames, want 2", len(frames))
	}

	var first wireEnvelope
	if err := json.Unmarshal(frames[0].Data, &first); err != nil {
		t.Fatal(err)
	}
	if first.Seq != 5 || first.SchemaVersion != 7 || first.Scope != "watch" ||
		string(first.Payload) != `{"ok":true}` {
		t.Fatalf("data envelope = %+v, want seq 5, schema 7, scope watch, payload kept", first)
	}
	if frames[0].ID != 5 || frames[0].Event != "watch.ready" {
		t.Fatalf("frame id/event = %d/%q, want 5/watch.ready", frames[0].ID, frames[0].Event)
	}

	var second wireEnvelope
	if err := json.Unmarshal(frames[1].Data, &second); err != nil {
		t.Fatal(err)
	}
	if second.SchemaVersion != SchemaVersion || string(second.Payload) != "null" {
		t.Fatalf("default envelope = %+v, want schema %d and null payload", second, SchemaVersion)
	}

	wire := string(frames[0].Bytes())
	if !strings.HasPrefix(wire, "id: 5\nevent: watch.ready\ndata: ") || !strings.HasSuffix(wire, "\n\n") {
		t.Fatalf("SSE wire = %q, want id/event/data lines and blank-line terminator", wire)
	}
}

func TestReaderError(t *testing.T) {
	sentinel := errors.New("journal read failed")
	r := &stubReader{err: sentinel}
	p := NewProjection(r)

	if _, err := p.ReadAfter(context.Background(), 0); !errors.Is(err, sentinel) {
		t.Fatalf("ReadAfter error = %v, want wrapped %v", err, sentinel)
	}
	if _, err := p.Frames(context.Background(), 0); !errors.Is(err, sentinel) {
		t.Fatalf("Frames error = %v, want wrapped %v", err, sentinel)
	}
	if _, err := p.ReplayAndLive(context.Background(), 0, nil); !errors.Is(err, sentinel) {
		t.Fatalf("ReplayAndLive error = %v, want wrapped %v", err, sentinel)
	}
}

func TestCursorFromRequest(t *testing.T) {
	withHeader := httptest.NewRequest("GET", "/api/v1/events?replay=3", nil)
	withHeader.Header.Set("Last-Event-ID", "9")
	if got, err := CursorFromRequest(withHeader); err != nil || got != 9 {
		t.Fatalf("header + query = %d, %v; want 9 (header wins)", got, err)
	}

	withQuery := httptest.NewRequest("GET", "/api/v1/events?replay=3", nil)
	if got, err := CursorFromRequest(withQuery); err != nil || got != 3 {
		t.Fatalf("query only = %d, %v; want 3", got, err)
	}

	empty := httptest.NewRequest("GET", "/api/v1/events", nil)
	if got, err := CursorFromRequest(empty); err != nil || got != 0 {
		t.Fatalf("no cursor = %d, %v; want 0", got, err)
	}

	malformed := httptest.NewRequest("GET", "/api/v1/events?replay=3", nil)
	malformed.Header.Set("Last-Event-ID", "not-a-seq")
	if _, err := CursorFromRequest(malformed); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("malformed header error = %v, want ErrInvalidCursor", err)
	}
}

// midReplayReader publishes a live event from inside the first replay page,
// the way a writer appending while the page is read would.
type midReplayReader struct {
	live  chan<- Envelope
	first bool
}

func (m *midReplayReader) ReadAfter(_ context.Context, _ uint64, _ int) ([]Envelope, error) {
	if !m.first {
		return nil, nil
	}
	m.first = false
	m.live <- Envelope{Seq: 2, Type: "watch.ready", SchemaVersion: 1,
		Payload: json.RawMessage(`{}`)}
	return []Envelope{{Seq: 1, Type: "watch.start"}, {Seq: 2, Type: "watch.ready"}}, nil
}

func TestReplayDeliversMidReplayEventOnce(t *testing.T) {
	live := make(chan Envelope, 4)
	p := NewProjection(&midReplayReader{live: live, first: true})

	frames, err := p.ReplayAndLive(context.Background(), 0, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2 (no duplicate of seq 2)", len(frames))
	}
	want := []uint64{1, 2}
	for i, f := range frames {
		if f.ID != want[i] {
			t.Fatalf("frame %d id = %d, want %d", i, f.ID, want[i])
		}
	}
}

func TestReplayPagesAtPageSize(t *testing.T) {
	full := make([]Envelope, PageSize)
	for i := range full {
		full[i] = Envelope{Seq: uint64(i + 1), Type: "tick"}
	}
	r := &stubReader{pages: [][]Envelope{full, {{Seq: uint64(PageSize + 1)}}}}

	frames, err := NewProjection(r).ReplayAndLive(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != PageSize+1 {
		t.Fatalf("frames = %d, want %d", len(frames), PageSize+1)
	}
	if len(r.calls) != 2 {
		t.Fatalf("reader calls = %d, want 2 pages", len(r.calls))
	}
	if r.calls[1].cursor != uint64(PageSize) {
		t.Fatalf("second page cursor = %d, want %d", r.calls[1].cursor, PageSize)
	}
}
