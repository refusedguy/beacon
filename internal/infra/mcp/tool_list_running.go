// Package mcp: beacon_list_running as a pure read of Beacon state.
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// RegistryEntry is one read-only row of the agent registry carried by a
// StateSnapshot. It exposes the three axes named by T5.29: the subscriber role,
// the declared capabilities and the configuration id. ID is the opaque
// executor identifier, never an agent name (ADR 0086). Status is the lifecycle
// state from ADR 0090. No field is sourced from a write path.
type RegistryEntry struct {
	ID           string              `json:"id"`
	Role         domain.Role         `json:"role"`
	Capabilities []domain.Capability `json:"capabilities"`
	Config       string              `json:"config"`
	Status       string              `json:"status,omitempty"`
}

// Registry lifecycle states from ADR 0090. They are local strings because a
// snapshot is a read-only projection: the store owns every transition.
const (
	RegistryActive  = "active"
	RegistryStale   = "stale"
	RegistryStopped = "stopped"
)

// StateSnapshot is the read-only view of Beacon state shared by the two
// observability tools. A SnapshotReader produces it. No field is sourced from
// a write path, so the tools only project what was already persisted.
type StateSnapshot struct {
	// ActiveWatches holds watches in watching or notifying status. Kept for
	// readers built before blocked watches had a dedicated field.
	ActiveWatches []domain.Watch
	// LiveWatches adds blocked watches to ActiveWatches. The wiring layer fills
	// it from the store's LiveWatches so a blocked Watch never drops off the
	// radar. A snapshot without it falls back to ActiveWatches.
	LiveWatches []domain.Watch
	// Sessions carries the disk signals used to decide session liveness
	// without probing processes (ADR 0036 §4). The wiring layer fills it when
	// session artifacts and recorded PIDs are available; a snapshot without
	// sessions keeps the previous behavior.
	Sessions        []SessionLiveness
	PointsThisHour  int
	Budget          int
	PersistSeq      uint64
	LastPersistErr  string
	LoadErrors      []string
	LadderPending   int
	LadderEscalated int
	Version         string
	// Registry is the read-only agent registry projection shared by both
	// observability tools (T5.29, ADR 0094). The wiring layer fills it when
	// registry state is available. A snapshot without entries keeps the exact
	// previous output of both tools.
	Registry []RegistryEntry
}

// RadarWatches returns every Watch the observability tools must show. It
// prefers LiveWatches, which includes blocked watches, and falls back to the
// legacy ActiveWatches field for a reader that has not been upgraded yet.
func (s StateSnapshot) RadarWatches() []domain.Watch {
	if len(s.LiveWatches) > 0 {
		return s.LiveWatches
	}
	return s.ActiveWatches
}

// RegisteredAgents returns the number of registry rows in the snapshot.
func (s StateSnapshot) RegisteredAgents() int { return len(s.Registry) }

// StaleAgents counts rows whose lifecycle state is stale. Stale is a registry
// state and never rewrites a Watch verdict (ADR 0090).
func (s StateSnapshot) StaleAgents() int {
	n := 0
	for _, entry := range s.Registry {
		if entry.Status == RegistryStale {
			n++
		}
	}
	return n
}

// RegistryConfig returns the configuration id shared by every registry row.
// Rows from different configurations yield an empty string: reporting one id
// for a mixed snapshot would hide a config change (ADR 0087). An empty
// registry has no config and also yields an empty string.
func (s StateSnapshot) RegistryConfig() string {
	if len(s.Registry) == 0 {
		return ""
	}
	config := s.Registry[0].Config
	for _, entry := range s.Registry[1:] {
		if entry.Config != config {
			return ""
		}
	}
	return config
}

// FindRegistryEntry looks up one row by its opaque executor id. The bool
// reports whether the row exists.
func (s StateSnapshot) FindRegistryEntry(id string) (RegistryEntry, bool) {
	for _, entry := range s.Registry {
		if entry.ID == id {
			return entry, true
		}
	}
	return RegistryEntry{}, false
}

// StatusIdle is the status beacon_list_running reports for a watch whose agent
// session is gone. It is a projection only: the store keeps the original
// lifecycle status and never receives idle as a domain.Status (ADR 0036 §4).
const StatusIdle = "idle"

// SessionLiveness holds the disk signals used to decide whether an agent
// session is still live (ADR 0036 §4) and whether its task has died (ADR 0042
// §2). SessionID matches a subscriber's Target. Artifacts are paths Beacon
// expects on disk while the session is active. PID is the process recorded
// when the session started. LastActivity and LastHeartbeat are the two signals
// added by ADR 0042 §2; the wiring layer fills them from persisted state, and
// a zero value means the signal is unknown.
type SessionLiveness struct {
	SessionID     string    `json:"sessionID"`
	Artifacts     []string  `json:"artifacts"`
	PID           int       `json:"pid"`
	LastActivity  time.Time `json:"lastActivity"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	// StartTime and BootID are the ADR 0047 §6 owner fields. They add pid-reuse
	// and reboot detection to the T3.86 death signals. Zero values mean the
	// signal is unknown and keep the T3.86 behavior unchanged.
	StartTime uint64 `json:"startTime,omitempty"`
	BootID    string `json:"bootId,omitempty"`
}

// OwnerVerdict is the worktree owner liveness verdict from ADR 0047 §6. It is a
// separate type from domain.Verdict: a worktree owner is not a watch, and an
// unverifiable owner must never be confused with a watch verdict.
type OwnerVerdict string

const (
	OwnerVerdictLive         OwnerVerdict = "live"
	OwnerVerdictDead         OwnerVerdict = "dead"
	OwnerVerdictUnverifiable OwnerVerdict = "unverifiable"
)

// OwnerIdentity holds the three fields that describe a worktree slot owner
// (ADR 0047 §6): pid, start time from field 22 of /proc/<pid>/stat, and boot id.
// Boot id distinguishes a machine reboot from a pid reuse.
type OwnerIdentity struct {
	PID       int    `json:"pid"`
	StartTime uint64 `json:"startTime"`
	BootID    string `json:"bootId"`
}

// OwnerReclaimCode is the dedicated refusal code for a worktree slot reclaim
// attempt (ADR 0047 §6). It is local to this package because the card does not
// extend the domain error-code enum.
type OwnerReclaimCode string

const (
	OwnerReclaimGranted        OwnerReclaimCode = "RECLAIM_GRANTED"
	OwnerReclaimRefusedLive    OwnerReclaimCode = "RECLAIM_REFUSED_LIVE"
	OwnerReclaimRefusedUnknown OwnerReclaimCode = "RECLAIM_REFUSED_UNVERIFIABLE"
)

// SnapshotReader is the single read-only state accessor used by
// beacon_list_running and beacon_stats. The interface deliberately has no
// write method: a tool holding only this dependency cannot mutate state, which
// is non-variant 9 of WORKER-PROMPT §4.2.
type SnapshotReader interface {
	Snapshot() (StateSnapshot, error)
}

// SnapshotFunc adapts a read function to SnapshotReader. The wiring layer can
// pass a closure over the store, the point counter and the ladder reader
// without adding another type to this package.
type SnapshotFunc func() (StateSnapshot, error)

func (f SnapshotFunc) Snapshot() (StateSnapshot, error) { return f() }

// ListRunningFilters narrows the watches returned by beacon_list_running.
// Empty fields are ignored, so an empty filter selects every running watch.
type ListRunningFilters struct {
	Verdict string `json:"verdict,omitempty" jsonschema:"only watches with this verdict"`
	Status  string `json:"status,omitempty" jsonschema:"only watches with this status"`
}

func (f *ListRunningFilters) matches(w domain.Watch) bool {
	if f.Verdict != "" && string(w.Verdict) != f.Verdict {
		return false
	}
	if f.Status != "" && string(w.Status) != f.Status {
		return false
	}
	return true
}

// responseLimits is a pair of ceilings on one read tool's output. The byte
// ceiling counts the encoded structured response, which is what an MCP client
// loads into its context.
type responseLimits struct {
	records int
	bytes   int
}

// checkResponseLimits refuses an oversized response. The error is a
// LIMIT_EXCEEDED tool error whose text names the ceiling and tells the caller
// how to ask for less. It carries no clock value or run data, so the negative
// control is reproducible.
func checkResponseLimits(tool, recordName string, records int, limits responseLimits, narrow string, out any) error {
	if records > limits.records {
		return domain.NewAppError(domain.CodeLimitExceeded, fmt.Sprintf(
			"%s: response exceeds the %d-%s limit; %s", tool, limits.records, recordName, narrow))
	}
	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("%s: encode response for size check: %w", tool, err)
	}
	if len(data) > limits.bytes {
		return domain.NewAppError(domain.CodeLimitExceeded, fmt.Sprintf(
			"%s: response exceeds the %d-byte limit; %s", tool, limits.bytes, narrow))
	}
	return nil
}

// The ceilings bound the watches and events in one list_running response.
const (
	listRunningMaxRecords = 200
	listRunningMaxBytes   = 256 * 1024
)

const listRunningNarrowHint = "narrow the selection with filters.verdict or filters.status, then page with cursor"

// ListRunningInput is the input of beacon_list_running. Filters and Cursor are
// optional: an empty Cursor selects every current event (ADR 0025).
type ListRunningInput struct {
	Filters *ListRunningFilters `json:"filters,omitempty" jsonschema:"optional filter on running watches"`
	Cursor  string              `json:"cursor,omitempty" jsonschema:"opaque cursor from a previous response"`
}

// ListRunningHandler implements beacon_list_running over the read-only
// snapshot port. It holds no writer, so the handler cannot mutate Beacon state.
// Alongside the reader it keeps only an event cursor and the terminal verdict
// (ADR 0025): no stream history is retained.
type ListRunningHandler struct {
	reader SnapshotReader

	mu              sync.Mutex
	cursor          uint64
	terminalStatus  string
	terminalVerdict string
}

// NewListRunningHandler builds the handler. Pass handler.Handle to
// mcpsdk.AddTool when wiring the tool into a server.
func NewListRunningHandler(reader SnapshotReader) *ListRunningHandler {
	return &ListRunningHandler{reader: reader}
}

// encodeEventCursor turns a monotonic event position into an opaque string
// suitable for Last-Event-ID. Clients never have to interpret it. The opt-in
// t3_thread_read tool reuses this base64-of-decimal shape for its
// earlierPosition cursor, so both read tools speak the same continuation form.
func encodeEventCursor(seq uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatUint(seq, 10)))
}

// decodeEventCursor reverses encodeEventCursor. An empty cursor means "from
// the beginning" and decodes to zero; a malformed value is an input error.
func decodeEventCursor(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor: %w", err)
	}
	seq, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor: %w", err)
	}
	return seq, nil
}

const (
	// activityStaleAfter is how old the last recorded activity may be before
	// the activity signal indicates a stalled task (ADR 0042 §2).
	activityStaleAfter = 5 * time.Minute
	// heartbeatStaleAfter is how old the last heartbeat may be before the
	// heartbeat signal indicates a stalled task (ADR 0042 §2). A heartbeat is
	// expected more often than real activity, hence the shorter bound.
	heartbeatStaleAfter = 2 * time.Minute
)

// currentTime is the clock used to age the activity and heartbeat signals.
// It is a variable so tests can freeze time; production uses time.Now.
var currentTime = time.Now

// processAlive checks one recorded PID with signal 0. The kernel reports
// whether the process exists without delivering a signal, so this neither
// enumerates processes nor starts ps (ADR 0036 §4). EPERM still means the
// process is alive but owned by another user. The variable is replaceable in
// tests; production uses the same syscall the taskstore lock uses.
var processAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// parseProcStatStartTime extracts field 22 (starttime, in clock ticks) from the
// contents of /proc/<pid>/stat. Field 2 (comm) is parenthesized and may itself
// contain spaces or parentheses, so the parse starts after the final ')'.
func parseProcStatStartTime(stat string) (uint64, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[end+1:])
	// Field 22 is the 20th field after the parenthesized comm (field 2).
	if len(fields) < 20 {
		return 0, false
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return start, true
}

// readProcStartTime returns field 22 of /proc/<pid>/stat. It is a variable so
// tests can substitute a fixed value; production reads the kernel table.
var readProcStartTime = func(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	start, ok := parseProcStatStartTime(string(data))
	if !ok {
		return 0
	}
	return start
}

// readBootID returns the machine boot id. It is a variable so tests can
// substitute a fixed value; production reads the kernel's boot_id.
var readBootID = func() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ownerVerdict compares a recorded owner identity with the observed one and
// returns the ADR 0047 §6 liveness verdict. Incomplete data on either side
// yields the separate unverifiable verdict. A boot id change means the machine
// rebooted, so the recorded pid cannot still be the same process; a start time
// change under the same boot means the pid was reused.
func ownerVerdict(recorded, observed OwnerIdentity, pidAlive bool) OwnerVerdict {
	if recorded.PID <= 0 || recorded.StartTime == 0 || recorded.BootID == "" {
		return OwnerVerdictUnverifiable
	}
	if observed.StartTime == 0 || observed.BootID == "" {
		return OwnerVerdictUnverifiable
	}
	if recorded.BootID != observed.BootID {
		return OwnerVerdictDead
	}
	if !pidAlive {
		return OwnerVerdictDead
	}
	if recorded.StartTime != observed.StartTime {
		return OwnerVerdictDead
	}
	return OwnerVerdictLive
}

// ownerIsLive treats unverifiable as a live owner: the slot is not taken when
// identity data is missing (ADR 0047 §6). Only a confirmed dead owner frees it.
func ownerIsLive(v OwnerVerdict) bool {
	return v != OwnerVerdictDead
}

// ownerReclaimCode maps an owner verdict to the reclaim outcome. Only a dead
// owner frees the slot. An unverifiable owner gets its own refusal code so a
// caller can tell "unknown" apart from "confirmed live" (ADR 0047 §6).
func ownerReclaimCode(v OwnerVerdict) OwnerReclaimCode {
	switch v {
	case OwnerVerdictDead:
		return OwnerReclaimGranted
	case OwnerVerdictUnverifiable:
		return OwnerReclaimRefusedUnknown
	default:
		return OwnerReclaimRefusedLive
	}
}

// sessionOwnerVerdict evaluates a session's recorded owner identity against the
// current machine. Without all three ADR 0047 §6 fields it returns
// unverifiable, which callers treat as live, so T3.86 keeps its old behavior.
func sessionOwnerVerdict(s SessionLiveness) OwnerVerdict {
	if s.PID <= 0 || s.StartTime == 0 || s.BootID == "" {
		return OwnerVerdictUnverifiable
	}
	recorded := OwnerIdentity{PID: s.PID, StartTime: s.StartTime, BootID: s.BootID}
	observed := OwnerIdentity{
		PID:       s.PID,
		StartTime: readProcStartTime(s.PID),
		BootID:    readBootID(),
	}
	return ownerVerdict(recorded, observed, processAlive(s.PID))
}

// artifactsPresent reports whether every expected artifact path is a regular
// file on disk. An empty list adds no requirement.
func artifactsPresent(paths []string) bool {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

// sessionDead is the compound death detector from ADR 0042 §2. It reads all
// three signals together and needs a recorded PID to run. A dead PID is the
// quick sign inherited from T3.44. When the PID is live, a task that has both
// stalled activity and a stale heartbeat is dead too: a live PID alone does
// not prove the task is working (ADR 0042 §2). Unknown time signals never
// contribute a death vote, so a snapshot without them keeps the T3.44
// behavior.
func sessionDead(s SessionLiveness) bool {
	// Without a recorded PID the compound detector has only two of its three
	// signals, which is not enough to declare the task dead.
	if s.PID == 0 {
		return false
	}
	if !processAlive(s.PID) {
		return true
	}
	// ADR 0047 §6: the boot id and start time add reboot and pid-reuse
	// detection to T3.86. An unverifiable owner is treated as live, so this
	// only fires when all three identity fields are present and disagree.
	if sessionOwnerVerdict(s) == OwnerVerdictDead {
		return true
	}
	if s.LastActivity.IsZero() || s.LastHeartbeat.IsZero() {
		return false
	}
	now := currentTime()
	return now.Sub(s.LastActivity) > activityStaleAfter &&
		now.Sub(s.LastHeartbeat) > heartbeatStaleAfter
}

// sessionLive combines the liveness signals from ADR 0036 §4 and the compound
// death detector from ADR 0042 §2. A session the detector calls dead is idle at
// once. Otherwise the artifact files decide. A session with no signals at all
// is unknown and is left alone, so callers that supply no liveness data keep
// the previous behavior.
func sessionLive(s SessionLiveness) bool {
	if s.PID == 0 && len(s.Artifacts) == 0 && s.LastActivity.IsZero() && s.LastHeartbeat.IsZero() {
		return true
	}
	if sessionDead(s) {
		return false
	}
	return artifactsPresent(s.Artifacts)
}

// effectiveStatus projects a stored watch status into the status Beacon
// reports. A closed watch stays closed. Otherwise, if any subscriber belongs
// to a session that is no longer live, the watch is idle (ADR 0036 §4, ADR
// 0042 §2).
func effectiveStatus(w domain.Watch, sessions map[string]SessionLiveness) domain.Status {
	if w.Status == domain.StatusClosed {
		return w.Status
	}
	for _, sub := range w.Subscribers {
		s, ok := sessions[sub.Target]
		if ok && !sessionLive(s) {
			return domain.Status(StatusIdle)
		}
	}
	return w.Status
}

// statusRank orders the projected statuses: closed outranks idle, idle
// outranks notifying and notifying outranks watching.
func statusRank(s string) int {
	switch domain.Status(s) {
	case domain.StatusClosed:
		return 3
	case StatusIdle:
		return 2
	case domain.StatusNotifying:
		return 1
	default:
		return 0
	}
}

// aggregateStatus returns the lifecycle status implied by the observed events.
// A closed watch wins over idle, which wins over notifying, which wins over
// watching.
func aggregateStatus(events []WatchEvent) string {
	status := string(domain.StatusWatching)
	for _, ev := range events {
		if statusRank(ev.Status) > statusRank(status) {
			status = ev.Status
		}
	}
	return status
}

// subscriberPhases projects the per-subscriber worker phase of one Watch into
// a machine-readable map. An unset phase is reported as waiting: a subscriber
// with no recorded obligation is not doing work yet, and fail-closed here
// keeps a fresh Watch from disappearing from the radar.
func subscriberPhases(w domain.Watch) map[string]string {
	phases := make(map[string]string, len(w.Subscribers))
	for _, sub := range w.Subscribers {
		phase := sub.Phase
		if !phase.Valid() {
			phase = domain.WorkerPhaseWaiting
		}
		phases[sub.ID] = string(phase)
	}
	return phases
}

// watchOutput copies one domain Watch into the explicit MCP DTO. A nil slice
// or map stays nil so the JSON output keeps the same null values as before.
// Fields the DTO does not declare are dropped, so a new domain field stays out
// of tools/list until someone adds it here and updates the golden schema.
func watchOutput(w domain.Watch) WatchOutput {
	out := WatchOutput{
		ID:       w.ID,
		Repo:     w.Repo,
		PR:       w.PR,
		HeadSHA:  w.HeadSHA,
		BaseRef:  w.BaseRef,
		Issue:    w.Issue,
		Status:   w.Status,
		Verdict:  w.Verdict,
		Revision: w.Revision,
		Merge: MergeInfoOutput{
			Mergeable:        w.Merge.Mergeable,
			MergeStateStatus: w.Merge.MergeStateStatus,
			ReviewDecision:   w.Merge.ReviewDecision,
		},
		Flaky: FlakyConfigOutput{
			Names:      cloneStrings(w.Flaky.Names),
			MaxRetries: w.Flaky.MaxRetries,
		},
		Policy: PolicyOutput{
			RequireUpToDate:  w.Policy.RequireUpToDate,
			IgnoreReviews:    w.Policy.IgnoreReviews,
			MaxWakesPerWatch: w.Policy.MaxWakesPerWatch,
			MaxRebases:       w.Policy.MaxRebases,
		},
		MergePolicy: MergePolicyOutput{
			AutoMerge:       w.MergePolicy.AutoMerge,
			RequireApproval: w.MergePolicy.RequireApproval,
			WaitFor:         cloneStrings(w.MergePolicy.WaitFor),
			WaitForMain:     cloneStrings(w.MergePolicy.WaitForMain),
		},
		OrchestratorID: w.OrchestratorID,
		FixFor:         w.FixFor,
		BlockerID:      w.BlockerID,
		FixBlockerID:   w.FixBlockerID,
		DeadlineAt:     w.DeadlineAt,
		NoChecksSince:  w.NoChecksSince,
		CreatedAt:      w.CreatedAt,
		ClosedAt:       w.ClosedAt,
		CloseReason:    w.CloseReason,
		ConfigID:       w.ConfigID,
	}
	if w.Blocked != nil {
		out.Blocked = &BlockedInfoOutput{
			Reason:       w.Blocked.Reason,
			FailedChecks: cloneStrings(w.Blocked.FailedChecks),
			Since:        w.Blocked.Since,
			BlockedBy:    w.Blocked.BlockedBy,
			FixAttempts:  w.Blocked.FixAttempts,
		}
	}
	if w.Checks != nil {
		out.Checks = make(map[string]CheckOutput, len(w.Checks))
		for name, check := range w.Checks {
			out.Checks[name] = CheckOutput{
				State:      check.State,
				Conclusion: check.Conclusion,
				Required:   check.Required,
				DetailsURL: check.DetailsURL,
			}
		}
	}
	if w.Flaky.Used != nil {
		out.Flaky.Used = make(map[string]int, len(w.Flaky.Used))
		for name, used := range w.Flaky.Used {
			out.Flaky.Used[name] = used
		}
	}
	if w.Subscribers != nil {
		out.Subscribers = make([]SubscriberOutput, 0, len(w.Subscribers))
		for _, sub := range w.Subscribers {
			out.Subscribers = append(out.Subscribers, SubscriberOutput{
				ID:             sub.ID,
				Role:           sub.Role,
				Adapter:        sub.Adapter,
				Target:         sub.Target,
				OrchestratorID: sub.OrchestratorID,
				FixFor:         sub.FixFor,
				Phase:          sub.Phase,
				LastWakeAt:     sub.LastWakeAt,
				LastError:      sub.LastError,
				AddedAt:        sub.AddedAt,
				LastActivity:   sub.LastActivity,
			})
		}
	}
	if w.Depth != nil {
		out.Depth = &DescentOutput{
			Layer:      w.Depth.Layer,
			Hypothesis: w.Depth.Hypothesis,
			Falsifier:  w.Depth.Falsifier,
			Evidence:   w.Depth.Evidence,
			Steps:      w.Depth.Steps,
		}
	}
	return out
}

// cloneStrings keeps nil nil. A fresh Watch emits null for its optional string
// lists, so copying with append would change the JSON from null to an empty
// array.
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

// watchOutputs converts a page of domain watches into DTO rows in order.
func watchOutputs(in []domain.Watch) []WatchOutput {
	out := make([]WatchOutput, 0, len(in))
	for _, w := range in {
		out = append(out, watchOutput(w))
	}
	return out
}

// Handle reads a snapshot and projects it onto counts, a watch array and a
// cursor-delimited event stream. Every counter is derived from the returned
// array, so counts stay consistent with watches for any filter. With an empty
// input cursor the read is stateless and returns every current event; with a
// cursor it returns only events newer than that position (ADR 0025).
func (h *ListRunningHandler) Handle(_ context.Context, _ *mcpsdk.CallToolRequest, in ListRunningInput) (*mcpsdk.CallToolResult, ListRunningOutput, error) {
	from, err := decodeEventCursor(in.Cursor)
	if err != nil {
		return nil, ListRunningOutput{}, domain.NewAppError(domain.CodeInvalidInput, err.Error())
	}

	snap, err := h.reader.Snapshot()
	if err != nil {
		observe.ActorError("beacon_list_running", "", err)
		return nil, ListRunningOutput{}, fmt.Errorf("beacon_list_running: %w", err)
	}

	radar := snap.RadarWatches()
	sessions := make(map[string]SessionLiveness, len(snap.Sessions))
	for _, s := range snap.Sessions {
		sessions[s.SessionID] = s
	}

	watches := make([]domain.Watch, 0, len(radar))
	for _, w := range radar {
		if in.Filters != nil && !in.Filters.matches(w) {
			continue
		}
		w.Status = effectiveStatus(w, sessions)
		watches = append(watches, w)
	}

	byVerdict := make(map[string]int)
	subscribers := 0
	for _, w := range watches {
		byVerdict[string(w.Verdict)]++
		subscribers += len(w.Subscribers)
	}

	// Events are a position-indexed view over the snapshot. Seq is the 1-based
	// position, so a cursor stays opaque to clients yet stable within one
	// stream. No event history is stored between calls.
	events := make([]WatchEvent, 0, len(radar))
	terminalStatus, terminalVerdict := "", ""
	for i, w := range radar {
		ev := WatchEvent{
			Seq:       uint64(i + 1),
			WatchID:   w.ID,
			Status:    string(effectiveStatus(w, sessions)),
			Verdict:   string(w.Verdict),
			BlockerID: w.BlockerID,
			Blocked:   w.Status == domain.StatusBlocked || w.Blocked != nil,
			Phases:    subscriberPhases(w),
		}
		events = append(events, ev)
		if terminalVerdict == "" && w.Status == domain.StatusClosed {
			terminalStatus = string(w.Status)
			terminalVerdict = string(w.Verdict)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Work on local copies of the cursor and terminal verdict. A response
	// refused by the size limits leaves both untouched, so the client can
	// narrow its filters and retry without losing its place in the stream.
	nextStatus, nextVerdict := h.terminalStatus, h.terminalVerdict
	if terminalVerdict != "" {
		nextStatus, nextVerdict = terminalStatus, terminalVerdict
	}

	newEvents := make([]WatchEvent, 0, len(events))
	nextCursor := h.cursor
	if in.Cursor == "" {
		// Stateless full read: every current event, no cursor state consumed.
		newEvents = append(newEvents, events...)
	} else {
		// A cursor call returns only events past the client cursor and past the
		// last delivered position, so a reconnect never replays old events.
		floor := from
		if nextCursor > floor {
			floor = nextCursor
		}
		for _, ev := range events {
			if ev.Seq > floor {
				newEvents = append(newEvents, ev)
			}
		}
		if uint64(len(events)) > nextCursor {
			nextCursor = uint64(len(events))
		}
	}

	out := ListRunningOutput{
		Counts: WatchCounts{
			Watches:     len(watches),
			ByVerdict:   byVerdict,
			Subscribers: subscribers,
		},
		Watches: watchOutputs(watches),
		Status:  aggregateStatus(events),
		Cursor:  encodeEventCursor(uint64(len(events))),
		Events:  newEvents,
	}

	if nextVerdict != "" {
		out.Status = nextStatus
		out.Verdict = nextVerdict
		out.Events = nil
	}

	limits := responseLimits{records: listRunningMaxRecords, bytes: listRunningMaxBytes}
	if err := checkResponseLimits("beacon_list_running", "record", max(len(out.Watches), len(out.Events)), limits, listRunningNarrowHint, out); err != nil {
		return nil, ListRunningOutput{}, err
	}

	// Commit only a response that passed the limits. Remember the cursor and
	// the terminal verdict, never the history.
	h.cursor = nextCursor
	h.terminalStatus, h.terminalVerdict = nextStatus, nextVerdict
	return &mcpsdk.CallToolResult{}, out, nil
}
