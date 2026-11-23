package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// deferredRunner is the dispatch seam, like the other command runners.
// CLI.dispatch routes `beacon deferred` here, and RegisterDeferred replaces
// the runner in tests.
var deferredRunner = func(c *CLI, args []string) int { return runDeferred(c, args) }

// RegisterDeferred installs the deferred command implementation.
func RegisterDeferred(fn func(*CLI, []string) int) { deferredRunner = fn }

// deferredDeps is the single port the command consumes: the same DeferWriter
// the MCP tool writes through.
type deferredDeps struct {
	Writer mcp.DeferWriter
	Done   deferredDoneWriter
}

// deferredDoneWriter marks a Watch's deferred rows done. It is a separate port
// from mcp.DeferWriter so the MCP tool contract does not grow a CLI-only method.
type deferredDoneWriter interface {
	MarkDone(ctx context.Context, watchID string) (int, error)
}

type deferredDepsFactory func(*CLI) (deferredDeps, func() error, error)

var makeDeferredDeps deferredDepsFactory = defaultDeferredDeps

// deferredStoreDB is the part of *store.DB the adapter uses. Keeping it as an
// interface lets tests drive the adapter with a fake and check the errors it
// returns to callers.
type deferredStoreDB interface {
	DeferWiring(in store.DeferredWiring) (store.DeferredWiring, error)
	ListDeferred(status string) ([]store.DeferredWiring, error)
	MarkDeferredDone(watchID, reason string) (store.DeferredWiring, error)
}

// durableDeferredStore adapts the deferred queue in store.DB to mcp.DeferWriter.
// The write maps an mcp card onto a store row and reads that row back to decide
// whether this call created it. The read maps stored rows back to cards.
type durableDeferredStore struct {
	db deferredStoreDB
}

var (
	_ mcp.DeferWriter    = (*durableDeferredStore)(nil)
	_ deferredDoneWriter = (*durableDeferredStore)(nil)
)

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

// MarkDone moves every deferred row of a Watch into the done status and
// returns the number of rows marked. A Watch with no rows is an error so the
// CLI can report an unknown watchID as code 2. The store transition is
// idempotent, so repeating this on an already done Watch changes no row.
func (s *durableDeferredStore) MarkDone(_ context.Context, watchID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("beacon deferred: store is nil")
	}
	if strings.TrimSpace(watchID) == "" {
		return 0, fmt.Errorf("beacon deferred: empty watch id")
	}
	rows, err := s.db.ListDeferred("")
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, row := range rows {
		if row.WatchID != watchID {
			continue
		}
		if _, err := s.db.MarkDeferredDone(row.WatchID, row.Reason); err != nil {
			return marked, err
		}
		marked++
	}
	if marked == 0 {
		return 0, store.ErrDeferredNotFound
	}
	return marked, nil
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
	s := newDurableDeferredStore(db)
	return deferredDeps{Writer: s, Done: s}, func() error { return db.Close() }, nil
}

// runDeferred implements `beacon deferred [--format table|json]` and routes
// `beacon deferred done <watchID>` to the done handler. The list path reads
// the deferred wiring cards through the MCP tool's own store port and renders
// them with the shared pure formatter, so table and JSON carry the same data.
func runDeferred(c *CLI, args []string) int {
	if len(args) > 0 && args[0] == "done" {
		return runDeferredDone(c, args[1:])
	}
	fs := flag.NewFlagSet("deferred", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("deferred", args, c.errOut, fs) {
		return 2
	}
	if !parseFormatFlag(*format, "beacon deferred: неизвестный формат %q, ожидается table или json\n", c.errOut, true) {
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

// runDeferredDone implements `beacon deferred done <watchID>`. It uses the
// same store transition as the merged-Watch path in cmd/beacon/mcp.go, so the
// manual and automatic paths leave the same row status.
func runDeferredDone(c *CLI, args []string) int {
	fs := flag.NewFlagSet("deferred done", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		writeUsage(c.errOut)
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(c.errOut, "beacon deferred done: нужен ровно один watchID")
		writeUsage(c.errOut)
		return 2
	}
	watchID := fs.Arg(0)
	if strings.TrimSpace(watchID) == "" {
		fmt.Fprintln(c.errOut, "beacon deferred done: пустой watchID")
		return 2
	}

	deps, closeDeps, err := makeDeferredDeps(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon deferred done: %v\n", err)
		return 2
	}
	defer func() { _ = closeDeps() }()
	if deps.Done == nil {
		fmt.Fprintln(c.errOut, "beacon deferred done: порт завершения недоступен")
		return 2
	}

	marked, err := deps.Done.MarkDone(context.Background(), watchID)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon deferred done: %v\n", err)
		return 2
	}
	fmt.Fprintf(c.out, "deferred done: %s (%d)\n", watchID, marked)
	return 0
}
