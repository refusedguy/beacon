package main

// beacon orchestrator bind records the one orchestrator session of a project
// and prints a digest of every blocked, orphaned and escalated Watch, including
// the ESCALATIONS.md journal read through the diagnostics port. The store owns
// persistence so cmd never names a domain write API; the CLI owns flag parsing
// and rendering. `rebind` stays as a deprecated alias.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// sessionIDEnv carries the orchestrator session from the host harness when
// --session is omitted.
const sessionIDEnv = "BEACON_SESSION_ID"

// orchestratorCommandRunner is the dispatch seam for the orchestrator
// subcommands. RegisterOrchestratorCommand replaces it in tests.
var orchestratorCommandRunner = func(c *CLI, args []string) int { return runOrchestratorCommand(c, args) }

// RegisterOrchestratorCommand installs the orchestrator command implementation.
func RegisterOrchestratorCommand(fn func(*CLI, []string) int) { orchestratorCommandRunner = fn }

// bindStore is the slice of the state store the bind command needs. The
// concrete *store.DB implements it; tests install a recording fake.
type bindStore interface {
	BindOrchestrator(projectKey, sessionID, adapter string, now time.Time) error
	LiveWatches() ([]domain.Watch, error)
	ActiveBlockers() ([]domain.BlockerIncident, error)
}

type bindStoreFactory func(*CLI) (bindStore, func() error, error)

var makeBindStore bindStoreFactory = defaultBindStore

func defaultBindStore(*CLI) (bindStore, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, nil, err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return nil, nil, err
	}
	return db, db.Close, nil
}

// runOrchestratorCommand dispatches `beacon orchestrator bind|rebind`.
func runOrchestratorCommand(c *CLI, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(c.errOut, "beacon orchestrator: ожидается подкоманда bind или rebind\n")
		writeUsage(c.errOut)
		return 2
	}
	switch args[0] {
	case "bind":
		return runOrchestratorBind(c, args[1:])
	case "rebind":
		fmt.Fprintln(c.errOut, "beacon orchestrator rebind устарел, используйте beacon orchestrator bind")
		return runOrchestratorRebind(c, args[1:])
	default:
		fmt.Fprintf(c.errOut, "beacon orchestrator: неизвестная подкоманда %q\n", args[0])
		writeUsage(c.errOut)
		return 2
	}
}

// runOrchestratorBind implements `beacon orchestrator bind [--session <id>]
// [--dry-run]`. An absent --session falls back to BEACON_SESSION_ID and is an
// error when neither is set. The destination must be an OpenCode session id.
func runOrchestratorBind(c *CLI, args []string) int {
	fs := flag.NewFlagSet("orchestrator bind", flag.ContinueOnError)
	session := fs.String("session", "", "sessionID оркестратора проекта")
	dryRun := fs.Bool("dry-run", false, "показать дайджест без записи привязки")
	fs.SetOutput(c.errOut)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		writeUsage(c.errOut)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(c.errOut, "beacon orchestrator bind: лишние аргументы: %v\n", fs.Args())
		writeUsage(c.errOut)
		return 2
	}

	id := strings.TrimSpace(*session)
	if id == "" {
		id = strings.TrimSpace(os.Getenv(sessionIDEnv))
	}
	if id == "" {
		fmt.Fprintf(c.errOut, "beacon orchestrator bind: нужен --session или %s\n", sessionIDEnv)
		return 2
	}
	if err := orchestrator.ValidateWakeTarget(id); err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrator bind: %v\n", err)
		return 2
	}

	s, closeStore, err := makeBindStore(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrator bind: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	projectKey := orchestratorProjectKey()
	if !*dryRun {
		if err := s.BindOrchestrator(projectKey, id, wake.AdapterOpenCode, time.Now().UTC()); err != nil {
			fmt.Fprintf(c.errOut, "beacon orchestrator bind: %v\n", err)
			return 2
		}
	}
	renderBindDigest(c.out, c.errOut, s, id, projectKey, *dryRun)
	return 0
}

// renderBindDigest prints the durable facts a new orchestrator needs: the
// binding, every blocked or orphaned Watch, open blocker incidents and the
// escalation journal. A failed diagnostic read is reported on stderr and never
// hides the facts that were read successfully.
func renderBindDigest(out, errOut io.Writer, s bindStore, session, projectKey string, dryRun bool) {
	if dryRun {
		fmt.Fprintln(out, "РЕЖИМ: просмотр без записи привязки")
	}
	fmt.Fprintf(out, "orchestrator=%s project=%s\n", session, projectKey)

	watches, err := s.LiveWatches()
	if err != nil {
		fmt.Fprintf(errOut, "beacon orchestrator bind: live watches: %v\n", err)
	} else {
		for _, w := range watches {
			if w.Status == domain.StatusBlocked || w.Blocked != nil {
				reason := ""
				if w.Blocked != nil {
					reason = string(w.Blocked.Reason)
				}
				fmt.Fprintf(out, "BLOCKED watch=%s pr=%d repo=%s reason=%s\n", w.ID, w.PR, w.Repo, reason)
			}
			for _, sub := range w.Subscribers {
				if sub.Phase == domain.WorkerPhaseOrphaned {
					fmt.Fprintf(out, "ORPHANED watch=%s pr=%d session=%s\n", w.ID, w.PR, sub.ID)
				}
			}
		}
	}

	blockers, err := s.ActiveBlockers()
	if err != nil {
		fmt.Fprintf(errOut, "beacon orchestrator bind: blockers: %v\n", err)
	} else {
		for _, b := range blockers {
			fmt.Fprintf(out, "BLOCKER id=%s repo=%s checks=%s fixAttempts=%d\n",
				b.ID, b.Repo, strings.Join(b.FailedChecks, ","), b.FixAttempts)
		}
	}

	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(errOut, "beacon orchestrator bind: каталог состояния: %v\n", err)
		return
	}
	lines, err := readEscalations(dir)
	if err != nil {
		fmt.Fprintf(errOut, "beacon orchestrator bind: ESCALATIONS.md: %v\n", err)
		return
	}
	for _, line := range lines {
		fmt.Fprintf(out, "ESCALATED %s\n", line)
	}
}

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

// defaultRebindStore opens the same state store the MCP server uses.
func defaultRebindStore(*CLI) (rebindStore, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, nil, err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return nil, nil, err
	}
	return db, db.Close, nil
}

// rebindWake is the single summary delivery seam. Production uses the delivery
// target installed by the OpenCode wiring; tests replace it to capture the
// text and the addressee.
var rebindWake = func(sessionID, text string) error {
	return callPrompt(deliverTarget, sessionID, text)
}

// runOrchestratorRebind implements the deprecated `beacon orchestrator rebind
// --from <old> --to <new> [--dry-run]` command.
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
