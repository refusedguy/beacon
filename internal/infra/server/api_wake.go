// Package server exposes the privileged client commands the TUI needs: wake,
// question answer and approval respond. Every command carries a clientRequestId
// (ADR 0028) and reaches the durable state only through injected ports, so this
// package never opens SQLite and never imports a sibling infra package
// (BS.37, ADR 0198/0262).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// WakePath initiates a wake for a watch.
	WakePath = "/api/v1/wake"
	// QuestionAnswerPath answers a pending question.
	QuestionAnswerPath = "/api/v1/questions/answer"
	// ApprovalRespondPath responds to a pending approval.
	ApprovalRespondPath = "/api/v1/approvals/respond"
)

// Audit action names. Each privileged command records one of these through
// the AuditWriter port; the store owns the durable log (BS.22).
const (
	actionWake            = "wake"
	actionQuestionAnswer  = "question.answer"
	actionApprovalRespond = "approval.respond"

	statusPosted = "posted"
)

var (
	// ErrNilPort reports a processor without a command port.
	ErrNilPort = errors.New("server: nil command port")
	// ErrNilAudit reports a privileged processor without an audit writer.
	ErrNilAudit = errors.New("server: nil audit writer")
	// ErrInvalidClientRequestID reports a missing or malformed clientRequestId.
	ErrInvalidClientRequestID = errors.New("server: invalid clientRequestId")
	// ErrEmptyActor reports a privileged command with no actor.
	ErrEmptyActor = errors.New("server: empty command actor")
	// ErrInvalidIngress reports an unknown audit ingress.
	ErrInvalidIngress = errors.New("server: invalid command ingress")
	// ErrEmptyTarget reports a command without the id it acts on.
	ErrEmptyTarget = errors.New("server: empty command target")
	// ErrInvalidDecision reports an approval decision other than approve or reject.
	ErrInvalidDecision = errors.New("server: invalid approval decision")
)

// Ingress is the transport a privileged command arrived on. It mirrors the
// store's audit ingress values as plain data, so this package does not import
// the store that owns the audit table.
type Ingress string

const (
	IngressTUI     Ingress = "tui"
	IngressMCP     Ingress = "mcp"
	IngressWebhook Ingress = "webhook"
)

func (i Ingress) valid() bool {
	switch i {
	case IngressTUI, IngressMCP, IngressWebhook:
		return true
	default:
		return false
	}
}

// AuditEvent is one privileged command recorded through the AuditWriter port.
// CommandID is the id the port assigned to the effect; a repeat never produces
// a second event.
type AuditEvent struct {
	Actor     string  `json:"actor"`
	Ingress   Ingress `json:"ingress"`
	CommandID string  `json:"commandId"`
	Action    string  `json:"action"`
}

// AuditWriter is the port that records privileged actions. The composition
// root implements it over the store's transactional audit log; this package
// only names the event.
type AuditWriter interface {
	RecordAudit(ctx context.Context, e AuditEvent) error
}

// WakeCommand starts a wake for one watch.
type WakeCommand struct {
	ClientRequestID string
	WatchID         string
	Actor           string
	Ingress         Ingress
}

// AnswerCommand answers one pending question.
type AnswerCommand struct {
	ClientRequestID string
	QuestionID      string
	Answer          string
	Actor           string
	Ingress         Ingress
}

// ApprovalCommand responds to one pending approval. Decision is either
// "approve" or "reject".
type ApprovalCommand struct {
	ClientRequestID string
	ApprovalID      string
	Decision        string
	Note            string
	Actor           string
	Ingress         Ingress
}

// EffectResult is the outcome of a command effect from the port. The processor
// turns it into a client-facing Receipt and adds the idempotency fields.
type EffectResult struct {
	CommandID string
	Status    string
}

// Receipt is the durable acknowledgement returned for one clientRequestId. A
// repeat returns the stored receipt with Replayed set, so a lost response
// never triggers a second effect.
type Receipt struct {
	ClientRequestID string `json:"clientRequestId"`
	CommandKey      string `json:"commandKey"`
	CommandID       string `json:"commandId"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	Replayed        bool   `json:"replayed,omitempty"`
}

// CommandPort is the transactional boundary for privileged commands. The
// composition root implements it over the store's single writer. Each method
// must be idempotent by ClientRequestID: a repeat reports the first result and
// performs no second effect.
type CommandPort interface {
	Wake(ctx context.Context, cmd WakeCommand) (EffectResult, error)
	AnswerQuestion(ctx context.Context, cmd AnswerCommand) (EffectResult, error)
	RespondApproval(ctx context.Context, cmd ApprovalCommand) (EffectResult, error)
}

// CommandProcessor validates commands, enforces clientRequestId idempotency
// and writes audit. It holds no database and delegates every effect to the
// port. The mutex is held across the port call so two concurrent requests with
// the same key cannot both start an effect.
type CommandProcessor struct {
	port     CommandPort
	audit    AuditWriter
	mu       sync.Mutex
	receipts map[string]Receipt
}

// NewCommandProcessor builds a processor over port and audit. Either
// dependency may be nil at construction; the command methods then report a
// typed error instead of panicking, which keeps bad wiring visible.
func NewCommandProcessor(port CommandPort, audit AuditWriter) *CommandProcessor {
	return &CommandProcessor{port: port, audit: audit, receipts: make(map[string]Receipt)}
}

// Wake records and executes a wake command.
func (p *CommandProcessor) Wake(ctx context.Context, cmd WakeCommand) (Receipt, error) {
	if strings.TrimSpace(cmd.WatchID) == "" {
		return Receipt{}, ErrEmptyTarget
	}
	return p.execute(ctx, cmd.ClientRequestID, cmd.Actor, cmd.Ingress, actionWake,
		func(ctx context.Context) (EffectResult, error) { return p.port.Wake(ctx, cmd) })
}

// AnswerQuestion records and executes a question answer.
func (p *CommandProcessor) AnswerQuestion(ctx context.Context, cmd AnswerCommand) (Receipt, error) {
	if strings.TrimSpace(cmd.QuestionID) == "" {
		return Receipt{}, ErrEmptyTarget
	}
	return p.execute(ctx, cmd.ClientRequestID, cmd.Actor, cmd.Ingress, actionQuestionAnswer,
		func(ctx context.Context) (EffectResult, error) { return p.port.AnswerQuestion(ctx, cmd) })
}

// RespondApproval records and executes an approval response.
func (p *CommandProcessor) RespondApproval(ctx context.Context, cmd ApprovalCommand) (Receipt, error) {
	if strings.TrimSpace(cmd.ApprovalID) == "" {
		return Receipt{}, ErrEmptyTarget
	}
	decision := strings.ToLower(strings.TrimSpace(cmd.Decision))
	if decision != "approve" && decision != "reject" {
		return Receipt{}, ErrInvalidDecision
	}
	cmd.Decision = decision
	return p.execute(ctx, cmd.ClientRequestID, cmd.Actor, cmd.Ingress, actionApprovalRespond,
		func(ctx context.Context) (EffectResult, error) { return p.port.RespondApproval(ctx, cmd) })
}

// execute is the single idempotency and audit path for all three commands. A
// repeat of requestID returns the stored receipt without calling the port or
// writing a second audit event.
func (p *CommandProcessor) execute(ctx context.Context, requestID, actor string, ingress Ingress, action string, run func(context.Context) (EffectResult, error)) (Receipt, error) {
	if p == nil || p.port == nil {
		return Receipt{}, ErrNilPort
	}
	if p.audit == nil {
		return Receipt{}, ErrNilAudit
	}
	id, err := domain.NewClientRequestID(requestID)
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: %v", ErrInvalidClientRequestID, err)
	}
	if strings.TrimSpace(actor) == "" {
		return Receipt{}, ErrEmptyActor
	}
	if !ingress.valid() {
		return Receipt{}, fmt.Errorf("%w: %q", ErrInvalidIngress, ingress)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.receipts == nil {
		p.receipts = make(map[string]Receipt)
	}

	key := id.String()
	if receipt, ok := p.receipts[key]; ok {
		receipt.Replayed = true
		return receipt, nil
	}

	result, err := run(ctx)
	if err != nil {
		return Receipt{}, err
	}
	if err := p.audit.RecordAudit(ctx, AuditEvent{
		Actor:     actor,
		Ingress:   ingress,
		CommandID: result.CommandID,
		Action:    action,
	}); err != nil {
		return Receipt{}, fmt.Errorf("server: record audit: %w", err)
	}

	status := result.Status
	if status == "" {
		status = statusPosted
	}
	receipt := Receipt{
		ClientRequestID: key,
		CommandKey:      domain.CommandKey(id),
		CommandID:       result.CommandID,
		Kind:            action,
		Status:          status,
	}
	p.receipts[key] = receipt
	return receipt, nil
}

// Handler returns the HTTP surface for the three command routes. It is a thin
// decoder over the processor: validation, idempotency and audit stay in the
// processor so they can be tested without a socket.
func (p *CommandProcessor) Handler() http.Handler {
	return http.HandlerFunc(p.handle)
}

type commandRequest struct {
	ClientRequestID string  `json:"clientRequestId"`
	Target          string  `json:"target"`
	Answer          string  `json:"answer"`
	Decision        string  `json:"decision"`
	Note            string  `json:"note"`
	Actor           string  `json:"actor"`
	Ingress         Ingress `json:"ingress"`
}

func (p *CommandProcessor) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeCommandError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only POST is supported")
		return
	}
	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCommandError(w, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}

	var (
		receipt Receipt
		err     error
	)
	switch r.URL.Path {
	case WakePath:
		receipt, err = p.Wake(r.Context(), WakeCommand{
			ClientRequestID: req.ClientRequestID, WatchID: req.Target,
			Actor: req.Actor, Ingress: req.Ingress,
		})
	case QuestionAnswerPath:
		receipt, err = p.AnswerQuestion(r.Context(), AnswerCommand{
			ClientRequestID: req.ClientRequestID, QuestionID: req.Target,
			Answer: req.Answer, Actor: req.Actor, Ingress: req.Ingress,
		})
	case ApprovalRespondPath:
		receipt, err = p.RespondApproval(r.Context(), ApprovalCommand{
			ClientRequestID: req.ClientRequestID, ApprovalID: req.Target,
			Decision: req.Decision, Note: req.Note, Actor: req.Actor, Ingress: req.Ingress,
		})
	default:
		writeCommandError(w, http.StatusNotFound, "NOT_FOUND", "unknown command route")
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrInvalidClientRequestID) || errors.Is(err, ErrEmptyActor) ||
			errors.Is(err, ErrInvalidIngress) || errors.Is(err, ErrEmptyTarget) ||
			errors.Is(err, ErrInvalidDecision) {
			status = http.StatusBadRequest
		}
		writeCommandError(w, status, "COMMAND_FAILED", err.Error())
		return
	}
	writeCommandJSON(w, http.StatusOK, receipt)
}

func writeCommandJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type commandErrorBody struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeCommandError(w http.ResponseWriter, status int, code, message string) {
	writeCommandJSON(w, status, commandErrorBody{Code: code, Message: message})
}
