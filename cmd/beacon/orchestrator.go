package main

// beacon orchestrator rebind replaces the orchestrator identity on active
// Watches in place and sends the new orchestrator one summary wake listing the
// Watches it now owns. The store owns the persistence operation so cmd never
// names a domain type (ADR 0005); the CLI owns flag parsing, rendering and the
// single delivery.

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// orchestratorCommandRunner is the dispatch seam for the orchestrator
// subcommands. RegisterOrchestratorCommand replaces it in tests.
var orchestratorCommandRunner = func(c *CLI, args []string) int { return runOrchestratorCommand(c, args) }

// RegisterOrchestratorCommand installs the orchestrator command implementation.
func RegisterOrchestratorCommand(fn func(*CLI, []string) int) { orchestratorCommandRunner = fn }

// rebindStore is the slice of the state store the rebind command needs. The
// concrete *store.DB implements it; tests install a recording fake.
type rebindStore interface {
	RebindOrchestrator(from, to string, dryRun bool) ([]store.RebindTarget, error)
}

type rebindStoreFactory func(*CLI) (rebindStore, func() error, error)

var makeRebindStore rebindStoreFactory = defaultRebindStore

// SetRebindStore replaces the store factory. Passing nil restores production.
func SetRebindStore(fn rebindStoreFactory) {
	if fn == nil {
		fn = defaultRebindStore
	}
	makeRebindStore = fn
}

// defaultRebindStore opens the same state store the MCP server uses. The
// store's RebindOrchestrator method keeps domain types out of this layer.
func defaultRebindStore(*CLI) (rebindStore, func() error, error) {
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		return nil, nil, err
	}
	db, ok := deps.Store.(*store.DB)
	if !ok {
		_ = closeStore()
		return nil, nil, fmt.Errorf("beacon orchestrator rebind: store is not *store.DB")
	}
	return db, closeStore, nil
}

// rebindWake is the single summary delivery seam. Production uses the delivery
// target installed by the OpenCode wiring; tests replace it to capture the
// text and the addressee.
var rebindWake = func(sessionID, text string) error {
	return callPrompt(deliverTarget, sessionID, text)
}

// runOrchestratorCommand dispatches `beacon orchestrator <subcommand>`.
func runOrchestratorCommand(c *CLI, args []string) int {
	if len(args) == 0 || args[0] != "rebind" {
		fmt.Fprintf(c.errOut, "beacon orchestrator: ожидается подкоманда rebind\n")
		writeUsage(c.errOut)
		return 2
	}
	return runOrchestratorRebind(c, args[1:])
}

// runOrchestratorRebind implements `beacon orchestrator rebind --from <old>
// --to <new> [--dry-run]`. The new id must be an OpenCode session id, because
// it becomes the single wake addressee. A dry run reports the Watches that
// would change without writing or waking anyone.
func runOrchestratorRebind(c *CLI, args []string) int {
	fs := flag.NewFlagSet("orchestrator rebind", flag.ContinueOnError)
	from := fs.String("from", "", "текущий orchestrator sessionID")
	to := fs.String("to", "", "новый orchestrator sessionID")
	dryRun := fs.Bool("dry-run", false, "показать затронутые Watch без записи и wake")
	if !parseFlags("orchestrator rebind", args, c.errOut, fs) {
		return 2
	}
	if *from == "" || *to == "" {
		fmt.Fprintf(c.errOut, "beacon orchestrator rebind: нужны --from и --to\n")
		return 2
	}
	if err := orchestrator.ValidateWakeTarget(*to); err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrator rebind: %v\n", err)
		return 2
	}

	s, closeStore, err := makeRebindStore(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrator rebind: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	targets, err := s.RebindOrchestrator(*from, *to, *dryRun)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrator rebind: %v\n", err)
		return 2
	}
	renderRebind(c.out, *from, *to, *dryRun, targets)
	if *dryRun || len(targets) == 0 {
		return 0
	}
	if err := rebindWake(*to, rebindSummary(*from, *to, targets)); err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrator rebind: wake %s: %v\n", *to, err)
		return 2
	}
	return 0
}

// renderRebind prints the affected Watches. The same fields go into the wake
// text, so the new orchestrator reads a list, not one pointer per Watch.
func renderRebind(w io.Writer, from, to string, dryRun bool, targets []store.RebindTarget) {
	if dryRun {
		fmt.Fprintln(w, "РЕЖИМ: просмотр без записи и wake")
	}
	fmt.Fprintf(w, "orchestrator %s -> %s: Watch %d\n", from, to, len(targets))
	for _, t := range targets {
		fmt.Fprintf(w, "watch=%s pr=%d reason=%s\n", t.WatchID, t.PR, t.Reason)
	}
}

// rebindSummary builds the one steering message for the new orchestrator. It
// carries real watch ids, PR numbers and what was replaced on each Watch.
func rebindSummary(from, to string, targets []store.RebindTarget) string {
	var b strings.Builder
	fmt.Fprintf(&b, "beacon orchestrator rebind: %s -> %s, Watch: %d", from, to, len(targets))
	for _, t := range targets {
		fmt.Fprintf(&b, "\nwatch=%s pr=%d reason=%s", t.WatchID, t.PR, t.Reason)
	}
	return b.String()
}
