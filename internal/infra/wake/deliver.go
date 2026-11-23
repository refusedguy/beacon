// Package wake adapts Beacon's wake pointer to the host harness.
// Consolidated from PRs #281, #286, #282, #287, #285, #290, #291, #288, #283, #284, #289, #276.
package wake

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// ===== база wake (#281) =====
// Package wake adapts Beacon's wake pointer to the host harness. The default
// adapter uses the OpenCode HTTP API through `opencode api`, the same path the
// CLI and TUI use. It never invents a verdict and never merges a PR.

// PromptFunc writes text into an existing session through the already running
// HTTP service. delivery is "steer" or "queue": "steer" also resumes an idle
// session, "queue" parks the input without starting a new turn. A port that
// discovers the HTTP service is gone returns an error wrapping ErrNoPromptPort
// so Deliver can fall back to the CLI without inventing a second delivery.
type PromptFunc func(ctx context.Context, sessionID, text, delivery string) error

// PromptWithResumeFunc is the same HTTP port with the resume flag attached.
// An idle session gets resume=true so the host starts a new turn; a busy one
// gets resume=false and delivery=queue so its input parks (MEM-03). Keeping
// the older PromptFunc alongside it lets existing adapters compile until they
// adopt the new port in MEM-04.
type PromptWithResumeFunc func(ctx context.Context, sessionID, text, delivery string, resume bool) error

// PromptWithIDFunc is the id-aware HTTP port. obligationID becomes the prompt's
// message id, so a retried wake is a duplicate instead of a second turn
// (ADR 0306, ADR 0077). delivery and resume come from the same decision the
// CLI fallback uses. The adapter, not wake, derives the wire id: wake must not
// import a sibling infra package (ADR 0005).
type PromptWithIDFunc func(ctx context.Context, sessionID, text, delivery, obligationID string, resume bool) error

// SessionActiveFunc reports whether sessionID is busy and still alive. A
// session that no longer exists (HTTP 404) is an error wrapping ErrSessionDead.
type SessionActiveFunc func(ctx context.Context, sessionID string) (busy bool, alive bool, err error)

// ObligationDeliveryStore is the slice of the domain store the deliverer needs.
// A SQLite-backed state store satisfies it; wake declares the port in terms of
// domain types and never imports internal/infra/store (ADR 0005).
type ObligationDeliveryStore interface {
	Obligation(id string) (domain.Obligation, error)
	MarkObligationDelivered(id string, at time.Time, attempt int, lastErr string) error
}

var (
	// ErrSessionDead marks a session that no longer exists. errors.Is finds it
	// through any transport wrapper.
	ErrSessionDead = errors.New("wake: session dead")
	// ErrNoPromptPort asks Deliver to use the CLI fallback. It is the honest
	// answer when service.json is absent under a plain TUI.
	ErrNoPromptPort = errors.New("wake: no http prompt port")
	// ErrExportLeak refuses a wake whose envelope trips an export policy. No
	// prompt reaches the session, and Deliver releases its lease on the way out.
	ErrExportLeak = errors.New("wake: export leak")
)

const (
	wakeDeliverySteer = "steer"
	wakeDeliveryQueue = "queue"

	// opencodeCLITimeout bounds every fallback `opencode api` call. A hung CLI
	// must not hold a wake forever when the HTTP port is not configured.
	opencodeCLITimeout = 10 * time.Second
)

// OpenCodeDeliverer posts a wake pointer to an existing OpenCode session.
// The sessionID comes from the MCP caller's _meta, so Beacon already knows
// which agent to wake without a second registry.
type OpenCodeDeliverer struct {
	// Runner is replaceable in tests. Nil means exec.CommandContext.
	Runner func(ctx context.Context, args ...string) ([]byte, error)
	// PromptFunc is the live HTTP port. When set it is tried first, so no
	// process is spawned for a wake on a service that is already running.
	PromptFunc PromptFunc
	// PromptWithResumeFunc is the resume-aware form of the same port. It wins
	// over PromptFunc when both are set, so the transport gets delivery and
	// resume from one call. Nil keeps older adapters working unchanged.
	PromptWithResumeFunc PromptWithResumeFunc
	// PromptWithIDFunc is the id-aware form of the same port. It wins over
	// PromptWithResumeFunc and PromptFunc so the adapter can send the message
	// id. Nil keeps existing adapters working unchanged.
	PromptWithIDFunc PromptWithIDFunc
	// SessionActiveFunc decides queue vs steer before a prompt is sent.
	SessionActiveFunc SessionActiveFunc
	// Obligations makes the delivery fact survive a process restart.
	Obligations ObligationDeliveryStore
	// LeaseGuard serializes delivery per target. Deliver builds one with the
	// default ttl when nil; set a restored guard here after a restart.
	LeaseGuard *DeliveryLeaseGuard
	// DraftAware, when set, replaces the immediate PromptFunc/CLI path with
	// verified composer/draft delivery (T3.244). A nil value keeps the default
	// behavior: the whole Envelope is steered into the session at once.
	DraftAware domain.WakeDeliverer
	// ExportPolicy guards the wake text before it leaves the process. Nil
	// keeps today's envelope untouched (T3.344). A set policy scans the text
	// at the context-pack boundary before any transport call.
	ExportPolicy *domain.ExportPolicy

	mu   sync.Mutex
	seen map[wakeDeliveryKey]struct{}
}

// wakeDeliveryKey is the pointer a wake carries. A repeated pointer must not
// start a second prompt in the same session (ADR 0028, T4.1).
type wakeDeliveryKey struct {
	watchID      string
	obligationID string
	revision     uint64
}

var _ domain.WakeDeliverer = (*OpenCodeDeliverer)(nil)

// ===== Shared wake-identity dedup (T3.414) =====

// DeliveryDedup is the ledger of wake identities already admitted by a
// delivery supplier. The CI bridge path and the daemon path consult one shared
// instance, so an obligation admitted by one supplier is a duplicate for the
// other. The key is the obligation id, which already folds watchID, revision
// and subscriber into one identity.
type DeliveryDedup struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewDeliveryDedup returns an empty ledger. Tests use a fresh ledger so cases
// cannot influence each other; production wiring uses SharedDeliveryDedup.
func NewDeliveryDedup() *DeliveryDedup {
	return &DeliveryDedup{seen: make(map[string]struct{})}
}

// Claim records obligationID and reports whether the caller is the first to
// claim it. A false return means another supplier already admitted this wake,
// so the caller must not start a second delivery.
func (d *DeliveryDedup) Claim(obligationID string) bool {
	if d == nil {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = make(map[string]struct{})
	}
	if _, exists := d.seen[obligationID]; exists {
		return false
	}
	d.seen[obligationID] = struct{}{}
	return true
}

// Seen reports whether obligationID was already claimed by either supplier.
func (d *DeliveryDedup) Seen(obligationID string) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, exists := d.seen[obligationID]
	return exists
}

// Release drops a claim after a failed delivery, so the obligation stays
// eligible for a later pass.
func (d *DeliveryDedup) Release(obligationID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, obligationID)
}

var sharedDeliveryDedup = NewDeliveryDedup()

// SharedDeliveryDedup is the single ledger the two process suppliers share.
// The bridge takes it through AdapterDedupDeliverer; the daemon takes it in
// NewDaemonWaker. Both therefore agree on what counts as an already delivered
// wake identity.
func SharedDeliveryDedup() *DeliveryDedup { return sharedDeliveryDedup }

// AdapterDedupDeliverer wraps an adapter-aware deliverer with a wake-identity
// ledger. The bridge dispatches through the adapter name; a second wake for the
// same obligation is coalesced before it reaches the inner deliverer.
type AdapterDedupDeliverer struct {
	Inner domain.AdapterWakeDeliverer
	Dedup *DeliveryDedup
}

var _ domain.AdapterWakeDeliverer = (*AdapterDedupDeliverer)(nil)

func (d *AdapterDedupDeliverer) Deliver(ctx context.Context, adapter, sessionID string, p domain.WakePayload) error {
	return d.deliver(ctx, adapter, sessionID, p, nil)
}

// deliver admits one wake identity and runs onDelivered only after the inner
// deliverer accepted the pointer. The callback carries no role: the watch-aware
// caller decides which counter a delivered wake raises.
func (d *AdapterDedupDeliverer) deliver(ctx context.Context, adapter, sessionID string, p domain.WakePayload, onDelivered func()) error {
	if d == nil || d.Inner == nil {
		return nil
	}
	ledger := d.Dedup
	if ledger == nil {
		ledger = SharedDeliveryDedup()
	}
	if !ledger.Claim(p.ObligationID) {
		// A coalesced wake is not a delivery. The public Deliver methods keep
		// their nil-on-duplicate contract, so a caller that records a "posted"
		// outcome for every nil still counts the duplicate. The observe layer
		// cancels that second count: it records one posted outcome per
		// obligationID, and the duplicate already raised the duplicate outcome.
		observe.WakeDuplicate(p.WatchID, p.Revision)
		return nil
	}
	if err := d.Inner.Deliver(ctx, adapter, sessionID, p); err != nil {
		ledger.Release(p.ObligationID)
		return err
	}
	if onDelivered != nil {
		onDelivered()
	}
	return nil
}

// DeliverToSubscriber is the watch-aware entry point the CI bridge selects
// when the waker implements it. It resolves the one subscriber from the Watch,
// then counts a wake admitted to a fixer (ATX-43). No caller-supplied role or
// session can stand in for that subscriber. A duplicate wake returns before
// the callback, so the fix counter sees only admitted deliveries.
func (d *AdapterDedupDeliverer) DeliverToSubscriber(ctx context.Context, w domain.Watch, p domain.WakePayload, subscriberID string) error {
	if d == nil || d.Inner == nil {
		return nil
	}
	target, err := SubscriberTarget(w, p.WatchID, subscriberID)
	if err != nil {
		return err
	}
	if want := domain.ObligationKey(p.WatchID, p.Revision, target); p.ObligationID != want {
		return fmt.Errorf("wake: obligation %q does not belong to subscriber %q of watch %s", p.ObligationID, target, p.WatchID)
	}
	adapter := ""
	for _, sub := range w.Subscribers {
		if sub.ID == target {
			adapter = sub.Adapter
			break
		}
	}
	return d.deliver(ctx, adapter, target, p, func() { recordFixerDelivery(w, target) })
}

// Deliver emits a <beacon-event> envelope with only identifiers. The host's
// prompt admission treats the text as untrusted input, never as a command.
// A live HTTP port is used first; the CLI path is the fallback when that port
// is absent. A busy session gets delivery=queue so an in-flight dialogue is not
// cut; an idle one gets delivery=steer with resume=true.
func (d *OpenCodeDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	// The obligation id is domain.ObligationKey(watchID, revision,
	// subscriberID), and the subscriber id is the session that registered the
	// watch. A pointer addressed to another subscriber must never wake this
	// session, even when the caller knows a valid session id.
	if want := domain.ObligationKey(p.WatchID, p.Revision, sessionID); p.ObligationID != want {
		return fmt.Errorf("wake: session %q is not the subscriber of obligation %q", sessionID, p.ObligationID)
	}
	key := wakeDeliveryKey{watchID: p.WatchID, obligationID: p.ObligationID, revision: p.Revision}
	d.mu.Lock()
	if d.seen == nil {
		d.seen = make(map[wakeDeliveryKey]struct{})
	}
	if _, duplicate := d.seen[key]; duplicate {
		d.mu.Unlock()
		observe.WakeDuplicate(p.WatchID, p.Revision)
		return nil
	}
	d.mu.Unlock()

	// Only one owner may deliver to a target at a time. A repeat from the
	// same owner is a duplicate; a different owner is refused before any
	// prompt reaches the pane (T3.242).
	guard := d.leaseGuard()
	acquired, err := guard.Acquire(sessionID, p.ObligationID)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer guard.Release(sessionID, p.ObligationID)

	// The store is the durable half of dedup: a restarted process has an empty
	// seen map but must still not wake the same obligation twice.
	if d.Obligations != nil {
		o, err := d.Obligations.Obligation(p.ObligationID)
		if err == nil && !o.DeliveredAt.IsZero() {
			d.remember(key)
			observe.WakeDuplicate(p.WatchID, p.Revision)
			return nil
		}
		if err != nil && !isObligationMissing(err) {
			return fmt.Errorf("wake: obligation %s: %w", p.ObligationID, err)
		}
	}

	delivery, resume := wakeDeliverySteer, false
	if d.SessionActiveFunc != nil {
		busy, alive, err := d.SessionActiveFunc(ctx, sessionID)
		if err != nil {
			if sessionNotFound(err, nil) {
				return fmt.Errorf("%w: session %s: %v", ErrSessionDead, sessionID, err)
			}
			return fmt.Errorf("wake: session %s status: %w", sessionID, err)
		}
		if !alive {
			return fmt.Errorf("%w: session %s", ErrSessionDead, sessionID)
		}
		if busy {
			delivery = wakeDeliveryQueue
		} else {
			resume = true
		}
	}

	text := Envelope(p)
	if d.ExportPolicy != nil {
		findings := domain.ScanExport(domain.BoundaryContextPack, strings.Split(text, "\n"), *d.ExportPolicy)
		if len(findings) > 0 {
			return fmt.Errorf("%w: %v", ErrExportLeak, findings)
		}
	}
	if d.DraftAware != nil {
		// Draft-aware mode stages the pointer into the composer and presses
		// Enter only after the composer text matches the expected Envelope.
		// A refusal is returned verbatim and does not mark the obligation
		// delivered, so it stays retryable (T3.244).
		if err := d.DraftAware.Deliver(ctx, sessionID, p); err != nil {
			return err
		}
		return d.markDelivered(key, p)
	}
	// Both transports must see the same delivery and resume decision. The CLI
	// fallback carries both in its body. The HTTP port gets them here, and the
	// id-aware port also gets the obligation id for the prompt's message id.
	// Older ports are wrapped so they keep working without the id.
	var prompt func(context.Context, string, string, string, string, bool) error
	switch {
	case d.PromptWithIDFunc != nil:
		prompt = d.PromptWithIDFunc
	case d.PromptWithResumeFunc != nil:
		prompt = func(ctx context.Context, sessionID, text, delivery string, _ string, resume bool) error {
			return d.PromptWithResumeFunc(ctx, sessionID, text, delivery, resume)
		}
	case d.PromptFunc != nil:
		prompt = func(ctx context.Context, sessionID, text, delivery string, _ string, _ bool) error {
			return d.PromptFunc(ctx, sessionID, text, delivery)
		}
	}
	if prompt != nil {
		err := prompt(ctx, sessionID, text, delivery, p.ObligationID, resume)
		switch {
		case err == nil:
			return d.markDelivered(key, p)
		case errors.Is(err, ErrNoPromptPort):
			// No service.json under a plain TUI: fall through to `opencode api`.
		case sessionNotFound(err, nil):
			return fmt.Errorf("%w: session %s: %v", ErrSessionDead, sessionID, err)
		default:
			return fmt.Errorf("wake: prompt session %s: %w", sessionID, err)
		}
	}
	if err := d.deliverCLI(ctx, sessionID, text, delivery, resume); err != nil {
		return err
	}
	return d.markDelivered(key, p)
}

// deliverCLI is the fallback transport: one `opencode api` call under a hard
// timeout, so a hung process cannot park a wake forever.
func (d *OpenCodeDeliverer) deliverCLI(ctx context.Context, sessionID, text, delivery string, resume bool) error {
	body := map[string]any{"text": text, "delivery": delivery}
	if resume {
		body["resume"] = true
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	args := []string{"api", "POST", "/api/session/" + sessionID + "/prompt", "--data", string(raw)}
	cliCtx, cancel := context.WithTimeout(ctx, opencodeCLITimeout)
	defer cancel()
	var out []byte
	if d.Runner != nil {
		out, err = d.Runner(cliCtx, args...)
	} else {
		out, err = exec.CommandContext(cliCtx, "opencode", args...).CombinedOutput()
	}
	if err != nil {
		// CombinedOutput keeps stderr in out. Carry it into the error text so
		// a failed parse or a 404 can be diagnosed from the journal alone.
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			detail = ": " + detail
		}
		if sessionNotFound(err, out) {
			return fmt.Errorf("%w: session %s: %v%s", ErrSessionDead, sessionID, err, detail)
		}
		return fmt.Errorf("wake: opencode api: %w%s", err, detail)
	}
	return verifyAck(out)
}

// remember records the pointer in process memory. It runs only after a host
// admitted the prompt, so a transport failure stays retryable (ADR 0017).
func (d *OpenCodeDeliverer) remember(key wakeDeliveryKey) {
	d.mu.Lock()
	if d.seen == nil {
		d.seen = make(map[wakeDeliveryKey]struct{})
	}
	d.seen[key] = struct{}{}
	d.mu.Unlock()
}

// markDelivered records the fact in both places: memory stops a second prompt
// in this process, SQLite stops one after a restart.
func (d *OpenCodeDeliverer) markDelivered(key wakeDeliveryKey, p domain.WakePayload) error {
	d.remember(key)
	if d.Obligations == nil {
		return nil
	}
	if err := d.Obligations.MarkObligationDelivered(p.ObligationID, time.Now(), 1, ""); err != nil {
		return fmt.Errorf("wake: mark obligation %s delivered: %w", p.ObligationID, err)
	}
	return nil
}

// isObligationMissing accepts the two honest "not staged yet" answers from a
// domain store: sql.ErrNoRows and the domain's WATCH_NOT_FOUND AppError.
func isObligationMissing(err error) bool {
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	var appErr *domain.AppError
	return errors.As(err, &appErr) && appErr.Code == domain.CodeWatchNotFound
}

// sessionNotFound maps a 404 or 409 from either port to the typed ErrSessionDead.
// The text check keeps a transport that only reports a status honest, and reads
// stderr through out so a CLI fallback that only printed the status is caught.
func sessionNotFound(err error, out []byte) bool {
	if errors.Is(err, ErrSessionDead) {
		return true
	}
	msg := string(out)
	if err != nil {
		msg += " " + err.Error()
	}
	return strings.Contains(msg, "404") || strings.Contains(msg, "409") || strings.Contains(strings.ToLower(msg), "not found")
}

// Envelope renders the pointer into the host's wake text. Every field is
// whitelisted: no check body or PR title is included. The explicit id is the
// ObligationKey (p.ObligationID is domain.ObligationKey of this pointer). The
// adapter keys its durable dedup on it, so a replay after a client restart
// cannot steer the same obligation twice (RS-WK-02).
func Envelope(p domain.WakePayload) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"id":           p.ObligationID,
		"watchID":      p.WatchID,
		"obligationID": p.ObligationID,
		"revision":     p.Revision,
		"verdict":      p.Verdict,
	})
	return "<beacon-event>" + strings.TrimSpace(buf.String()) + "</beacon-event>"
}

// WatchTarget resolves the orchestrator session that owns a wake pointer. The
// watchID must name the very Watch the pointer came from: a substituted id
// never resolves, and neither does a Watch without an orchestrator subscriber.
func WatchTarget(w domain.Watch, watchID string) (string, error) {
	if watchID == "" || w.ID != watchID {
		return "", fmt.Errorf("wake: watchID %q does not name watch %q", watchID, w.ID)
	}
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator && strings.HasPrefix(sub.ID, "ses_") {
			return sub.ID, nil
		}
	}
	return "", fmt.Errorf("wake: no orchestrator session on watch %s", watchID)
}

// SubscriberTarget resolves the one session a wake may address on w. The
// addressee must be subscriber.ID on the very Watch the pointer names, with
// role executor, orchestrator or fixer. A fixer wakes like an executor and
// carries FixFor, so it owns its own obligation and may be steered too (ADR
// 0260). No caller session, _meta value, notify target or role-wide broadcast
// can stand in for that id. Seat addresses are not accepted here: the caller
// must resolve one concrete session first (T3.248).
func SubscriberTarget(w domain.Watch, watchID, addressee string) (string, error) {
	if watchID == "" || w.ID != watchID {
		return "", fmt.Errorf("wake: watchID %q does not name watch %q", watchID, w.ID)
	}
	if !domain.ValidSessionID(addressee) {
		return "", fmt.Errorf("wake: subscriber %q on watch %s is not a session", addressee, watchID)
	}
	for _, sub := range w.Subscribers {
		if sub.ID != addressee {
			continue
		}
		if sub.Role != domain.RoleExecutor && sub.Role != domain.RoleOrchestrator && sub.Role != domain.RoleFixer {
			return "", fmt.Errorf("wake: subscriber %q has role %q on watch %s", sub.ID, sub.Role, watchID)
		}
		return sub.ID, nil
	}
	return "", fmt.Errorf("wake: session %q is not a subscriber of watch %s", addressee, watchID)
}

// DeliverToSubscriber wakes exactly one subscriber of w. Both the address and
// the obligation come from the same subscriber.ID, so a wake for one party can
// never be steered into another party's session. A wake admitted to a fixer
// increments the fix counter; executor and orchestrator wakes keep the shared
// delivery counter only (ATX-43, ADR 0260).
func DeliverToSubscriber(ctx context.Context, d domain.WakeDeliverer, w domain.Watch, p domain.WakePayload, subscriberID string) error {
	if d == nil {
		return fmt.Errorf("wake: deliver to subscriber: deliverer is nil")
	}
	target, err := SubscriberTarget(w, p.WatchID, subscriberID)
	if err != nil {
		return err
	}
	if want := domain.ObligationKey(p.WatchID, p.Revision, target); p.ObligationID != want {
		return fmt.Errorf("wake: obligation %q does not belong to subscriber %q of watch %s", p.ObligationID, target, p.WatchID)
	}
	if err := d.Deliver(ctx, target, p); err != nil {
		return err
	}
	recordFixerDelivery(w, target)
	return nil
}

// recordFixerDelivery counts a wake that reached a fixer subscriber. It runs
// only after the deliverer admitted the pointer, so a refused or failed wake
// never raises fix_total. The role comes from the Watch subscriber that owns
// the resolved target, never from a caller-supplied value.
func recordFixerDelivery(w domain.Watch, target string) {
	for _, sub := range w.Subscribers {
		if sub.ID == target && sub.Role == domain.RoleFixer {
			observe.FixDelivered(w.ID)
			return
		}
	}
}

// DeliverToWatch wakes the orchestrator of w through d. It is the legacy
// single-target form: DeliverToSubscriber is the watch-aware path used when a
// verdict names its subscriber explicitly.
func DeliverToWatch(ctx context.Context, d domain.WakeDeliverer, w domain.Watch, p domain.WakePayload) error {
	target, err := WatchTarget(w, p.WatchID)
	if err != nil {
		return err
	}
	return DeliverToSubscriber(ctx, d, w, p, target)
}

// TargetDeliverer sends one identifier-only payload to one opaque wake target.
// Notification routing resolves an event to targets outside this package and
// calls this port for each one. Keeping the transport half here means a route
// never steers a target the wake rules refuse (ADR 0072).
type TargetDeliverer func(ctx context.Context, target, payload string) error

// PromptTargetDeliverer builds a TargetDeliverer over the HTTP prompt port. A
// target that is not a session id has no delivery and makes no transport call.
// The payload is the verdict text the host already treats as untrusted input.
func PromptTargetDeliverer(api *T202API) TargetDeliverer {
	return func(ctx context.Context, target, payload string) error {
		if !strings.HasPrefix(target, "ses_") || len(target) == len("ses_") {
			return fmt.Errorf("wake: invalid notify target %q", target)
		}
		if api == nil {
			return errors.New("wake: prompt target deliverer has no api")
		}
		_, err := api.Prompt(ctx, target, payload, T202DeliverySteer)
		return err
	}
}

// verifyAck checks that the API admitted the prompt. An error response from
// opencode api is already an exec error; this covers a 2xx with an unexpected
// shape so a silently dropped wake is not reported as delivered.
func verifyAck(out []byte) error {
	if len(out) == 0 {
		return fmt.Errorf("wake: empty api response")
	}
	var ack struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return fmt.Errorf("wake: decode api response: %w", err)
	}
	if ack.Data.ID == "" {
		return fmt.Errorf("wake: api response has no admitted message id")
	}
	return nil
}

// leaseGuard returns the injected guard or lazily builds the default one. The
// short critical section shares d.mu with the seen map; the guard has its own
// mutex and never calls back into the deliverer.
func (d *OpenCodeDeliverer) leaseGuard() *DeliveryLeaseGuard {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.LeaseGuard == nil {
		d.LeaseGuard = NewDeliveryLeaseGuard(0)
	}
	return d.LeaseGuard
}

// ===== T3.242: per-target delivery lease =====
// The seen map dedups one pointer, but two different pointers can still reach
// the transport for the same session at the same time. A lease serializes
// delivery per target: one owner holds it, a different owner is refused while
// it is held, and a crashed owner loses it when the ttl passes. Rows can be
// snapshotted and restored so a restarted process keeps a still-live owner.

// ErrTargetBusy is the refusal for a wake whose target already has a lease
// owner. It is retryable and no prompt is sent for the refused wake.
var ErrTargetBusy = errors.New("wake: target busy: delivery lease held")

// defaultDeliveryLeaseTTL bounds a delivery whose process died before it could
// release the lease. It exceeds opencodeCLITimeout so a slow transport cannot
// lose its lease while a prompt is still in flight; the cost is that a crashed
// owner parks its target for this long.
const defaultDeliveryLeaseTTL = 30 * time.Second

// TargetLeaseRecord is the durable shape of one lease. A caller persists these
// rows outside this package and feeds them back through Restore after a
// restart.
type TargetLeaseRecord struct {
	Target  string    `json:"target"`
	Owner   string    `json:"owner"`
	Expires time.Time `json:"expires"`
}

// DeliveryLeaseGuard grants at most one owner per wake target. Acquire returns
// acquired=true when the caller took the lease, acquired=false with a nil
// error when the same owner already holds it, and ErrTargetBusy when a
// different owner holds it.
type DeliveryLeaseGuard struct {
	mu     sync.Mutex
	ttl    time.Duration
	now    func() time.Time
	leases map[string]TargetLeaseRecord
}

// NewDeliveryLeaseGuard builds an empty guard. A non-positive ttl uses the
// package default.
func NewDeliveryLeaseGuard(ttl time.Duration) *DeliveryLeaseGuard {
	if ttl <= 0 {
		ttl = defaultDeliveryLeaseTTL
	}
	return &DeliveryLeaseGuard{ttl: ttl, now: time.Now, leases: make(map[string]TargetLeaseRecord)}
}

// SetClock replaces the clock. Tests expire a lease with it instead of
// sleeping.
func (g *DeliveryLeaseGuard) SetClock(now func() time.Time) {
	if g == nil || now == nil {
		return
	}
	g.mu.Lock()
	g.now = now
	g.mu.Unlock()
}

// Restore seeds the guard from durable rows. Expired rows are dropped so a
// restarted process does not inherit a lease that can no longer be held.
func (g *DeliveryLeaseGuard) Restore(rows []TargetLeaseRecord) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.leases == nil {
		g.leases = make(map[string]TargetLeaseRecord)
	}
	now := g.now()
	for _, row := range rows {
		if row.Target == "" || row.Owner == "" || !row.Expires.After(now) {
			continue
		}
		g.leases[row.Target] = row
	}
}

// Snapshot returns live leases sorted by target. A caller persists these rows
// and passes them to Restore after a restart.
func (g *DeliveryLeaseGuard) Snapshot() []TargetLeaseRecord {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	out := make([]TargetLeaseRecord, 0, len(g.leases))
	for _, row := range g.leases {
		if row.Expires.After(now) {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// Acquire takes the lease for target. Repeating the call with the same owner
// is a duplicate that reports acquired=false; a different owner is refused.
func (g *DeliveryLeaseGuard) Acquire(target, owner string) (bool, error) {
	if g == nil || target == "" || owner == "" {
		return false, ErrTargetBusy
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.leases == nil {
		g.leases = make(map[string]TargetLeaseRecord)
	}
	now := g.now()
	if row, ok := g.leases[target]; ok && row.Expires.After(now) {
		if row.Owner == owner {
			return false, nil
		}
		return false, fmt.Errorf("%w: %s held by %s", ErrTargetBusy, target, row.Owner)
	}
	g.leases[target] = TargetLeaseRecord{Target: target, Owner: owner, Expires: now.Add(g.ttl)}
	return true, nil
}

// Release frees target only when owner still holds it. A stale owner whose
// lease expired and was reclaimed cannot free the new owner's lease.
func (g *DeliveryLeaseGuard) Release(target, owner string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if row, ok := g.leases[target]; ok && row.Owner == owner {
		delete(g.leases, target)
	}
}

// ===== T3.202 steer/queue и interrupt (#287) =====
// Package wake encodes the delivery rules for waking a parked OpenCode
// session. It chooses steer or queue at admission, plans how an input is
// consumed at the active or idle boundary, and issues the three calls the host
// T202API exposes: prompt, interrupt and inbox patch. It never invents a verdict
// and never merges a PR (ADR 0074, 0075, 0076, 0077, 0078, 0079).

// T202DeliveryMode is the ADR 0077 choice made once at prompt admission.
type T202DeliveryMode string

const (
	T202DeliverySteer T202DeliveryMode = "steer"
	T202DeliveryQueue T202DeliveryMode = "queue"
)

// T202ExecutionState says whether the target session is running a step.
type T202ExecutionState string

const (
	T202ExecIdle   T202ExecutionState = "idle"
	T202ExecActive T202ExecutionState = "active"
)

// T202InputKind separates inputs a prompt hook may rewrite from inputs it never
// sees. ADR 0077: no hook for synthetic, shell, compaction or move.
type T202InputKind string

const (
	T202InputPrompt    T202InputKind = "prompt"
	T202InputSynthetic T202InputKind = "synthetic"
	T202InputShell     T202InputKind = "shell"
	T202InputCompact   T202InputKind = "compact"
	T202InputMove      T202InputKind = "move"
)

// T202HooksApply reports whether a prompt hook may change the delivery mode. A
// hook fires once at admission and never for a synthetic or system input.
func T202HooksApply(k T202InputKind) bool { return k == T202InputPrompt }

// T202DefaultDelivery is the admission default. Prompt, synthetic and compact
// start as steer; a hook may demote an eligible input to queue once.
func T202DefaultDelivery(k T202InputKind) T202DeliveryMode {
	if k == T202InputPrompt || k == T202InputSynthetic || k == T202InputCompact {
		return T202DeliverySteer
	}
	return T202DeliveryQueue
}

// T202Admission is where an admitted input is consumed.
type T202Admission struct {
	Mode           T202DeliveryMode
	SchedulesLoop  bool
	AtStepBoundary bool
	IdlePriority   bool
	Parked         bool
}

// T202PlanAdmission applies ADR 0077 to a delivery mode and an execution state.
// steer schedules the loop, is consumed at the next safe step boundary while
// the loop is active, and wins at idle. queue never schedules: it stays parked
// in enqueue order.
func T202PlanAdmission(mode T202DeliveryMode, state T202ExecutionState) T202Admission {
	switch mode {
	case T202DeliverySteer:
		a := T202Admission{Mode: T202DeliverySteer, SchedulesLoop: true, IdlePriority: true}
		if state == T202ExecActive {
			a.AtStepBoundary = true
		}
		return a
	case T202DeliveryQueue:
		return T202Admission{Mode: T202DeliveryQueue, Parked: true}
	default:
		return T202Admission{Mode: mode}
	}
}

// T202AfterInterrupt returns an input's placement after POST interrupt. The resume
// flag governs the running loop, never the inbox: a queued input stays parked
// for both resume=true and resume=false (ADR 0077, 0079).
func T202AfterInterrupt(mode T202DeliveryMode, resume bool) T202Admission {
	return T202PlanAdmission(mode, T202ExecActive)
}

// T202PendingInput is one admitted input waiting in the inbox.
type T202PendingInput struct {
	ID   string
	Mode T202DeliveryMode
}

// T202NextInput picks the input consumed when the session reaches idle. Steers win
// over queued items regardless of enqueue order (ADR 0077).
func T202NextInput(pending []T202PendingInput) (T202PendingInput, bool) {
	for _, p := range pending {
		if p.Mode == T202DeliverySteer {
			return p, true
		}
	}
	for _, p := range pending {
		if p.Mode == T202DeliveryQueue {
			return p, true
		}
	}
	return T202PendingInput{}, false
}

// T202AdmissionStatus is the classified outcome of a prompt response.
type T202AdmissionStatus string

const (
	T202StatusAdmitted    T202AdmissionStatus = "admitted"
	T202StatusDuplicateID T202AdmissionStatus = "duplicate-id"
	T202StatusError       T202AdmissionStatus = "error"
)

// T202ClassifyHTTP maps a response code to an admission outcome. ADR 0077: 409 is
// a duplicate message id, not a busy session. There is no busy-session status,
// because an active loop consumes steer at its next step boundary.
func T202ClassifyHTTP(code int) T202AdmissionStatus {
	switch code {
	case 200, 201, 202, 204:
		return T202StatusAdmitted
	case 409:
		return T202StatusDuplicateID
	default:
		return T202StatusError
	}
}

// T202BusySession reports whether a status means the session refused work as
// busy. It never does; the function exists so callers stop reading 409 that
// way (ADR 0077).
func T202BusySession(s T202AdmissionStatus) bool { return false }

// T202Pointer is the identifier-only payload a verdict source hands to a wake
// adapter. It never carries check bodies or PR text (ADR 0074).
type T202Pointer struct {
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID"`
	Revision     uint64 `json:"revision"`
	Verdict      string `json:"verdict"`
}

// T202Envelope renders a pointer as the untrusted wake text admitted to a session.
// The explicit id is the ObligationKey, the same value carried in obligationID.
func T202Envelope(p T202Pointer) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"id":           p.ObligationID,
		"watchID":      p.WatchID,
		"obligationID": p.ObligationID,
		"revision":     p.Revision,
		"verdict":      p.Verdict,
	})
	return "<beacon-event>" + strings.TrimSpace(buf.String()) + "</beacon-event>"
}

// T202Runner executes an `opencode api` invocation. Tests replace it.
type T202Runner func(ctx context.Context, args ...string) ([]byte, error)

// T202API is a thin client over the opencode api CLI (ADR 0078).
type T202API struct {
	T202Runner T202Runner
}

func (a *T202API) run(ctx context.Context, args ...string) ([]byte, error) {
	if a.T202Runner != nil {
		return a.T202Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).CombinedOutput()
}

// Prompt admits one session input and returns its message id. mode is the
// explicit delivery choice: steer schedules execution, queue only parks.
func (a *T202API) Prompt(ctx context.Context, sessionID, text string, mode T202DeliveryMode) (string, error) {
	if !strings.HasPrefix(sessionID, "ses_") {
		return "", fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	// A subscriber may already have finished its turn (outcome succeeded) when
	// the verdict arrives. The prompt route accepts resume to continue such a
	// session, so a wake prompt sets it explicitly (OpenAPI prompt.resume).
	body := map[string]any{"text": text, "delivery": string(mode), "resume": true}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	args := []string{"api", "POST", "/api/session/" + sessionID + "/prompt", "--data", string(raw)}
	out, err := a.run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("wake: opencode api prompt: %w", err)
	}
	return T202messageID(out)
}

// Park admits a queued input that does not schedule execution (ADR 0076).
func (a *T202API) Park(ctx context.Context, sessionID, text string) (string, error) {
	return a.Prompt(ctx, sessionID, text, T202DeliveryQueue)
}

// Interrupt posts POST /api/session/{id}/interrupt?resume=<bool>. resume=true
// stops the running loop but leaves queued inputs parked; resume=false is the
// admit-only stop (ADR 0079).
func (a *T202API) Interrupt(ctx context.Context, sessionID string, resume bool) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	path := "/api/session/" + sessionID + "/interrupt?resume=" + strconv.FormatBool(resume)
	if _, err := a.run(ctx, "api", "POST", path); err != nil {
		return fmt.Errorf("wake: opencode api interrupt: %w", err)
	}
	return nil
}

// Promote patches a parked inbox item to steer so the session wakes (ADR 0076).
func (a *T202API) Promote(ctx context.Context, sessionID, inboxID string) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	path := "/api/session/" + sessionID + "/inbox/" + inboxID
	raw := []byte(`{"delivery":"steer"}`)
	if _, err := a.run(ctx, "api", "PATCH", path, "--data", string(raw)); err != nil {
		return fmt.Errorf("wake: opencode api promote: %w", err)
	}
	return nil
}

// T202messageID reads data.id from a prompt response. An empty id is a silent
// drop and is never reported as admitted.
func T202messageID(out []byte) (string, error) {
	if len(out) == 0 {
		return "", fmt.Errorf("wake: empty api response")
	}
	var ack struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return "", fmt.Errorf("wake: decode api response: %w", err)
	}
	if ack.Data.ID == "" {
		return "", fmt.Errorf("wake: api response has no admitted message id")
	}
	return ack.Data.ID, nil
}

// ===== T3.206 Delivery status vocabulary (#288) =====
// The status words and the two reuse errors below are shared with the Cursor
// adapter at the end of this file. The webhook journal prototype that used to
// live here was never connected to a daemon and is gone.

// T206DeliveryStatus is the honest outcome of a wake attempt.
type T206DeliveryStatus string

const (
	T206StatusStarted     T206DeliveryStatus = "started"
	T206StatusQueued      T206DeliveryStatus = "queued"
	T206StatusDelivered   T206DeliveryStatus = "delivered"
	T206StatusDuplicate   T206DeliveryStatus = "duplicate"
	T206StatusClosed      T206DeliveryStatus = "closed"
	T206StatusUnsupported T206DeliveryStatus = "unsupported"
)

// T206DeliveryResult reports what the adapter did with one delivery.
type T206DeliveryResult struct {
	DeliveryID string             `json:"deliveryId"`
	Status     T206DeliveryStatus `json:"status"`
	Coalesced  bool               `json:"coalesced"`
}

var (
	T206ErrDeliveryIDRequired = errors.New("wake: deliveryId is required")
	T206ErrDeliveryIDReuse    = errors.New("wake: deliveryId reused with a different body")
)

// ===== T3.210-T3.215: universal wake bridge (ADR 0069, 0071, 0072, 0075) =====

// T210Target is an opaque session handle. Only the adapter that produced it
// may interpret it. The core never parses it and never guesses a session.
type T210Target string

// T210WakePointer carries identifiers only. Check names, PR titles and branch
// names never enter the bridge.
type T210WakePointer struct {
	Adapter      string `json:"adapter,omitempty"`
	Target       string `json:"target,omitempty"`
	WatchID      string `json:"watchID,omitempty"`
	ObligationID string `json:"obligationID,omitempty"`
	Revision     uint64 `json:"revision,omitempty"`
	VerdictRef   string `json:"verdictRef,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	Delivery     string `json:"delivery,omitempty"`
	Resume       *bool  `json:"resume,omitempty"`
}

// T210Receipt records one delivery attempt. AgentID and ConfigID carry the
// registry identity of the executor that admitted the wake, so a reader can
// resolve the receipt to its registry entry (T5.45). Both are optional: an
// older receipt has neither field and still decodes to empty strings.
type T210Receipt struct {
	ObligationID string `json:"obligationID"`
	Attempt      int    `json:"attempt"`
	Delivery     string `json:"delivery"`
	Outcome      string `json:"outcome"`
	AgentID      string `json:"agent_id,omitempty"`
	ConfigID     string `json:"config_id,omitempty"`
}

// RegistryKey returns the pair that identifies the registry entry behind this
// delivery. The wake layer cannot import the store, so it carries the key and
// leaves resolution to the layer that owns the registry.
func (r T210Receipt) RegistryKey() (agentID, configID string) {
	return r.AgentID, r.ConfigID
}

const (
	T210Delivered = "delivered"
	T210Queued    = "queued"
	T210Enqueued  = "enqueued"
	T210Skipped   = "skipped"
	T210Errored   = "error"
)

// T210WakeAdapter is the minimal contract shared by every environment.
// Observe and Dispose stay optional so an adapter with only bind and deliver
// is a valid implementation.
type T210WakeAdapter interface {
	Bind(p T210WakePointer, sessions *T211SessionRegistry) (T210Target, bool)
	Deliver(p T210WakePointer, target T210Target) (T210Receipt, error)
}

type T210ObservingAdapter interface {
	Observe(p T210WakePointer, r T210Receipt) error
}

type T210DisposingAdapter interface {
	Dispose(p T210WakePointer) error
}

// T210PointerSource yields pointers and commits progress after delivery.
type T210PointerSource interface {
	Next() (T210WakePointer, bool)
	Commit(p T210WakePointer) error
}

// ValidateT210Target checks that an explicit subscriber address and its
// obligation name the same party. A pointer whose target was replaced by the
// current caller's session or by _meta fails here, before any adapter binds.
// Opaque non-session targets are left to the adapter's own registry.
func ValidateT210Target(p T210WakePointer) error {
	if p.Target == "" {
		return fmt.Errorf("wake: empty subscriber target")
	}
	if !strings.HasPrefix(p.Target, "ses_") || len(p.Target) == len("ses_") {
		return nil
	}
	if strings.Contains(p.ObligationID, ":") {
		if want := domain.ObligationKey(p.WatchID, p.Revision, p.Target); p.ObligationID != want {
			return fmt.Errorf("wake: subscriber %q does not own obligation %q", p.Target, p.ObligationID)
		}
	}
	return nil
}

// T210Deliver is the central path. It binds through the registry only. A
// pointer with no exact match is skipped, never routed to a guessed session.
func T210Deliver(ctx context.Context, p T210WakePointer, src T210PointerSource, sessions *T211SessionRegistry, adapters *T211AdapterRegistry, deliveries *T211DeliveryRegistry) (T210Receipt, bool) {
	// One span per delivery attempt, on every exit path. Only identifiers
	// ride on it: the wake envelope and its prompt text never enter a span.
	// The process tracer is a no-op unless a caller installed one (T3.394).
	_, end := observe.Current().Start(ctx, "beacon.wake.deliver",
		observe.String("watchID", p.WatchID),
		observe.String("obligation_id", p.ObligationID))
	defer end()
	started := time.Now()
	if err := ValidateT210Target(p); err != nil {
		observe.WakeSkipped(p.Adapter, p.WatchID, "invalid wake target: "+err.Error())
		log.Printf("wake: skip pointer %s: %v", p.ObligationID, err)
		T210settle(src, p, deliveries, T210Receipt{ObligationID: p.ObligationID, Attempt: p.Attempt, Delivery: p.Delivery, Outcome: T210Skipped}, nil)
		return T210Receipt{}, false
	}
	adapter, ok := adapters.Get(p.Adapter)
	if !ok {
		observe.WakeSkipped(p.Adapter, p.WatchID, "no wake adapter registered")
		T210settle(src, p, deliveries, T210Receipt{ObligationID: p.ObligationID, Attempt: p.Attempt, Outcome: T210Skipped}, nil)
		return T210Receipt{}, false
	}
	target, ok := adapter.Bind(p, sessions)
	if !ok {
		observe.WakeSkipped(p.Adapter, p.WatchID, "adapter could not bind target")
		r := T210Receipt{ObligationID: p.ObligationID, Attempt: p.Attempt, Delivery: p.Delivery, Outcome: T210Skipped}
		T210settle(src, p, deliveries, r, adapter)
		return r, false
	}
	// An adapter must not substitute a different session for an explicit
	// subscriber address, even when its own registry maps that address.
	// Opaque non-session targets stay under the adapter's own registry.
	if strings.HasPrefix(p.Target, "ses_") && string(target) != p.Target {
		observe.WakeSkipped(p.Adapter, p.WatchID, "adapter bound a different session")
		log.Printf("wake: skip pointer %s: adapter bound %q, subscriber %q", p.ObligationID, target, p.Target)
		r := T210Receipt{ObligationID: p.ObligationID, Attempt: p.Attempt, Delivery: p.Delivery, Outcome: T210Skipped}
		T210settle(src, p, deliveries, r, adapter)
		return r, false
	}
	r, err := adapter.Deliver(p, target)
	observe.WakeDuration(p.Adapter, time.Since(started))
	if err != nil {
		observe.WakeFailed(p.Adapter, p.WatchID, err, p.Revision)
		r = T210Receipt{ObligationID: p.ObligationID, Attempt: p.Attempt, Delivery: p.Delivery, Outcome: T210Errored}
	} else {
		switch r.Outcome {
		case T210Delivered, T210Queued, T210Enqueued:
			observe.WakeDeliveredSource(p.Adapter, p.WatchID, observe.WakeSourceDaemon, p.Revision)
		case T210Errored:
			observe.WakeFailed(p.Adapter, p.WatchID, errors.New(r.Outcome), p.Revision)
		default:
			observe.WakeSkipped(p.Adapter, p.WatchID, "adapter outcome: "+r.Outcome)
		}
	}
	// The bound target is the opaque executor id. An adapter may return an
	// empty target, so fall back to the session registry that mapped the
	// pointer to its session (T5.45).
	agentID := string(target)
	if agentID == "" {
		if sessionID, ok := sessions.Lookup(p.Adapter, p.Target); ok {
			agentID = sessionID
		}
	}
	if r.AgentID == "" {
		r.AgentID = agentID
	}
	T210settle(src, p, deliveries, r, adapter)
	return r, true
}

func T210settle(src T210PointerSource, p T210WakePointer, deliveries *T211DeliveryRegistry, r T210Receipt, adapter T210WakeAdapter) {
	// A ses_ target is the opaque executor id from _meta (ADR 0086/0094). Copy
	// it into the receipt when no adapter supplied one, so the receipt names
	// the registry entry that admitted the wake (T5.45).
	if r.AgentID == "" && strings.HasPrefix(p.Target, "ses_") {
		r.AgentID = p.Target
	}
	if adapter != nil {
		if obs, ok := adapter.(T210ObservingAdapter); ok {
			_ = obs.Observe(p, r)
		}
		if d, ok := adapter.(T210DisposingAdapter); ok {
			_ = d.Dispose(p)
		}
	}
	if deliveries != nil {
		deliveries.ImportReceipts([]T210Receipt{r})
	}
	if src != nil {
		_ = src.Commit(p)
	}
}

// ===== T3.211: three explicit registries =====

type T211SessionKey struct {
	Adapter string
	Target  string
}

// T211SessionRegistry maps an opaque adapter/target pair to a session ID.
type T211SessionRegistry struct {
	mu    sync.RWMutex
	byKey map[T211SessionKey]string
}

func (r *T211SessionRegistry) Add(adapter, target, sessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byKey == nil {
		r.byKey = make(map[T211SessionKey]string)
	}
	r.byKey[T211SessionKey{Adapter: adapter, Target: target}] = sessionID
}

func (r *T211SessionRegistry) Lookup(adapter, target string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byKey[T211SessionKey{Adapter: adapter, Target: target}]
	return s, ok
}

// AddFromMeta fills the registry from the MCP caller's _meta and notify.target.
// The values stay opaque strings here, so no environment name reaches the core.
func (r *T211SessionRegistry) AddFromMeta(adapter string, meta map[string]string, notifyTarget string) bool {
	target := meta["ai.opencode/sessionID"]
	if target == "" {
		target = meta["beacon/sessionID"]
	}
	if target == "" {
		target = notifyTarget
	}
	if target == "" {
		return false
	}
	sessionID := meta["beacon/sessionID"]
	if sessionID == "" {
		sessionID = target
	}
	r.Add(adapter, target, sessionID)
	return true
}

// T211AdapterRegistry keeps adapter names opaque; the core does not switch on
// them and one beacon_watch_pr can be served by two bridges at once.
type T211AdapterRegistry struct {
	mu       sync.RWMutex
	adapters map[string]T210WakeAdapter
}

func T211NewAdapterRegistry() *T211AdapterRegistry {
	return &T211AdapterRegistry{adapters: make(map[string]T210WakeAdapter)}
}

func (r *T211AdapterRegistry) Register(name string, a T210WakeAdapter) {
	if r == nil || a == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adapters == nil {
		r.adapters = make(map[string]T210WakeAdapter)
	}
	r.adapters[name] = a
}

func (r *T211AdapterRegistry) Get(name string) (T210WakeAdapter, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

type T211DeliveryKey struct {
	ObligationID string
	Attempt      int
}

// T211DeliveryRegistry imports receipts from files into one in-memory view.
type T211DeliveryRegistry struct {
	mu       sync.RWMutex
	receipts map[T211DeliveryKey]T210Receipt
}

// ImportReceipts stores unseen (obligationID, attempt) pairs and returns the
// number of newly imported receipts.
func (r *T211DeliveryRegistry) ImportReceipts(in []T210Receipt) int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.receipts == nil {
		r.receipts = make(map[T211DeliveryKey]T210Receipt)
	}
	added := 0
	for _, receipt := range in {
		k := T211DeliveryKey{ObligationID: receipt.ObligationID, Attempt: receipt.Attempt}
		if _, ok := r.receipts[k]; ok {
			continue
		}
		r.receipts[k] = receipt
		added++
	}
	return added
}

func (r *T211DeliveryRegistry) Has(obligationID string, attempt int) bool {
	_, ok := r.Get(obligationID, attempt)
	return ok
}

func (r *T211DeliveryRegistry) Get(obligationID string, attempt int) (T210Receipt, bool) {
	if r == nil {
		return T210Receipt{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	receipt, ok := r.receipts[T211DeliveryKey{ObligationID: obligationID, Attempt: attempt}]
	return receipt, ok
}

// ===== T3.212: prompt text with identifiers only =====

// T212escapeUntrusted escapes every character that could turn an identifier
// into markup, a JSON string delimiter or a control sequence.
func T212escapeUntrusted(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(fmt.Sprintf("\\u%04x", r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func T212clamp(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

type T212PromptData struct {
	ID           string `json:"id"`
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID"`
	Revision     uint64 `json:"revision"`
	VerdictRef   string `json:"verdictRef"`
}

// T212buildPromptText returns the <beacon-event> envelope. It has no check
// name, PR title or branch, and never exceeds 512 bytes. ID is the explicit
// notification id, set to the same ObligationKey as obligationID.
//
// The size cap works on whole JSON fields, never on the marshaled bytes.
// Trimming the serialized text with text[:keep] can split a string token and
// leave invalid JSON, which the wake receiver cannot parse. Instead we shrink
// the string values by runes and marshal again until the envelope fits. Each
// attempt produces a complete JSON object, so the result stays parseable.
func T212buildPromptText(p T210WakePointer) string {
	const (
		limit    = 512
		maxField = 96
	)
	prefix := "<beacon-event>"
	suffix := "</beacon-event>"

	watched := T212escapeUntrusted(p.WatchID)
	obligation := T212escapeUntrusted(p.ObligationID)
	verdict := T212escapeUntrusted(p.VerdictRef)

	build := func(fieldCap int) T212PromptData {
		ob := T212clamp(obligation, fieldCap)
		return T212PromptData{
			ID:           ob,
			WatchID:      T212clamp(watched, fieldCap),
			ObligationID: ob,
			Revision:     p.Revision,
			VerdictRef:   T212clamp(verdict, fieldCap),
		}
	}
	marshal := func(d T212PromptData) []byte {
		raw, err := json.Marshal(d)
		if err != nil {
			return []byte(`{}`)
		}
		return raw
	}

	fieldCap := maxField
	raw := marshal(build(fieldCap))
	for len(prefix)+len(raw)+len(suffix) > limit && fieldCap > 0 {
		fieldCap /= 2
		raw = marshal(build(fieldCap))
	}

	return prefix + string(raw) + suffix
}

// ===== T3.222 канонический ключ _meta для сессии (#320) =====
// A subscriber target is an opaque string. The neutral key beacon/sessionID is
// canonical; ai.opencode/sessionID stays readable for callers that still send
// it. A watch with neither _meta nor an explicit notify.target is skipped: no
// prompt and no receipt, which is the W01 negative control.

const (
	SessionIDMetaKey       = "beacon/sessionID"
	LegacySessionIDMetaKey = "ai.opencode/sessionID"
)

// ErrNoTarget is the skip outcome for a watch with no deliverable target. It is
// not a transport failure: no prompt is sent and no receipt is written.
var ErrNoTarget = errors.New("wake: skip: no session target for watch")

// SessionIDFromMeta reads the caller's opaque session id from _meta. The
// neutral key wins; the legacy key is accepted for compatibility. A non-string
// or empty value is absent, and the value is never parsed or validated.
func SessionIDFromMeta(meta map[string]any) string {
	for _, key := range []string{SessionIDMetaKey, LegacySessionIDMetaKey} {
		if raw, ok := meta[key]; ok {
			if s, ok := raw.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// ResolveSessionTarget chooses one target for a watch. An explicit
// notify.target is the fallback when neither _meta key is present. With no
// source at all the watch must be skipped, not guessed.
func ResolveSessionTarget(meta map[string]any, notifyTarget string) (string, bool) {
	if id := SessionIDFromMeta(meta); id != "" {
		return id, true
	}
	if target := strings.TrimSpace(notifyTarget); target != "" {
		return target, true
	}
	return "", false
}

// SessionRegistry records the watchID -> target binding used by W01. Targets
// are opaque strings; the registry never interprets a session id or a harness
// name, and it never invents a binding for a watch with no target.
type SessionRegistry struct {
	mu      sync.RWMutex
	targets map[string]map[string]string
}

// NewSessionRegistry builds an empty binding registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{targets: make(map[string]map[string]string)}
}

// Bind records target for one subscriber of a watch. Empty identifiers and
// empty targets are ignored so a missing _meta cannot create a row.
func (r *SessionRegistry) Bind(watchID, subscriberID, target string) bool {
	if r == nil || watchID == "" || target == "" {
		return false
	}
	if subscriberID == "" {
		subscriberID = "*"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.targets == nil {
		r.targets = make(map[string]map[string]string)
	}
	rows := r.targets[watchID]
	if rows == nil {
		rows = make(map[string]string)
		r.targets[watchID] = rows
	}
	rows[subscriberID] = target
	return true
}

// BindWatch resolves the target from _meta or notify.target and binds it. A
// watch with no source returns ErrNoTarget and leaves no row behind.
func (r *SessionRegistry) BindWatch(watchID, subscriberID string, meta map[string]any, notifyTarget string) (string, error) {
	target, ok := ResolveSessionTarget(meta, notifyTarget)
	if !ok {
		return "", ErrNoTarget
	}
	if !r.Bind(watchID, subscriberID, target) {
		return "", ErrNoTarget
	}
	return target, nil
}

// Target returns the binding for one subscriber, or false when none exists.
func (r *SessionRegistry) Target(watchID, subscriberID string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	target, ok := r.targets[watchID][subscriberID]
	return target, ok
}

// Bound reports whether target is registered for watchID by any subscriber.
func (r *SessionRegistry) Bound(watchID, target string) bool {
	if r == nil || target == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, bound := range r.targets[watchID] {
		if bound == target {
			return true
		}
	}
	return false
}

// Rows returns one watchID -> target row per bound watch, sorted by watchID.
func (r *SessionRegistry) Rows() map[string]string {
	out := make(map[string]string)
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for watchID, rows := range r.targets {
		if len(rows) == 0 {
			continue
		}
		keys := make([]string, 0, len(rows))
		for sub := range rows {
			keys = append(keys, sub)
		}
		sort.Strings(keys)
		out[watchID] = rows[keys[0]]
	}
	return out
}

// BoundDeliverer refuses to send a pointer to a session that was never bound
// to the watch. Without a binding it returns ErrNoTarget before the inner
// deliverer runs, so W01 produces neither a prompt nor a receipt.
type BoundDeliverer struct {
	Registry *SessionRegistry
	Inner    domain.WakeDeliverer
}

var _ domain.WakeDeliverer = (*BoundDeliverer)(nil)

// Deliver checks the binding before handing the pointer to the inner adapter.
func (d *BoundDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	if d == nil || d.Registry == nil || d.Inner == nil {
		return ErrNoTarget
	}
	if !d.Registry.Bound(p.WatchID, sessionID) {
		return ErrNoTarget
	}
	return d.Inner.Deliver(ctx, sessionID, p)
}

// ===== T3.223 живучесть локации OpenCode (#320) =====
// An idle OpenCode location is unloaded, which takes the plugin out of
// /api/plugin and stops wake polling. The chosen mitigation is to disable the
// idle timeout. W12 records an observed fact instead of trusting an idle
// report: the spike unloaded at about 2m47s with 2 minutes and held the plugin
// for more than 3m39s with the timeout disabled.

type LocationMitigation string

const (
	LocationMitigationFalse     LocationMitigation = "location_idle_timeout=false"
	LocationMitigationKeepalive LocationMitigation = "external_keepalive"
	LocationMitigationAlarm     LocationMitigation = "external_alarm"
)

// locationHoldFloor is the minimum idle period W12 must observe.
const locationHoldFloor = 3 * time.Minute

// LocationConfig records the mitigation chosen for a host. IdleTimeout is the
// configured value as written, so "false" and "2 minutes" are both valid.
type LocationConfig struct {
	Mitigation  LocationMitigation
	IdleTimeout string
}

// LocationObservation is an external fact: how long the host was idle and
// whether the adapter was still in /api/plugin at the end of that period.
type LocationObservation struct {
	Mitigation   LocationMitigation
	IdleFor      time.Duration
	PluginActive bool
}

// ValidateLocationLiveness proves that the chosen mitigation survived an idle
// period longer than three minutes. A short observation, a different
// mitigation or a vanished plugin is not proof, even if the host reported
// itself idle.
func ValidateLocationLiveness(cfg LocationConfig, obs LocationObservation) error {
	if cfg.Mitigation == "" {
		return errors.New("wake: location: no mitigation chosen")
	}
	if obs.Mitigation != cfg.Mitigation {
		return fmt.Errorf("wake: location: observation used %q, want %q", obs.Mitigation, cfg.Mitigation)
	}
	if obs.IdleFor <= locationHoldFloor {
		return fmt.Errorf("wake: location: idle %s does not exceed %s", obs.IdleFor, locationHoldFloor)
	}
	if !obs.PluginActive {
		return errors.New("wake: location: adapter disappeared from /api/plugin")
	}
	return nil
}

// W12LocationFacts are the measured spike results recorded with the card. They
// are observations, not a promise: a fresh observation still has to pass
// ValidateLocationLiveness for a new host.
var W12LocationFacts = struct {
	ShortTimeout       string
	UnloadedAfter      time.Duration
	ChosenMitigation   LocationMitigation
	HeldWithMitigation time.Duration
}{
	ShortTimeout:       "2 minutes",
	UnloadedAfter:      2*time.Minute + 47*time.Second,
	ChosenMitigation:   LocationMitigationFalse,
	HeldWithMitigation: 3*time.Minute + 39*time.Second,
}

// ===== T3.224 дедупликация доставки в Cursor Cloud Agents (#320) =====
// Cursor's steer body only accepts prompt and delivery; deliveryId is not a
// field the API will echo back. A steer retried after a network error can
// therefore reach the agent twice. The adapter owns the deliveryId and
// coalesces the repeat before the transport runs.

// CursorSteer is the entire accepted steer body. DeliveryID is deliberately
// absent: adding it returns 400 from the Cursor v1 API.
type CursorSteer struct {
	Prompt   string `json:"prompt"`
	Delivery string `json:"delivery"`
}

// NewCursorSteer builds the minimal body for a Cursor steer.
func NewCursorSteer(text, delivery string) CursorSteer {
	return CursorSteer{Prompt: text, Delivery: delivery}
}

// CursorTransport performs the external steer call.
type CursorTransport func(ctx context.Context, steer CursorSteer) (T206DeliveryStatus, error)

// CursorDeliveryRecord is the adapter-side memory of one admitted delivery.
type CursorDeliveryRecord struct {
	DeliveryID string
	BodyHash   string
	Status     T206DeliveryStatus
}

// CursorDeliveryJournal keeps one body hash per deliveryId. A staged entry
// survives a transport error, so a retry is a duplicate rather than a second
// job even when the first call may have reached the agent.
type CursorDeliveryJournal struct {
	mu   sync.Mutex
	byID map[string]CursorDeliveryRecord
}

// NewCursorDeliveryJournal builds an empty delivery journal.
func NewCursorDeliveryJournal() *CursorDeliveryJournal {
	return &CursorDeliveryJournal{byID: make(map[string]CursorDeliveryRecord)}
}

// Get reports the journaled outcome for a deliveryId without calling the
// transport. It is the read side used by observability and by tests.
func (j *CursorDeliveryJournal) Get(deliveryID string) (T206DeliveryResult, bool) {
	if j == nil {
		return T206DeliveryResult{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	rec, ok := j.byID[deliveryID]
	if !ok {
		return T206DeliveryResult{}, false
	}
	return T206DeliveryResult{DeliveryID: rec.DeliveryID, Status: rec.Status}, true
}

// CursorDeliverer is the adapter-side dedup for Cursor cloud agents.
type CursorDeliverer struct {
	Journal   *CursorDeliveryJournal
	Transport CursorTransport
}

var _ domain.WakeDeliverer = (*CursorDeliverer)(nil)

// Deliver sends one pointer under its obligationID. The obligationID is fixed
// before the call, so a retry of the same obligation cannot start a second
// wake.
func (d *CursorDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	text := fmt.Sprintf("WAKE watchID=%s obligationID=%s revision=%d verdict=%s", p.WatchID, p.ObligationID, p.Revision, p.Verdict)
	_, err := d.DeliverSteer(ctx, p.ObligationID, NewCursorSteer(text, "steer"))
	return err
}

// DeliverSteer admits one steer by deliveryId. A repeat with the same body is
// coalesced without calling the transport again; a repeat with a different
// body is rejected, the same rule Cursor applies to a reused Idempotency-Key.
func (d *CursorDeliverer) DeliverSteer(ctx context.Context, deliveryID string, steer CursorSteer) (T206DeliveryResult, error) {
	if d == nil || d.Journal == nil {
		return T206DeliveryResult{}, errors.New("wake: cursor deliverer has no journal")
	}
	if strings.TrimSpace(deliveryID) == "" {
		return T206DeliveryResult{}, T206ErrDeliveryIDRequired
	}
	if d.Transport == nil {
		return T206DeliveryResult{}, errors.New("wake: cursor deliverer has no transport")
	}
	body, err := json.Marshal(steer)
	if err != nil {
		return T206DeliveryResult{}, err
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])

	j := d.Journal
	j.mu.Lock()
	if rec, ok := j.byID[deliveryID]; ok {
		j.mu.Unlock()
		if rec.BodyHash != hash {
			return T206DeliveryResult{}, fmt.Errorf("%w: %s", T206ErrDeliveryIDReuse, deliveryID)
		}
		return T206DeliveryResult{DeliveryID: deliveryID, Status: T206StatusDuplicate, Coalesced: true}, nil
	}
	j.byID[deliveryID] = CursorDeliveryRecord{DeliveryID: deliveryID, BodyHash: hash, Status: T206StatusQueued}
	j.mu.Unlock()

	status, err := d.Transport(ctx, steer)
	if err != nil {
		return T206DeliveryResult{DeliveryID: deliveryID}, err
	}
	j.mu.Lock()
	rec := j.byID[deliveryID]
	rec.Status = status
	j.byID[deliveryID] = rec
	j.mu.Unlock()
	return T206DeliveryResult{DeliveryID: deliveryID, Status: status}, nil
}

// ===== T3.250: history search with an explicit backend =====

// HistorySearchBackend names the program that answered a history query. It
// travels with the result so a caller can show which backend ran (T3.250).
type HistorySearchBackend string

const (
	HistorySearchRipgrep HistorySearchBackend = "rg"
	HistorySearchGrep    HistorySearchBackend = "grep -E"
)

// HistorySearchRunner runs one search command. Tests inject a fake, so a
// missing rg is reproduced without changing anything on the host.
type HistorySearchRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// HistorySearchResult is the bounded answer to a history query. Matches hold
// ANSI-free lines with line numbers. Truncated reports that a limit dropped
// content. NextCursor is empty when nothing is left; pass it back to
// HistorySearchPage to fetch the next page.
type HistorySearchResult struct {
	Backend    HistorySearchBackend
	Matches    []string
	Truncated  bool
	NextCursor string
}

const (
	historySearchMaxLines = 200
	historySearchMaxBytes = 16 * 1024
)

// historyANSI matches SGR color codes and OSC sequences that a transcript
// recorder writes into a terminal capture.
var historyANSI = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

func defaultHistorySearchRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// runHistorySearch runs one backend. Exit code 1 means no matches for both rg
// and grep, so it maps to an empty result rather than an error.
func runHistorySearch(ctx context.Context, runner HistorySearchRunner, backend HistorySearchBackend, pattern, path string) ([]byte, error) {
	var name string
	var args []string
	switch backend {
	case HistorySearchRipgrep:
		name = "rg"
		args = []string{"--color=never", "--line-number", "--", pattern, path}
	case HistorySearchGrep:
		name = "grep"
		args = []string{"-E", "-n", "--color=never", "--", pattern, path}
	default:
		return nil, fmt.Errorf("wake: unknown history backend %q", backend)
	}
	out, err := runner(ctx, name, args...)
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil, nil
	}
	return nil, err
}

// HistorySearch returns the first page of a history query. It is
// HistorySearchPage with an empty cursor.
func HistorySearch(ctx context.Context, path, pattern string, runner HistorySearchRunner, maxLines, maxBytes int) (HistorySearchResult, error) {
	return HistorySearchPage(ctx, path, pattern, runner, maxLines, maxBytes, "")
}

// HistorySearchPage returns one page of a history query. cursor is the
// NextCursor from a previous result; an empty cursor starts at the first
// match. A caller pages until NextCursor is empty.
func HistorySearchPage(ctx context.Context, path, pattern string, runner HistorySearchRunner, maxLines, maxBytes int, cursor string) (HistorySearchResult, error) {
	if pattern == "" {
		return HistorySearchResult{}, errors.New("wake: history search: empty pattern")
	}
	offset, err := parseHistoryCursor(cursor)
	if err != nil {
		return HistorySearchResult{}, err
	}
	if runner == nil {
		runner = defaultHistorySearchRunner
	}
	if maxLines <= 0 {
		maxLines = historySearchMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = historySearchMaxBytes
	}

	backend := HistorySearchRipgrep
	out, err := runHistorySearch(ctx, runner, backend, pattern, path)
	if errors.Is(err, exec.ErrNotFound) {
		backend = HistorySearchGrep
		out, err = runHistorySearch(ctx, runner, backend, pattern, path)
	}
	if err != nil {
		return HistorySearchResult{}, fmt.Errorf("wake: history search: %w", err)
	}

	lines, truncated, next := clampHistoryPage(historyANSI.ReplaceAllString(string(out), ""), maxLines, maxBytes, offset)
	return HistorySearchResult{Backend: backend, Matches: lines, Truncated: truncated, NextCursor: next}, nil
}

// parseHistoryCursor decodes the offset carried by a NextCursor. An empty
// cursor asks for the first page.
func parseHistoryCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("wake: history search: invalid cursor %q", cursor)
	}
	return offset, nil
}

// clampHistoryPage keeps at most maxLines lines and maxBytes bytes starting at
// offset. Truncated reports that either limit dropped content. nextCursor is
// empty when nothing remains; otherwise it encodes the offset of the first
// line not returned.
func clampHistoryPage(s string, maxLines, maxBytes, offset int) ([]string, bool, string) {
	if s == "" {
		return nil, false, ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if offset >= len(lines) {
		return nil, false, ""
	}
	truncated := len(lines)-offset > maxLines
	if truncated {
		lines = lines[offset : offset+maxLines]
	} else {
		lines = lines[offset:]
	}
	kept := make([]string, 0, len(lines))
	used := 0
	for i, line := range lines {
		if used+len(line) > maxBytes {
			if i == 0 {
				// A line larger than maxBytes would stall pagination at the
				// same offset. Return it once and move past it.
				return []string{line}, true, strconv.Itoa(offset + 1)
			}
			return kept, true, strconv.Itoa(offset + i)
		}
		used += len(line)
		kept = append(kept, line)
	}
	if truncated {
		return kept, true, strconv.Itoa(offset + len(kept))
	}
	return kept, false, ""
}

// clampHistoryOutput keeps the first page within both limits for callers that
// do not need a cursor.
func clampHistoryOutput(s string, maxLines, maxBytes int) ([]string, bool) {
	lines, truncated, _ := clampHistoryPage(s, maxLines, maxBytes, 0)
	return lines, truncated
}
