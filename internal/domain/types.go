// Package domain holds the Beacon entities, status enums and port interfaces.
// It imports only the standard library; adapters live in internal/infra.
package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// IsErr reports whether err wraps a value of type T. The IsXxx classifiers
// share this one errors.As call. It lives in domain because the layer matrix
// lets infra import domain but forbids app from importing domain (ADR 0005).
func IsErr[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}

// Status is the lifecycle state of a Watch.
type Status string

const (
	StatusWatching  Status = "watching"
	StatusNotifying Status = "notifying"
	StatusBlocked   Status = "blocked"
	StatusClosed    Status = "closed"
)

// Verdict is the result computed for one Watch revision.
type Verdict string

const (
	VerdictWatching    Verdict = "watching"
	VerdictReady       Verdict = "ready"
	VerdictRebase      Verdict = "rebase"
	VerdictRetry       Verdict = "retry"
	VerdictKeepWorking Verdict = "keep_working"
	VerdictTimeout     Verdict = "timeout"
	VerdictMerged      Verdict = "merged"
	VerdictClosed      Verdict = "closed"
	// VerdictBlocked is carried while a Watch is blocked by failed checks that
	// need outside action. The CI-cycle engine produces it when a failure is
	// already present on the base branch; a block event can also set it.
	VerdictBlocked Verdict = "blocked"
	// VerdictRecheck asks the subscriber to repeat CI evaluation against the
	// current base branch. The merge path produces it when main moved after
	// the batch snapshot, before any write reaches GitHub (ADR 0266).
	VerdictRecheck Verdict = "recheck"
)

// ---- Verdict contract ------------------------------------------------------
//
// Verdict and its reasons are defined once, here. The layer matrix (ADR 0005)
// forbids internal/app from importing internal/domain, so adapters keep small
// mirrors of these strings. Verdicts is the authoritative set. A test in
// internal/app/orchestrator compares every mirror against it, so a value added
// only to a mirror fails CI.
//
// Verdict values and the constant that owns each one:
//
//	watching, ready, rebase, retry, keep_working, timeout, merged, closed,
//	blocked, recheck   (this file)
//	needs_input, parked, accepted   (transition.go)
//	fail, wait, wait_dependency, green_blocked   (this file, below)
//
// Machine-readable reasons carried by blocked or stopped Watches are owned by
// their domain constants and listed here so adapters can find them:
//
//	blocked:not_mine, blocked:resolved   (beacon_ack decisions, ADR 0072)
//	stale_base_after_main_fix            (ReasonStaleBaseAfterMainFix, verdict.go)
//	deadline_exceeded                    (DeadlineReasonExceeded, this file)
//	limit, budget, crash, error          (ParkReason*, transition.go)
//	head_changed, cancelled, merged, closed, accepted   (CloseReason*, transition.go)
//	unknown                              (TombstoneReasonUnknown, this file)

const (
	// VerdictFail is set when a step fails and the Watch is left for the
	// subscriber. The orchestrator writes it after matching a decision.
	VerdictFail Verdict = "fail"
	// VerdictWait is the in-process hold while a dependency stays unresolved.
	// It carries a reason in the Action recorded for the Watch.
	VerdictWait Verdict = "wait"
	// VerdictWaitDependency is the persisted form of VerdictWait, written to
	// the Watch so the next tick re-checks the dependency instead of dropping
	// the Watch.
	VerdictWaitDependency Verdict = "wait_dependency"
	// VerdictGreenBlocked is the "green but blocked" verdict of ADR 0059 §3:
	// CI is green and the PR is ready to ship, but an open blocker holds the
	// run. The run is suspended and frees its execution slot so the scheduler
	// can start another task while it waits (ADR 0072). It is the same value as
	// GreenBlocked, the single definition owned by verdict.go.
	VerdictGreenBlocked Verdict = Verdict(GreenBlocked)
)

// Verdicts returns every verdict the system can emit, in one place. It is the
// source the mirror-equality test reads. Add a new verdict here first, then
// update the app mirrors so the test stays green. The returned slice is a copy.
func Verdicts() []Verdict {
	return []Verdict{
		VerdictWatching, VerdictReady, VerdictRebase, VerdictRetry,
		VerdictKeepWorking, VerdictTimeout, VerdictMerged, VerdictClosed,
		VerdictBlocked, VerdictRecheck,
		VerdictNeedsInput, VerdictParked, VerdictAccepted,
		VerdictFail, VerdictWait, VerdictWaitDependency, VerdictGreenBlocked,
	}
}

// Role is the subscriber role on a Watch.
type Role string

// RoleFixer repairs a blocked Watch, wakes like an executor and adds FixFor,
// the ID of the Watch being repaired. Keeping it separate from RoleExecutor
// lets a fix be counted on its own.
const (
	RoleExecutor     Role = "executor"
	RoleOrchestrator Role = "orchestrator"
	RoleFixer        Role = "fixer"
)

// WorkerPhase is the lifecycle phase of a worker bound to a Watch. It is used
// by the orchestrator to tell a still-live worker from one that needs a new
// wake, and is stored on the subscriber so it survives a process restart.
type WorkerPhase string

const (
	// WorkerPhaseWaiting means the worker has no in-flight obligation.
	WorkerPhaseWaiting WorkerPhase = "waiting"
	// WorkerPhaseInWork means the worker accepted work and is running it.
	WorkerPhaseInWork WorkerPhase = "in_work"
	// WorkerPhaseBlocked means the worker cannot proceed without outside help.
	WorkerPhaseBlocked WorkerPhase = "blocked"
	// WorkerPhaseOrphaned means the worker lost its session and must be replaced.
	WorkerPhaseOrphaned WorkerPhase = "orphaned"
	// WorkerPhaseFinished means the worker completed its task.
	WorkerPhaseFinished WorkerPhase = "finished"
)

// Valid reports whether p is one of the defined worker phases. The zero value
// fails closed, so an uninitialized subscriber is never treated as live work.
func (p WorkerPhase) Valid() bool {
	switch p {
	case WorkerPhaseWaiting, WorkerPhaseInWork, WorkerPhaseBlocked,
		WorkerPhaseOrphaned, WorkerPhaseFinished:
		return true
	default:
		return false
	}
}

// AttemptOutcome is the observed outcome of one delivery attempt.
type AttemptOutcome string

const (
	OutcomePosted          AttemptOutcome = "posted"
	OutcomeTransportFailed AttemptOutcome = "transport-failed"
	OutcomeIndeterminate   AttemptOutcome = "indeterminate"
)

// Check is one status check attached to a Watch.
type Check struct {
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
	Required   bool   `json:"required"`
	DetailsURL string `json:"detailsUrl"`
}

// MergeInfo mirrors the merge fields read from the pull request.
type MergeInfo struct {
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
}

// FlakyConfig holds the flaky check names and their retry budget.
type FlakyConfig struct {
	Names      []string       `json:"names"`
	MaxRetries int            `json:"maxRetries"`
	Used       map[string]int `json:"used"`
}

// DefaultMaxWakesPerWatch bounds the wake attempts for one Watch. After this
// many wakes without green CI, the orchestrator parks the Watch instead of
// spending more wake budget on it.
const DefaultMaxWakesPerWatch = 6

// Policy holds per-watch verification settings. MaxWakesPerWatch uses zero as
// the unset value; WakeCap resolves it to DefaultMaxWakesPerWatch. MaxRebases
// carries the project's rebase budget onto the Watch at registration; zero
// means the project did not set one.
type Policy struct {
	RequireUpToDate  bool `json:"requireUpToDate"`
	IgnoreReviews    bool `json:"ignoreReviews"`
	MaxWakesPerWatch int  `json:"maxWakesPerWatch"`
	MaxRebases       int  `json:"maxRebases,omitempty"`
}

// WakeCap returns the effective wake limit for a Watch. A zero or negative
// setting selects the default, so a legacy policy without the new field gets
// the same limit as a fresh one.
func (p Policy) WakeCap() int {
	if p.MaxWakesPerWatch <= 0 {
		return DefaultMaxWakesPerWatch
	}
	return p.MaxWakesPerWatch
}

// DefaultPolicy returns the verification settings used when a caller supplies
// none. The wake limit is stated explicitly so it cannot drift from the
// constant.
func DefaultPolicy() Policy {
	return Policy{MaxWakesPerWatch: DefaultMaxWakesPerWatch}
}

// ParkIncidentKey is the stable incident id of one park. It groups the
// structured escalation and every subscriber notice under the same Watch and
// reason, so a reader can follow a park without parsing free text.
func ParkIncidentKey(watchID string, reason ParkReason) string {
	return "park:" + watchID + ":" + string(reason)
}

// ParkEscalation is the structured record written when a Watch exhausts its
// wake budget. WatchID and Incident are always present: a bare text line cannot
// be correlated with the Watch that parked, which is why the bridge writes this
// value instead of a sentence. Wakes carries the per-subscriber counts at the
// moment of parking, so a reader can see which party spent the budget.
type ParkEscalation struct {
	WatchID  string         `json:"watchID"`
	Incident string         `json:"incident"`
	Reason   string         `json:"reason"`
	PR       int            `json:"pr"`
	Revision uint64         `json:"revision"`
	Wakes    map[string]int `json:"wakes,omitempty"`
	At       time.Time      `json:"at"`
}

// Line renders the escalation as one JSON line for the journal. An encode
// failure cannot happen for these field types; an empty fallback would drop the
// watchID, so the method returns the minimal record instead.
func (e ParkEscalation) Line() string {
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Sprintf(`{"watchID":%q,"incident":%q}`, e.WatchID, e.Incident)
	}
	return string(raw)
}

// MergePolicy holds the auto-merge settings of a Watch. WaitFor and WaitForMain
// carry opaque condition identifiers (a PR, an issue or a task); the domain
// stores them without resolving them.
type MergePolicy struct {
	AutoMerge       bool     `json:"autoMerge"`
	RequireApproval bool     `json:"requireApproval"`
	WaitFor         []string `json:"waitFor"`
	WaitForMain     []string `json:"waitForMain"`
}

// DefaultMergePolicy returns the policy used when a caller supplies no merge
// settings: auto-merge is on and every condition is empty. Callers that build a
// Watch directly should start from this value instead of the Go zero value,
// because a plain bool cannot express "on" as its zero state.
func DefaultMergePolicy() MergePolicy {
	return MergePolicy{AutoMerge: true}
}

// PermissionMode is the tool-permission mode of one agent session. The set is
// closed and stored as data, so a session can switch mode on the control channel
// without rebuilding the tool handler. The zero value fails closed: an unset
// mode is never read as a permissive default.
//
// PermissionMode and MergePolicy are different axes. MergePolicy decides how a
// Watch merges. PermissionMode decides which tool calls an agent may make. A
// merge setting must never grant tool access, so the two types stay separate.
type PermissionMode string

const (
	PermissionModeDefault           PermissionMode = "default"
	PermissionModeAcceptEdits       PermissionMode = "acceptEdits"
	PermissionModePlan              PermissionMode = "plan"
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
	PermissionModeDontAsk           PermissionMode = "dontAsk"
	PermissionModeAuto              PermissionMode = "auto"
)

// Valid reports whether m is one of the defined modes.
func (m PermissionMode) Valid() bool {
	switch m {
	case PermissionModeDefault, PermissionModeAcceptEdits, PermissionModePlan,
		PermissionModeBypassPermissions, PermissionModeDontAsk, PermissionModeAuto:
		return true
	default:
		return false
	}
}

// PermissionScope is the reach of a rule update. ScopeSession confines an update
// to one session. The settings scopes name rules that outlive a session and are
// stored here as opaque identifiers. A session-scoped update never writes to
// another session's state.
type PermissionScope string

const (
	PermissionScopeSession         PermissionScope = "session"
	PermissionScopeLocalSettings   PermissionScope = "localSettings"
	PermissionScopeProjectSettings PermissionScope = "projectSettings"
	PermissionScopeUserSettings    PermissionScope = "userSettings"
)

// Valid reports whether s is one of the defined scopes.
func (s PermissionScope) Valid() bool {
	switch s {
	case PermissionScopeSession, PermissionScopeLocalSettings,
		PermissionScopeProjectSettings, PermissionScopeUserSettings:
		return true
	default:
		return false
	}
}

// PermissionUpdate is one rule update carried on the control channel. Scope and
// Tools are mandatory. Mode is optional and narrows the rule to a mode.
type PermissionUpdate struct {
	Scope PermissionScope `json:"scope"`
	Tools []string        `json:"tools"`
	Mode  PermissionMode  `json:"mode,omitempty"`
	Deny  bool            `json:"deny,omitempty"`
}

// Validate rejects an incomplete update before any state is written. All or
// nothing is deliberate: a partial rule would grant access the caller never
// asked for. An unknown scope, an empty tool list, an empty tool name or an
// unknown mode each reject the whole update.
func (u PermissionUpdate) Validate() error {
	if !u.Scope.Valid() {
		return fmt.Errorf("permission update: invalid scope %q", u.Scope)
	}
	if len(u.Tools) == 0 {
		return errors.New("permission update: tools must not be empty")
	}
	for _, tool := range u.Tools {
		if tool == "" {
			return errors.New("permission update: tool name must not be empty")
		}
	}
	if u.Mode != "" && !u.Mode.Valid() {
		return fmt.Errorf("permission update: invalid mode %q", u.Mode)
	}
	return nil
}

// SessionPermission is the permission state of one session. SessionID keys the
// state, so a session-scoped update applies to this record alone.
type SessionPermission struct {
	SessionID string             `json:"sessionID"`
	Mode      PermissionMode     `json:"mode"`
	Updates   []PermissionUpdate `json:"updates,omitempty"`
}

// SetPermissionMode is the control command that switches a session's mode during
// the session. No restart is needed: the command is data on the control channel.
type SetPermissionMode struct {
	SessionID string         `json:"sessionID"`
	Mode      PermissionMode `json:"mode"`
}

// Validate rejects a mode switch with no session or an unknown mode.
func (c SetPermissionMode) Validate() error {
	if c.SessionID == "" {
		return errors.New("set permission mode: sessionID is required")
	}
	if !c.Mode.Valid() {
		return fmt.Errorf("set permission mode: invalid mode %q", c.Mode)
	}
	return nil
}

// ApplyMode returns a copy of s with the mode switched. It never mutates the
// caller's state, so a rejected command leaves no partial write.
func (s SessionPermission) ApplyMode(cmd SetPermissionMode) (SessionPermission, error) {
	if err := cmd.Validate(); err != nil {
		return SessionPermission{}, err
	}
	if s.SessionID != "" && s.SessionID != cmd.SessionID {
		return SessionPermission{}, errors.New("set permission mode: sessionID does not match state")
	}
	s.SessionID = cmd.SessionID
	s.Mode = cmd.Mode
	return s, nil
}

// ApplyUpdates validates every update before applying any of them, then returns
// a copy with the updates appended. A session-scoped update stays in the record
// for sessionID, so it cannot reach another session.
func (s SessionPermission) ApplyUpdates(sessionID string, updates []PermissionUpdate) (SessionPermission, error) {
	for _, update := range updates {
		if err := update.Validate(); err != nil {
			return SessionPermission{}, err
		}
	}
	if sessionID == "" {
		return SessionPermission{}, errors.New("permission update: sessionID is required")
	}
	if s.SessionID != "" && s.SessionID != sessionID {
		return SessionPermission{}, errors.New("permission update: sessionID does not match state")
	}
	s.SessionID = sessionID
	if len(updates) > 0 {
		s.Updates = append(append([]PermissionUpdate(nil), s.Updates...), updates...)
	}
	return s, nil
}

// Subscriber is one party waiting for a Watch result. OrchestratorID is the
// optional parent session carried by an executor at registration. It is stored
// as opaque caller data and never selects a wake recipient: blocked wakes keep
// following the subscriber with role orchestrator. FixFor names the blocked
// Watch a fixer repairs. It is stored on the subscriber so the fix can be
// attributed to a specific block. It is empty for every other role.
type Subscriber struct {
	ID             string      `json:"id"`
	Role           Role        `json:"role"`
	Adapter        string      `json:"adapter"`
	Target         string      `json:"target"`
	OrchestratorID string      `json:"orchestratorID,omitempty"`
	FixFor         string      `json:"fixFor,omitempty"`
	Phase          WorkerPhase `json:"phase,omitempty"`
	LastWakeAt     time.Time   `json:"lastWakeAt,omitempty"`
	LastError      string      `json:"lastError,omitempty"`
	AddedAt        time.Time   `json:"addedAt"`
	LastActivity   time.Time   `json:"lastActivity"`
}

// BlockReason is the machine-readable cause of a block. The set is closed so
// no free-text sentence can reach a log line or the UI (BCX-14). Callers that
// persist a block must use one of these codes.
type BlockReason string

const (
	// BlockReasonNotMine marks a Watch blocked by a blocked:not_mine ack.
	BlockReasonNotMine BlockReason = "not_mine"
	// BlockReasonResolved is the code for a confirmed fix. A resolve ack clears
	// the block instead of writing this code, but it stays in the closed set so
	// every downstream consumer can handle the same vocabulary.
	BlockReasonResolved BlockReason = "resolved"
	// BlockReasonBaseChecksFailed marks a Watch blocked by checks that were
	// already red on the base branch.
	BlockReasonBaseChecksFailed BlockReason = "base_checks_failed"
	// BlockReasonRebaseBudgetExhausted marks a Watch blocked after its rebase
	// budget ran out. The budget count itself is carried by FailedChecks.
	BlockReasonRebaseBudgetExhausted BlockReason = "rebase_budget_exhausted"
)

// Valid reports whether r is one of the defined block reasons. The zero value
// fails closed, so a block with an unset reason is never persisted.
func (r BlockReason) Valid() bool {
	switch r {
	case BlockReasonNotMine, BlockReasonResolved,
		BlockReasonBaseChecksFailed, BlockReasonRebaseBudgetExhausted:
		return true
	default:
		return false
	}
}

// BlockedInfo records why a Watch is blocked, which checks failed, when the
// block started and which actor or rule set it. It is stored as JSON on the
// Watch, so a legacy payload without the field decodes to nil and needs no
// schema migration. FixAttempts counts the FIX agents already spawned for this
// block. It lives on the block record, so an unblock that clears Blocked also
// resets the budget and the next block starts from zero.
type BlockedInfo struct {
	Reason       BlockReason `json:"reason"`
	FailedChecks []string    `json:"failedChecks,omitempty"`
	Since        time.Time   `json:"since"`
	BlockedBy    string      `json:"blockedBy"`
	FixAttempts  int         `json:"fixAttempts,omitempty"`
}

// BlockerIncident is one control-plane record for a failed check on the base
// branch. Several Watches can be blocked by the same signature; the incident
// lets the orchestrator fix main once instead of rebasing each Watch in turn.
// FixAttempts and MaxFixAttempts bound the repair attempts for that incident.
// Status is one of BlockerStatusOpen or BlockerStatusResolved.
type BlockerIncident struct {
	ID              string    `json:"id"`
	Repo            string    `json:"repo"`
	BaseRef         string    `json:"baseRef"`
	Signature       string    `json:"signature"`
	FailedChecks    []string  `json:"failedChecks"`
	BlockedWatchIDs []string  `json:"blockedWatchIDs"`
	FixerWatchID    string    `json:"fixerWatchID,omitempty"`
	FixAttempts     int       `json:"fixAttempts"`
	MaxFixAttempts  int       `json:"maxFixAttempts"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
	ResolvedAt      time.Time `json:"resolvedAt"`
}

const (
	// BlockerStatusOpen marks an incident that still needs a fix.
	BlockerStatusOpen = "open"
	// BlockerStatusResolved marks an incident whose base branch is green again.
	BlockerStatusResolved = "resolved"
)

// OrchestratorBinding records which session currently owns the orchestrator
// role for one project. ProjectKey is stable across process restarts, so a
// daemon can find the session to wake without guessing from Watch state.
type OrchestratorBinding struct {
	ProjectKey string    `json:"projectKey"`
	SessionID  string    `json:"sessionID"`
	Adapter    string    `json:"adapter"`
	BoundAt    time.Time `json:"boundAt"`
}

// Watch is the unit of work. Optional fields use the Go zero value when absent.
// OrchestratorID records the parent session supplied at registration. It is
// persisted and rebindable, but it is not a wake address: routing still uses
// the subscriber with role orchestrator. FixFor records the blocked Watch a
// registering fixer repairs; it is empty on a Watch with no fixer subscriber.
type Watch struct {
	ID             string           `json:"id"`
	Repo           string           `json:"repo"`
	PR             int              `json:"pr"`
	HeadSHA        string           `json:"headSHA"`
	BaseRef        string           `json:"baseRef"`
	Issue          string           `json:"issue"`
	Status         Status           `json:"status"`
	Verdict        Verdict          `json:"verdict"`
	Blocked        *BlockedInfo     `json:"blocked,omitempty"`
	Revision       uint64           `json:"revision"`
	Checks         map[string]Check `json:"checks"`
	Merge          MergeInfo        `json:"merge"`
	Flaky          FlakyConfig      `json:"flaky"`
	Policy         Policy           `json:"policy"`
	MergePolicy    MergePolicy      `json:"mergePolicy"`
	Subscribers    []Subscriber     `json:"subscribers"`
	OrchestratorID string           `json:"orchestratorID,omitempty"`
	FixFor         string           `json:"fixFor,omitempty"`
	BlockerID      string           `json:"blockerID,omitempty"`
	FixBlockerID   string           `json:"fixBlockerID,omitempty"`
	DeadlineAt     time.Time        `json:"deadlineAt"`
	NoChecksSince  time.Time        `json:"noChecksSince"`
	// NoChecksTimeout is the window after NoChecksSince without any CI check
	// during which the no-checks timeout rule fires. Zero disables the rule.
	// A head-change successor inherits it with the rest of the deadline state.
	NoChecksTimeout time.Duration `json:"noChecksTimeout,omitempty"`
	CreatedAt       time.Time     `json:"createdAt"`
	ClosedAt        time.Time     `json:"closedAt"`
	CloseReason     string        `json:"closeReason"`
	// ConfigID records the configuration identity from ADR 0087 at creation
	// time. Old Watch records carry no value and read back as the zero string.
	ConfigID string `json:"configId,omitempty"`
	// Depth stores the depth decision for this task (depth.look-below). A nil
	// value means no descent has been decided yet, so old records read back
	// unchanged.
	Depth *Descent `json:"depth,omitempty"`
}

// DeferredToolUse is a tool call a defer decision parks for a Watch. ID is the
// caller's tool-use identifier; Name and Input are the tool to run and its
// arguments. A call is parked, not dispatched: no wake is sent and no agent is
// resumed by the decision itself. The ack receipt returns this value, and the
// ack handler keeps one per Watch, so the caller can read the call after the
// run stops.
type DeferredToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// RecordFixAttempt returns a copy of w with one more FIX agent recorded for
// the current block. A fixer is only spawned for a blocked Watch, so the call
// is a no-op when w has no Blocked record. The caller persists the returned
// Watch. The copy shares no BlockedInfo with w, so later caller mutation of
// the original cannot change the recorded count.
func (w Watch) RecordFixAttempt() Watch {
	if w.Blocked == nil {
		return w
	}
	next := w
	blocked := *w.Blocked
	blocked.FixAttempts++
	next.Blocked = &blocked
	return next
}

// Attempt is one recorded delivery try for an Obligation.
type Attempt struct {
	N       int            `json:"n"`
	At      time.Time      `json:"at"`
	Channel string         `json:"channel"`
	Outcome AttemptOutcome `json:"outcome"`
}

// Obligation is one subscriber's claim on a Watch revision.
// ID is derived from WatchID, Revision and SubscriberID.
type Obligation struct {
	ID              string    `json:"id"`
	WatchID         string    `json:"watchID"`
	Revision        uint64    `json:"revision"`
	SubscriberID    string    `json:"subscriberID"`
	VerdictPointer  Verdict   `json:"verdictPointer"`
	StagedAt        time.Time `json:"stagedAt"`
	Attempts        []Attempt `json:"attempts"`
	DeliveredAt     time.Time `json:"deliveredAt"`
	ClaimedAt       time.Time `json:"claimedAt"`
	ReadAt          time.Time `json:"readAt"`
	EscalatedAt     time.Time `json:"escalatedAt"`
	EscalationLevel int       `json:"escalationLevel"`
	// DeliveryRung is the explicit step of the wake delivery ladder. Rung 1 is
	// the initial pointer, rungs 2 and 3 are its two retries (ADR 0017). Zero
	// means the obligation has not been staged for delivery.
	DeliveryRung int `json:"deliveryRung,omitempty"`
	// DeliveryAttempts counts every wake send for the obligation, including the
	// initial pointer and every failed try closed by a revoke. It can pass the
	// retry cap, which is why an incident needs a single human alert.
	DeliveryAttempts int `json:"deliveryAttempts,omitempty"`
	// NextRetryAt is the instant the next retry is due. It is zero when no
	// retry is armed.
	NextRetryAt time.Time `json:"nextRetryAt,omitempty"`
}

// Pointer is the payload sent through a notification channel. It carries no
// result content, only identifiers and the revision.
type Pointer struct {
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID"`
	Revision     uint64 `json:"revision"`
}

// Handoff is the structured transfer passed from one agent to a successor. The
// five fields from ADR 0032 §1 (T3.25) stay mandatory so the successor reads a
// fixed contract instead of the whole chat history. ADR 0046 §5 adds three
// continuation sections: MustNotRedo names actions the successor must not
// repeat, MustPreserve names state it must keep, and WorkingSet bounds the
// files and facts needed to continue. The sections sit on top of the five
// fields and never replace them.
type Handoff struct {
	Intent           string `json:"intent"`
	Evidence         string `json:"evidence"`
	PatchPlan        string `json:"patch_plan"`
	VerificationPlan string `json:"verification_plan"`
	RiskNotes        string `json:"risk_notes"`
	MustNotRedo      string `json:"must_not_redo"`
	MustPreserve     string `json:"must_preserve"`
	WorkingSet       string `json:"working_set"`
}

// Validate reports whether the handoff is complete. The five T3.25 fields are
// always required. The three ADR 0046 §5 sections follow an all-or-nothing
// rule: a legacy five-field packet declares none of them and stays valid, so
// callers built before T3.107 keep working; a continuation packet that fills
// at least one section must fill all three. Validate returns an INVALID_INPUT
// AppError naming the first empty field.
func (h Handoff) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"intent", h.Intent},
		{"evidence", h.Evidence},
		{"patch_plan", h.PatchPlan},
		{"verification_plan", h.VerificationPlan},
		{"risk_notes", h.RiskNotes},
	} {
		if strings.TrimSpace(field.value) == "" {
			return NewAppError(CodeInvalidInput, fmt.Sprintf("handoff: пустое поле %s", field.name))
		}
	}

	sections := []struct{ name, value string }{
		{"must_not_redo", h.MustNotRedo},
		{"must_preserve", h.MustPreserve},
		{"working_set", h.WorkingSet},
	}
	declared := 0
	for _, section := range sections {
		if strings.TrimSpace(section.value) != "" {
			declared++
		}
	}
	if declared > 0 && declared < len(sections) {
		for _, section := range sections {
			if strings.TrimSpace(section.value) == "" {
				return NewAppError(CodeInvalidInput, fmt.Sprintf("handoff: пустое поле %s", section.name))
			}
		}
	}
	return nil
}

// PRState is the normalized pull request snapshot returned by a CIProvider.
type PRState struct {
	State            string           `json:"state"`
	IsDraft          bool             `json:"isDraft"`
	HeadRefOid       string           `json:"headRefOid"`
	BaseRefName      string           `json:"baseRefName"`
	Mergeable        string           `json:"mergeable"`
	MergeStateStatus string           `json:"mergeStateStatus"`
	ReviewDecision   string           `json:"reviewDecision"`
	Checks           map[string]Check `json:"checks"`
	// BaseHeadSHA is the base branch head seen in the same batch as this PR.
	// cibridge copies it from BaseBranchState.HeadSHA before calling
	// MergePRWithState, so the merge path can detect a base that moved after
	// the snapshot without a signature change on the provider port.
	BaseHeadSHA string `json:"baseHeadSHA,omitempty"`
}

// WakePayload is the minimal pointer payload a verdict source hands to a wake
// adapter. It carries identifiers only, never check bodies or PR text.
// The name differs from the WakePointer kind value in verdict.go.
type WakePayload struct {
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID"`
	Revision     uint64 `json:"revision"`
	Verdict      string `json:"verdict"`
}

// WakeDeliverer is the port a thin harness adapter implements to steer a
// session after a terminal verdict. The core never names a concrete harness.
type WakeDeliverer interface {
	Deliver(ctx context.Context, sessionID string, p WakePayload) error
}

// AdapterWakeDeliverer is the adapter-aware variant of WakeDeliverer. It carries
// the subscriber's adapter name on every call, so a registry can dispatch by
// it. The adapter is an opaque string owned by the subscriber; the domain still
// never names a concrete harness.
type AdapterWakeDeliverer interface {
	Deliver(ctx context.Context, adapter, sessionID string, p WakePayload) error
}

// Clock supplies time to the domain. Adapters implement it in infra.
type Clock interface {
	Now() time.Time
}

// WatchStore persists watches. The domain only names the port; infra implements it.
type WatchStore interface {
	SaveWatch(w Watch) error
	Watch(id string) (Watch, error)
	ActiveWatches() ([]Watch, error)
	AllWatches() ([]Watch, error)
}

// ObligationStore persists subscriber obligations. A single WatchStore and a
// single ObligationStore are assembled into one state store only in the
// composition root; consumers declare the narrow port they need.
type ObligationStore interface {
	SaveObligation(o Obligation) error
	Obligation(id string) (Obligation, error)
	PendingObligations() ([]Obligation, error)
}

// CIProvider reads pull request state from the hosting service.
type CIProvider interface {
	PRState(ctx context.Context, repo string, pr int) (PRState, error)
}

// BaseBranchState is the CI state of the base branch a Watch targets. It is
// read separately from the PR so a failure already present on main can be
// attributed to the base and not to the change under review. ChecksKnown
// reports whether FailedChecks is an observation rather than an unknown state.
type BaseBranchState struct {
	BaseRef      string          `json:"baseRef"`
	HeadSHA      string          `json:"headSHA"`
	FailedChecks map[string]bool `json:"failedChecks"`
	ChecksKnown  bool            `json:"checksKnown"`
}

// PRSnapshot is one batched CI observation: the PR state plus the state of its
// base branch. Keeping both in one value lets EvaluatePR detect mixed blame in
// a single pass.
type PRSnapshot struct {
	PR   PRState         `json:"pr"`
	Base BaseBranchState `json:"base"`
}

// BatchCIProvider reads many Watches in one call. The returned map is keyed by
// Watch.ID; a missing key means that Watch had no snapshot. PointStats reports
// the point budget used so far and the total budget, so a caller can decide
// whether another pinpoint read is affordable.
type BatchCIProvider interface {
	FetchBatch(ctx context.Context, watches []Watch) (map[string]PRSnapshot, error)
	PointStats() (used int, budget int)
}

// Merger merges one pull request. merged reports a confirmed merge, held
// reports that the merge was intentionally withheld and reason carries the
// machine-readable cause when either happened. An error reports a transport or
// provider failure, not a policy decision.
type Merger interface {
	Merge(ctx context.Context, repo string, pr int) (merged bool, held bool, reason string, err error)
}

// BlockerIncidentStore persists blocker incidents and steers their lifecycle.
// GetOrCreateOpenBlocker folds failed checks into one incident and reports
// whether it created a new one. ResolveBlocker returns the Watches it unblocked.
type BlockerIncidentStore interface {
	GetOrCreateOpenBlocker(repo, baseRef string, failedChecks []string, watchID string, now time.Time) (BlockerIncident, bool, error)
	AssignFixer(blockerID, fixerWatchID string) (BlockerIncident, error)
	ResolveBlocker(blockerID string, now time.Time) ([]Watch, error)
	ResolveBlockersByBaseGreen(repo, baseRef string, baseFailed map[string]bool, now time.Time) ([]BlockerIncident, error)
	ActiveBlockers() ([]BlockerIncident, error)
}

// OrchestratorBindingStore persists the session bound to a project.
// BoundOrchestrator reports false when no binding exists. The incident and
// binding ports are assembled into one store only in the composition root.
type OrchestratorBindingStore interface {
	BindOrchestrator(projectKey, sessionID, adapter string, now time.Time) error
	BoundOrchestrator(projectKey string) (OrchestratorBinding, bool, error)
}

// ErrorCode is one of the contract v2 error codes.
type ErrorCode string

const (
	CodeInvalidInput   ErrorCode = "INVALID_INPUT"
	CodeWatchNotFound  ErrorCode = "WATCH_NOT_FOUND"
	CodeWatchExpired   ErrorCode = "WATCH_EXPIRED"
	CodeHeadSHAChanged ErrorCode = "HEAD_SHA_CHANGED"
	CodeRateLimited    ErrorCode = "RATE_LIMITED"
	CodeLimitExceeded  ErrorCode = "LIMIT_EXCEEDED"
	CodeCancelled      ErrorCode = "CANCELLED"
	CodeGone           ErrorCode = "GONE"
	CodePersistFailed  ErrorCode = "PERSIST_FAILED"
)

// AppError is the shared error shape. No Result[T] type is used. cause is not
// exported and carries no JSON tag, so it never appears in the wire response.
type AppError struct {
	Code         ErrorCode `json:"code"`
	Message      string    `json:"message"`
	Status       int       `json:"status"`
	RetryAfterMs *int64    `json:"retryAfterMs,omitempty"`
	NewWatchID   string    `json:"newWatchID,omitempty"`
	cause        error
}

// statusByErrorCode is the canonical code registry. It is the only table that
// maps a domain error code to its HTTP status; boundary packages read it
// through StatusForCode instead of keeping a mirror. REJECT_DUPLICATE is
// registered here too, so a code declared in identity.go has one status.
//
// Two families stay outside this table by design and set their status in their
// own constructor: CI_* codes live in infra/github and travel as
// domain.ErrorCode with a 400 status; RECLAIM_* codes are a separate local type
// in infra/mcp (ADR 0047) and are never passed to StatusForCode.
var statusByErrorCode = map[ErrorCode]int{
	CodeInvalidInput:    400,
	CodeWatchNotFound:   404,
	CodeWatchExpired:    410,
	CodeHeadSHAChanged:  409,
	CodeRateLimited:     429,
	CodeLimitExceeded:   429,
	CodeCancelled:       410,
	CodeGone:            410,
	CodePersistFailed:   503,
	CodeRejectDuplicate: 409,
}

// StatusForCode returns the contract HTTP status for a registered code, or 0
// for a code outside the registry. Callers that must never emit status 0 use
// NewAppError, which supplies a transport fallback for extensions.
func StatusForCode(code ErrorCode) int {
	return statusByErrorCode[code]
}

// KnownErrorCodes lists every code in the canonical registry. The returned
// slice is a copy, so callers cannot mutate the registry through it.
func KnownErrorCodes() []ErrorCode {
	codes := make([]ErrorCode, 0, len(statusByErrorCode))
	for code := range statusByErrorCode {
		codes = append(codes, code)
	}
	return codes
}

// NewAppError builds an AppError with the status defined by the contract.
// Status 0 is not a valid HTTP status, so an unregistered code falls back to
// 500 instead of reaching a caller as an empty response code.
func NewAppError(code ErrorCode, message string) *AppError {
	status := StatusForCode(code)
	if status == 0 {
		status = 500
	}
	return &AppError{Code: code, Message: message, Status: status}
}

// NewAppErrorWithCause builds an AppError that keeps cause reachable through
// Unwrap. Message is independent of cause, so callers can keep the existing
// response text while errors.Is and errors.As still see the original error.
func NewAppErrorWithCause(code ErrorCode, message string, cause error) *AppError {
	e := NewAppError(code, message)
	e.cause = cause
	return e
}

func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the stored cause so callers can match a store error with
// errors.Is or errors.As after it crossed an AppError boundary. A nil receiver
// has no cause.
func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// StreamEvent is one chunk of a Beacon event stream. Seq is a monotonic,
// 1-based position: a client restores chunk order by sorting on Seq and sees a
// gap when a position never arrives. WatchID ties the chunk to a Watch and
// Payload is an opaque preview of the result. The authoritative, complete
// result travels separately in StreamFinal, so losing a chunk never costs the
// client the full answer (ADR 0031 §1, Q 1.2 mcp-partials).
type StreamEvent struct {
	Seq     uint64 `json:"seq"`
	WatchID string `json:"watchID"`
	Payload []byte `json:"payload,omitempty"`
}

// StreamFinal is the terminal event of a Beacon stream. Chunks is the number
// of chunks the producer emitted; Missing lists the 1-based positions the
// client never received; Duplicates counts the extra copies of a position the
// client saw. Events are the delivered chunks in ascending Seq order. Result
// is the complete authoritative result, identical whether or not the chunks
// were delivered, and Streamed reports whether at least one chunk arrived
// (ADR 0031 §1).
type StreamFinal struct {
	Chunks     uint64        `json:"chunks"`
	Missing    []uint64      `json:"missing"`
	Duplicates uint64        `json:"duplicates"`
	Streamed   bool          `json:"streamed"`
	Events     []StreamEvent `json:"events,omitempty"`
	Result     []byte        `json:"result,omitempty"`
}

// defaultReorderWindow is how far ahead of a gap the assembler waits before it
// gives that position up as missing. It mirrors mcp-partials' reorderWindow.
const defaultReorderWindow uint64 = 16

// StreamAssembler restores chunk order on the client. It accepts chunks that
// arrive out of order or more than once, emits them in ascending Seq order and
// remembers which positions were never seen. Finish reports honest missing
// and duplicate counts plus the complete result, so a client that lost chunks
// still ends with the full answer.
type StreamAssembler struct {
	window     uint64
	next       uint64
	pending    map[uint64]StreamEvent
	seen       map[uint64]bool
	duplicates uint64
	missing    []uint64
	ordered    []StreamEvent
}

// NewStreamAssembler builds an assembler over the given reorder window. A
// zero window selects the default, so callers cannot build one that waits
// forever for a gap.
func NewStreamAssembler(window uint64) *StreamAssembler {
	if window == 0 {
		window = defaultReorderWindow
	}
	return &StreamAssembler{
		window:  window,
		next:    1,
		pending: make(map[uint64]StreamEvent),
		seen:    make(map[uint64]bool),
	}
}

// Push records a chunk. Seq zero is ignored because positions start at one.
// Repeating a position is counted as a duplicate and never emitted twice.
func (a *StreamAssembler) Push(ev StreamEvent) {
	if ev.Seq == 0 {
		return
	}
	if a.seen[ev.Seq] {
		a.duplicates++
		return
	}
	a.seen[ev.Seq] = true
	a.pending[ev.Seq] = ev
	a.flush()
}

// flush emits contiguous chunks and, once a gap is further ahead than the
// window, gives up on the skipped positions and continues after them.
func (a *StreamAssembler) flush() {
	for {
		if ev, ok := a.pending[a.next]; ok {
			a.ordered = append(a.ordered, ev)
			delete(a.pending, a.next)
			a.next++
			continue
		}
		if len(a.pending) == 0 {
			return
		}
		minSeq := a.minPending()
		if minSeq > a.next && minSeq-a.next > a.window {
			for s := a.next; s < minSeq; s++ {
				a.missing = append(a.missing, s)
			}
			a.next = minSeq
			continue
		}
		return
	}
}

// minPending returns the smallest buffered position. The caller only calls it
// with a non-empty buffer.
func (a *StreamAssembler) minPending() uint64 {
	var min uint64
	first := true
	for seq := range a.pending {
		if first || seq < min {
			min = seq
			first = false
		}
	}
	return min
}

// OrderedChunks returns the chunks emitted so far in ascending Seq order.
func (a *StreamAssembler) OrderedChunks() []StreamEvent {
	out := make([]StreamEvent, len(a.ordered))
	copy(out, a.ordered)
	return out
}

// Finish closes the stream. expected is the number of chunks the producer
// emitted and result is the complete authoritative result. Every position in
// 1..expected that was never seen is reported missing, buffered chunks are
// returned in ascending order, and the result is carried through unchanged.
func (a *StreamAssembler) Finish(expected uint64, result []byte) StreamFinal {
	a.flush()
	// The producer count can never be smaller than the positions already
	// emitted; take the larger value so accounting stays honest.
	if emitted := a.next - 1; expected < emitted {
		expected = emitted
	}

	reported := make(map[uint64]bool, len(a.missing))
	for _, s := range a.missing {
		reported[s] = true
	}
	for s := a.next; s <= expected; s++ {
		if !a.seen[s] && !reported[s] {
			a.missing = append(a.missing, s)
		}
	}
	slices.Sort(a.missing)

	rest := make([]uint64, 0, len(a.pending))
	for seq := range a.pending {
		rest = append(rest, seq)
	}
	slices.Sort(rest)
	ordered := make([]StreamEvent, 0, len(a.ordered)+len(rest))
	ordered = append(ordered, a.ordered...)
	for _, seq := range rest {
		ordered = append(ordered, a.pending[seq])
	}

	return StreamFinal{
		Chunks:     expected,
		Missing:    a.missing,
		Duplicates: a.duplicates,
		Streamed:   len(ordered) > 0,
		Events:     ordered,
		Result:     result,
	}
}

// ---- ADR 0042 п1 / T3.85: дескриптор задачи с дедлайном ----
//
// Дедлайн это сквозной контекст задачи, а не таймаут одного вызова и не
// бюджет ожидания из ADR 0017. Три свойства задают контракт: дескриптор идёт
// во вложенные вызовы, поэтому потомок не живёт дольше родителя; дескриптор
// сохраняется в хранилище, поэтому абсолютный дедлайн переживает рестарт;
// по истечении дедлайна задача завершается с причиной, а не уходит в вечное
// ожидание. Дедлайн хранится абсолютным instant, а не длительностью: иначе
// перезапуск процесса отсчитывал бы бюджет заново.

// TaskDescriptor is the cross-cutting context of one task. DeadlineAt is an
// absolute instant so a restarted process restores the same limit instead of
// restarting a duration. The descriptor is passed by value into every nested
// call, so a child cannot extend or mutate the parent's deadline.
type TaskDescriptor struct {
	ID         string    `json:"id"`
	DeadlineAt time.Time `json:"deadlineAt"`
}

// DeadlineReasonExceeded is the termination reason recorded when a task hits
// its hard deadline. It is not a poll tick and not a wait budget.
const DeadlineReasonExceeded = "deadline_exceeded"

// NewTaskDescriptor builds a descriptor and rejects an empty id or a zero
// deadline. Validate reports the first problem as INVALID_INPUT.
func NewTaskDescriptor(id string, deadlineAt time.Time) (TaskDescriptor, error) {
	d := TaskDescriptor{ID: strings.TrimSpace(id), DeadlineAt: deadlineAt}
	if err := d.Validate(); err != nil {
		return TaskDescriptor{}, err
	}
	return d, nil
}

// Validate reports whether the descriptor is usable: it needs an id and an
// absolute deadline. It returns INVALID_INPUT naming the first empty field.
func (d TaskDescriptor) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return NewAppError(CodeInvalidInput, "task descriptor: пустое поле id")
	}
	if d.DeadlineAt.IsZero() {
		return NewAppError(CodeInvalidInput, "task descriptor: пустое поле deadlineAt")
	}
	return nil
}

// CheckDeadline checks the hard limit at at. When the deadline passed it
// returns expired=true and the termination reason, so the caller ends the
// task with a cause instead of waiting forever. Before the deadline it
// returns expired=false and an empty reason. A zero deadline sets no limit.
func (d TaskDescriptor) CheckDeadline(at time.Time) (bool, string) {
	if !d.DeadlineAt.IsZero() && !at.Before(d.DeadlineAt) {
		return true, DeadlineReasonExceeded
	}
	return false, ""
}

// DeadlineError builds the AppError for a task terminated on deadline expiry.
// It returns nil while the task still has time. CodeGone (410) says the task
// is gone because its hard limit passed; the message carries the reason.
func DeadlineError(d TaskDescriptor, at time.Time) error {
	expired, reason := d.CheckDeadline(at)
	if !expired {
		return nil
	}
	return NewAppError(CodeGone, fmt.Sprintf("task %s: %s", d.ID, reason))
}

// taskDescriptorKey is private so only this package can attach or read a
// descriptor, keeping the propagation contract in one place.
type taskDescriptorKey struct{}

// WithTaskDescriptor returns a context carrying d. Every nested call receives
// the same descriptor, so none of them can outlive the parent's deadline.
func WithTaskDescriptor(ctx context.Context, d TaskDescriptor) context.Context {
	return context.WithValue(ctx, taskDescriptorKey{}, d)
}

// TaskDescriptorFrom returns the descriptor carried by ctx. ok is false when
// no descriptor was attached to the context chain.
func TaskDescriptorFrom(ctx context.Context) (TaskDescriptor, bool) {
	d, ok := ctx.Value(taskDescriptorKey{}).(TaskDescriptor)
	return d, ok
}

// NestedContext derives a context for a nested call and caps its deadline by
// the parent descriptor's deadline, so the child can never outlive the parent.
// A requested deadline later than the parent's (or a zero one) is clamped to
// the parent's; an earlier one is kept. When ctx carries no descriptor the
// requested deadline is used unchanged, and a zero requested deadline yields a
// plain cancellable context. The caller must call the returned cancel func.
func NestedContext(ctx context.Context, requested time.Time) (context.Context, context.CancelFunc) {
	if d, ok := TaskDescriptorFrom(ctx); ok && !d.DeadlineAt.IsZero() {
		if requested.IsZero() || requested.After(d.DeadlineAt) {
			requested = d.DeadlineAt
		}
	}
	if requested.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, requested)
}

// ---- ADR 265: explicit idempotency protocol for mutating operations ----
//
// A mutating tool call carries an operation_id chosen by the caller. The
// receipt and the effect commit in one transaction, so a crash between them
// cannot leave an effect without a receipt or a receipt without an effect.
// A retry with the same id and the same parameters returns the first result;
// a retry with a different body is a conflict; a retry while the operation is
// still running is in flight; and a retry after an indeterminate outcome is
// outcome_unknown instead of a second execution of the effect. Deduplication
// by JSON-RPC id is deliberately not used.

// OperationID is the caller-supplied idempotency key of one mutating operation
// (ADR 265). It travels as a tool argument or a _meta field, never an HTTP
// header, and is never derived from a JSON-RPC id.
type OperationID string

// OperationState is the lifecycle state recorded on an operation receipt
// (ADR 265). accepted and running are transient, succeeded and failed are
// terminal, and outcome_unknown is the state a caller observes on a tombstone.
type OperationState string

const (
	OperationAccepted       OperationState = "accepted"
	OperationRunning        OperationState = "running"
	OperationSucceeded      OperationState = "succeeded"
	OperationFailed         OperationState = "failed"
	OperationOutcomeUnknown OperationState = "outcome_unknown"
)

// TombstoneReasonUnknown marks a failed receipt whose effect may or may not
// have applied (a timeout or a 5xx). Such a receipt is a tombstone: no later
// retry may run the action again.
const TombstoneReasonUnknown = "unknown"

// Next-action values carried by a receipt so the caller knows how to proceed
// without re-running the effect (ADR 265).
const (
	NextActionPoll         = "poll"
	NextActionDone         = "done"
	NextActionRetry        = "retry"
	NextActionWaitForHuman = "wait_for_human"
)

// OperationReceipt is the durable record of one operation (ADR 265).
// OperationID is the caller's key, State is the lifecycle state, ParamsHash
// binds the receipt to the exact parameters of the first call, NextAction
// tells the caller what to do, Result is the redacted effect result and Reason
// explains a failed or unknown outcome.
type OperationReceipt struct {
	OperationID OperationID    `json:"operationId"`
	State       OperationState `json:"state"`
	ParamsHash  string         `json:"paramsHash"`
	NextAction  string         `json:"nextAction"`
	Result      []byte         `json:"result,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

// IsTombstone reports whether the receipt forbids any further attempt. A
// receipt is a tombstone when its state is outcome_unknown, or when it failed
// with the unknown reason (ADR 265).
func (r OperationReceipt) IsTombstone() bool {
	return r.State == OperationOutcomeUnknown ||
		(r.State == OperationFailed && r.Reason == TombstoneReasonUnknown)
}

// ObservedState returns the state a caller should see. A failed-unknown
// tombstone is reported as outcome_unknown, so a retry never mistakes it for
// a clean failure it could retry.
func (r OperationReceipt) ObservedState() OperationState {
	if r.IsTombstone() {
		return OperationOutcomeUnknown
	}
	return r.State
}

// ErrIndeterminateOutcome marks an error whose effect may or may not have
// applied: a timeout or a 5xx from a downstream service (ADR 265). When an
// effect returns this error the store writes a tombstone instead of a clean
// failure, and no later retry runs the action again.
var ErrIndeterminateOutcome = errors.New("domain: indeterminate operation outcome")

// IsIndeterminate reports whether err marks an indeterminate outcome.
func IsIndeterminate(err error) bool {
	return errors.Is(err, ErrIndeterminateOutcome)
}

// ---- IDX-09: graph readiness marker ----
//
// Agents need one signal that says the task graph exists and still matches the
// card tree. The rule lives in the domain so every tool that reports the marker
// applies the same definition. A missing or stale index must never advertise a
// graph an agent cannot trust.

// GraphStatus is the read-only state of the task graph index. IndexHash is the
// source digest the index was built from; SourceHash is the digest observed in
// the same read. Equal non-empty hashes mean the graph is current.
type GraphStatus struct {
	Built      bool
	IndexHash  string
	SourceHash string
}

// Ready reports the graphReady marker. It is true only when the index exists
// and both hashes are present and equal. An index built from an older card set
// fails the comparison, so a stale graph reads as not ready.
func (g GraphStatus) Ready() bool {
	return g.Built && g.IndexHash != "" && g.IndexHash == g.SourceHash
}

// ---- CSx-08: rate-limit event ----
//
// The call budget reports a state change before a call is rejected. The
// planner needs that early signal to slow new spawns. HTTP 429 stays a separate
// call error (CodeRateLimited); this event only carries budget state.

// RateLimitStatus is the state reported by a rate-limit event. The values
// mirror allowed, allowed_warning and rejected from the agent SDK.
type RateLimitStatus string

const (
	RateLimitAllowed        RateLimitStatus = "allowed"
	RateLimitAllowedWarning RateLimitStatus = "allowed_warning"
	RateLimitRejected       RateLimitStatus = "rejected"
)

// RateLimitEvent is one change in the call budget. Utilization is the used
// share of the window in [0, 1]. ResetsAt is the refill time; the zero value
// means the producer did not supply one. A rejected status is a scheduling
// signal and does not replace the HTTP 429 error path.
type RateLimitEvent struct {
	Status      RateLimitStatus `json:"status"`
	Utilization float64         `json:"utilization"`
	ResetsAt    time.Time       `json:"resets_at"`
}

// IsThrottled reports whether the event asks the caller to hold new spawns.
// Only the warning and rejected states throttle; allowed and unrecognised
// states do not.
func (e RateLimitEvent) IsThrottled() bool {
	return e.Status == RateLimitAllowedWarning || e.Status == RateLimitRejected
}
