package main

// T5.51/T5.52: checked stop of a registry executor (ADR 0091).
//
// A stop starts with the four-value precondition. The registry must still hold
// the selection the operator decided to stop, and the session must have no
// undelivered obligations. Only then does the command send the terminal effect
// and save the stopped state. A failed or doubtful effect is reported as
// stop_uncertain with the phase and the reason, never as success (ADR 0091).
//
// The effect ports keep a process kill and a database write out of the pure
// decision code. Tests install stand-ins; production uses the DB adapter below.

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// Refusal reasons are stable strings so callers and the CLI can branch on them
// without parsing the human-readable Detail.
const (
	stopRefusalSelectionChanged  = "selection_changed"
	stopRefusalActiveObligations = "active_obligations"
)

// Outcomes of a checked stop. A refusal is a normal answer, an uncertain stop
// is an operational error: the operator cannot tell whether the executor died.
const (
	stopOutcomeRefused   = "stop_refused"
	stopOutcomeStopped   = "stopped"
	stopOutcomeUncertain = "stop_uncertain"
	stopOutcomeError     = "stop_error"
)

// Phases name the step at which a stop became uncertain. A precondition refusal
// has no phase because the stop never started.
const (
	stopPhaseTerminate  = "terminate"
	stopPhaseStateWrite = "state_write"
)

// Reasons for an uncertain stop. The phase says where it happened and the
// reason says what the command saw.
const (
	stopReasonTerminateFailed  = "terminate_failed"
	stopReasonStateWriteFailed = "state_write_failed"
)

// registryStopSelection is the four-value precondition of a checked stop: the
// Watch id and revision, the executor session and the fencing token that guard
// the binding. All four must match before a stop may proceed (ADR 0091).
type registryStopSelection struct {
	WatchID   string
	Revision  uint64
	SessionID string
	Token     string
}

// registryStopStore is the read-only infra port of a checked stop. It reads the
// registry selection from SQLite and counts the session's undelivered
// obligations. Running the check cannot stop an executor or change the registry.
type registryStopStore interface {
	// Selection returns the current registry binding for watchID. found is
	// false when the registry holds no binding for that watch.
	Selection(watchID string) (registryStopSelection, bool, error)
	// ActiveObligations returns the number of undelivered obligations held by
	// the session.
	ActiveObligations(sessionID string) (int, error)
}

// registryStopVerdict is the result of the precondition or of the whole stop.
// A precondition refusal carries a Reason and no StopPhase. An uncertain stop
// carries the StopPhase and the Reason that made the outcome unknown. Outcome
// is empty on an allowed precondition so a caller cannot mistake it for a
// completed stop.
type registryStopVerdict struct {
	Allowed   bool
	Outcome   string
	StopPhase string
	Reason    string
	Detail    string
	Selection registryStopSelection
}

// selectionDiff lists the fields that differ between the stored selection and
// the expected one. The list gives the operator a concrete reason to refresh
// the precondition instead of a bare mismatch.
func selectionDiff(current, want registryStopSelection) []string {
	var diff []string
	if current.WatchID != want.WatchID {
		diff = append(diff, fmt.Sprintf("watchID: want %q, got %q", want.WatchID, current.WatchID))
	}
	if current.Revision != want.Revision {
		diff = append(diff, fmt.Sprintf("revision: want %d, got %d", want.Revision, current.Revision))
	}
	if current.SessionID != want.SessionID {
		diff = append(diff, fmt.Sprintf("sessionID: want %q, got %q", want.SessionID, current.SessionID))
	}
	if current.Token != want.Token {
		diff = append(diff, fmt.Sprintf("token: want %q, got %q", want.Token, current.Token))
	}
	return diff
}

func validateStopSelection(want registryStopSelection) error {
	switch {
	case strings.TrimSpace(want.WatchID) == "":
		return fmt.Errorf("registry stop: expected watchID is required")
	case want.Revision == 0:
		return fmt.Errorf("registry stop: expected revision is required")
	case strings.TrimSpace(want.SessionID) == "":
		return fmt.Errorf("registry stop: expected sessionID is required")
	case strings.TrimSpace(want.Token) == "":
		return fmt.Errorf("registry stop: expected token is required")
	}
	return nil
}

// checkRegistryStop runs the precondition of a checked stop. It reads the
// registry selection through the port and refuses with selection_changed when
// any of the four values differs from the expected ones. If the selection still
// matches, it refuses with active_obligations while the session holds work that
// has not been delivered. Only a matching selection with no active obligations
// allows the caller to proceed to the stop.
func checkRegistryStop(port registryStopStore, want registryStopSelection) (registryStopVerdict, error) {
	if port == nil {
		return registryStopVerdict{}, fmt.Errorf("registry stop: store not connected")
	}
	if err := validateStopSelection(want); err != nil {
		return registryStopVerdict{}, err
	}

	current, found, err := port.Selection(want.WatchID)
	if err != nil {
		return registryStopVerdict{}, fmt.Errorf("registry stop: read selection: %w", err)
	}
	if !found {
		return registryStopVerdict{
			Outcome: stopOutcomeRefused,
			Reason:  stopRefusalSelectionChanged,
			Detail:  fmt.Sprintf("registry has no binding for watch %q", want.WatchID),
		}, nil
	}
	if diff := selectionDiff(current, want); len(diff) > 0 {
		return registryStopVerdict{
			Outcome: stopOutcomeRefused,
			Reason:  stopRefusalSelectionChanged,
			Detail:  "selection changed: " + strings.Join(diff, "; "),
		}, nil
	}

	active, err := port.ActiveObligations(want.SessionID)
	if err != nil {
		return registryStopVerdict{}, fmt.Errorf("registry stop: read obligations: %w", err)
	}
	if active > 0 {
		return registryStopVerdict{
			Outcome: stopOutcomeRefused,
			Reason:  stopRefusalActiveObligations,
			Detail:  fmt.Sprintf("session %q holds %d undelivered obligation(s)", want.SessionID, active),
		}, nil
	}

	return registryStopVerdict{Allowed: true}, nil
}

// registryStopEffector owns the two effects that follow an allowed
// precondition. Terminate is the terminal call on the executor. SaveState is
// the durable record that the stop happened. Keeping them on one port lets a
// test drive both failure orders without a live process or a database.
type registryStopEffector interface {
	Terminate(want registryStopSelection) error
	SaveState(want registryStopSelection) error
}

// registryStopMemoryEffector is the stand-in for a store that only implements
// the read port. It records nothing and reports success, so tests that inject a
// read-only store keep testing the precondition without needing effect methods.
// The production store below implements the real effector.
type registryStopMemoryEffector struct{}

func (registryStopMemoryEffector) Terminate(registryStopSelection) error { return nil }
func (registryStopMemoryEffector) SaveState(registryStopSelection) error { return nil }

// registryStopEffectorFor selects the effect port of an opened store. A store
// that implements registryStopEffector drives the real stop; a read-only
// stand-in gets the in-memory effector.
var registryStopEffectorFor = func(port registryStopStore) registryStopEffector {
	if effector, ok := port.(registryStopEffector); ok {
		return effector
	}
	return registryStopMemoryEffector{}
}

// stopRegistryExecutor runs the whole checked stop. Every call starts with the
// precondition. A refusal returns before the effector sees the selection. A
// terminate error is uncertain because the signal may have reached the process
// before the error surfaced. A state write error after a successful terminate
// is uncertain for the same reason. Only a saved state yields a stopped
// outcome, and the caller prints success only after this verdict returns.
func stopRegistryExecutor(port registryStopStore, effector registryStopEffector, want registryStopSelection) (registryStopVerdict, error) {
	precondition, err := checkRegistryStop(port, want)
	if err != nil {
		return registryStopVerdict{Outcome: stopOutcomeError, Selection: want}, err
	}
	if !precondition.Allowed {
		precondition.Selection = want
		return precondition, nil
	}
	if effector == nil {
		return registryStopVerdict{Outcome: stopOutcomeError, Selection: want},
			fmt.Errorf("registry stop: effect port not connected")
	}

	if err := effector.Terminate(want); err != nil {
		return registryStopVerdict{
			Outcome:   stopOutcomeUncertain,
			StopPhase: stopPhaseTerminate,
			Reason:    stopReasonTerminateFailed,
			Detail:    err.Error(),
			Selection: want,
		}, nil
	}
	if err := effector.SaveState(want); err != nil {
		return registryStopVerdict{
			Outcome:   stopOutcomeUncertain,
			StopPhase: stopPhaseStateWrite,
			Reason:    stopReasonStateWriteFailed,
			Detail:    err.Error(),
			Selection: want,
		}, nil
	}
	return registryStopVerdict{Outcome: stopOutcomeStopped, Selection: want}, nil
}

// registryStopDBStore adapts the existing store reads to registryStopStore.
// Selection uses the latest binding of the watch, so a new revision makes the
// precondition fail. ActiveObligations uses the pending-obligation view, which
// contains only rows with no delivered_at timestamp.
type registryStopDBStore struct {
	db *store.DB
}

func (s registryStopDBStore) Selection(watchID string) (registryStopSelection, bool, error) {
	if s.db == nil {
		return registryStopSelection{}, false, fmt.Errorf("registry stop: database not connected")
	}
	binding, found, err := s.db.LatestWatchBinding(watchID)
	if err != nil || !found {
		return registryStopSelection{}, found, err
	}
	return registryStopSelection{
		WatchID:   binding.WatchID,
		Revision:  binding.Revision,
		SessionID: binding.SubscriberID,
		Token:     strconv.FormatInt(binding.FencingToken, 10),
	}, true, nil
}

func (s registryStopDBStore) ActiveObligations(sessionID string) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("registry stop: database not connected")
	}
	pending, err := s.db.PendingObligations()
	if err != nil {
		return 0, err
	}
	active := 0
	for _, obligation := range pending {
		if obligation.SubscriberID == sessionID {
			active++
		}
	}
	return active, nil
}

// Terminate sends SIGTERM to the executor process. The process id comes from
// the session metadata key "pid" or from a numeric session id. An opaque
// session id without a pid has no local process to signal, so the call errors
// and the stop is reported as uncertain at the terminate phase.
func (s registryStopDBStore) Terminate(want registryStopSelection) error {
	if s.db == nil {
		return fmt.Errorf("registry stop: database not connected")
	}
	pid := 0
	session, found, err := s.db.GetSession(want.SessionID)
	if err != nil {
		return fmt.Errorf("registry stop: read session: %w", err)
	}
	if found {
		raw := strings.TrimSpace(session.Metadata["pid"])
		if raw != "" {
			pid, err = strconv.Atoi(raw)
			if err != nil || pid <= 0 {
				return fmt.Errorf("registry stop: session %q has invalid pid %q", want.SessionID, raw)
			}
		}
	}
	if pid == 0 {
		pid, err = strconv.Atoi(want.SessionID)
		if err != nil || pid <= 0 {
			return fmt.Errorf("registry stop: session %q has no process id", want.SessionID)
		}
	}
	if err := opencode.TerminateServe(pid); err != nil {
		return fmt.Errorf("registry stop: terminate session %q: %w", want.SessionID, err)
	}
	return nil
}

// SaveState marks the executor stopped in the registry. It reads the binding
// again so a selection that changed after the terminate cannot write stale
// state. The status update goes through the same store adapter as registry
// sync, using the stopped status that ADR 0090 defines.
func (s registryStopDBStore) SaveState(want registryStopSelection) error {
	if s.db == nil {
		return fmt.Errorf("registry stop: database not connected")
	}
	binding, found, err := s.db.LatestWatchBinding(want.WatchID)
	if err != nil {
		return fmt.Errorf("registry stop: read binding: %w", err)
	}
	if !found {
		return fmt.Errorf("registry stop: binding for watch %q disappeared after terminate", want.WatchID)
	}
	if binding.Revision != want.Revision || strconv.FormatInt(binding.FencingToken, 10) != want.Token {
		return fmt.Errorf("registry stop: binding changed after terminate")
	}
	if err := (registryDBStore{db: s.db}).SetStatus(binding.ProjectKey, binding.AgentID, registryStatusStopped); err != nil {
		return fmt.Errorf("registry stop: save stopped state: %w", err)
	}
	return nil
}

// registryStopOpenStore opens the read-only store of a checked stop. It is a
// seam so tests can replace the database with a stand-in (ADR 0091).
var registryStopOpenStore = func() (registryStopStore, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, nil, err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return nil, nil, err
	}
	return registryStopDBStore{db: db}, func() error { return db.Close() }, nil
}

// runRegistry dispatches the registry subcommands. T5.56 owns stop and T5.55
// owns sync; the check seam stays in its own card until its dispatch is wired.
func runRegistry(c *CLI, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(c.errOut, "beacon registry: нужна подкоманда (stop|sync)\n")
		writeUsage(c.errOut)
		return 2
	}
	switch args[0] {
	case "stop":
		return runRegistryStop(c, args[1:])
	case "sync":
		return runRegistrySync(c, args[1:])
	default:
		fmt.Fprintf(c.errOut, "beacon registry: неизвестная подкоманда %q\n", args[0])
		writeUsage(c.errOut)
		return 2
	}
}

// runRegistryStop checks the expected selection, runs the two effects and
// hands the verdict to the CLI printer. The four expectation flags are
// mandatory: validateStopSelection rejects a missing watchID, revision,
// sessionID or token before any store is opened. Printing belongs to the
// printer, so a caller cannot report success before the state write.
func runRegistryStop(c *CLI, args []string) int {
	fs := flag.NewFlagSet("registry stop", flag.ContinueOnError)
	watchID := fs.String("watch", "", "expected watchID")
	revision := fs.Uint64("revision", 0, "expected revision")
	sessionID := fs.String("session", "", "expected executor sessionID")
	token := fs.String("token", "", "expected fencing token")
	if !parseFlags("registry stop", args, c.errOut, fs) {
		return 2
	}

	want := registryStopSelection{
		WatchID:   *watchID,
		Revision:  *revision,
		SessionID: *sessionID,
		Token:     *token,
	}
	if err := validateStopSelection(want); err != nil {
		fmt.Fprintf(c.errOut, "beacon registry stop: %v\n", err)
		return 2
	}

	port, closeStore, err := registryStopOpenStore()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry stop: store: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	verdict, err := stopRegistryExecutor(port, registryStopEffectorFor(port), want)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry stop: %v\n", err)
		return 2
	}
	return printRegistryStopVerdict(c, verdict)
}
