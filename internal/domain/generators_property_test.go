// T1.6 property-test generators.
//
// The generators in this file draw the six commands from the task card
// (watch_pr, wait_result, ack, cancel, tick, head_changed) plus the opaque
// strings that must pass through the core untouched. Every generator is a
// package-level value so rapid can shrink a failing sequence to a small
// counterexample and reproduce it from the seed it prints.
package domain

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

const (
	// invariantSeed fixes the starting PRNG seed. rapid derives one seed per
	// sequence, so the ten checks always see the same 1000 sequences. A failure
	// is reproducible with the -rapid.seed=... hint rapid prints.
	invariantSeed = uint64(20261119)
	// invariantChecks is the §7 requirement: 1000 sequences per property.
	invariantChecks = 1000
	// printedCommands limits how much of a failing sequence we print.
	printedCommands = 12
)

// commandKind is one of the six generated operations.
type commandKind string

const (
	cmdWatchPR     commandKind = "watch_pr"
	cmdWaitResult  commandKind = "wait_result"
	cmdAck         commandKind = "ack"
	cmdCancel      commandKind = "cancel"
	cmdTick        commandKind = "tick"
	cmdHeadChanged commandKind = "head_changed"
)

// command is one operation in a generated sequence. Fields that do not apply
// to Kind keep their zero value. WatchIndex and ObligationIndex are drawn, not
// resolved, so a sequence does not depend on earlier state; the harness maps
// them onto whatever it currently holds.
type command struct {
	Kind            commandKind
	Repo            string
	PR              int
	HeadSHA         string
	SubscriberID    string
	Role            Role
	Target          string
	SessionID       string
	Verdict         Verdict
	NewHeadSHA      string
	Outcome         AttemptOutcome
	Validated       bool
	WatchIndex      int
	ObligationIndex int
}

// String returns the compact form printed when a property fails.
func (c command) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", c.Kind)
	switch c.Kind {
	case cmdWatchPR:
		fmt.Fprintf(&b, "{repo=%s pr=%d sha=%s sub=%s role=%s target=%q session=%q}",
			c.Repo, c.PR, c.HeadSHA, c.SubscriberID, c.Role, c.Target, c.SessionID)
	case cmdWaitResult:
		fmt.Fprintf(&b, "{watch#%d}", c.WatchIndex)
	case cmdAck:
		fmt.Fprintf(&b, "{obligation#%d}", c.ObligationIndex)
	case cmdCancel:
		fmt.Fprintf(&b, "{watch#%d}", c.WatchIndex)
	case cmdTick:
		fmt.Fprintf(&b, "{watch#%d verdict=%s outcome=%s validated=%t}",
			c.WatchIndex, c.Verdict, c.Outcome, c.Validated)
	case cmdHeadChanged:
		fmt.Fprintf(&b, "{watch#%d sha=%s}", c.WatchIndex, c.NewHeadSHA)
	}
	return b.String()
}

var (
	// Small domains make aliases collide often, so renew and fan-out are
	// exercised in nearly every sequence.
	repoGen       = rapid.SampledFrom([]string{"org/repo", "org/fork"})
	prGen         = rapid.IntRange(1, 3)
	shaGen        = rapid.SampledFrom([]string{"sha-a", "sha-b", "sha-c"})
	subscriberGen = rapid.SampledFrom([]string{"executor", "orchestrator", "sub:3"})
	roleGen       = rapid.SampledFrom([]Role{RoleExecutor, RoleOrchestrator})
	verdictGen    = rapid.SampledFrom([]Verdict{
		VerdictWatching, VerdictReady, VerdictRebase, VerdictRetry,
		VerdictKeepWorking, VerdictTimeout, VerdictMerged, VerdictClosed,
	})
	outcomeGen = rapid.SampledFrom([]AttemptOutcome{
		OutcomePosted, OutcomeTransportFailed, OutcomeIndeterminate,
	})
	indexGen = rapid.IntRange(0, 7)
	kindGen  = rapid.SampledFrom([]commandKind{
		cmdWatchPR, cmdWaitResult, cmdAck, cmdCancel, cmdTick, cmdHeadChanged,
	})

	// opaqueGen produces strings with separators and prompt-looking text. The
	// core must store them verbatim and must not read them as instructions.
	opaqueGen = rapid.StringMatching(`[A-Za-z0-9:/\\@._<>="-]{0,10}`)

	commandGen = rapid.Custom(func(t *rapid.T) command {
		return command{
			Kind:            kindGen.Draw(t, "kind"),
			Repo:            repoGen.Draw(t, "repo"),
			PR:              prGen.Draw(t, "pr"),
			HeadSHA:         shaGen.Draw(t, "sha"),
			SubscriberID:    subscriberGen.Draw(t, "subscriber"),
			Role:            roleGen.Draw(t, "role"),
			Target:          opaqueGen.Draw(t, "target"),
			SessionID:       opaqueGen.Draw(t, "session"),
			Verdict:         verdictGen.Draw(t, "verdict"),
			NewHeadSHA:      shaGen.Draw(t, "new_sha"),
			Outcome:         outcomeGen.Draw(t, "outcome"),
			Validated:       rapid.Bool().Draw(t, "suite_validated"),
			WatchIndex:      indexGen.Draw(t, "watch_index"),
			ObligationIndex: indexGen.Draw(t, "obligation_index"),
		}
	})

	// commandSeqGen draws one whole sequence. rapid.Check draws one sequence
	// per check, and invariantChecks fixes the number of checks at 1000.
	commandSeqGen = rapid.SliceOfN(commandGen, 1, 40)
)

// runInvariantProperty pins rapid's flags to the card's values for the duration
// of one property and restores them afterwards. Setting the flags in code keeps
// the run deterministic even when CI invokes go test without -rapid.* flags.
func runInvariantProperty(t *testing.T, prop func(*rapid.T)) {
	t.Helper()
	restore := pinRapidFlags(t)
	defer restore()
	rapid.Check(t, prop)
}

func pinRapidFlags(t *testing.T) func() {
	t.Helper()
	want := map[string]string{
		"rapid.checks": strconv.Itoa(invariantChecks),
		"rapid.seed":   strconv.FormatUint(invariantSeed, 10),
	}
	previous := make(map[string]string, len(want))
	for name, value := range want {
		f := flag.Lookup(name)
		if f == nil {
			t.Fatalf("rapid flag %q is not registered", name)
		}
		previous[name] = f.Value.String()
		if err := flag.Set(name, value); err != nil {
			t.Fatalf("set -%s=%s: %v", name, value, err)
		}
	}
	return func() {
		for name, value := range previous {
			_ = flag.Set(name, value)
		}
	}
}

// shortenTrace returns the tail of a command sequence. rapid shrinks the drawn
// sequence before reporting a failure, so this tail is already minimized.
func shortenTrace(trace []string) string {
	if len(trace) > printedCommands {
		trace = trace[len(trace)-printedCommands:]
	}
	return strings.Join(trace, " -> ")
}
