// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_watch_pr: an additive, idempotent registration
// and fan-out tool (WORKER-PROMPT §4.1, ADR 0019). It relies on the domain
// ports and on domain.Fanout for the alias and obligation invariants, and on
// the tool output type and error envelope produced by T2.2.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// watchPRPollAfterMs is the suggested delay before a client reads the
	// result again. It is a hint, not a timeout or a deadline.
	// Polling is a hint for a client that chose to poll, not a heartbeat.
	// Five seconds produced a storm of calls without adding information, so
	// the default is three minutes.
	watchPRPollAfterMs int64 = 180000

	// watchPRSessionIDMetaKey is the opaque session identifier key that T2.3
	// reads from _meta. The value is passed through without parsing; it is
	// never treated as authorization (WORKER-PROMPT §2, invariant 6).
	watchPRSessionIDMetaKey = "ai.opencode/sessionID"

	// sessionIDPrefix is the shape of a real opencode session id. Executors
	// and fixers must be wakeable: a card id such as "<BS.42>" or an empty
	// target is not an address, so those registrations are refused.
	sessionIDPrefix = "ses_"

	// watchConfigDirName and the two file names are the project-level inputs a
	// registration reads when the caller omits flaky or policy limits.
	watchConfigDirName     = ".beacon"
	watchConfigFileName    = "config.yaml"
	watchFlakyFileName     = "flaky.json"
	watchConfigPathEnv     = "BEACON_CONFIG"
	watchDefaultMaxRebases = 3
	watchMaxMaxRebases     = 10
)

// WatchDefaults are the layered values a registration uses when the caller
// omits them. The global config file is read first and the project file
// overrides only the keys it sets, so a project needs no copy of shared values.
type WatchDefaults struct {
	MaxRebases       int
	MaxWakesPerWatch int
	Flaky            *WatchPRFlaky
	// NoChecksTimeout bounds the window after registration without any CI
	// check before the no-checks timeout verdict fires. Zero disables it.
	NoChecksTimeout time.Duration
}

// watchPRDefaultsLoader is the process seam for loading the layered config.
// Tests replace it to keep registration independent of the developer's home
// directory.
var watchPRDefaultsLoader = loadWatchDefaults

// loadWatchDefaults merges the built-in values, the global YAML file and the
// project YAML file, then reads the project's flaky.json. A missing or broken
// file leaves the earlier layer intact: registration never fails because an
// optional config file is malformed.
func loadWatchDefaults(projectRoot string) WatchDefaults {
	defaults := WatchDefaults{
		MaxRebases:       watchDefaultMaxRebases,
		MaxWakesPerWatch: domain.DefaultMaxWakesPerWatch,
	}
	for _, path := range watchConfigPaths(projectRoot) {
		if path != "" {
			applyWatchConfigFile(&defaults, path)
		}
	}
	defaults.Flaky = loadWatchFlaky(projectRoot)
	return defaults
}

// watchConfigPaths returns the global layer followed by the project layer, so
// applying them in order gives the project the last word.
func watchConfigPaths(projectRoot string) []string {
	paths := []string{globalWatchConfigPath()}
	if root := strings.TrimSpace(projectRoot); root != "" {
		paths = append(paths, filepath.Join(root, watchConfigDirName, watchConfigFileName))
	}
	return paths
}

// globalWatchConfigPath resolves the shared layer. BEACON_CONFIG names a file;
// otherwise the file lives under ~/.config/beacon.
func globalWatchConfigPath() string {
	if path := strings.TrimSpace(os.Getenv(watchConfigPathEnv)); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "beacon", watchConfigFileName)
}

// applyWatchConfigFile overlays the known limits from one YAML file. Keys are
// normalized so maxRebases, max_rebases and MAX-REBASES all name the same
// setting. Unknown keys are ignored: the other config consumers own them.
func applyWatchConfigFile(defaults *WatchDefaults, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return
	}
	for key, raw := range doc {
		switch normalizeWatchConfigKey(key) {
		case "maxrebases":
			if n, ok := watchInt(raw); ok && n > 0 && n <= watchMaxMaxRebases {
				defaults.MaxRebases = n
			}
		case "maxwakesperwatch":
			if n, ok := watchInt(raw); ok && n > 0 {
				defaults.MaxWakesPerWatch = n
			}
		case "nocheckstimeout":
			if d, ok := watchDuration(raw); ok && d > 0 {
				defaults.NoChecksTimeout = d
			}
		}
	}
}

func normalizeWatchConfigKey(key string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(
		strings.ToLower(strings.TrimSpace(key)))
}

// watchDuration accepts a YAML duration string such as "30m" or a numeric
// millisecond value. Anything else keeps the previous layer's value.
func watchDuration(raw any) (time.Duration, bool) {
	switch v := raw.(type) {
	case string:
		d, err := time.ParseDuration(strings.TrimSpace(v))
		return d, err == nil && d > 0
	case int:
		return time.Duration(v) * time.Millisecond, v > 0
	case int64:
		return time.Duration(v) * time.Millisecond, v > 0
	case uint64:
		return time.Duration(v) * time.Millisecond, v > 0
	case float64:
		return time.Duration(v) * time.Millisecond, v > 0
	default:
		return 0, false
	}
}

// watchInt accepts a YAML integer or a quoted integer string. Anything else
// keeps the previous layer's value.
func watchInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case uint64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	default:
		return 0, false
	}
}

// loadWatchFlaky reads <root>/.beacon/flaky.json. The file uses `checks` in
// the original contract and `names` in the current input shape; both are
// accepted. A missing file means no project flaky set.
func loadWatchFlaky(projectRoot string) *WatchPRFlaky {
	if strings.TrimSpace(projectRoot) == "" {
		return nil
	}
	path := filepath.Join(projectRoot, watchConfigDirName, watchFlakyFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Checks     []string `json:"checks"`
		Names      []string `json:"names"`
		MaxRetries int      `json:"maxRetries"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	names := doc.Names
	if len(names) == 0 {
		names = doc.Checks
	}
	if len(names) == 0 && doc.MaxRetries == 0 {
		return nil
	}
	return &WatchPRFlaky{Names: names, MaxRetries: doc.MaxRetries}
}

// WatchPRInput is the argument shape of beacon_watch_pr (WORKER-PROMPT §4.1).
// All fields except repo, pr and role are optional in the contract.
type WatchPRInput struct {
	Repo           string           `json:"repo" jsonschema:"repository slug, for example owner/name"`
	PR             int              `json:"pr" jsonschema:"pull request number, must be positive"`
	HeadSHA        string           `json:"headSHA,omitempty" jsonschema:"head commit SHA; resolved from the CI provider when omitted"`
	Issue          string           `json:"issue,omitempty"`
	Role           domain.Role      `json:"role" jsonschema:"subscriber role: executor, orchestrator or fixer"`
	Notify         *WatchPRNotify   `json:"notify,omitempty"`
	Executor       *WatchPRExecutor `json:"executor,omitempty"`
	Flaky          *WatchPRFlaky    `json:"flaky,omitempty"`
	Policy         *WatchPRPolicy   `json:"policy,omitempty"`
	DeadlineMs     *int64           `json:"deadlineMs,omitempty" jsonschema:"watch deadline in milliseconds from now"`
	OrchestratorID string           `json:"orchestratorID,omitempty" jsonschema:"opaque parent session id of the orchestrator; stored on the Watch, never used for wake routing"`
	FixFor         string           `json:"fixFor,omitempty" jsonschema:"watchID of the blocked Watch a fixer repairs; required with role fixer, empty for every other role"`
	FixBlockerID   string           `json:"fixBlockerID,omitempty" jsonschema:"blocker incident id this fixer repairs; derived from fixFor when omitted"`
}

// WatchPRNotify is the optional delivery endpoint of the registering party.
type WatchPRNotify struct {
	Adapter string `json:"adapter"`
	Target  string `json:"target"`
}

// WatchPRExecutor is the optional descriptor of the executor that registers
// the Watch. Every field is opaque caller data: the tool accepts it at the MCP
// boundary and never interprets the agent name, the runtime or the declared
// capabilities (ADR 0088, ADR 0089). An absent descriptor keeps the behaviour
// of calls made before this field existed.
type WatchPRExecutor struct {
	ID           string   `json:"id,omitempty" jsonschema:"opaque executor identifier"`
	Role         string   `json:"role,omitempty" jsonschema:"declared role, carried without interpretation"`
	Runtime      string   `json:"runtime,omitempty" jsonschema:"declared session mode, carried without interpretation"`
	Agent        string   `json:"agent,omitempty" jsonschema:"opaque agent name; the core never interprets it"`
	Capabilities []string `json:"capabilities,omitempty" jsonschema:"declared capabilities, carried without interpretation"`
	ConfigID     string   `json:"configId,omitempty" jsonschema:"configuration identity from ADR 0087"`
}

// WatchPRFlaky is the input form of the flaky-check configuration. It carries
// no per-check usage counters; those belong to the persisted Watch.
type WatchPRFlaky struct {
	Names      []string `json:"names"`
	MaxRetries int      `json:"maxRetries"`
}

// WatchPRPolicy is the input form of the per-watch verification policy.
// Merge is optional: when it is absent the Watch keeps auto-merge enabled, so
// callers written before this field existed keep their behaviour.
type WatchPRPolicy struct {
	RequireUpToDate bool                `json:"requireUpToDate"`
	IgnoreReviews   bool                `json:"ignoreReviews"`
	Merge           *WatchPRMergePolicy `json:"merge,omitempty"`
	// MaxRebases and MaxWakesPerWatch override the layered config when set.
	// Zero means "use the project default".
	MaxRebases       int `json:"maxRebases,omitempty" jsonschema:"rebase budget override; zero uses the project config"`
	MaxWakesPerWatch int `json:"maxWakesPerWatch,omitempty" jsonschema:"wake budget override; zero uses the project config"`
}

// WatchPRMergePolicy is the input form of domain.MergePolicy. AutoMerge is a
// pointer so an omitted field keeps the enabled default while an explicit
// false turns auto-merge off.
type WatchPRMergePolicy struct {
	AutoMerge       *bool    `json:"autoMerge,omitempty"`
	RequireApproval bool     `json:"requireApproval,omitempty"`
	WaitFor         []string `json:"waitFor,omitempty"`
	WaitForMain     []string `json:"waitForMain,omitempty"`
}

// watchPRMergePolicy resolves the optional input into a domain policy. An
// absent merge block, or a block that does not set autoMerge explicitly, keeps
// the default of auto-merge on. Slices are copied so the stored Watch does not
// alias the caller's input.
func watchPRMergePolicy(in *WatchPRPolicy) domain.MergePolicy {
	policy := domain.DefaultMergePolicy()
	if in == nil || in.Merge == nil {
		return policy
	}
	policy.RequireApproval = in.Merge.RequireApproval
	policy.WaitFor = append([]string(nil), in.Merge.WaitFor...)
	policy.WaitForMain = append([]string(nil), in.Merge.WaitForMain...)
	if in.Merge.AutoMerge != nil {
		policy.AutoMerge = *in.Merge.AutoMerge
	}
	return policy
}

// WatchPRRegistrar records one executor registration and its Watch binding in
// the project registry. The SQLite store implements this port, so this layer
// passes only neutral values and imports no sibling infra package (ADR 0005).
// The repo slug is the project key stored on both registry rows.
type WatchPRRegistrar interface {
	RegisterWatchExecutor(projectKey, watchID, subscriberID, role, runtime, adapter, target, configID string, revision uint64) error
}

// WatchPRHandler implements beacon_watch_pr over the domain ports. One handler
// owns a Fanout, which serializes the check-then-act sequence inside one
// process; cross-process exclusion remains the daemon's job (ADR 0021).
type WatchPRHandler struct {
	store     StateStore
	ci        domain.CIProvider
	clock     domain.Clock
	fanout    *domain.Fanout
	blockers  domain.BlockerIncidents
	fixer     domain.FixerBinding
	registrar WatchPRRegistrar

	// projectRoot and defaultsLoader carry the layered config into a Watch.
	// The composition root sets the root; tests replace the loader to run
	// without touching the host's config directory.
	projectRoot    string
	defaultsLoader func(string) WatchDefaults
}

// NewWatchPRHandler builds a handler. A nil clock falls back to the wall
// clock inside domain.NewFanout; a nil CI provider is allowed when callers
// always supply headSHA.
func NewWatchPRHandler(store StateStore, ci domain.CIProvider, clock domain.Clock) *WatchPRHandler {
	return &WatchPRHandler{
		store:          store,
		ci:             ci,
		clock:          clock,
		fanout:         domain.NewFanout(store, clock),
		defaultsLoader: watchPRDefaultsLoader,
	}
}

// SetProjectRoot binds the root used to read .beacon/config.yaml and
// .beacon/flaky.json. An empty root keeps the built-in defaults.
func (h *WatchPRHandler) SetProjectRoot(root string) {
	if h != nil {
		h.projectRoot = root
	}
}

// watchDefaults resolves the layered config through the handler's seam.
func (h *WatchPRHandler) watchDefaults() WatchDefaults {
	if h == nil || h.defaultsLoader == nil {
		return loadWatchDefaults("")
	}
	return h.defaultsLoader(h.projectRoot)
}

// SetBlockerIncidents injects the durable incident port. A nil argument keeps
// the handler's local-only behaviour, which is how unit tests run without SQLite.
func (h *WatchPRHandler) SetBlockerIncidents(b domain.BlockerIncidents) {
	if h != nil {
		h.blockers = b
	}
}

// SetFixerBinding injects the port that binds a fixer Watch to an incident.
// A nil argument keeps the local-only behaviour of a Watch without a binding.
func (h *WatchPRHandler) SetFixerBinding(b domain.FixerBinding) {
	if h != nil {
		h.fixer = b
	}
}

// SetRegistry installs the optional executor registrar. A nil argument keeps
// the older behaviour of writing only the Watch, which is how unit tests run.
func (h *WatchPRHandler) SetRegistry(r WatchPRRegistrar) {
	if h != nil {
		h.registrar = r
	}
}

// Handle adapts Register to the go-sdk typed tool handler signature. Domain
// errors are returned as-is so the MCP boundary can convert them with
// ToErrorShape from T2.2.
func (h *WatchPRHandler) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in WatchPRInput) (*mcpsdk.CallToolResult, WatchPROutput, error) {
	out, err := h.Register(ctx, req, in)
	if err != nil {
		return nil, WatchPROutput{}, err
	}
	return nil, out, nil
}

// Register is the core operation. It validates input, derives the subscriber
// identity, resolves headSHA when omitted, closes a superseded Watch on head
// change, then upserts the Watch and subscriber through domain.Fanout.
//
// An optional in.Executor descriptor is accepted as opaque caller data. The
// core never interprets its agent name and never uses it to change subscriber
// identity or routing. Its configId is the one descriptor field copied onto the
// Watch and into the registry binding (T5.31, ADR 0087, ADR 0088).
//
// An optional in.OrchestratorID is likewise opaque: it is stored on the Watch
// and on the registering Subscriber for a later rebind, but it never selects a
// wake recipient. Blocked wakes keep following the orchestrator subscriber.
//
// Role fixer behaves like an executor for wake routing and requires in.FixFor,
// the ID of a Watch in status blocked. validateFixFor rejects a missing or
// stale target before any write. FixFor is stored on both the Watch and the
// registering Subscriber so a fix can be attributed to its block.
func (h *WatchPRHandler) Register(ctx context.Context, req *mcpsdk.CallToolRequest, in WatchPRInput) (WatchPROutput, error) {
	if in.Repo == "" || in.PR <= 0 {
		return WatchPROutput{}, domain.NewAppError(domain.CodeInvalidInput, "watch_pr: repo and a positive pr are required")
	}
	if in.Role != domain.RoleExecutor && in.Role != domain.RoleOrchestrator && in.Role != domain.RoleFixer {
		return WatchPROutput{}, domain.NewAppError(domain.CodeInvalidInput, "watch_pr: role must be executor, orchestrator or fixer")
	}
	// A registration carries the project's own limits and flaky set when the
	// caller says nothing. Every project gets its values without copying a
	// shared slug or flaky list into this file.
	defaults := h.watchDefaults()
	// Validate the fix target before any write. A fixer that cannot name a real
	// blocked Watch must not create or renew its own Watch.
	blockerID, err := h.validateFixFor(in)
	if err != nil {
		return WatchPROutput{}, err
	}

	subscriberID, adapter, target, err := watchPRSubscriber(req, in)
	if err != nil {
		return WatchPROutput{}, err
	}
	if in.Role == domain.RoleExecutor || in.Role == domain.RoleFixer {
		if !strings.HasPrefix(subscriberID, sessionIDPrefix) {
			return WatchPROutput{}, domain.NewAppError(domain.CodeInvalidInput,
				fmt.Sprintf("watch_pr: %s registration needs a real session id starting with %q from _meta[%q] or notify.target, got %q (a card id is not a session)",
					in.Role, sessionIDPrefix, watchPRSessionIDMetaKey, subscriberID))
		}
	}

	headSHA, baseRef, err := h.resolveHead(ctx, in)
	if err != nil {
		observe.ActorError("watch_pr-head", "", err)
		return WatchPROutput{}, err
	}

	now := h.now()

	// A different head on the same PR supersedes the active Watch. ADR 0019
	// closes the old Watch with closeReason=head_changed and carries its
	// subscribers to the successor.
	carried, err := h.closeSuperseded(in.Repo, in.PR, headSHA, now)
	if err != nil {
		observe.ActorError("watch_pr-supersede", "", err)
		return WatchPROutput{}, err
	}

	configID := ""
	if in.Executor != nil {
		configID = in.Executor.ConfigID
	}
	w := domain.Watch{
		Repo:           in.Repo,
		PR:             in.PR,
		HeadSHA:        headSHA,
		BaseRef:        baseRef,
		Issue:          in.Issue,
		Status:         domain.StatusWatching,
		Verdict:        domain.VerdictWatching,
		Subscribers:    carried,
		OrchestratorID: in.OrchestratorID,
		FixFor:         in.FixFor,
		FixBlockerID:   blockerID,
		ConfigID:       configID,
		CreatedAt:      now,
	}
	flaky := in.Flaky
	if flaky == nil {
		flaky = defaults.Flaky
	}
	if flaky != nil {
		w.Flaky = domain.FlakyConfig{
			Names:      append([]string(nil), flaky.Names...),
			MaxRetries: flaky.MaxRetries,
			Used:       map[string]int{},
		}
	}
	maxWakes := defaults.MaxWakesPerWatch
	if in.Policy != nil && in.Policy.MaxWakesPerWatch > 0 {
		maxWakes = in.Policy.MaxWakesPerWatch
	}
	// cibridge reads MaxRebases from the stored Watch. It charges each rebase
	// to a durable obligation and blocks the Watch on the shared incident when
	// the budget runs out, so the value must be materialised here.
	maxRebases := defaults.MaxRebases
	if in.Policy != nil && in.Policy.MaxRebases > 0 {
		maxRebases = in.Policy.MaxRebases
	}
	w.Policy = domain.Policy{
		RequireUpToDate:  in.Policy != nil && in.Policy.RequireUpToDate,
		IgnoreReviews:    in.Policy != nil && in.Policy.IgnoreReviews,
		MaxWakesPerWatch: maxWakes,
		MaxRebases:       maxRebases,
	}
	// Always materialise a concrete policy, so an absent policy.merge leaves a
	// stored Watch with auto-merge on and empty conditions.
	w.MergePolicy = watchPRMergePolicy(in.Policy)
	if in.DeadlineMs != nil && *in.DeadlineMs > 0 {
		w.DeadlineAt = now.Add(time.Duration(*in.DeadlineMs) * time.Millisecond)
	}
	// Starting the clock at registration makes the rule reachable: the first
	// observation carries both NoChecksSince and NoChecksTimeout into the
	// verdict engine.
	w.NoChecksTimeout = defaults.NoChecksTimeout
	if w.NoChecksTimeout > 0 {
		w.NoChecksSince = now
	}

	s := domain.Subscriber{
		ID:             subscriberID,
		Role:           in.Role,
		Adapter:        adapter,
		Target:         target,
		OrchestratorID: in.OrchestratorID,
		FixFor:         in.FixFor,
		Phase:          domain.WorkerPhaseWaiting,
	}

	sub, err := h.fanout.Subscribe(w, s)
	if err != nil {
		observe.ActorError("watch_pr", "", err)
		return WatchPROutput{}, err
	}
	observe.WatchRegistered(sub.Watch.ID, in.PR, sub.Watch.Revision, adapter)

	// Fanout rebuilds its target from the stored Watch when the (repo, pr,
	// headSHA) alias already exists, so new fields would be dropped on a
	// renewal. Apply them in place to the Watch Fanout just persisted. No
	// subscriber is added or removed here, so wake routing is unchanged.
	updated := sub.Watch
	changed := false
	if in.OrchestratorID != "" && updated.OrchestratorID != in.OrchestratorID {
		updated.OrchestratorID = in.OrchestratorID
		changed = true
	}
	if in.FixFor != "" && updated.FixFor != in.FixFor {
		updated.FixFor = in.FixFor
		changed = true
	}
	if blockerID != "" && updated.FixBlockerID != blockerID {
		updated.FixBlockerID = blockerID
		changed = true
	}
	if configID != "" && updated.ConfigID != configID {
		updated.ConfigID = configID
		changed = true
	}
	for i := range updated.Subscribers {
		if updated.Subscribers[i].ID != subscriberID {
			continue
		}
		if in.OrchestratorID != "" && updated.Subscribers[i].OrchestratorID != in.OrchestratorID {
			updated.Subscribers[i].OrchestratorID = in.OrchestratorID
			changed = true
		}
		if in.FixFor != "" && updated.Subscribers[i].FixFor != in.FixFor {
			updated.Subscribers[i].FixFor = in.FixFor
			changed = true
		}
		// A repeated registration resets the worker to waiting. Fanout already
		// refreshed LastActivity on the renew path.
		if updated.Subscribers[i].Phase != domain.WorkerPhaseWaiting {
			updated.Subscribers[i].Phase = domain.WorkerPhaseWaiting
			changed = true
		}
	}
	if changed {
		if err := h.store.SaveWatch(updated); err != nil {
			observe.ActorError("watch_pr-fields", "", err)
			return WatchPROutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
		sub.Watch = updated
	}

	// The registry row is part of registration: registry sync must see the
	// executor and its Watch binding as soon as watch_pr returns. Calls made
	// before the descriptor existed write no registry row.
	if in.Executor != nil && h.registrar != nil {
		err := h.registrar.RegisterWatchExecutor(
			w.Repo, sub.Watch.ID, subscriberID, string(in.Role), in.Executor.Runtime,
			adapter, target, configID, sub.Watch.Revision,
		)
		if err != nil {
			observe.ActorError("watch_pr-registry", sub.Watch.ID, err)
			return WatchPROutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
	}

	// Binding a fixer to its incident and spending one fix attempt happens
	// here and only here. A renewal keeps the attempt count: re-registering the
	// same agent must not drain the fixer budget or fake a new repair.
	if in.Role == domain.RoleFixer && blockerID != "" && h.fixer != nil && !sub.Renewed {
		incident, err := h.fixer.AssignFixer(blockerID, sub.Watch.ID)
		if err != nil {
			observe.ActorError("watch_pr-assign-fixer", sub.Watch.ID, err)
			return WatchPROutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
		observe.Event(observe.LevelInfo, "blocker_fixer_assigned", observe.Fields{
			"blockerID": incident.ID, "watchID": sub.Watch.ID, "fixAttempts": incident.FixAttempts,
		})
	}

	// The registration parameters are logged at the boundary, not inferred
	// from the persisted Watch later: an operator needs to see what the caller
	// asked for, including fields that are intentionally not wake addresses.
	observe.Event(observe.LevelInfo, "watch_pr_registered", observe.Fields{
		"watchID": sub.Watch.ID, "pr": in.PR, "role": string(in.Role),
		"subscriberID": subscriberID, "orchestratorID": in.OrchestratorID, "fixFor": in.FixFor,
	})
	h.reportInformationalChecks(ctx, in, sub.Watch.ID)

	return WatchPROutput{
		WatchID:     sub.Watch.ID,
		Revision:    sub.Watch.Revision,
		Status:      string(sub.Watch.Status),
		Verdict:     string(sub.Watch.Verdict),
		PollAfterMs: watchPRPollAfterMs,
	}, nil
}

// validateFixFor checks the fixer target before registration writes anything
// and returns the incident id the fix will be charged to. A fixer without
// fixFor has nothing to repair; a fixFor that names no Watch is a dangling
// reference; a Watch that is not blocked has nothing to fix. All three are hard
// errors: a fix count must always point at a real block. fixBlockerID can pin
// the incident explicitly, otherwise the blocked Watch carries it in
// BlockerID. With a blocker port installed the incident must exist and be open.
func (h *WatchPRHandler) validateFixFor(in WatchPRInput) (string, error) {
	if in.Role != domain.RoleFixer && in.FixFor != "" {
		return "", domain.NewAppError(domain.CodeInvalidInput, "watch_pr: fixFor is only valid with role fixer")
	}
	if in.Role != domain.RoleFixer {
		if in.FixBlockerID != "" {
			return "", domain.NewAppError(domain.CodeInvalidInput, "watch_pr: fixBlockerID is only valid with role fixer")
		}
		return "", nil
	}
	if strings.TrimSpace(in.FixFor) == "" {
		return "", domain.NewAppError(domain.CodeInvalidInput, "watch_pr: role fixer requires fixFor")
	}
	if h.store == nil {
		return "", domain.NewAppError(domain.CodePersistFailed, "watch_pr: store is nil")
	}
	target, err := h.store.Watch(in.FixFor)
	if err != nil {
		return "", domain.NewAppError(domain.CodeWatchNotFound, "watch_pr: fixFor watch not found: "+in.FixFor)
	}
	if target.Status != domain.StatusBlocked {
		return "", domain.NewAppError(domain.CodeInvalidInput, "watch_pr: fixFor watch is not blocked: "+in.FixFor)
	}

	blockerID := in.FixBlockerID
	if blockerID == "" {
		blockerID = target.BlockerID
	}
	if h.blockers != nil {
		if blockerID == "" {
			return "", domain.NewAppError(domain.CodeInvalidInput,
				"watch_pr: fixer needs fixBlockerID or a blocked Watch with blockerID")
		}
		active, err := h.blockers.ActiveBlockers()
		if err != nil {
			return "", domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
		found := false
		for _, incident := range active {
			if incident.ID == blockerID {
				found = true
				break
			}
		}
		if !found {
			return "", domain.NewAppError(domain.CodeInvalidInput,
				"watch_pr: fixBlockerID is not an open blocker incident: "+blockerID)
		}
	}
	return blockerID, nil
}

// watchPRSubscriber derives the subscriber identity and delivery endpoint.
// The sessionID from _meta wins when present. Otherwise notify.target is
// required and becomes the identity so the subscriber can still be addressed.
func watchPRSubscriber(req *mcpsdk.CallToolRequest, in WatchPRInput) (string, string, string, error) {
	adapter, target := "", ""
	if in.Notify != nil {
		adapter, target = in.Notify.Adapter, in.Notify.Target
	}

	id := watchPRSessionID(req)
	if id == "" {
		if target == "" {
			return "", "", "", domain.NewAppError(domain.CodeInvalidInput,
				"watch_pr: notify.target is required when the sessionID is absent")
		}
		id = target
	}
	return id, adapter, target, nil
}

// watchPRSessionID reads the opaque sessionID from _meta. This is the T2.3
// contract inlined so T2.4 does not couple to an unexported symbol in meta.go.
// A missing key, a nil value or a non-string value all yield an empty string;
// nothing is validated and no access decision is made here.
func watchPRSessionID(req *mcpsdk.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	value, ok := req.Params.GetMeta()[watchPRSessionIDMetaKey]
	if !ok || value == nil {
		return ""
	}
	id, _ := value.(string)
	return id
}

// resolveHead returns the caller's headSHA or resolves the current one from
// the CI provider. baseRef is only known on the resolved path.
func (h *WatchPRHandler) resolveHead(ctx context.Context, in WatchPRInput) (string, string, error) {
	if in.HeadSHA != "" {
		return in.HeadSHA, "", nil
	}
	if h.ci == nil {
		return "", "", domain.NewAppError(domain.CodeInvalidInput,
			"watch_pr: headSHA is required when no CI provider is configured")
	}
	state, err := h.ci.PRState(ctx, in.Repo, in.PR)
	if err != nil {
		return "", "", err
	}
	if state.HeadRefOid == "" {
		return "", "", domain.NewAppError(domain.CodeInvalidInput,
			"watch_pr: CI provider returned an empty head SHA")
	}
	return state.HeadRefOid, state.BaseRefName, nil
}

// reportInformationalChecks makes a required:false decision visible at Watch
// registration. A red check with Required=false is dropped from the verdict on
// purpose, so until now a Watch could stay in watching with no log line to
// explain it. Both events carry an error field so `beacon logs --errors` finds
// them even though the record level is WARN. A provider failure is ignored:
// diagnostics must never refuse a registration.
func (h *WatchPRHandler) reportInformationalChecks(ctx context.Context, in WatchPRInput, watchID string) {
	if h.ci == nil {
		return
	}
	state, err := h.ci.PRState(ctx, in.Repo, in.PR)
	if err != nil {
		return
	}
	all, ignored := domain.InformationalCheckDiagnostics(domain.Snapshot{
		Checks:   state.Checks,
		Failures: failedCheckNamesFromChecks(state.Checks),
	})
	if all {
		observe.Event(observe.LevelWarn, domain.EventInformationalChecksOnly, observe.Fields{
			observe.FieldWatchID: watchID,
			observe.FieldError:   "all observed checks are informational (required:false)",
		})
	}
	for _, name := range ignored {
		observe.Event(observe.LevelWarn, domain.EventInformationalFailureIgnored, observe.Fields{
			observe.FieldWatchID: watchID,
			observe.FieldError:   "failed informational check excluded from verdict: " + name,
			"check":              name,
		})
	}
}

// failedCheckNamesFromChecks derives the failure list from an observed check
// set. A provider reports one Check per context, so the names are the keys.
// The name stays distinct from failedCheckNames in tool_ack.go, which takes a
// different input type in the same package.
func failedCheckNamesFromChecks(checks map[string]domain.Check) []string {
	names := make([]string, 0, len(checks))
	for name, c := range checks {
		if domain.NormalizeConclusion(c.Conclusion) == "FAILURE" ||
			domain.NormalizeConclusion(c.State) == "FAILURE" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// liveWatchSource is the optional store port that includes blocked Watches.
// StateStore only promises ActiveWatches, so a store that has not been upgraded
// falls back to the active set.
type liveWatchSource interface {
	LiveWatches() ([]domain.Watch, error)
}

// unclosedWatchSource is the optional store port that returns every Watch that
// is not closed, including needs_input and parked. closeSuperseded uses it so
// a force-push also closes a Watch waiting for a human. A store without it
// falls back to the narrower live or active set.
type unclosedWatchSource interface {
	UnclosedWatches() ([]domain.Watch, error)
}

// closeSuperseded closes every unclosed Watch on (repo, pr) whose head differs
// from headSHA and returns the subscribers to carry to the successor. A blocked,
// needs_input or parked Watch counts as live: a force-push must close it instead
// of leaving a second Watch on the same alias. It reuses the domain state machine
// so closeReason and ClosedAt follow T1.2.
func (h *WatchPRHandler) closeSuperseded(repo string, pr int, headSHA string, now time.Time) ([]domain.Subscriber, error) {
	if h.store == nil {
		return nil, domain.NewAppError(domain.CodePersistFailed, "watch_pr: store is nil")
	}
	var watches []domain.Watch
	var err error
	if src, ok := h.store.(unclosedWatchSource); ok {
		watches, err = src.UnclosedWatches()
	} else if src, ok := h.store.(liveWatchSource); ok {
		watches, err = src.LiveWatches()
	} else {
		watches, err = h.store.ActiveWatches()
	}
	if err != nil {
		return nil, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}

	var carried []domain.Subscriber
	for i := range watches {
		w := &watches[i]
		if w.Repo != repo || w.PR != pr || w.HeadSHA == headSHA {
			continue
		}
		closed, effects := domain.Transition(*w, domain.HeadChangedEvent(headSHA, now))
		if err := h.store.SaveWatch(closed); err != nil {
			return nil, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
		observe.Event(observe.LevelWarn, "watch_pr_superseded", observe.Fields{
			observe.FieldWatchID: w.ID,
			observe.FieldError: fmt.Sprintf(
				"headSHA changed from %s to %s; subscribers carried to successor", w.HeadSHA, headSHA),
		})
		if carried == nil {
			for _, effect := range effects {
				if effect.Kind == domain.EffectCreateWatch {
					carried = effect.Subscribers
					break
				}
			}
		}
	}
	return carried, nil
}

func (h *WatchPRHandler) now() time.Time {
	if h.clock == nil {
		return time.Now()
	}
	return h.clock.Now()
}

// attachToolAnnotations builds the SDK annotations shared by the opt-in Attach*
// tools. DestructiveHint is a pointer in the SDK, so it stays set explicitly to
// false even for additive tools. IdempotentHint is the only value that differs
// between tools, so callers pass it in.
func attachToolAnnotations(idempotent bool) *mcpsdk.ToolAnnotations {
	no := false
	return &mcpsdk.ToolAnnotations{
		DestructiveHint: &no,
		IdempotentHint:  idempotent,
	}
}

// AttachWatchPR replaces the T2.1 stub with the real tool on a go-sdk server.
// Wiring all seven tools into NewServer belongs to a later task; this helper
// keeps the feature usable on its own.
func AttachWatchPR(server *mcpsdk.Server, handler *WatchPRHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools("beacon_watch_pr")
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_watch_pr",
		Description: "Register or renew a Watch on a pull request head and subscribe the caller.",
		Annotations: attachToolAnnotations(true),
	}, handler.Handle)
}

// AttachWatchPRWithConfig installs the real watch_pr handler bound to the
// project's layered config. The composition root calls it after
// NewServerWithDeps so a registration uses this project's flaky set and limits
// instead of the built-in values. Re-registering the tool is safe: the
// NewServerWithDeps instance has no extra wiring to preserve.
func AttachWatchPRWithConfig(server *mcpsdk.Server, deps Dependencies, projectRoot string) {
	if server == nil {
		return
	}
	handler := NewWatchPRHandler(deps.Store, deps.CI, deps.Clock)
	handler.SetBlockerIncidents(deps.Blockers)
	handler.SetFixerBinding(deps.FixerBinding)
	if registrar, ok := deps.Store.(WatchPRRegistrar); ok {
		handler.SetRegistry(registrar)
	}
	handler.SetProjectRoot(projectRoot)
	AttachWatchPR(server, handler)
}
