// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_checks_explain (AT-05): a read-only projection of
// every check on a Watch that says, for each one, whether it can change the
// verdict. The handler reuses the verdict predicates and the ship-fact rule
// from tool_get_result.go, so an informational check (required=false) is never
// reported as a blocker. It only reads the Watch: no stamper and no write port
// is held, so readAt, verdict and revision are left untouched.
package mcp

import (
	"context"
	"sort"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// ChecksExplainToolName is the MCP name of the checks explainer.
const ChecksExplainToolName = "beacon_checks_explain"

// The explanation strings are fixed so a client can branch on them. The first
// one is the wording recorded in AT-05: a red check with required=false stays
// silent in the verdict engine, which is the confusion this tool resolves.
const (
	checksExplainInformational = "informational: required=false; verdict engine ignores this check"
	checksExplainRequiredFail  = "required check failed: " + string(domain.VerdictKeepWorking)
	checksExplainRequiredPass  = "required check passed: counts for the ready verdict"
	checksExplainPending       = "pending check: neither green nor failed; no verdict effect yet"
)

// ChecksExplainInput is the argument object of beacon_checks_explain.
type ChecksExplainInput struct {
	WatchID string `json:"watchID" jsonschema:"Watch to explain; required"`
}

// CheckExplanation is one check on the Watch and its verdict role.
// CountsForVerdict is true only for a required check that is either green or
// failed, which is the same rule the verdict engine applies. ShipFact is set
// only for a required green check, using the shipFactsFromWatch name rules.
type CheckExplanation struct {
	Name             string `json:"name"`
	State            string `json:"state"`
	Conclusion       string `json:"conclusion"`
	Required         bool   `json:"required"`
	IsFailure        bool   `json:"isFailure"`
	CountsForVerdict bool   `json:"countsForVerdict"`
	ShipFact         string `json:"shipFact,omitempty"`
	Explanation      string `json:"explanation"`
}

// ChecksExplainOutput is the response of beacon_checks_explain. It carries the
// Watch verdict and revision unchanged so a client can prove the call was a
// read, then compare them with a later beacon_get_result response.
type ChecksExplainOutput struct {
	WatchID  string             `json:"watchID"`
	Revision uint64             `json:"revision"`
	Verdict  string             `json:"verdict"`
	Checks   []CheckExplanation `json:"checks"`
}

// checksExplainStore is the read-only port of this tool. The production store
// satisfies it with Watch; no write or stamp method is part of the contract.
type checksExplainStore interface {
	Watch(id string) (domain.Watch, error)
}

// ChecksExplainHandler reads a Watch from the store and projects its checks.
type ChecksExplainHandler struct {
	store checksExplainStore
}

// NewChecksExplainHandler builds the handler. A nil store is kept so Explain
// reports a persist error instead of returning an empty projection.
func NewChecksExplainHandler(store checksExplainStore) *ChecksExplainHandler {
	return &ChecksExplainHandler{store: store}
}

// Handle adapts Explain to the go-sdk typed tool handler signature.
func (h *ChecksExplainHandler) Handle(_ context.Context, _ *mcpsdk.CallToolRequest, in ChecksExplainInput) (*mcpsdk.CallToolResult, ChecksExplainOutput, error) {
	out, err := h.Explain(in)
	if err != nil {
		return nil, ChecksExplainOutput{}, err
	}
	return nil, out, nil
}

// Explain returns one explanation per check, ordered by name so a map
// iteration cannot change the wire order. It never writes to the store.
func (h *ChecksExplainHandler) Explain(in ChecksExplainInput) (ChecksExplainOutput, error) {
	if in.WatchID == "" {
		return ChecksExplainOutput{}, domain.NewAppError(domain.CodeInvalidInput, "checks_explain: watchID is required")
	}
	if h == nil || h.store == nil {
		return ChecksExplainOutput{}, domain.NewAppError(domain.CodePersistFailed, "checks_explain: store port is nil")
	}
	w, err := h.store.Watch(in.WatchID)
	if err != nil {
		return ChecksExplainOutput{}, domain.NewAppError(domain.CodeWatchNotFound, "checks_explain: watch: "+err.Error())
	}

	out := ChecksExplainOutput{
		WatchID:  in.WatchID,
		Revision: w.Revision,
		Verdict:  string(w.Verdict),
		Checks:   make([]CheckExplanation, 0, len(w.Checks)),
	}
	names := make([]string, 0, len(w.Checks))
	for name := range w.Checks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out.Checks = append(out.Checks, explainCheck(name, w.Checks[name]))
	}
	return out, nil
}

// explainCheck classifies one check with the same predicates the verdict
// engine uses. isFailedCheck and isGreenCheck come from tool_get_result.go, so
// a convention change there cannot leave this projection behind.
func explainCheck(name string, check domain.Check) CheckExplanation {
	isFailure := isFailedCheck(check)
	isGreen := isGreenCheck(check)
	e := CheckExplanation{
		Name:       name,
		State:      check.State,
		Conclusion: check.Conclusion,
		Required:   check.Required,
		IsFailure:  isFailure,
	}
	switch {
	case !check.Required:
		e.Explanation = checksExplainInformational
	case isFailure:
		e.CountsForVerdict = true
		e.Explanation = checksExplainRequiredFail
	case isGreen:
		e.CountsForVerdict = true
		e.ShipFact = shipFactFor(name, check)
		e.Explanation = checksExplainRequiredPass
	default:
		e.Explanation = checksExplainPending
	}
	return e
}

// shipFactFor derives the single ship fact a green required check contributes.
// It calls shipFactsFromWatch on a one-check Watch instead of repeating the
// name matching. If a name matches more than one fact, build wins, then audit,
// then tdd, so the field stays one stable string.
func shipFactFor(name string, check domain.Check) string {
	if !check.Required || !isGreenCheck(check) {
		return ""
	}
	facts := shipFactsFromWatch(domain.Watch{
		Checks: map[string]domain.Check{name: check},
	})
	switch {
	case facts.Build:
		return "build"
	case facts.Audit:
		return "audit"
	case facts.TDD:
		return "tdd"
	}
	return ""
}

// ChecksExplainOutputSchema exposes the derived output schema for the wiring
// descriptor. The schema comes from the output type above, never from a hand
// written JSON literal (ADR 0003, ADR 0268).
func ChecksExplainOutputSchema() (*jsonschema.Schema, error) {
	return OutputSchema[ChecksExplainOutput]()
}

// AttachChecksExplain registers the real handler on a go-sdk server. The
// advertised descriptor stays in the owner's shared wiring PR; this helper is
// opt-in, like AttachGetResult, so the tool can be exercised on its own.
func AttachChecksExplain(server *mcpsdk.Server, handler *ChecksExplainHandler) {
	if handler == nil {
		return
	}
	registerTool(server, ChecksExplainToolName,
		"Explain every check on a Watch and whether each one affects the verdict; read-only and never marks the Watch as read.",
		mustChecksExplainOutputSchema(), newToolAnnotations(true, false, false), handler.Handle)
}

// mustChecksExplainOutputSchema is safe to call while registering a tool: the
// reflection over the output type above cannot fail with a fixed struct.
func mustChecksExplainOutputSchema() *jsonschema.Schema {
	schema, err := ChecksExplainOutputSchema()
	if err != nil {
		return &jsonschema.Schema{Type: "object"}
	}
	return schema
}
