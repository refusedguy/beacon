// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_defer_wiring, the eighth tool. It records a
// deferred wiring card for a PR so the hand-off survives the agent turn. The
// tool is idempotent by (watchID, reason): a repeat returns the original card
// with created=false. It also returns the markdown of the card
// docs/tasks/WIRING-<pr>.md so the caller can paste it into its own PR.
//
// The durable write side arrives with W12-A. Until then this file declares the
// minimal DeferWriter port and cmd/beacon/deferred.go supplies an adapter, so
// the MCP layer still imports neither a sibling infra package nor the store.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// DeferWiringInput is the argument object of beacon_defer_wiring. Reason is
// part of the idempotency key together with WatchID, so a second reason for
// the same Watch records a second card instead of overwriting the first.
type DeferWiringInput struct {
	WatchID    string   `json:"watchID" jsonschema:"identifier of the Watch that deferred the wiring"`
	PR         int      `json:"pr" jsonschema:"pull request number the wiring card belongs to"`
	Title      string   `json:"title" jsonschema:"title of the deferred wiring card"`
	Reason     string   `json:"reason" jsonschema:"why the wiring was deferred; part of the idempotency key together with watchID"`
	OwnerFiles []string `json:"ownerFiles" jsonschema:"files the future owner must change"`
}

// DeferredStatusDeferred is the status of a freshly recorded wiring card.
const DeferredStatusDeferred = "deferred"

// DeferredWiringCard is one deferred-wiring record. CardID is the task-card ID
// WIRING-<pr> and matches the file docs/tasks/WIRING-<pr>.md. The pair
// (WatchID, Reason) is the idempotency key, not the CardID: two independent
// deferrals for the same PR may legitimately produce two records.
type DeferredWiringCard struct {
	CardID     string   `json:"cardID"`
	WatchID    string   `json:"watchID"`
	PR         int      `json:"pr"`
	Title      string   `json:"title"`
	Reason     string   `json:"reason"`
	OwnerFiles []string `json:"ownerFiles"`
	Status     string   `json:"status"`
}

// DeferWriter is the store port of beacon_defer_wiring. DeferWiring stores one
// card and reports whether this call created it; a repeat of the same
// (watchID, reason) reports false without changing the stored card.
// DeferredWirings returns every stored card for the CLI. W12-A supplies the
// durable implementation; until it lands this is the minimal contract.
type DeferWriter interface {
	DeferWiring(ctx context.Context, card DeferredWiringCard) (created bool, err error)
	DeferredWirings(ctx context.Context) ([]DeferredWiringCard, error)
}

// DeferWiringHandler implements beacon_defer_wiring over the DeferWriter port.
// The in-process seen index is a fast path for repeats inside one server
// lifetime; the store is authoritative across restarts.
type DeferWiringHandler struct {
	writer DeferWriter
	mu     sync.Mutex
	seen   map[string]DeferredWiringCard
}

// NewDeferWiringHandler builds a handler. A nil writer is kept so Handle can
// report a persist error instead of pretending the card was stored.
func NewDeferWiringHandler(writer DeferWriter) *DeferWiringHandler {
	return &DeferWiringHandler{writer: writer, seen: make(map[string]DeferredWiringCard)}
}

// Handle adapts Defer to the go-sdk typed tool handler signature.
func (h *DeferWiringHandler) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in DeferWiringInput) (*mcpsdk.CallToolResult, DeferWiringOutput, error) {
	return typedTool(h.Defer, false)(ctx, req, in)
}

// deferWiringKey is the (watchID, reason) idempotency key. The NUL separator
// keeps a Watch id containing a colon or a space from colliding with a reason.
func deferWiringKey(watchID, reason string) string {
	return watchID + "\x00" + reason
}

// Defer validates the input, stores the card once and returns the ready
// markdown. A repeat with the same (watchID, reason) leaves the stored card
// untouched and reports created=false, even when title or ownerFiles differ.
func (h *DeferWiringHandler) Defer(ctx context.Context, in DeferWiringInput) (DeferWiringOutput, error) {
	if in.WatchID == "" || in.PR <= 0 || in.Reason == "" {
		return DeferWiringOutput{}, domain.NewAppError(domain.CodeInvalidInput, "defer_wiring: watchID, pr and reason are required")
	}
	if h == nil || h.writer == nil {
		return DeferWiringOutput{}, domain.NewAppError(domain.CodePersistFailed, "defer_wiring: store port is nil")
	}

	card := DeferredWiringCard{
		CardID:     fmt.Sprintf("WIRING-%d", in.PR),
		WatchID:    in.WatchID,
		PR:         in.PR,
		Title:      in.Title,
		Reason:     in.Reason,
		OwnerFiles: append([]string(nil), in.OwnerFiles...),
		Status:     DeferredStatusDeferred,
	}
	key := deferWiringKey(in.WatchID, in.Reason)

	// Hold the lock across the whole operation, including the writer call.
	// The cache check and the cache write must cover the store write as one
	// critical section. Otherwise two calls with the same key both miss the
	// cache, both reach the writer, and the last one to finish overwrites
	// h.seen while the store keeps the first card. A later call then reads a
	// card from the cache that differs from the stored row. Serializing here
	// makes the cached card the same one the store accepted.
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing, seen := h.seen[key]; seen {
		return deferWiringOutput(existing, false), nil
	}

	created, err := h.writer.DeferWiring(ctx, card)
	if err != nil {
		return DeferWiringOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}
	h.seen[key] = card
	return deferWiringOutput(card, created), nil
}

func deferWiringOutput(card DeferredWiringCard, created bool) DeferWiringOutput {
	return DeferWiringOutput{
		CardID:       card.CardID,
		Created:      created,
		Status:       card.Status,
		CardMarkdown: DeferredCardMarkdown(card),
	}
}

// DeferredCardMarkdown renders the ready-to-commit content of the card file.
// It is pure so a caller can verify the exact bytes before adding them to a PR.
func DeferredCardMarkdown(c DeferredWiringCard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s. %s\n\n", c.CardID, c.Title)
	fmt.Fprintf(&b, "Статус: %s\n", c.Status)
	fmt.Fprintln(&b, "Фаза: P3")
	fmt.Fprintln(&b, "Тип: task")
	fmt.Fprintf(&b, "Владелец файлов: %s\n", strings.Join(c.OwnerFiles, ", "))
	fmt.Fprintln(&b, "Зависимости: нет")
	fmt.Fprintln(&b, "Ссылка: W12-B")
	fmt.Fprintf(&b, "Агент: %s\n", c.WatchID)
	fmt.Fprintln(&b, "Время: нет")
	fmt.Fprintf(&b, "Цель: %s\n\n", c.Title)
	fmt.Fprintf(&b, "Контекст: Wiring для PR #%d отложен из Watch %s. Причина: %s\n\n", c.PR, c.WatchID, c.Reason)
	fmt.Fprintln(&b, "Шаги: Перенести wiring в отдельный PR и обновить контракт инструментов.")
	fmt.Fprintln(&b, "Проверка: Новый инструмент и CLI-команда работают на зелёном CI.")
	fmt.Fprintf(&b, "Приёмка: Wiring для PR #%d выполнен, карточка %s закрыта.\n", c.PR, c.CardID)
	return b.String()
}

// RenderDeferred renders stored cards for `beacon deferred --format`. Table
// and JSON carry the same cards; order is by cardID, watchID and reason so the
// output is stable across processes. The input slice is never mutated.
func RenderDeferred(format string, cards []DeferredWiringCard) (string, error) {
	sorted := append([]DeferredWiringCard(nil), cards...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CardID != sorted[j].CardID {
			return sorted[i].CardID < sorted[j].CardID
		}
		if sorted[i].WatchID != sorted[j].WatchID {
			return sorted[i].WatchID < sorted[j].WatchID
		}
		return sorted[i].Reason < sorted[j].Reason
	})

	var b bytes.Buffer
	switch format {
	case "json":
		if err := json.NewEncoder(&b).Encode(sorted); err != nil {
			return "", err
		}
	case "table":
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "CARDID\tPR\tSTATUS\tWATCHID\tTITLE\tREASON")
		for _, c := range sorted {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", c.CardID, c.PR, c.Status, c.WatchID, c.Title, c.Reason)
		}
		_ = w.Flush()
	default:
		return "", fmt.Errorf("defer_wiring: unknown format %q", format)
	}
	return b.String(), nil
}

// AttachDeferWiring replaces the startup stub with the real tool. It registers
// exactly one beacon_defer_wiring tool; the annotation is additive and
// idempotent, never read-only or destructive.
func AttachDeferWiring(server *mcpsdk.Server, handler *DeferWiringHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools("beacon_defer_wiring")
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_defer_wiring",
		Description: "Record a deferred wiring card for a PR, idempotent by (watchID, reason). Returns cardID, created, status and the ready markdown for docs/tasks/WIRING-<pr>.md.",
		Annotations: attachToolAnnotations(true),
	}, handler.Handle)
}
