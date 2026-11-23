package reachability

import (
	"fmt"
	"sort"
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

// exceptionVerdict is the outcome for one REACH-UNWIRED row.
type exceptionVerdict int

const (
	verdictOK exceptionVerdict = iota
	verdictWarning
	verdictBlocking
)

func (v exceptionVerdict) String() string {
	switch v {
	case verdictOK:
		return "ok"
	case verdictWarning:
		return "warning"
	default:
		return "blocking"
	}
}

// adr0263Promises are the words that turn a reason into a promise to wire a
// symbol in a later wave. ADR 0263 forbids that promise without a new ADR.
var adr0263Promises = []string{"wave", "подключ", "регистриру", "ожида"}

// reasonAligned reports whether the reason records the ADR 0263 decision and
// avoids a future-wave wiring promise.
func reasonAligned(reason string) bool {
	lower := strings.ToLower(reason)
	if !strings.Contains(lower, "adr 0263") {
		return false
	}
	for _, promise := range adr0263Promises {
		if strings.Contains(lower, promise) {
			return false
		}
	}
	return true
}

// exceptionRow is one printed line of the exception audit.
type exceptionRow struct {
	Symbol     string
	InCode     bool
	FromCmd    bool
	ADRAligned bool
	Verdict    exceptionVerdict
	Reason     string
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// String prints the five required fields plus the reason.
func (r exceptionRow) String() string {
	return fmt.Sprintf("%s in_code=%d from_cmd=%d adr0263=%d verdict=%s reason=%q",
		r.Symbol, bit(r.InCode), bit(r.FromCmd), bit(r.ADRAligned), r.Verdict, r.Reason)
}

// evaluateExceptions grades every REACH-UNWIRED row against one graph.
func evaluateExceptions(ex gate.Exceptions, a *analysis) ([]exceptionRow, int) {
	rows := make([]exceptionRow, 0, len(ex.Permanent))
	exit := gate.Green
	for _, p := range ex.Permanent {
		if p.Rule != gate.RuleReachUnwired {
			continue
		}
		_, inCode := a.nodes[p.Path]
		fromCmd := a.reachable[p.Path]
		aligned := reasonAligned(p.Reason)
		v := verdictOK
		switch {
		case !inCode:
			// A deleted symbol has no live reason left to suppress.
			v = verdictBlocking
		case fromCmd && !aligned:
			// Wired from cmd with no recorded owner decision.
			v = verdictBlocking
		case fromCmd || !aligned:
			// Wired under an ADR decision, or a reason that promises a
			// closed wave. Both wait for the owner without blocking.
			v = verdictWarning
		}
		if v == verdictBlocking {
			exit = gate.Red
		}
		rows = append(rows, exceptionRow{
			Symbol: p.Path, InCode: inCode, FromCmd: fromCmd,
			ADRAligned: aligned, Verdict: v, Reason: p.Reason,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Symbol < rows[j].Symbol })
	return rows, exit
}

// checkExceptions is the --check-exceptions entry point. It prints one line
// per REACH-UNWIRED row and returns Red when any row is blocking.
func checkExceptions(root string) int {
	ex, err := gate.LoadReachabilityExceptions()
	if err != nil {
		return gate.Brokenf("load exceptions: %v", err)
	}
	a, err := parseAnalysis(root)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	rows, exit := evaluateExceptions(ex, a)
	for _, row := range rows {
		fmt.Println(row.String())
	}
	blocking, warnings := 0, 0
	for _, row := range rows {
		switch row.Verdict {
		case verdictBlocking:
			blocking++
		case verdictWarning:
			warnings++
		}
	}
	fmt.Printf("exceptions: %d rows, %d blocking, %d warning\n", len(rows), blocking, warnings)
	return exit
}
