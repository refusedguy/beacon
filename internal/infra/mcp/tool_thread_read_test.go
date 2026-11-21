package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeThreadSource is an in-memory history with one bucket per thread. It
// records every requested thread id so a test can prove the handler never asks
// for another thread's history.
type fakeThreadSource struct {
	byThread map[string][]ThreadMessage
	calls    []string
	err      error
}

func (f *fakeThreadSource) ThreadHistory(_ context.Context, threadID string) ([]ThreadMessage, error) {
	f.calls = append(f.calls, threadID)
	if f.err != nil {
		return nil, f.err
	}
	return append([]ThreadMessage(nil), f.byThread[threadID]...), nil
}

func longHistory(threadID string, count, chars int) []ThreadMessage {
	out := make([]ThreadMessage, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, ThreadMessage{
			Position:       uint64(i + 1),
			Text:           strings.Repeat("x", chars),
			CreatedBy:      "ses_actor",
			CreationSource: "mcp",
		})
	}
	return out
}

func TestThreadReadRespectsCharacterLimit(t *testing.T) {
	// 200 messages at 300 characters each is 60000 characters, twice the
	// 30000 ceiling. The page must stop at the boundary and report a
	// continuation.
	source := &fakeThreadSource{byThread: map[string][]ThreadMessage{
		"alpha": longHistory("alpha", 200, 300),
	}}
	_, out, err := NewThreadReadHandler(source).Handle(context.Background(), nil, ThreadReadInput{ThreadID: "alpha"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if out.CharCount > threadReadCharLimit {
		t.Errorf("CharCount = %d, want at most %d", out.CharCount, threadReadCharLimit)
	}
	if out.CharCount <= 0 {
		t.Fatalf("CharCount = %d, want a non-empty page", out.CharCount)
	}
	if len(out.Messages) >= 200 {
		t.Errorf("page returned %d messages, want fewer than the 200 in history", len(out.Messages))
	}
	if !out.HasMore {
		t.Error("HasMore = false, want true when older messages remain")
	}
	if out.EarlierPosition == "" {
		t.Error("EarlierPosition is empty, want an opaque continuation cursor")
	}
	if len(source.calls) != 1 || source.calls[0] != "alpha" {
		t.Errorf("source calls = %v, want [alpha]", source.calls)
	}
}

func TestThreadReadContinuesWithoutOverlap(t *testing.T) {
	source := &fakeThreadSource{byThread: map[string][]ThreadMessage{
		"alpha": longHistory("alpha", 200, 300),
	}}
	handler := NewThreadReadHandler(source)
	ctx := context.Background()

	seen := make(map[uint64]bool)
	cursor := ""
	pages := 0
	for {
		_, out, err := handler.Handle(ctx, nil, ThreadReadInput{ThreadID: "alpha", Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: Handle: %v", pages, err)
		}
		if out.CharCount > threadReadCharLimit {
			t.Fatalf("page %d: CharCount = %d, over the limit", pages, out.CharCount)
		}
		prev := uint64(0)
		for _, m := range out.Messages {
			if seen[m.Position] {
				t.Fatalf("page %d repeated position %d", pages, m.Position)
			}
			if prev != 0 && m.Position <= prev {
				t.Fatalf("page %d positions are not ascending: %d after %d", pages, m.Position, prev)
			}
			seen[m.Position] = true
			prev = m.Position
		}
		pages++
		if !out.HasMore {
			if out.EarlierPosition != "" {
				t.Error("last page has an EarlierPosition but no more history")
			}
			break
		}
		if out.EarlierPosition == "" {
			t.Fatalf("page %d has HasMore but no EarlierPosition", pages-1)
		}
		cursor = out.EarlierPosition
		if pages > 10 {
			t.Fatal("continuation did not converge")
		}
	}
	if len(seen) != 200 {
		t.Errorf("collected %d positions, want all 200", len(seen))
	}
}

func TestThreadReadKeepsProvenanceFieldsSeparate(t *testing.T) {
	source := &fakeThreadSource{byThread: map[string][]ThreadMessage{
		"alpha": {
			{Position: 1, Text: "hello", CreatedBy: "ses_human_7", CreationSource: "http-api"},
			{Position: 2, Text: "done", CreatedBy: "ses_agent_9", CreationSource: "mcp"},
		},
	}}
	_, out, err := NewThreadReadHandler(source).Handle(context.Background(), nil, ThreadReadInput{ThreadID: "alpha"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if out.HasMore || out.EarlierPosition != "" {
		t.Errorf("short history reported continuation: hasMore=%v earlier=%q", out.HasMore, out.EarlierPosition)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(out.Messages))
	}
	if out.Messages[0].CreatedBy != "ses_human_7" || out.Messages[0].CreationSource != "http-api" {
		t.Errorf("message 1 provenance = %q/%q", out.Messages[0].CreatedBy, out.Messages[0].CreationSource)
	}
	if out.Messages[1].CreatedBy != "ses_agent_9" || out.Messages[1].CreationSource != "mcp" {
		t.Errorf("message 2 provenance = %q/%q", out.Messages[1].CreatedBy, out.Messages[1].CreationSource)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	items, ok := wire["messages"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("wire messages = %v", wire["messages"])
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first message = %v", items[0])
	}
	for _, key := range []string{"createdBy", "creationSource", "position", "text"} {
		if _, ok := first[key]; !ok {
			t.Errorf("wire message missing %q: %v", key, first)
		}
	}
	for _, key := range []string{"threadID", "charCount", "hasMore", "earlierPosition"} {
		if _, ok := wire[key]; !ok {
			if key == "earlierPosition" && !out.HasMore {
				continue
			}
			t.Errorf("wire output missing %q: %v", key, wire)
		}
	}
}

func TestThreadReadSchemaCarriesContinuationAndProvenance(t *testing.T) {
	schema, err := ThreadReadOutputSchema()
	if err != nil {
		t.Fatalf("ThreadReadOutputSchema: %v", err)
	}
	for _, name := range []string{"threadID", "messages", "charCount", "hasMore", "earlierPosition"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("output schema missing %q", name)
		}
	}
	messages := schema.Properties["messages"]
	if messages == nil || messages.Items == nil {
		t.Fatal("output schema has no messages item schema")
	}
	for _, name := range []string{"position", "text", "createdBy", "creationSource"} {
		if _, ok := messages.Items.Properties[name]; !ok {
			t.Errorf("message schema missing %q", name)
		}
	}
}

func TestThreadReadCountsCharactersNotBytes(t *testing.T) {
	source := &fakeThreadSource{byThread: map[string][]ThreadMessage{
		"alpha": longHistory("alpha", 100, 1),
	}}
	// Replace every message with a multibyte rune. A 100-rune history must
	// still fit in one page, which a byte-based ceiling would reject.
	for i := range source.byThread["alpha"] {
		source.byThread["alpha"][i].Text = "é"
	}
	_, out, err := NewThreadReadHandler(source).Handle(context.Background(), nil, ThreadReadInput{ThreadID: "alpha"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if out.CharCount != 100 {
		t.Errorf("CharCount = %d, want 100 runes", out.CharCount)
	}
	if out.HasMore || len(out.Messages) != 100 {
		t.Errorf("multibyte page split unexpectedly: messages=%d hasMore=%v", len(out.Messages), out.HasMore)
	}
}

func TestThreadReadRejectsBadInput(t *testing.T) {
	source := &fakeThreadSource{byThread: map[string][]ThreadMessage{}}
	handler := NewThreadReadHandler(source)

	if _, _, err := handler.Handle(context.Background(), nil, ThreadReadInput{}); err == nil {
		t.Error("empty threadID was accepted")
	}
	_, _, err := handler.Handle(context.Background(), nil, ThreadReadInput{ThreadID: "alpha", Cursor: "not-a-cursor"})
	if err == nil {
		t.Fatal("malformed cursor was accepted")
	}
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
		t.Errorf("cursor error = %v, want CodeInvalidInput", err)
	}
}
