package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
		Usage:   "beacon human [--format table|json] <approve|reject|rework|unblock|pause|resume|cancel> <watchID|blockerID> [--note \"...\"]",
	})
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
}

// humanOutcome is a declared outcome of the human gate. The set is closed: an
// answer outside it is a type error, never free text (RS-CMP-06). The gate
// outcomes come from ADR 0031 section 6; unblock, pause and cancel are operator
// additions from ADR 0041.
type humanOutcome string

const (
	humanApprove humanOutcome = "approve"
	humanReject  humanOutcome = "reject"
	humanRework  humanOutcome = "rework"
	humanUnblock humanOutcome = "unblock"
	humanPause   humanOutcome = "pause"
	humanResume  humanOutcome = "resume"
	humanCancel  humanOutcome = "cancel"
)

// humanOutcomes is the closed set of declared gate outcomes. It is both the
// allow-list used when an answer is parsed and the text shown in a type error.
var humanOutcomes = []humanOutcome{
	humanApprove, humanReject, humanRework, humanUnblock,
	humanPause, humanResume, humanCancel,
}

// valid reports whether o is one of the declared outcomes.
func (o humanOutcome) valid() bool {
	for _, declared := range humanOutcomes {
		if o == declared {
			return true
		}
	}
	return false
}

func (o humanOutcome) String() string { return string(o) }

// outcomeNames renders the allow-list in a stable order for error messages.
func outcomeNames() string {
	names := make([]string, 0, len(humanOutcomes))
	for _, o := range humanOutcomes {
		names = append(names, string(o))
	}
	return strings.Join(names, ", ")
}

// humanResponse is the typed answer to a human gate question. It is the
// response schema: cmd parses a raw answer into it and validates every field
// before a write path runs, so text can never stand in for a decision.
type humanResponse struct {
	Outcome humanOutcome `json:"outcome"`
	Target  string       `json:"target"`
	Note    string       `json:"note,omitempty"`
}

// parseHumanResponse validates a raw answer against the response schema. It
// returns a clear error when the outcome type is unknown or a required field is
// missing. Callers must call it before decider.Decide or store.Apply.
func parseHumanResponse(verb, target, note string) (humanResponse, error) {
	outcome := humanOutcome(strings.TrimSpace(verb))
	if !outcome.valid() {
		return humanResponse{}, fmt.Errorf("неизвестный тип ответа %q; допустимые исходы: %s", verb, outcomeNames())
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return humanResponse{}, fmt.Errorf("в ответе пропущено поле watchID|blockerID")
	}
	return humanResponse{Outcome: outcome, Target: target, Note: note}, nil
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
	humanStateDir   = func() (string, error) { return stateDir() }
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

// runHuman is `beacon human [--format table|json] <verb> <target> [--note "..."]`.
// It posts to the server when connection.json names one and falls back to
// SQLite when there is no listener.
func runHuman(c *CLI, args []string) int {
	fs := flag.NewFlagSet("human", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	fs.Usage = func() {}
	format := fs.String("format", "table", "формат вывода: table или json")
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
	if !parseFormatFlag(*format, "beacon human: неизвестный формат %q, ожидается table или json\n", c.errOut, true) {
		return 2
	}
	answer, err := parseHumanResponse(fs.Arg(0), fs.Arg(1), *note)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon human: %v\n", err)
		writeUsage(c.errOut)
		return 2
	}
	verb, target := string(answer.Outcome), answer.Target

	req := httpapi.ClientDecision{Verb: verb, Target: target, Note: answer.Note}
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
			state := "ok"
			if !receipt.Applied {
				state = "accepted"
			}
			if err := printHumanResult(*format, c, humanResult{
				Verb: verb, Target: target, State: state,
				Message: receipt.Message, Transport: "http",
			}); err != nil {
				fmt.Fprintf(c.errOut, "beacon human: %v\n", err)
				return 2
			}
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
	if err := printHumanResult(*format, c, humanResult{
		Verb: verb, Target: target, State: "applied via sqlite", Transport: "sqlite",
	}); err != nil {
		fmt.Fprintf(c.errOut, "beacon human: %v\n", err)
		return 2
	}
	return 0
}

// humanResult is the structured outcome of one operator decision. The table
// rendering keeps the existing prefix; JSON exposes the same fields by name.
type humanResult struct {
	Verb      string `json:"verb"`
	Target    string `json:"target"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	Transport string `json:"transport"`
}

// renderHumanJSON encodes the decision result for a structured caller.
func renderHumanJSON(w io.Writer, r humanResult) error {
	return json.NewEncoder(w).Encode(r)
}

// printHumanResult writes one decision result in the requested format.
func printHumanResult(format string, c *CLI, r humanResult) error {
	if format == "json" {
		return renderHumanJSON(c.out, r)
	}
	fmt.Fprintf(c.out, "human: %s %s: %s\n", r.Verb, r.Target, r.State)
	if r.Message != "" {
		fmt.Fprintf(c.out, "human: %s\n", r.Message)
	}
	return nil
}
