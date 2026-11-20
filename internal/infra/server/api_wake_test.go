package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// mockCommandPort records every effect and hands out deterministic command
// ids. An injected err simulates a failed write without touching SQLite.
type mockCommandPort struct {
	mu        sync.Mutex
	wakes     []WakeCommand
	answers   []AnswerCommand
	approvals []ApprovalCommand
	err       error
	seq       int
}

func (m *mockCommandPort) result() (EffectResult, error) {
	if m.err != nil {
		return EffectResult{}, m.err
	}
	m.seq++
	return EffectResult{CommandID: "cmd-" + strconv.Itoa(m.seq), Status: statusPosted}, nil
}

func (m *mockCommandPort) Wake(_ context.Context, cmd WakeCommand) (EffectResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wakes = append(m.wakes, cmd)
	return m.result()
}

func (m *mockCommandPort) AnswerQuestion(_ context.Context, cmd AnswerCommand) (EffectResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers = append(m.answers, cmd)
	return m.result()
}

func (m *mockCommandPort) RespondApproval(_ context.Context, cmd ApprovalCommand) (EffectResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvals = append(m.approvals, cmd)
	return m.result()
}

// mockAuditWriter records the audit events a privileged command emits.
type mockAuditWriter struct {
	mu     sync.Mutex
	events []AuditEvent
	err    error
}

func (m *mockAuditWriter) RecordAudit(_ context.Context, e AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, e)
	return nil
}

func TestWakeReturnsReceiptAndAudits(t *testing.T) {
	port, audit := &mockCommandPort{}, &mockAuditWriter{}
	p := NewCommandProcessor(port, audit)

	receipt, err := p.Wake(context.Background(), WakeCommand{
		ClientRequestID: "req-wake", WatchID: "watch-1", Actor: "ses_1", Ingress: IngressTUI,
	})
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	id, _ := domain.NewClientRequestID("req-wake")
	if receipt.ClientRequestID != "req-wake" {
		t.Errorf("clientRequestId = %q", receipt.ClientRequestID)
	}
	if receipt.CommandKey != domain.CommandKey(id) {
		t.Errorf("commandKey = %q, want %q", receipt.CommandKey, domain.CommandKey(id))
	}
	if receipt.CommandID != "cmd-1" {
		t.Errorf("commandID = %q", receipt.CommandID)
	}
	if receipt.Kind != actionWake || receipt.Status != statusPosted {
		t.Errorf("kind/status = %q/%q", receipt.Kind, receipt.Status)
	}
	if receipt.Replayed {
		t.Error("first call must not be a replay")
	}
	if len(port.wakes) != 1 || port.wakes[0].WatchID != "watch-1" {
		t.Fatalf("wakes = %+v", port.wakes)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	got := audit.events[0]
	if got.Actor != "ses_1" || got.Ingress != IngressTUI || got.Action != actionWake || got.CommandID != "cmd-1" {
		t.Errorf("audit event = %+v", got)
	}
}

func TestAnswerQuestionReturnsReceipt(t *testing.T) {
	port, audit := &mockCommandPort{}, &mockAuditWriter{}
	p := NewCommandProcessor(port, audit)

	receipt, err := p.AnswerQuestion(context.Background(), AnswerCommand{
		ClientRequestID: "req-answer", QuestionID: "q-1", Answer: "yes",
		Actor: "ses_2", Ingress: IngressMCP,
	})
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if receipt.Kind != actionQuestionAnswer || receipt.CommandID != "cmd-1" {
		t.Errorf("receipt = %+v", receipt)
	}
	if len(port.answers) != 1 || port.answers[0].QuestionID != "q-1" || port.answers[0].Answer != "yes" {
		t.Fatalf("answers = %+v", port.answers)
	}
	if len(port.wakes) != 0 || len(port.approvals) != 0 {
		t.Error("answer must not emit other command effects")
	}
	if len(audit.events) != 1 || audit.events[0].Action != actionQuestionAnswer {
		t.Fatalf("audit events = %+v", audit.events)
	}
}

func TestRespondApprovalReturnsReceipt(t *testing.T) {
	port, audit := &mockCommandPort{}, &mockAuditWriter{}
	p := NewCommandProcessor(port, audit)

	receipt, err := p.RespondApproval(context.Background(), ApprovalCommand{
		ClientRequestID: "req-approval", ApprovalID: "a-1", Decision: "APPROVE", Note: "lgtm",
		Actor: "ses_3", Ingress: IngressWebhook,
	})
	if err != nil {
		t.Fatalf("RespondApproval: %v", err)
	}
	if receipt.Kind != actionApprovalRespond || receipt.CommandID != "cmd-1" {
		t.Errorf("receipt = %+v", receipt)
	}
	if len(port.approvals) != 1 {
		t.Fatalf("approvals = %+v", port.approvals)
	}
	if port.approvals[0].Decision != "approve" || port.approvals[0].Note != "lgtm" {
		t.Errorf("approval = %+v", port.approvals[0])
	}
	if len(audit.events) != 1 || audit.events[0].Action != actionApprovalRespond ||
		audit.events[0].Ingress != IngressWebhook {
		t.Fatalf("audit events = %+v", audit.events)
	}
}

func TestRepeatClientRequestIDReturnsStoredReceipt(t *testing.T) {
	port, audit := &mockCommandPort{}, &mockAuditWriter{}
	p := NewCommandProcessor(port, audit)
	cmd := WakeCommand{
		ClientRequestID: "req-repeat", WatchID: "watch-1", Actor: "ses_1", Ingress: IngressTUI,
	}

	first, err := p.Wake(context.Background(), cmd)
	if err != nil {
		t.Fatalf("first Wake: %v", err)
	}
	second, err := p.Wake(context.Background(), cmd)
	if err != nil {
		t.Fatalf("repeat Wake: %v", err)
	}

	if len(port.wakes) != 1 {
		t.Fatalf("port called %d times, want 1", len(port.wakes))
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	if first.Replayed {
		t.Error("first receipt must not be a replay")
	}
	if !second.Replayed {
		t.Error("repeat receipt must be marked replayed")
	}
	if second.ClientRequestID != first.ClientRequestID ||
		second.CommandKey != first.CommandKey || second.CommandID != first.CommandID ||
		second.Kind != first.Kind || second.Status != first.Status {
		t.Fatalf("repeat receipt %+v differs from first %+v", second, first)
	}
}

func TestRepeatAcrossCommandsUsesSameKeySpace(t *testing.T) {
	port, audit := &mockCommandPort{}, &mockAuditWriter{}
	p := NewCommandProcessor(port, audit)

	first, err := p.Wake(context.Background(), WakeCommand{
		ClientRequestID: "req-shared", WatchID: "watch-1", Actor: "ses_1", Ingress: IngressTUI,
	})
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	second, err := p.AnswerQuestion(context.Background(), AnswerCommand{
		ClientRequestID: "req-shared", QuestionID: "q-1", Actor: "ses_1", Ingress: IngressTUI,
	})
	if err != nil {
		t.Fatalf("AnswerQuestion with repeated key: %v", err)
	}
	if second.CommandID != first.CommandID || second.Kind != first.Kind {
		t.Fatalf("repeat returned %+v, want first command %+v", second, first)
	}
	if len(port.wakes)+len(port.answers) != 1 {
		t.Fatalf("port called %d times, want 1", len(port.wakes)+len(port.answers))
	}
}

func TestPortErrorIsNotCached(t *testing.T) {
	boom := errors.New("port down")
	port, audit := &mockCommandPort{err: boom}, &mockAuditWriter{}
	p := NewCommandProcessor(port, audit)
	cmd := WakeCommand{
		ClientRequestID: "req-error", WatchID: "watch-1", Actor: "ses_1", Ingress: IngressTUI,
	}

	if _, err := p.Wake(context.Background(), cmd); !errors.Is(err, boom) {
		t.Fatalf("first Wake error = %v, want %v", err, boom)
	}
	if len(audit.events) != 0 {
		t.Fatalf("failed command wrote audit: %+v", audit.events)
	}

	port.err = nil
	receipt, err := p.Wake(context.Background(), cmd)
	if err != nil {
		t.Fatalf("retry Wake: %v", err)
	}
	if receipt.CommandID != "cmd-1" {
		t.Errorf("retry commandID = %q, want cmd-1", receipt.CommandID)
	}
	if len(port.wakes) != 2 {
		t.Fatalf("port called %d times, want 2", len(port.wakes))
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
}

func TestInvalidCommandsDoNotReachPort(t *testing.T) {
	tests := []struct {
		name string
		run  func(*CommandProcessor) error
		want error
	}{
		{
			name: "empty clientRequestId",
			run: func(p *CommandProcessor) error {
				_, err := p.Wake(context.Background(), WakeCommand{WatchID: "w", Actor: "a", Ingress: IngressTUI})
				return err
			},
			want: ErrInvalidClientRequestID,
		},
		{
			name: "empty watch id",
			run: func(p *CommandProcessor) error {
				_, err := p.Wake(context.Background(), WakeCommand{ClientRequestID: "r", Actor: "a", Ingress: IngressTUI})
				return err
			},
			want: ErrEmptyTarget,
		},
		{
			name: "empty actor",
			run: func(p *CommandProcessor) error {
				_, err := p.Wake(context.Background(), WakeCommand{ClientRequestID: "r", WatchID: "w", Ingress: IngressTUI})
				return err
			},
			want: ErrEmptyActor,
		},
		{
			name: "unknown ingress",
			run: func(p *CommandProcessor) error {
				_, err := p.Wake(context.Background(), WakeCommand{ClientRequestID: "r", WatchID: "w", Actor: "a", Ingress: "cli"})
				return err
			},
			want: ErrInvalidIngress,
		},
		{
			name: "invalid approval decision",
			run: func(p *CommandProcessor) error {
				_, err := p.RespondApproval(context.Background(), ApprovalCommand{
					ClientRequestID: "r", ApprovalID: "a", Decision: "maybe", Actor: "a", Ingress: IngressTUI,
				})
				return err
			},
			want: ErrInvalidDecision,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			port, audit := &mockCommandPort{}, &mockAuditWriter{}
			p := NewCommandProcessor(port, audit)
			if err := tc.run(p); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if len(port.wakes)+len(port.answers)+len(port.approvals) != 0 {
				t.Error("invalid command reached the port")
			}
			if len(audit.events) != 0 {
				t.Error("invalid command wrote audit")
			}
		})
	}
}

func TestNilDependenciesAreErrors(t *testing.T) {
	cmd := WakeCommand{ClientRequestID: "r", WatchID: "w", Actor: "a", Ingress: IngressTUI}
	if _, err := NewCommandProcessor(nil, &mockAuditWriter{}).Wake(context.Background(), cmd); !errors.Is(err, ErrNilPort) {
		t.Errorf("nil port error = %v", err)
	}
	if _, err := NewCommandProcessor(&mockCommandPort{}, nil).Wake(context.Background(), cmd); !errors.Is(err, ErrNilAudit) {
		t.Errorf("nil audit error = %v", err)
	}
}

func TestHandlerPostsCommandAndReturnsReceipt(t *testing.T) {
	port, audit := &mockCommandPort{}, &mockAuditWriter{}
	p := NewCommandProcessor(port, audit)
	body := `{"clientRequestId":"req-http","target":"watch-9","actor":"ses_1","ingress":"tui"}`
	r := httptest.NewRequest(http.MethodPost, WakePath, strings.NewReader(body))
	w := httptest.NewRecorder()

	p.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var receipt Receipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.ClientRequestID != "req-http" || receipt.CommandID != "cmd-1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(port.wakes) != 1 {
		t.Fatalf("wakes = %+v", port.wakes)
	}

	get := httptest.NewRequest(http.MethodGet, WakePath, bytes.NewReader(nil))
	w2 := httptest.NewRecorder()
	p.Handler().ServeHTTP(w2, get)
	if w2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", w2.Code)
	}
}
