package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

func init() {
	catalog = append(catalog, command{
		Name:    "human",
		Summary: "Принять решение оператора по вотче или блокеру",
		Usage:   "beacon human <approve|reject|rework|unblock|pause|resume|cancel> <watchID|blockerID> [--note \"...\"]",
	})
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
}

// humanVerbs is the closed set of operator commands. An unknown verb is a
// usage error, never a request to the server.
var humanVerbs = map[string]bool{
	"approve": true,
	"reject":  true,
	"rework":  true,
	"unblock": true,
	"pause":   true,
	"resume":  true,
	"cancel":  true,
}

// humanRunner delegates to the CLI implementation. RegisterHuman replaces it
// in tests, mirroring RegisterWatch.
var humanRunner = func(c *CLI, args []string) int { return runHuman(c, args) }

// RegisterHuman installs the human command implementation.
func RegisterHuman(fn func(*CLI, []string) int) { humanRunner = fn }

// humanDecider and humanDecisionStore are the two write paths of this command.
// The HTTP path is preferred; the store path is used when no server is
// listening. Both are ports: cmd sends a request, it does not open a socket or
// read a database file itself.
type humanDecider interface {
	Decide(context.Context, httpapi.ClientDecision) (httpapi.ClientDecisionReceipt, error)
}

type humanDecisionStore interface {
	Apply(context.Context, httpapi.ClientDecision) error
}

var (
	humanStateDir   = stateDir
	humanDeciderFor = func(conn httpapi.ClientConn) (humanDecider, error) { return httpapi.NewProjectionClient(conn, nil) }
	humanStoreFor   = func(dir string) humanDecisionStore { return sqliteHumanStore{dir: dir} }
)

// humanDecisionEvent is the durable record appended to the Watch event journal
// when a command is applied locally. Keeping it append-only means every
// operator action is visible even when the verb has no domain transition.
type humanDecisionEvent struct {
	Kind   string    `json:"kind"`
	Verb   string    `json:"verb"`
	Target string    `json:"target"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
}

// sqliteHumanStore applies decisions through the existing store and MCP ports.
// It never uses os.ReadFile or os.WriteFile: the store owns its SQLite file.
type sqliteHumanStore struct {
	dir string
}

// Apply records the decision and then applies the state change the domain
// supports. approve/reject resume a stopped Watch, rework/resume continue the
// parked task, unblock resolves a blocker incident and cancel closes a Watch.
// pause only journals the intent: Beacon has no separate operator pause state,
// so a parked needs_input Watch already represents a pause (ADR 0031).
func (s sqliteHumanStore) Apply(ctx context.Context, req httpapi.ClientDecision) error {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := openProcessStore(s.dir)
	if err != nil {
		return fmt.Errorf("human: store: %w", err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now()
	payload, err := json.Marshal(humanDecisionEvent{
		Kind: "human_decision", Verb: req.Verb, Target: req.Target, Note: req.Note, At: now,
	})
	if err != nil {
		return fmt.Errorf("human: encode decision: %w", err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO events (watch_id, schema_version, payload, created_at)
			VALUES (?, ?, ?, ?)`, req.Target, 1, payload, now.UnixNano())
		return err
	}); err != nil {
		return fmt.Errorf("human: journal: %w", err)
	}

	switch req.Verb {
	case "approve", "reject", "rework", "resume":
		return applyAckDecision(ctx, db, req)
	case "unblock":
		return resolveBlocker(db, req.Target, now)
	case "cancel":
		watcher := mcp.NewCancelWatcher(db, mcp.NewSQLCancelEventLog(db), nil)
		return watcher.Cancel(ctx, req.Target, mcp.CancelScopeAll, "")
	case "pause":
		return nil
	}
	return nil
}

// applyAckDecision resumes a parked Watch through the same ack port the MCP
// tools use. The human note travels as the decision ID so the answer can be
// correlated with the request it closes.
func applyAckDecision(ctx context.Context, db *store.DB, req httpapi.ClientDecision) error {
	obligations, err := db.WatchObligations(req.Target)
	if err != nil {
		return fmt.Errorf("human: obligations: %w", err)
	}
	if len(obligations) == 0 {
		return fmt.Errorf("human: watch %s has no obligation", req.Target)
	}
	in := mcp.AckInput{
		WatchID:      req.Target,
		ObligationID: obligations[0].ID,
		DecisionID:   req.Note,
	}
	handler := mcp.NewAckHandler(db, mcp.NewSQLObligationStamper(db), nil)
	switch req.Verb {
	case "approve":
		_, err = handler.Decide(ctx, in, mcp.DecisionApprove, req.Note)
	case "reject":
		_, err = handler.Decide(ctx, in, mcp.DecisionReject, req.Note)
	case "rework", "resume":
		in.ContinueOf = req.Target
		err = handler.Ack(ctx, in)
	}
	if err != nil {
		return fmt.Errorf("human: %s: %w", req.Verb, err)
	}
	return nil
}

// resolveBlocker accepts either a blockerID or a watchID. In the latter case
// it finds the open incident that names the Watch, so an operator never has to
// look up the incident ID by hand.
func resolveBlocker(db *store.DB, target string, now time.Time) error {
	active, err := db.ActiveBlockers()
	if err != nil {
		return fmt.Errorf("human: blockers: %w", err)
	}
	id := ""
	for _, incident := range active {
		if incident.ID == target {
			id = target
			break
		}
		for _, watchID := range incident.BlockedWatchIDs {
			if watchID == target {
				id = incident.ID
				break
			}
		}
		if id != "" {
			break
		}
	}
	if id == "" {
		return fmt.Errorf("human: blockers: нет открытого блокера для %s", target)
	}
	if _, err := db.ResolveBlocker(id, now); err != nil {
		return fmt.Errorf("human: unblock %s: %w", id, err)
	}
	return nil
}

// runHuman is `beacon human <verb> <target> [--note "..."]`. It posts to the
// server when connection.json names one and falls back to SQLite when there is
// no listener.
func runHuman(c *CLI, args []string) int {
	fs := flag.NewFlagSet("human", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	fs.Usage = func() {}
	note := fs.String("note", "", "пояснение к решению оператора")
	if err := fs.Parse(args); err != nil {
		writeUsage(c.errOut)
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintf(c.errOut, "beacon human: нужны verb и watchID|blockerID\n")
		writeUsage(c.errOut)
		return 2
	}
	verb, target := strings.TrimSpace(fs.Arg(0)), strings.TrimSpace(fs.Arg(1))
	if !humanVerbs[verb] {
		fmt.Fprintf(c.errOut, "beacon human: неизвестное действие %q\n", verb)
		writeUsage(c.errOut)
		return 2
	}
	if target == "" {
		fmt.Fprintf(c.errOut, "beacon human: пустой watchID|blockerID\n")
		return 2
	}

	req := httpapi.ClientDecision{Verb: verb, Target: target, Note: *note}
	ctx := context.Background()
	dir, err := humanStateDir()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon human: state dir: %v\n", err)
		return 2
	}

	conn, connErr := httpapi.LoadClientConn(dir)
	if connErr == nil {
		decider, err := humanDeciderFor(conn)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon human: http client: %v\n", err)
			return 2
		}
		receipt, err := decider.Decide(ctx, req)
		if err == nil {
			printHumanReceipt(c, verb, target, receipt)
			return 0
		}
		if !errors.Is(err, httpapi.ErrNoServer) {
			fmt.Fprintf(c.errOut, "beacon human: %v\n", err)
			return 2
		}
		// No listener: fall through to the local SQLite port.
	} else if !errors.Is(connErr, httpapi.ErrNoConnection) {
		fmt.Fprintf(c.errOut, "beacon human: connection.json: %v\n", connErr)
		return 2
	}

	storePort := humanStoreFor(dir)
	if storePort == nil {
		fmt.Fprintf(c.errOut, "beacon human: SQLite-порт не подключён\n")
		return 2
	}
	if err := storePort.Apply(ctx, req); err != nil {
		fmt.Fprintf(c.errOut, "beacon human: sqlite: %v\n", err)
		return 2
	}
	fmt.Fprintf(c.out, "human: %s %s: applied via sqlite\n", verb, target)
	return 0
}

func printHumanReceipt(c *CLI, verb, target string, receipt httpapi.ClientDecisionReceipt) {
	state := "ok"
	if !receipt.Applied {
		state = "accepted"
	}
	fmt.Fprintf(c.out, "human: %s %s: %s\n", verb, target, state)
	if receipt.Message != "" {
		fmt.Fprintf(c.out, "human: %s\n", receipt.Message)
	}
}
