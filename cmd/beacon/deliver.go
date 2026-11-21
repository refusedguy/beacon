package main

// beacon deliver writes the Beacon verdict for one watch into the live OpenCode
// session that subscribed to it (ADR 0060 p4). The command is a thin adapter:
// it reads a Beacon snapshot, decides whether the verdict is actionable and
// asks a narrow deliveryTarget port to write the result. It never talks HTTP
// and never builds an OpenCode client; T3.144 owns the client and T3.145 wires
// it into this port after that card merges.
//
// Honesty (ADR 0026): the printed delivery is steer, queue or unsupported. A
// queue is never printed as a write into the running turn, and unsupported is
// never a success (the command exits non-zero). Retry follows ADR 0017: only a
// transport-failed attempt is retried, with the same pointer and obligationID,
// capped at three attempts. A posted receipt, an indeterminate outcome and an
// unsupported environment are never retried.
//
// This file reads the snapshot through mcp.StateSnapshot and projects the few
// fields it needs into local types. The ADR 0005 layer matrix allows the shared
// cmd -> internal/domain edge, so the obligation key uses the single canonical
// domain.ObligationKey encoder instead of a local copy.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// deliveryTarget is the narrow port deliver uses to write one result into one
// live session. The production implementation is installed by T3.145 with the
// HTTP client from T3.144. A nil target is an honest unsupported delivery,
// never a fake success.
type deliveryTarget interface {
	Prompt(sessionID, text string) error
}

// deliveryTargetFunc adapts a function to deliveryTarget. Tests use it as a
// stand-in; T3.145 may use it to wrap the HTTP client.
type deliveryTargetFunc func(sessionID, text string) error

func (f deliveryTargetFunc) Prompt(sessionID, text string) error { return f(sessionID, text) }

// deliverTarget is the installed target. deliver.go never assigns it from a
// real client; only the wiring layer does that through SetDeliveryTarget.
var deliverTarget deliveryTarget

// SetDeliveryTarget installs the delivery target. A nil argument clears it and
// makes every delivery honestly unsupported. T3.145 calls this from production
// wiring; the CLI itself does not.
func SetDeliveryTarget(t deliveryTarget) { deliverTarget = t }

// deliverSnapshot reads one Beacon snapshot. It mirrors watchSnapshot so
// production wiring reuses the same MCP snapshot port while tests replace the
// whole function.
var deliverSnapshot = func() (mcp.StateSnapshot, error) {
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		return mcp.StateSnapshot{}, err
	}
	defer func() { _ = closeStore() }()
	return deps.Snapshot.Snapshot()
}

// deliverRunner is the dispatch seam. T3.145 adds the "deliver" case to
// main.go and calls this, leaving the rendering details in this file.
var deliverRunner = func(c *CLI, args []string) int { return runDeliver(c, args) }

// RegisterDeliver installs the deliver command implementation, mirroring
// RegisterWatch and RegisterGC.
func RegisterDeliver(fn func(*CLI, []string) int) { deliverRunner = fn }

// The snapshot carries verdicts and roles as strings. These are the two
// actionable verdicts and the two subscriber roles used for routing, kept as
// strings so this layer never names a domain type (ADR 0005).
const (
	deliverVerdictReady      = "ready"
	deliverVerdictNeedsInput = "needs_input"

	deliverRoleExecutor     = "executor"
	deliverRoleOrchestrator = "orchestrator"
)

// deliverWatchView is the part of a Beacon watch this command reads.
type deliverWatchView struct {
	id          string
	verdict     string
	revision    uint64
	subscribers []deliverSubView
}

// deliverSubView is the part of a subscriber this command reads. Target is the
// opaque session id carried in the subscription's _meta.
type deliverSubView struct {
	role   string
	target string
}

// deliverViews projects a snapshot into local read-only views. It converts the
// typed verdict and role fields to strings at the boundary; no domain name is
// needed here.
func deliverViews(snap mcp.StateSnapshot) []deliverWatchView {
	views := make([]deliverWatchView, 0, len(snap.ActiveWatches))
	for _, w := range snap.ActiveWatches {
		v := deliverWatchView{
			id:       w.ID,
			verdict:  string(w.Verdict),
			revision: w.Revision,
		}
		for _, s := range w.Subscribers {
			v.subscribers = append(v.subscribers, deliverSubView{
				role:   string(s.Role),
				target: s.Target,
			})
		}
		views = append(views, v)
	}
	return views
}

// deliveryKind is the honest ADR 0026 outcome printed by the command.
type deliveryKind string

const (
	deliverySteer       deliveryKind = "steer"
	deliveryQueue       deliveryKind = "queue"
	deliveryUnsupported deliveryKind = "unsupported"
)

// Prompt reports a write into the running turn as nil. The sentinel errors
// below let an adapter report every other honest outcome through the single
// error return of Prompt. Wrap them with %w to carry a reason; classifyPrompt
// unwraps with errors.Is.
var (
	errPromptQueued      = errors.New("delivery: queued")
	errPromptUnsupported = errors.New("delivery: unsupported")
	errPromptPosted      = errors.New("delivery: posted")
	errTransportFailed   = errors.New("delivery: transport-failed")
	errIndeterminate     = errors.New("delivery: indeterminate")
)

// attemptOutcome is the observed result of one Prompt call. steer and queue
// are ADR 0026 deliveries; the rest follow the ADR 0017 attempt vocabulary.
type attemptOutcome string

const (
	outcomeSteer           attemptOutcome = "steer"
	outcomeQueue           attemptOutcome = "queue"
	outcomePosted          attemptOutcome = "posted"
	outcomeTransportFailed attemptOutcome = "transport-failed"
	outcomeIndeterminate   attemptOutcome = "indeterminate"
	outcomeUnsupported     attemptOutcome = "unsupported"
)

// classifyPrompt maps a Prompt error to an attempt outcome. An unknown error
// is indeterminate: the call ran but its result is not known, so it is never
// retried and never reported as a delivery (ADR 0017 and ADR 0026).
func classifyPrompt(err error) attemptOutcome {
	switch {
	case err == nil:
		return outcomeSteer
	case errors.Is(err, errPromptQueued):
		return outcomeQueue
	case errors.Is(err, errPromptUnsupported):
		return outcomeUnsupported
	case errors.Is(err, errPromptPosted):
		return outcomePosted
	case errors.Is(err, errTransportFailed):
		return outcomeTransportFailed
	case errors.Is(err, errIndeterminate):
		return outcomeIndeterminate
	default:
		return outcomeIndeterminate
	}
}

// deliveryOf is the honest ADR 0026 field. Only an actual steer or queue is a
// delivery; every other outcome, including indeterminate and posted, is
// unsupported. A lost receipt is therefore never printed as a success.
func deliveryOf(out attemptOutcome) deliveryKind {
	switch out {
	case outcomeSteer:
		return deliverySteer
	case outcomeQueue:
		return deliveryQueue
	default:
		return deliveryUnsupported
	}
}

// delivered reports whether the outcome is an honest ADR 0026 delivery.
func delivered(out attemptOutcome) bool {
	return out == outcomeSteer || out == outcomeQueue
}

// retryable reports the single ADR 0017 case that may be retried.
func retryable(out attemptOutcome) bool { return out == outcomeTransportFailed }

// deliverMaxAttempts is the ADR 0017 cap: an initial try plus retries at 5 and
// 10 minutes, three tries in total.
const deliverMaxAttempts = 3

// deliverRetryDelays are the ADR 0017 backoff points. The core owns the ladder
// schedule; this adapter keeps the values so the wiring layer can hand them to
// an external scheduler instead of blocking the CLI process.
var deliverRetryDelays = []time.Duration{5 * time.Minute, 10 * time.Minute}

// deliverSleep is injectable so tests observe retry timing without waiting.
// It is a no-op by default: this card enforces retry eligibility and the cap,
// and T3.145 applies the ADR 0017 timing through this seam.
var deliverSleep = func(time.Duration) {}

// callPrompt turns a missing target into an honest unsupported outcome. It
// keeps the retry loop free of nil checks.
func callPrompt(target deliveryTarget, sessionID, text string) error {
	if target == nil {
		return errPromptUnsupported
	}
	return target.Prompt(sessionID, text)
}

// deliverPrompt performs one bounded delivery sequence. The same text (the
// pointer carrying the obligationID) is reused on every attempt, as ADR 0017
// requires for a retry. Only a transport-failed outcome advances the retry
// counter; any other outcome returns at once.
func deliverPrompt(target deliveryTarget, sessionID, text string, sleep func(time.Duration)) (attemptOutcome, int) {
	attempts := 0
	for {
		attempts++
		out := classifyPrompt(callPrompt(target, sessionID, text))
		if !retryable(out) || attempts >= deliverMaxAttempts {
			return out, attempts
		}
		delay := time.Duration(0)
		if attempts-1 < len(deliverRetryDelays) {
			delay = deliverRetryDelays[attempts-1]
		}
		if delay < 0 {
			delay = 0
		}
		sleep(delay)
	}
}

// findDeliverWatch returns the projected watch with the given ID.
func findDeliverWatch(views []deliverWatchView, watchID string) (deliverWatchView, bool) {
	for _, w := range views {
		if w.id == watchID {
			return w, true
		}
	}
	return deliverWatchView{}, false
}

// actionableVerdict reports whether the verdict asks for a follow-up to be
// written back into the live session (ADR 0060 p4).
func actionableVerdict(v string) bool {
	return v == deliverVerdictReady || v == deliverVerdictNeedsInput
}

// deliverySubscriber picks the subscriber session to write to. A ready verdict
// is for the orchestrator, a needs_input verdict is for the executor that asked
// the question. When the preferred role has no live target, any subscriber with
// a non-empty target is used, so a role mismatch never loses a delivery.
func deliverySubscriber(w deliverWatchView) (deliverSubView, bool) {
	preferred := deliverRoleOrchestrator
	if w.verdict == deliverVerdictNeedsInput {
		preferred = deliverRoleExecutor
	}
	for _, s := range w.subscribers {
		if strings.TrimSpace(s.target) != "" && s.role == preferred {
			return s, true
		}
	}
	for _, s := range w.subscribers {
		if strings.TrimSpace(s.target) != "" {
			return s, true
		}
	}
	return deliverSubView{}, false
}

// deliverText builds the pointer written into the session. It carries the
// watch identifier and the obligation key, never check contents, so a retry
// repeats the same pointer with the same obligationID (ADR 0017).
func deliverText(w deliverWatchView, sessionID string) string {
	obligationID := domain.ObligationKey(w.id, w.revision, sessionID)
	return fmt.Sprintf("beacon deliver watch=%s revision=%d verdict=%s obligation=%s",
		w.id, w.revision, w.verdict, obligationID)
}

// deliverResult is the outcome of one deliver command. The delivery field is
// always printed next to the watch identifier (ADR 0060 p4).
type deliverResult struct {
	WatchID  string         `json:"watchID"`
	Verdict  string         `json:"verdict"`
	Delivery deliveryKind   `json:"delivery"`
	Outcome  attemptOutcome `json:"outcome"`
	Attempts int            `json:"attempts"`
}

func (r deliverResult) line() string {
	return fmt.Sprintf("watch=%s verdict=%s delivery=%s attempts=%d",
		r.WatchID, r.Verdict, r.Delivery, r.Attempts)
}

// ===== Pi RPC adapter selection (T3.217) ====================================

// Config keys for the Pi RPC adapter. An empty adapter keeps the installed
// target, so an unconfigured CLI keeps the honest unsupported path.
const (
	deliverAdapterEnv   = "BEACON_DELIVER_ADAPTER"
	deliverPiModeEnv    = "BEACON_DELIVER_PI_MODE"
	deliverPiTimeoutEnv = "BEACON_DELIVER_PI_TIMEOUT"
)

type deliverAdapterConfig struct {
	Adapter string
	Mode    wake.PiDeliveryMode
	Timeout time.Duration
	Err     error
}

// resolveDeliverAdapterConfig reads the three keys through the supplied lookup,
// so tests need no process environment. A bad mode or timeout is an error: the
// caller keeps the current target instead of guessing a route.
func resolveDeliverAdapterConfig(lookup func(string) string) deliverAdapterConfig {
	cfg := deliverAdapterConfig{Adapter: strings.TrimSpace(lookup(deliverAdapterEnv))}
	mode, err := wake.ParsePiDeliveryMode(lookup(deliverPiModeEnv))
	if err != nil {
		cfg.Err = err
	} else {
		cfg.Mode = mode
	}
	if raw := strings.TrimSpace(lookup(deliverPiTimeoutEnv)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			cfg.Err = fmt.Errorf("beacon deliver: invalid pi timeout %q", raw)
		} else {
			cfg.Timeout = d
		}
	}
	return cfg
}

// piProbeResolver supplies the provider for a config. The default returns no
// probe: the CLI wires no live Pi connection, so the fallback stays the
// honest unsupported path. Tests and later wiring install a provider here.
type piProbeResolver func(deliverAdapterConfig) (wake.PiProbe, error)

var resolvePiProbe piProbeResolver = func(deliverAdapterConfig) (wake.PiProbe, error) {
	return nil, nil
}

// piDeliveryTarget adapts a Pi deliverer to the single Prompt port. It maps
// provider errors onto the ADR 0017 attempt vocabulary before deliverPrompt
// classifies them.
type piDeliveryTarget struct {
	deliverer *wake.PiDeliverer
	timeout   time.Duration
}

func (t *piDeliveryTarget) Prompt(sessionID, text string) error {
	ctx := context.Background()
	if t.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}
	mode, err := t.deliverer.DeliverText(ctx, sessionID, text)
	if err != nil {
		return mapPiDeliveryError(err)
	}
	if mode == wake.PiModeQueue {
		return fmt.Errorf("pi follow-up queued: %w", errPromptQueued)
	}
	return nil
}

func mapPiDeliveryError(err error) error {
	switch {
	case errors.Is(err, wake.ErrUnsupported):
		return fmt.Errorf("pi unsupported: %w", errPromptUnsupported)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("pi transport: %w", errTransportFailed)
	default:
		return fmt.Errorf("pi rpc: %w", errIndeterminate)
	}
}

// chooseDeliveryTarget selects the Pi adapter only when config names it, the
// config parses and a provider is available. Every other case returns the
// installed target unchanged.
func chooseDeliveryTarget(cfg deliverAdapterConfig, probe wake.PiProbe, fallback deliveryTarget) deliveryTarget {
	if cfg.Err != nil || probe == nil || !strings.EqualFold(strings.TrimSpace(cfg.Adapter), wake.AdapterPi) {
		return fallback
	}
	return &piDeliveryTarget{deliverer: &wake.PiDeliverer{Probe: probe, Mode: cfg.Mode}, timeout: cfg.Timeout}
}

// runDeliver implements `beacon deliver <watchID>`. It reads one snapshot,
// checks that the verdict is ready or needs_input, writes the result into the
// subscriber's live session and prints the honest delivery field. Exit 0 means
// a follow-up was delivered, 1 means the delivery was not achieved, and 2 is a
// usage or snapshot error.
func runDeliver(c *CLI, args []string) int {
	fs := flag.NewFlagSet("deliver", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		writeUsage(c.errOut)
		return 2
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		fmt.Fprintln(c.errOut, "beacon deliver: нужна одна команда: beacon deliver <watchID>")
		return 2
	}
	watchID := strings.TrimSpace(fs.Arg(0))

	snap, err := deliverSnapshot()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon deliver: snapshot: %v\n", err)
		return 2
	}
	w, ok := findDeliverWatch(deliverViews(snap), watchID)
	if !ok {
		fmt.Fprintf(c.errOut, "beacon deliver: watch %s не найден в снимке\n", watchID)
		return 2
	}
	if !actionableVerdict(w.verdict) {
		fmt.Fprintf(c.out, "watch=%s verdict=%s: доставка не требуется\n", w.id, w.verdict)
		return 0
	}
	sub, ok := deliverySubscriber(w)
	if !ok {
		res := deliverResult{
			WatchID:  w.id,
			Verdict:  w.verdict,
			Delivery: deliveryUnsupported,
			Outcome:  outcomeUnsupported,
		}
		fmt.Fprintln(c.out, res.line())
		fmt.Fprintf(c.errOut, "beacon deliver: у watch %s нет живой сессии для доставки\n", w.id)
		return 1
	}

	text := deliverText(w, sub.target)
	cfg := resolveDeliverAdapterConfig(os.Getenv)
	probe, _ := resolvePiProbe(cfg)
	target := chooseDeliveryTarget(cfg, probe, deliverTarget)
	out, attempts := deliverPrompt(target, sub.target, text, deliverSleep)
	res := deliverResult{
		WatchID:  w.id,
		Verdict:  w.verdict,
		Delivery: deliveryOf(out),
		Outcome:  out,
		Attempts: attempts,
	}
	fmt.Fprintln(c.out, res.line())
	if !delivered(out) {
		fmt.Fprintf(c.errOut, "beacon deliver: доставка не выполнена (outcome=%s)\n", out)
		return 1
	}
	return 0
}
