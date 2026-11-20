// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_watch_pr: an additive, idempotent registration
// and fan-out tool (WORKER-PROMPT §4.1, ADR 0019). It relies on the domain
// ports and on domain.Fanout for the alias and obligation invariants, and on
// the tool output type and error envelope produced by T2.2.
package mcp

import (
	"context"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
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
)

// WatchPRInput is the argument shape of beacon_watch_pr (WORKER-PROMPT §4.1).
// All fields except repo, pr and role are optional in the contract.
type WatchPRInput struct {
	Repo       string         `json:"repo" jsonschema:"repository slug, for example owner/name"`
	PR         int            `json:"pr" jsonschema:"pull request number, must be positive"`
	HeadSHA    string         `json:"headSHA,omitempty" jsonschema:"head commit SHA; resolved from the CI provider when omitted"`
	Issue      string         `json:"issue,omitempty"`
	Role       domain.Role    `json:"role" jsonschema:"subscriber role: executor or orchestrator"`
	Notify     *WatchPRNotify `json:"notify,omitempty"`
	Flaky      *WatchPRFlaky  `json:"flaky,omitempty"`
	Policy     *WatchPRPolicy `json:"policy,omitempty"`
	DeadlineMs *int64         `json:"deadlineMs,omitempty" jsonschema:"watch deadline in milliseconds from now"`
}

// WatchPRNotify is the optional delivery endpoint of the registering party.
type WatchPRNotify struct {
	Adapter string `json:"adapter"`
	Target  string `json:"target"`
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

// WatchPRHandler implements beacon_watch_pr over the domain ports. One handler
// owns a Fanout, which serializes the check-then-act sequence inside one
// process; cross-process exclusion remains the daemon's job (ADR 0021).
type WatchPRHandler struct {
	store   domain.Store
	ci      domain.CIProvider
	clock   domain.Clock
	fanout  *domain.Fanout
	bridges []Bridge
}

// NewWatchPRHandler builds a handler. A nil clock falls back to the wall
// clock inside domain.NewFanout; a nil CI provider is allowed when callers
// always supply headSHA.
func NewWatchPRHandler(store domain.Store, ci domain.CIProvider, clock domain.Clock) *WatchPRHandler {
	return &WatchPRHandler{
		store:  store,
		ci:     ci,
		clock:  clock,
		fanout: domain.NewFanout(store, clock),
	}
}

// WithBridges injects the bridges that a watch_pr registration is broadcast
// to. It returns the receiver so callers can chain it after NewWatchPRHandler.
// Adding another agent means passing another Bridge here; the core needs no
// change (ADR 0024).
func (h *WatchPRHandler) WithBridges(bridges ...Bridge) *WatchPRHandler {
	if h == nil {
		return nil
	}
	h.bridges = append([]Bridge(nil), bridges...)
	return h
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
func (h *WatchPRHandler) Register(ctx context.Context, req *mcpsdk.CallToolRequest, in WatchPRInput) (WatchPROutput, error) {
	if in.Repo == "" || in.PR <= 0 {
		return WatchPROutput{}, domain.NewAppError(domain.CodeInvalidInput, "watch_pr: repo and a positive pr are required")
	}
	if in.Role != domain.RoleExecutor && in.Role != domain.RoleOrchestrator {
		return WatchPROutput{}, domain.NewAppError(domain.CodeInvalidInput, "watch_pr: role must be executor or orchestrator")
	}

	subscriberID, adapter, target, err := watchPRSubscriber(req, in)
	if err != nil {
		return WatchPROutput{}, err
	}

	headSHA, baseRef, err := h.resolveHead(ctx, in)
	if err != nil {
		return WatchPROutput{}, err
	}

	now := h.now()

	// A different head on the same PR supersedes the active Watch. ADR 0019
	// closes the old Watch with closeReason=head_changed and carries its
	// subscribers to the successor.
	carried, err := h.closeSuperseded(in.Repo, in.PR, headSHA, now)
	if err != nil {
		return WatchPROutput{}, err
	}

	w := domain.Watch{
		Repo:        in.Repo,
		PR:          in.PR,
		HeadSHA:     headSHA,
		BaseRef:     baseRef,
		Issue:       in.Issue,
		Status:      domain.StatusWatching,
		Verdict:     domain.VerdictWatching,
		Subscribers: carried,
		CreatedAt:   now,
	}
	if in.Flaky != nil {
		w.Flaky = domain.FlakyConfig{
			Names:      in.Flaky.Names,
			MaxRetries: in.Flaky.MaxRetries,
			Used:       map[string]int{},
		}
	}
	if in.Policy != nil {
		w.Policy = domain.Policy{
			RequireUpToDate: in.Policy.RequireUpToDate,
			IgnoreReviews:   in.Policy.IgnoreReviews,
		}
	}
	// Always materialise a concrete policy, so an absent policy.merge leaves a
	// stored Watch with auto-merge on and empty conditions.
	w.MergePolicy = watchPRMergePolicy(in.Policy)
	if in.DeadlineMs != nil && *in.DeadlineMs > 0 {
		w.DeadlineAt = now.Add(time.Duration(*in.DeadlineMs) * time.Millisecond)
	}

	s := domain.Subscriber{
		ID:      subscriberID,
		Role:    in.Role,
		Adapter: adapter,
		Target:  target,
	}

	sub, err := h.fanout.Subscribe(w, s)
	if err != nil {
		return WatchPROutput{}, err
	}

	// One watch_pr call reaches every injected bridge through the same
	// six-operation port. Each bridge decides whether the work applies to its
	// agent; the core never names an agent (ADR 0024). Dispatch is best effort:
	// the persisted Watch is the source of truth and per-agent delivery
	// reliability belongs to the bridge.
	h.startBridges(ctx, sub.Watch, s)

	return WatchPROutput{
		WatchID:     sub.Watch.ID,
		Revision:    sub.Watch.Revision,
		Status:      string(sub.Watch.Status),
		Verdict:     string(sub.Watch.Verdict),
		PollAfterMs: watchPRPollAfterMs,
	}, nil
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

// closeSuperseded closes every active Watch on (repo, pr) whose head differs
// from headSHA and returns the subscribers to carry to the successor. It
// reuses the domain state machine so closeReason and ClosedAt follow T1.2.
func (h *WatchPRHandler) closeSuperseded(repo string, pr int, headSHA string, now time.Time) ([]domain.Subscriber, error) {
	if h.store == nil {
		return nil, domain.NewAppError(domain.CodePersistFailed, "watch_pr: store is nil")
	}
	active, err := h.store.ActiveWatches()
	if err != nil {
		return nil, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}

	var carried []domain.Subscriber
	for i := range active {
		w := &active[i]
		if w.Repo != repo || w.PR != pr || w.HeadSHA == headSHA {
			continue
		}
		closed, effects := domain.Transition(*w, domain.HeadChangedEvent(headSHA, now))
		if err := h.store.SaveWatch(closed); err != nil {
			return nil, domain.NewAppError(domain.CodePersistFailed, err.Error())
		}
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

// startBridges broadcasts the Start operation to every injected bridge. The
// request carries neutral identifiers only, so the same call drives two
// different agents without a core edit (ADR 0024). Errors are intentionally
// not propagated: registration has already succeeded in the domain store.
func (h *WatchPRHandler) startBridges(ctx context.Context, w domain.Watch, s domain.Subscriber) {
	req := BridgeRequest{
		WatchID:      w.ID,
		Repo:         w.Repo,
		PR:           w.PR,
		HeadSHA:      w.HeadSHA,
		SubscriberID: s.ID,
	}
	for _, bridge := range h.bridges {
		if bridge == nil {
			continue
		}
		_, _ = bridge.Start(ctx, req)
	}
}

func (h *WatchPRHandler) now() time.Time {
	if h.clock == nil {
		return time.Now()
	}
	return h.clock.Now()
}

// AttachWatchPR replaces the T2.1 stub with the real tool on a go-sdk server.
// Wiring all seven tools into NewServer belongs to a later task; this helper
// keeps the feature usable on its own.
func AttachWatchPR(server *mcpsdk.Server, handler *WatchPRHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools("beacon_watch_pr")
	no := false
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_watch_pr",
		Description: "Register or renew a Watch on a pull request head and subscribe the caller.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  true,
		},
	}, handler.Handle)
}
