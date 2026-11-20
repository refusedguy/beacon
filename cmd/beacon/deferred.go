package main

import (
	"context"
	"flag"
	"fmt"
	"sync"

	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// deferredRunner is the dispatch seam, like the other command runners. A
// follow-up wiring change adds `case "deferred"` to CLI.dispatch and the
// command to usageText and the catalog; RegisterDeferred replaces the runner
// in tests without that wiring.
var deferredRunner = func(c *CLI, args []string) int { return runDeferred(c, args) }

// RegisterDeferred installs the deferred command implementation.
func RegisterDeferred(fn func(*CLI, []string) int) { deferredRunner = fn }

// deferredDeps is the single port the command consumes: the same DeferWriter
// the MCP tool writes through. Tests install a fake with SetDeferredDeps.
type deferredDeps struct {
	Writer mcp.DeferWriter
}

type deferredDepsFactory func(*CLI) (deferredDeps, func() error, error)

var makeDeferredDeps deferredDepsFactory = defaultDeferredDeps

// SetDeferredDeps replaces the dependency factory. Passing nil restores the
// production factory.
func SetDeferredDeps(fn deferredDepsFactory) {
	if fn == nil {
		fn = defaultDeferredDeps
	}
	makeDeferredDeps = fn
}

// memoryDeferredStore is the minimal adapter used until W12-A lands a durable
// store implementation. It honours the same idempotency key as the MCP tool,
// (watchID, reason), so a repeat does not replace the first card. Once W12-A
// is available the composition root can hand its store-backed DeferWriter to
// deferredDeps without touching runDeferred.
type memoryDeferredStore struct {
	mu    sync.Mutex
	cards map[string]mcp.DeferredWiringCard
}

var _ mcp.DeferWriter = (*memoryDeferredStore)(nil)

func newMemoryDeferredStore() *memoryDeferredStore {
	return &memoryDeferredStore{cards: make(map[string]mcp.DeferredWiringCard)}
}

func (s *memoryDeferredStore) DeferWiring(_ context.Context, card mcp.DeferredWiringCard) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("beacon deferred: store is nil")
	}
	key := card.WatchID + "\x00" + card.Reason
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cards == nil {
		s.cards = make(map[string]mcp.DeferredWiringCard)
	}
	if _, ok := s.cards[key]; ok {
		return false, nil
	}
	s.cards[key] = card
	return true, nil
}

func (s *memoryDeferredStore) DeferredWirings(_ context.Context) ([]mcp.DeferredWiringCard, error) {
	if s == nil {
		return nil, fmt.Errorf("beacon deferred: store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]mcp.DeferredWiringCard, 0, len(s.cards))
	for _, c := range s.cards {
		out = append(out, c)
	}
	return out, nil
}

func defaultDeferredDeps(_ *CLI) (deferredDeps, func() error, error) {
	return deferredDeps{Writer: newMemoryDeferredStore()}, func() error { return nil }, nil
}

// runDeferred implements `beacon deferred [--format table|json]`. It reads the
// deferred wiring cards through the MCP tool's own store port and renders them
// with the shared pure formatter, so table and JSON carry the same data.
func runDeferred(c *CLI, args []string) int {
	fs := flag.NewFlagSet("deferred", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("deferred", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon deferred: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	deps, closeDeps, err := makeDeferredDeps(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon deferred: %v\n", err)
		return 2
	}
	defer func() { _ = closeDeps() }()

	cards, err := deps.Writer.DeferredWirings(context.Background())
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon deferred: %v\n", err)
		return 2
	}
	rendered, err := mcp.RenderDeferred(*format, cards)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon deferred: %v\n", err)
		return 2
	}
	fmt.Fprint(c.out, rendered)
	return 0
}
