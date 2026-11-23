// Package mcp: t3_thread_read reads a bounded tail of one thread history.
//
// The tool is opt-in. It stays outside ToolNames and outputSchemaFactories so
// the nine advertised tools keep their pinned tools/list contract. A
// composition root that needs thread reading calls AttachThreadRead (T3.236).
package mcp

import (
	"context"
	"sort"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// ThreadReadToolName is the MCP name of the opt-in thread reader.
	ThreadReadToolName = "t3_thread_read"
	// threadReadCharLimit is the T3.236 ceiling on characters in one response.
	threadReadCharLimit = 30000
)

// ThreadHistorySource is the read-only history port. A source returns the
// messages of one thread; the handler never writes to it. Positions are
// monotonic within a thread and the handler orders them before paging.
type ThreadHistorySource interface {
	ThreadHistory(ctx context.Context, threadID string) ([]ThreadMessage, error)
}

// ThreadReadInput selects one thread and an optional continuation cursor.
// An empty Cursor reads the newest messages. A non-empty Cursor is the
// EarlierPosition of a previous response and bounds the read to older messages.
type ThreadReadInput struct {
	ThreadID string `json:"threadID" jsonschema:"thread to read; messages from other threads are never returned"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"opaque earlierPosition from a previous response; omit to read the newest messages"`
}

// ThreadReadHandler implements t3_thread_read over the read-only history port.
// It keeps no history of its own: every call re-reads the source and projects
// one page, so a client that loses its cursor can restart from the newest page.
type ThreadReadHandler struct {
	source ThreadHistorySource
}

// NewThreadReadHandler builds the handler. Pass Handle to mcpsdk.AddTool when
// wiring the tool with AttachThreadRead.
func NewThreadReadHandler(source ThreadHistorySource) *ThreadReadHandler {
	return &ThreadReadHandler{source: source}
}

// threadCursorBounds interprets a continuation cursor. An empty cursor has no
// upper bound. A non-empty one shares the base64-of-decimal shape of
// beacon_list_running's event cursor, so both read tools page the same way.
func threadCursorBounds(cursor string) (uint64, bool, error) {
	if cursor == "" {
		return 0, false, nil
	}
	position, err := decodeEventCursor(cursor)
	if err != nil {
		return 0, false, err
	}
	return position, true, nil
}

// Handle returns the newest messages that fit in the character limit. Messages
// are emitted in ascending position order even though they are collected from
// the tail backwards. EarlierPosition names the message immediately before the
// page, so the next call with that cursor cannot skip a message. A single
// message longer than the limit is clamped; sources are expected to split such
// messages before handing them to the tool.
func (h *ThreadReadHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in ThreadReadInput) (*mcpsdk.CallToolResult, ThreadReadOutput, error) {
	if in.ThreadID == "" {
		return nil, ThreadReadOutput{}, domain.NewAppError(domain.CodeInvalidInput, "thread_read: threadID is required")
	}
	if h == nil || h.source == nil {
		return nil, ThreadReadOutput{}, domain.NewAppError(domain.CodePersistFailed, "thread_read: history source is nil")
	}

	upper, bounded, err := threadCursorBounds(in.Cursor)
	if err != nil {
		return nil, ThreadReadOutput{}, domain.NewAppErrorWithCause(domain.CodeInvalidInput, "thread_read: "+err.Error(), err)
	}

	history, err := h.source.ThreadHistory(ctx, in.ThreadID)
	if err != nil {
		return nil, ThreadReadOutput{}, domain.NewAppErrorWithCause(domain.CodeWatchNotFound, "thread_read: history: "+err.Error(), err)
	}

	ordered := append([]ThreadMessage(nil), history...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })

	candidates := make([]ThreadMessage, 0, len(ordered))
	for _, m := range ordered {
		if bounded && m.Position > upper {
			continue
		}
		candidates = append(candidates, m)
	}

	reverse := make([]ThreadMessage, 0, len(candidates))
	used := 0
	first := -1
	for i := len(candidates) - 1; i >= 0; i-- {
		m := candidates[i]
		chars := utf8.RuneCountInString(m.Text)
		if len(reverse) > 0 && used+chars > threadReadCharLimit {
			break
		}
		if chars > threadReadCharLimit {
			m.Text = clampRunes(m.Text, threadReadCharLimit)
			chars = threadReadCharLimit
		}
		reverse = append(reverse, m)
		used += chars
		first = i
	}

	page := make([]ThreadMessage, 0, len(reverse))
	for i := len(reverse) - 1; i >= 0; i-- {
		page = append(page, reverse[i])
	}

	out := ThreadReadOutput{
		ThreadID:  in.ThreadID,
		Messages:  page,
		CharCount: used,
	}
	if first > 0 {
		out.HasMore = true
		out.EarlierPosition = encodeEventCursor(candidates[first-1].Position)
	}
	return &mcpsdk.CallToolResult{}, out, nil
}
