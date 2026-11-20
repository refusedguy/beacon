package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
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

// deferredStoreDB is the part of *store.DB the adapter uses. Keeping it as an
// interface lets tests drive the adapter with a fake and check the errors it
// returns to callers.
type deferredStoreDB interface {
	DeferWiring(in store.DeferredWiring) (store.DeferredWiring, error)
	ListDeferred(status string) ([]store.DeferredWiring, error)
}

// durableDeferredStore adapts the deferred queue in store.DB to mcp.DeferWriter.
// The write maps an mcp card onto a store row and reads that row back to decide
// whether this call created it. The read maps stored rows back to cards.
type durableDeferredStore struct {
	db deferredStoreDB
}

var _ mcp.DeferWriter = (*durableDeferredStore)(nil)

func newDurableDeferredStore(db deferredStoreDB) *durableDeferredStore {
	return &durableDeferredStore{db: db}
}

// DeferWiring stores one card. The store keeps the first row for a (watchID,
// reason) key. A stored timestamp equal to the one sent here means this call
// inserted the row. Any other timestamp means an earlier call won.
func (s *durableDeferredStore) DeferWiring(_ context.Context, card mcp.DeferredWiringCard) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("beacon deferred: store is nil")
	}
	at := time.Now().UTC().UnixNano()
	stored, err := s.db.DeferWiring(store.DeferredWiring{
		WatchID:    card.WatchID,
		PR:         card.PR,
		Title:      card.Title,
		Reason:     card.Reason,
		OwnerFiles: append([]string(nil), card.OwnerFiles...),
		CreatedAt:  at,
	})
	if err != nil {
		return false, err
	}
	return stored.CreatedAt == at, nil
}

// DeferredWirings returns every stored row as an mcp card. A store row carries
// the lifecycle status (todo, claimed or done), so the CLI shows that status
// rather than the presentation status a card gets when it is first created.
func (s *durableDeferredStore) DeferredWirings(_ context.Context) ([]mcp.DeferredWiringCard, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("beacon deferred: store is nil")
	}
	rows, err := s.db.ListDeferred("")
	if err != nil {
		return nil, err
	}
	cards := make([]mcp.DeferredWiringCard, 0, len(rows))
	for _, row := range rows {
		cards = append(cards, mcp.DeferredWiringCard{
			CardID:     fmt.Sprintf("WIRING-%d", row.PR),
			WatchID:    row.WatchID,
			PR:         row.PR,
			Title:      row.Title,
			Reason:     row.Reason,
			OwnerFiles: append([]string(nil), row.OwnerFiles...),
			Status:     row.Status,
		})
	}
	return cards, nil
}

// defaultDeferredDeps opens the same state store the MCP process uses, so the
// CLI reads the cards beacon_defer_wiring writes. The caller owns the closer.
func defaultDeferredDeps(_ *CLI) (deferredDeps, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return deferredDeps{}, nil, err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return deferredDeps{}, nil, err
	}
	return deferredDeps{Writer: newDurableDeferredStore(db)}, func() error { return db.Close() }, nil
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
