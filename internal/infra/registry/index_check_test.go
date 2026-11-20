package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

func assertIndexError(t *testing.T, err error, field, agent string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error naming field %s and agent %q", field, agent)
	}
	var app *domain.AppError
	if !errors.As(err, &app) {
		t.Fatalf("error type = %T, want *domain.AppError", err)
	}
	if app.Code != domain.CodeInvalidInput {
		t.Fatalf("error code = %s, want %s", app.Code, domain.CodeInvalidInput)
	}
	if !strings.Contains(app.Message, field) {
		t.Fatalf("error message %q does not name field %q", app.Message, field)
	}
	if !strings.Contains(app.Message, agent) {
		t.Fatalf("error message %q does not name agent %q", app.Message, agent)
	}
}

func TestCheckCapabilityIndexAcceptsMatchingIndex(t *testing.T) {
	declarations := map[string][]string{
		"executor":     {"watch", "ack"},
		"orchestrator": {"review", "merge"},
	}
	index := map[string][]string{
		"ack":    {"executor"},
		"watch":  {"executor"},
		"merge":  {"orchestrator"},
		"review": {"orchestrator"},
	}
	if err := CheckCapabilityIndex(declarations, index); err != nil {
		t.Fatalf("CheckCapabilityIndex: %v", err)
	}
}

func TestCheckCapabilityIndexAcceptsEmptyRosterAndIndex(t *testing.T) {
	for _, tc := range []struct {
		name         string
		declarations map[string][]string
		index        map[string][]string
	}{
		{name: "both nil"},
		{name: "empty roster", index: map[string][]string{}},
		{name: "empty index", declarations: map[string][]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckCapabilityIndex(tc.declarations, tc.index); err != nil {
				t.Fatalf("CheckCapabilityIndex: %v", err)
			}
		})
	}
}

func TestCheckCapabilityIndexRejectsEmptyIndexForNonEmptyRoster(t *testing.T) {
	declarations := map[string][]string{
		"executor":     {"watch", "ack"},
		"orchestrator": {"review"},
	}
	for _, tc := range []struct {
		name  string
		index map[string][]string
	}{
		{name: "nil index"},
		{name: "empty map", index: map[string][]string{}},
		{name: "keys without members", index: map[string][]string{"watch": {}, "ack": {}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCapabilityIndex(declarations, tc.index)
			assertIndexError(t, err, "agent_capabilities", "executor")
		})
	}
}

func TestCheckCapabilityIndexRejectsMissingDeclarationEntry(t *testing.T) {
	declarations := map[string][]string{"executor": {"watch", "ack"}}
	for _, tc := range []struct {
		name  string
		index map[string][]string
	}{
		{name: "capability absent", index: map[string][]string{"watch": {"executor"}}},
		{name: "agent absent", index: map[string][]string{"watch": {"executor"}, "ack": {"orchestrator"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCapabilityIndex(declarations, tc.index)
			assertIndexError(t, err, "agent_capabilities", "executor")
		})
	}
}

func TestCheckCapabilityIndexRejectsNonExistentAgentReference(t *testing.T) {
	// Negative control from r09: an index entry pointing at a non-existent
	// agent is rejected even when every declared capability is present.
	declarations := map[string][]string{"executor": {"watch"}}
	index := map[string][]string{"watch": {"executor", "ghost"}}
	assertIndexError(t, CheckCapabilityIndex(declarations, index), "agent_capabilities", "ghost")
}

func TestCheckCapabilityIndexRejectsUndeclaredCapability(t *testing.T) {
	declarations := map[string][]string{"executor": {"watch"}}
	index := map[string][]string{
		"watch": {"executor"},
		"merge": {"executor"},
	}
	assertIndexError(t, CheckCapabilityIndex(declarations, index), "capabilities", "executor")
}

func TestCheckCapabilityIndexReportsSameErrorAcrossMapOrders(t *testing.T) {
	declarations := map[string][]string{
		"alpha": {"watch", "ack"},
		"beta":  {"merge", "review"},
	}
	index := map[string][]string{
		"watch":  {"alpha"},
		"ack":    {"alpha"},
		"merge":  {"beta"},
		"review": {"beta", "ghost"},
	}
	want := ""
	for i := 0; i < 50; i++ {
		err := CheckCapabilityIndex(declarations, index)
		if err == nil {
			t.Fatalf("iteration %d: expected an error", i)
		}
		if want == "" {
			want = err.Error()
			continue
		}
		if err.Error() != want {
			t.Fatalf("iteration %d: error = %q, want %q", i, err, want)
		}
	}
	assertIndexError(t, CheckCapabilityIndex(declarations, index), "agent_capabilities", "ghost")
}
