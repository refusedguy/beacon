// Package wake records the comparison of the Pi, Kilo Code and Cursor wake
// mechanisms and the four common primitives Beacon adopts from them
// (ADR 0077, T3.205).
//
// The comparison is data, not prose. Matrix holds one grounded fact per
// mechanism and axis, Primitives holds the four recurring primitives, and
// SourceAnchors holds the four verifiable facts the card's check requires.
// Overview renders the matrix as a table. Tests check that the table is
// complete and that no mechanism outside the three source reports was added.
//
// Every fact names the report it came from: wake-pi.md, wake-kilo-code.md or
// wake-cursor.md, all dated 20 November 2026. Beacon design ideas are kept out.
package wake

import "strings"

// Mechanism is one wake model described in the source reports.
type Mechanism string

const MechanismPi Mechanism = "pi"
const MechanismKilo Mechanism = "kilo"
const MechanismCursor Mechanism = "cursor"

// Axis is one comparison dimension required by T3.205.
type Axis string

const AxisTruth Axis = "source of truth"
const AxisDedup Axis = "deduplication"
const AxisBusy Axis = "busy behavior"
const AxisRestart Axis = "restart recovery"
const AxisDelivery Axis = "result delivery"

// Row is one cell of the comparison: what a mechanism does on an axis and
// which report states it. Source is a report filename, never a Beacon-only
// idea presented as a mechanism.
type Row struct {
	Mechanism Mechanism
	Axis      Axis
	Fact      string
	Source    string
}

func row(m Mechanism, a Axis, fact, source string) Row {
	return Row{Mechanism: m, Axis: a, Fact: fact, Source: source}
}

// Matrix is the comparison table. Fifteen rows cover three mechanisms on five
// axes. Each row is taken from a source report; nothing is added from
// Beacon's own design.
var Matrix = []Row{
	row(MechanismPi, AxisTruth, "State lives in the pi.inbox document and durable phase checkpoints; the durable since cursor survives a client restart, while Chord events are delivered once and are neither stored nor replayed to late subscribers.", "wake-pi.md"),
	row(MechanismPi, AxisDedup, "A repeated submit with the same requestId returns the same submission; a signal on an occupied key yields duplicate, and the idempotency key is fixed before the external call, as api.memo is fixed before park.", "wake-pi.md"),
	row(MechanismPi, AxisBusy, "Policy submit uses whenBusy steer with whenIdle reject, and busyDelivery is steer or human-first. A steer joins the current run after the tool round; a follow-up starts the next run.", "wake-pi.md"),
	row(MechanismPi, AxisRestart, "The coding-agent core has no sleep scheduler and no built-in background agents. Continuation needs a long-lived RPC or SDK listener, an extension with a watcher or timer, or durable resume(); the process never wakes itself.", "wake-pi.md"),
	row(MechanismPi, AxisDelivery, "Delivery results are delivered, duplicate, invalid, closed and not_found. A new submission continues the work, and agent_end is not final readiness.", "wake-pi.md"),
	row(MechanismKilo, AxisTruth, "Wake is advisory, not the source of truth. Truth lives in the durable record: the wakeup is written to Storage before its timer, and session events carry a monotonic SQLite seq; the signal only suggests re-reading state.", "wake-kilo-code.md"),
	row(MechanismKilo, AxisDedup, "A duplicate signal cannot start a second copy because of the write lock. Repeated wake calls collapse into one pendingWake, and fireNow deletes the durable record under the cross-process EffectFlock before resume.", "wake-kilo-code.md"),
	row(MechanismKilo, AxisBusy, "The coordinator's wake announces new durable work and collapses repeats into one pendingWake; resumeOrQueue places a signal in pending instead of starting a parallel run.", "wake-kilo-code.md"),
	row(MechanismKilo, AxisRestart, "adopt re-reads wakeup records from Storage at instance start and rearms timers, so an overdue record fires at once. A subscriber keeps the last seq and reopens the stream with after.", "wake-kilo-code.md"),
	row(MechanismKilo, AxisDelivery, "A background task injects its result into the parent session as a synthetic text part tagged background:true with state completed or error; drain publishes session.drained, and FINISHED is not final readiness.", "wake-kilo-code.md"),
	row(MechanismCursor, AxisTruth, "The pending-request list is the source of truth and delivery is best-effort. list-then-watch reads the full list first and stores a local streamCursor; a lost event is recovered by the next list, not redelivered.", "wake-cursor.md"),
	row(MechanismCursor, AxisDedup, "X-Webhook-ID is the unique delivery id and Idempotency-Key returns 422 for the same key with a different body and the first run for the same body. A steer retried after a network error can deliver twice, so deliveryId is required.", "wake-cursor.md"),
	row(MechanismCursor, AxisBusy, "delivery steer reports steered or queued, 409 is agent_busy, admission coalesce applies a Slack policy, and preempt interrupts the active turn only with an explicit flag.", "wake-cursor.md"),
	row(MechanismCursor, AxisRestart, "list-then-watch stores the cursor only in the controller. The cursor lives five minutes from the list, heartbeat and reconnect do not extend it, and 410 Gone with cursor_expired or a periodic jittered timer triggers a re-list.", "wake-cursor.md"),
	row(MechanismCursor, AxisDelivery, "A wake call returns 202 and the result arrives through events, callback or tool, with SSE streams scoped to one run or to an agent across runs. FINISHED and IDLE do not mean final readiness.", "wake-cursor.md"),
}

// Primitive is one recurring primitive chosen for Beacon.
type Primitive string

const PrimitiveDeliveryID Primitive = "deliveryId"
const PrimitiveEventsHint Primitive = "events as hint"
const PrimitiveStateTruth Primitive = "state as truth"
const PrimitiveLeaseRelease Primitive = "lease/release"

// PrimitiveDoc explains why a primitive was chosen and which reports ground it.
type PrimitiveDoc struct {
	Primitive Primitive
	Summary   string
	Reports   []string
}

func primitive(p Primitive, summary string, reports ...string) PrimitiveDoc {
	return PrimitiveDoc{Primitive: p, Summary: summary, Reports: reports}
}

// Primitives are the four repeating primitives from the reports. No primitive
// is invented here: each names at least one source report.
var Primitives = []PrimitiveDoc{
	primitive(PrimitiveDeliveryID, "A single delivery id runs from wake to ack. Cursor confirms a follow-up with deliveryId, X-Webhook-ID or a fresh dlv_, and rejects a reused Idempotency-Key with a different body. Pi returns the same submission for a repeated requestId and reports duplicate on an occupied key; Kilo collapses repeated signals into pendingWake. The key is fixed before the external call, as api.memo is fixed before park.", "wake-cursor.md", "wake-pi.md", "wake-kilo-code.md"),
	primitive(PrimitiveEventsHint, "An event is advice, never the obligation. Cursor says delivery is best-effort, the list is the truth, lost events are not redelivered and the next list fixes divergence. Kilo's wake is advisory with a dirty signal of capacity one over a durable record. Pi Chord events are delivered once, are not folded, stored or sent to late subscribers.", "wake-cursor.md", "wake-kilo-code.md", "wake-pi.md"),
	primitive(PrimitiveStateTruth, "A full read of authoritative state always wins. Cursor re-lists on 410 and cursor_expired and on a jittered timer; Kilo keeps the wakeup in Storage and re-reads it in adopt with a seq cursor; Pi recovers from the pi.inbox document and durable checkpoints with a durable since cursor.", "wake-cursor.md", "wake-kilo-code.md", "wake-pi.md"),
	primitive(PrimitiveLeaseRelease, "An obligation is leased with explicit release and fail paths. Cursor's claim reserves a request, rejects a second claim while the lease is live, returns 400 from release while the turn still holds the worker, and carries wakeTimeoutMs in claimed_offline; fail shows the text to the user. Kilo's fireNow removes the durable record under EffectFlock before resume.", "wake-cursor.md", "wake-kilo-code.md"),
}

// Anchor is a verifiable fact from a source report, used by the card's check.
type Anchor struct {
	ID    string
	Value string
}

func anchor(id, value string) Anchor {
	return Anchor{ID: id, Value: value}
}

// SourceAnchors are the four check facts: the dead original Pi URL and its
// live repository, the pinned Kilo commit, and the Cursor v0/v1 split.
var SourceAnchors = []Anchor{
	anchor("pi-original-url-404", "github.com/mariozechner/pi-coding-agent"),
	anchor("pi-repository", "https://github.com/earendil-works/pi"),
	anchor("kilo-commit", "bd8b98aeab348c08221a836ba091b855a62814e9"),
	anchor("cursor-api-split", "v0/v1"),
}

// NonMechanisms are behaviours the reports explicitly rule out. They are not
// wake mechanisms and must not be promoted into the matrix.
var NonMechanisms = []string{
	"a built-in sleep scheduler in the Pi coding-agent core",
	"automatic events.subscribe reconnect as a recovery path",
	"FINISHED, IDLE or agent_end as final readiness",
}

// Overview renders Matrix as a markdown table with one row per axis and one
// column per mechanism. It is the human-readable form of the comparison.
func Overview() string {
	var b strings.Builder
	b.WriteString("| Axis | Pi | Kilo Code | Cursor |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, axis := range []Axis{AxisTruth, AxisDedup, AxisBusy, AxisRestart, AxisDelivery} {
		b.WriteString("| " + string(axis) + " |")
		for _, m := range []Mechanism{MechanismPi, MechanismKilo, MechanismCursor} {
			b.WriteString(" ")
			b.WriteString(FactFor(m, axis))
			b.WriteString(" |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// FactFor returns the grounded fact for a mechanism and axis, or an empty
// string when the cell is missing.
func FactFor(m Mechanism, a Axis) string {
	for _, r := range Matrix {
		if r.Mechanism == m && r.Axis == a {
			return r.Fact
		}
	}
	return ""
}
