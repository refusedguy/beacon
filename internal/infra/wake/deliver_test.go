// Package wake adapts Beacon's wake pointer to the host harness.
// Consolidated from PRs #281, #286, #282, #287, #285, #290, #291, #288, #283, #284, #289, #276.
package wake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// ===== база wake (#281) =====

func TestEnvelopeWhitelistsFields(t *testing.T) {
	p := domain.WakePayload{WatchID: "watch-1", ObligationID: "obl-1", Revision: 7, Verdict: "ready"}
	env := Envelope(p)
	if !strings.HasPrefix(env, "<beacon-event>") || !strings.HasSuffix(env, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", env)
	}
	for _, want := range []string{`"watchID":"watch-1"`, `"obligationID":"obl-1"`, `"revision":7`, `"verdict":"ready"`} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %q: %s", want, env)
		}
	}
}

func TestOpenCodeDelivererPostsSteerPrompt(t *testing.T) {
	var gotArgs []string
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"data":{"id":"msg_1","sessionID":"ses_test"}}`), nil
	}}
	err := d.Deliver(context.Background(), "ses_test", domain.WakePayload{
		WatchID: "watch-1", ObligationID: domain.ObligationKey("watch-1", 1, "ses_test"), Revision: 1, Verdict: "ready",
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(gotArgs) < 5 {
		t.Fatalf("args = %q", gotArgs)
	}
	if gotArgs[0] != "api" || gotArgs[1] != "POST" || gotArgs[2] != "/api/session/ses_test/prompt" {
		t.Fatalf("unexpected api call: %q", gotArgs)
	}
	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(gotArgs[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "steer" {
		t.Fatalf("delivery = %q, want steer", sent.Delivery)
	}
	if !strings.HasPrefix(sent.Text, "<beacon-event>") || !strings.HasSuffix(sent.Text, "</beacon-event>") {
		t.Fatalf("missing envelope: %q", sent.Text)
	}
}

func TestOpenCodeDelivererRejectsBadSessionAndMissingAck(t *testing.T) {
	d := &OpenCodeDeliverer{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{}}`), nil
	}}
	if err := d.Deliver(context.Background(), "not-a-session", domain.WakePayload{}); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	p := domain.WakePayload{WatchID: "watch-2", ObligationID: domain.ObligationKey("watch-2", 1, "ses_test"), Revision: 1}
	if err := d.Deliver(context.Background(), "ses_test", p); err == nil {
		t.Fatal("ack without message id must be rejected")
	}
}

// A pointer names an obligation of one subscriber. Deliver must not steer a
// different session even when that session id is otherwise well formed.
func TestOpenCodeDelivererRejectsForeignSession(t *testing.T) {
	var prompts int
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		prompts++
		return []byte(`{"data":{"id":"msg_1"}}`), nil
	}}
	const owner = "ses_owner"
	p := domain.WakePayload{
		WatchID:      "watch-3",
		ObligationID: domain.ObligationKey("watch-3", 4, owner),
		Revision:     4,
		Verdict:      "ready",
	}
	if err := d.Deliver(context.Background(), "ses_other", p); err == nil {
		t.Fatal("Deliver accepted a session that is not the obligation subscriber")
	}
	if prompts != 0 {
		t.Fatalf("prompt calls = %d, want 0 for a foreign session", prompts)
	}
	if err := d.Deliver(context.Background(), owner, p); err != nil {
		t.Fatalf("Deliver for the owner: %v", err)
	}
	if prompts != 1 {
		t.Fatalf("prompt calls = %d, want 1 for the owner", prompts)
	}
}

// A repeated wake with the same pointer must be a no-op. A new revision of the
// same obligation is a new wake and gets its own prompt.
func TestOpenCodeDelivererRepeatedPointerPostsOnePrompt(t *testing.T) {
	var prompts int
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		prompts++
		return []byte(`{"data":{"id":"msg_1"}}`), nil
	}}
	const sessionID = "ses_test"
	p := domain.WakePayload{
		WatchID:      "watch-4",
		ObligationID: domain.ObligationKey("watch-4", 7, sessionID),
		Revision:     7,
		Verdict:      "ready",
	}
	for i := 0; i < 3; i++ {
		if err := d.Deliver(context.Background(), sessionID, p); err != nil {
			t.Fatalf("Deliver #%d: %v", i+1, err)
		}
	}
	if prompts != 1 {
		t.Fatalf("prompt calls = %d, want 1 after repeated wakes", prompts)
	}

	next := domain.WakePayload{
		WatchID:      p.WatchID,
		ObligationID: domain.ObligationKey(p.WatchID, p.Revision+1, sessionID),
		Revision:     p.Revision + 1,
		Verdict:      "ready",
	}
	if err := d.Deliver(context.Background(), sessionID, next); err != nil {
		t.Fatalf("Deliver on new revision: %v", err)
	}
	if prompts != 2 {
		t.Fatalf("prompt calls = %d, want 2 after a new revision", prompts)
	}
}

// T3.230: the wake target is the orchestrator session of a real Watch. A
// substituted watchID or an obligation of another subscriber must not wake it.
func TestDeliverToWatchUsesRealWatchTarget(t *testing.T) {
	const (
		watchID = "watch:refusedguy/beacon:286:177bf240"
		owner   = "ses_orchestrator"
	)
	w := domain.Watch{
		ID: watchID,
		Subscribers: []domain.Subscriber{
			{ID: "ses_executor", Role: domain.RoleExecutor},
			{ID: owner, Role: domain.RoleOrchestrator, Adapter: "opencode"},
		},
	}
	p := domain.WakePayload{
		WatchID:      watchID,
		ObligationID: domain.ObligationKey(watchID, 1, owner),
		Revision:     1,
		Verdict:      "ready",
	}

	var targets []string
	d := &recordingWakeDeliverer{fn: func(_ context.Context, sessionID string, _ domain.WakePayload) error {
		targets = append(targets, sessionID)
		return nil
	}}
	if err := DeliverToWatch(context.Background(), d, w, p); err != nil {
		t.Fatalf("DeliverToWatch: %v", err)
	}
	if !reflect.DeepEqual(targets, []string{owner}) {
		t.Fatalf("targets = %v, want [%s]", targets, owner)
	}

	stubbed := p
	stubbed.WatchID = "watch:stubbed"
	if err := DeliverToWatch(context.Background(), d, w, stubbed); err == nil {
		t.Fatal("a substituted watchID must not deliver a wake")
	}
	foreign := p
	foreign.ObligationID = domain.ObligationKey(watchID, 1, "ses_executor")
	if err := DeliverToWatch(context.Background(), d, w, foreign); err == nil {
		t.Fatal("an obligation of another subscriber must not wake the orchestrator")
	}
	if len(targets) != 1 {
		t.Fatalf("prompt targets = %v, want exactly one", targets)
	}
}

func TestDeliverToWatchPostsSteerAndReadsMessageID(t *testing.T) {
	const (
		watchID = "watch:refusedguy/beacon:286:177bf240"
		owner   = "ses_orchestrator"
	)
	w := domain.Watch{ID: watchID, Subscribers: []domain.Subscriber{
		{ID: owner, Role: domain.RoleOrchestrator},
	}}
	p := domain.WakePayload{
		WatchID:      watchID,
		ObligationID: domain.ObligationKey(watchID, 3, owner),
		Revision:     3,
		Verdict:      "ready",
	}
	var gotArgs []string
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"data":{"id":"msg_1f72fc9f","delivery":"steer"}}`), nil
	}}
	if err := DeliverToWatch(context.Background(), d, w, p); err != nil {
		t.Fatalf("DeliverToWatch: %v", err)
	}
	if len(gotArgs) < 5 || gotArgs[2] != "/api/session/"+owner+"/prompt" {
		t.Fatalf("api call = %v", gotArgs)
	}
	var body struct {
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(gotArgs[4]), &body); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if body.Delivery != "steer" {
		t.Fatalf("delivery = %q, want steer", body.Delivery)
	}
}

type recordingWakeDeliverer struct {
	fn func(context.Context, string, domain.WakePayload) error
}

func (r *recordingWakeDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	return r.fn(ctx, sessionID, p)
}

// ===== T3.200 прямой POST session.prompt (#286) =====

// T200recordedRequest is the part of an outgoing request a test cares about.
type T200recordedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// T200fakeDoer replaces the HTTP transport. No real server is started.
type T200fakeDoer struct {
	mu    sync.Mutex
	calls []T200recordedRequest
	fn    func(*http.Request) (*http.Response, error)
}

func (f *T200fakeDoer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, T200recordedRequest{
		method: req.Method,
		path:   req.URL.EscapedPath(),
		header: req.Header.Clone(),
		body:   body,
	})
	f.mu.Unlock()
	if f.fn == nil {
		return nil, errors.New("T200fakeDoer: fn не задан")
	}
	return f.fn(req)
}

func (f *T200fakeDoer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *T200fakeDoer) snapshot() []T200recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]T200recordedRequest(nil), f.calls...)
}

func T200fakeResponse(body string, code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     fmt.Sprintf("%d test", code),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func T200newTestDeliverer(t *testing.T, doer T200Doer) *T200Deliverer {
	t.Helper()
	d, err := T200New("http://127.0.0.1:49374/", "pw", doer)
	if err != nil {
		t.Fatalf("T200New: %v", err)
	}
	return d
}

func TestDeliverPostsSteerPromptT200(t *testing.T) {
	doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return T200fakeResponse(`{"data":{"id":"inbox_1","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := T200newTestDeliverer(t, doer)

	got, err := d.Deliver(context.Background(), T200Request{
		SessionID:  "ses_1",
		Text:       "новый факт: обязательство T3.200",
		DeliveryID: "obligation-1",
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got.ID != "inbox_1" || got.T200Delivery != T200DeliverySteer || got.Duplicate {
		t.Fatalf("неверный результат: %+v", got)
	}

	calls := doer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(calls))
	}
	call := calls[0]
	if call.method != http.MethodPost {
		t.Fatalf("ожидался POST, получен %s", call.method)
	}
	if call.path != "/api/session/ses_1/prompt" {
		t.Fatalf("неверный путь %q", call.path)
	}
	if call.header.Get("Content-Type") != "application/json" {
		t.Fatalf("неверный Content-Type %q", call.header.Get("Content-Type"))
	}
	if call.header.Get("Idempotency-Key") != "obligation-1" {
		t.Fatalf("неверный Idempotency-Key %q", call.header.Get("Idempotency-Key"))
	}
	user, pass, ok := T200basicAuth(call.header)
	if !ok || user != "opencode" || pass != "pw" {
		t.Fatalf("неверный basic auth: user=%q pass=%q ok=%v", user, pass, ok)
	}
	var payload struct {
		Text     string `json:"text"`
		T200Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal(call.body, &payload); err != nil {
		t.Fatalf("неверный JSON запроса: %v", err)
	}
	if payload.Text != "новый факт: обязательство T3.200" || payload.T200Delivery != "steer" {
		t.Fatalf("неверное тело запроса: %+v", payload)
	}
}

func T200basicAuth(header http.Header) (string, string, bool) {
	req := &http.Request{Header: header}
	return req.BasicAuth()
}

func TestDeliverDuplicateIDReturnsOriginalAdmissionT200(t *testing.T) {
	doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return T200fakeResponse(`{"data":{"id":"inbox_1","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := T200newTestDeliverer(t, doer)
	req := T200Request{SessionID: "ses_1", Text: "факт", DeliveryID: "obligation-1"}

	first, err := d.Deliver(context.Background(), req)
	if err != nil {
		t.Fatalf("первый Deliver: %v", err)
	}
	second, err := d.Deliver(context.Background(), req)
	if err != nil {
		t.Fatalf("второй Deliver: %v", err)
	}
	if first.Duplicate {
		t.Fatal("первый вызов не должен быть повтором")
	}
	if !second.Duplicate {
		t.Fatal("второй вызов должен быть повтором")
	}
	if second.ID != first.ID || second.T200Delivery != first.T200Delivery {
		t.Fatalf("повтор изменил admitting: first=%+v second=%+v", first, second)
	}
	if n := doer.count(); n != 1 {
		t.Fatalf("ожидался 1 POST, получено %d: вторая задача была бы создана", n)
	}
}

func TestDeliverDerivedIDCollapsesRepeatsT200(t *testing.T) {
	doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return T200fakeResponse(`{"data":{"id":"inbox_2","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := T200newTestDeliverer(t, doer)
	req := T200Request{SessionID: "ses_2", Text: "тот же текст"}

	first, err := d.Deliver(context.Background(), req)
	if err != nil {
		t.Fatalf("первый Deliver: %v", err)
	}
	second, err := d.Deliver(context.Background(), req)
	if err != nil {
		t.Fatalf("второй Deliver: %v", err)
	}
	if !second.Duplicate || second.ID != first.ID {
		t.Fatalf("повтор без id не схлопнулся: first=%+v second=%+v", first, second)
	}
	calls := doer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("ожидался 1 POST, получено %d", len(calls))
	}
	if calls[0].header.Get("Idempotency-Key") == "" {
		t.Fatal("производный ключ не отправлен")
	}
}

func TestDeliverDifferentTextCreatesNewTaskT200(t *testing.T) {
	doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return T200fakeResponse(`{"data":{"id":"inbox_3","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := T200newTestDeliverer(t, doer)

	if _, err := d.Deliver(context.Background(), T200Request{SessionID: "ses_3", Text: "факт один"}); err != nil {
		t.Fatalf("первый Deliver: %v", err)
	}
	if _, err := d.Deliver(context.Background(), T200Request{SessionID: "ses_3", Text: "факт два"}); err != nil {
		t.Fatalf("второй Deliver: %v", err)
	}
	if n := doer.count(); n != 2 {
		t.Fatalf("ожидалось 2 POST для разных текстов, получено %d", n)
	}
}

func TestDeliver409ReturnsOriginalAdmissionT200(t *testing.T) {
	calls := 0
	doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return T200fakeResponse(`{"data":{"id":"inbox_4","delivery":"steer"}}`, http.StatusOK), nil
		}
		return T200fakeResponse(`{"data":{"id":"inbox_4","delivery":"steer"}}`, http.StatusConflict), nil
	}}
	d := T200newTestDeliverer(t, doer)

	first, err := d.Deliver(context.Background(), T200Request{SessionID: "ses_4", Text: "факт", DeliveryID: "obligation-4"})
	if err != nil {
		t.Fatalf("первый Deliver: %v", err)
	}
	// A fresh deliverer simulates a restart: no local journal, server answers 409.
	restarted := T200newTestDeliverer(t, doer)
	second, err := restarted.Deliver(context.Background(), T200Request{SessionID: "ses_4", Text: "факт", DeliveryID: "obligation-4"})
	if err != nil {
		t.Fatalf("второй Deliver после рестарта: %v", err)
	}
	if !second.Duplicate || second.ID != first.ID {
		t.Fatalf("409 не вернул исходное admit: first=%+v second=%+v", first, second)
	}
}

func TestDeliverRejectsMissingDataIDT200(t *testing.T) {
	doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return T200fakeResponse(`{"data":{"delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := T200newTestDeliverer(t, doer)

	if _, err := d.Deliver(context.Background(), T200Request{SessionID: "ses_5", Text: "факт"}); err == nil {
		t.Fatal("ожидалась ошибка при отсутствии data.id")
	}
}

func TestDeliverRejectsWrongDeliveryT200(t *testing.T) {
	doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return T200fakeResponse(`{"data":{"id":"inbox_6","delivery":"queue"}}`, http.StatusOK), nil
	}}
	d := T200newTestDeliverer(t, doer)

	if _, err := d.Deliver(context.Background(), T200Request{SessionID: "ses_6", Text: "факт"}); err == nil {
		t.Fatal("ожидалась ошибка при data.delivery != steer")
	}
}

func TestDeliverHTTPErrorsT200(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
				return T200fakeResponse(`{"message":"нет сессии"}`, code), nil
			}}
			d := T200newTestDeliverer(t, doer)
			if _, err := d.Deliver(context.Background(), T200Request{SessionID: "ses_7", Text: "факт"}); err == nil {
				t.Fatal("ожидалась ошибка для неуспешного статуса")
			}
			// A failed admission is not journalled: a later retry must reach the server.
			doer.fn = func(*http.Request) (*http.Response, error) {
				return T200fakeResponse(`{"data":{"id":"inbox_7","delivery":"steer"}}`, http.StatusOK), nil
			}
			if _, err := d.Deliver(context.Background(), T200Request{SessionID: "ses_7", Text: "факт"}); err != nil {
				t.Fatalf("повтор после ошибки: %v", err)
			}
			if n := doer.count(); n != 2 {
				t.Fatalf("ожидалось 2 POST, получено %d", n)
			}
		})
	}
}

func TestDeliverValidatesRequestT200(t *testing.T) {
	doer := &T200fakeDoer{}
	d := T200newTestDeliverer(t, doer)

	cases := []T200Request{
		{SessionID: "", Text: "факт"},
		{SessionID: " ", Text: "факт"},
		{SessionID: "ses_8", Text: ""},
		{SessionID: "ses_8", Text: "факт", T200Delivery: "unknown"},
	}
	for _, req := range cases {
		if _, err := d.Deliver(context.Background(), req); err == nil {
			t.Fatalf("ожидалась ошибка для запроса %+v", req)
		}
	}
	if n := doer.count(); n != 0 {
		t.Fatalf("неверный запрос не должен отправляться, отправлено %d", n)
	}
}

func TestDeliverEscapesSessionIDT200(t *testing.T) {
	doer := &T200fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return T200fakeResponse(`{"data":{"id":"inbox_9","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := T200newTestDeliverer(t, doer)

	if _, err := d.Deliver(context.Background(), T200Request{SessionID: "ses 9", Text: "факт"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	calls := doer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(calls))
	}
	if calls[0].path != "/api/session/ses%209/prompt" {
		t.Fatalf("sessionID не экранирован: %q", calls[0].path)
	}
}

func TestNewRejectsBadURLT200(t *testing.T) {
	for _, raw := range []string{"", "ftp://127.0.0.1:1", "http://", "  "} {
		if _, err := T200New(raw, "pw", &T200fakeDoer{}); err == nil {
			t.Fatalf("ожидалась ошибка для url %q", raw)
		}
	}
}

func TestParseAdmissionBareObjectT200(t *testing.T) {
	got := T200parseAdmission([]byte(`{"id":"inbox_10","delivery":"steer"}`))
	if got.ID != "inbox_10" || got.T200Delivery != T200DeliverySteer {
		t.Fatalf("bare object не разобран: %+v", got)
	}
}

// ===== T3.201 PATCH inbox для отложенной задачи (#282) =====

func TestInboxParsesDeferredItemT201(t *testing.T) {
	a := &T201API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] != "GET" || args[2] != "/api/session/ses_test/inbox" {
			t.Fatalf("unexpected GET args: %q", args)
		}
		return []byte(`{"data":[{"id":"msg_456","delivery":"queue","text":"Recheck CI"}]}`), nil
	}}
	items, err := a.Inbox(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(items) != 1 || items[0].ID != "msg_456" || items[0].Delivery != "queue" || items[0].Text != "Recheck CI" {
		t.Fatalf("items = %+v", items)
	}
}

func TestInboxAcceptsBareArrayT201(t *testing.T) {
	a := &T201API{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`[{"id":"msg_1","delivery":"steer"}]`), nil
	}}
	items, err := a.Inbox(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(items) != 1 || items[0].ID != "msg_1" {
		t.Fatalf("items = %+v", items)
	}
}

func TestFindInboxItemT201(t *testing.T) {
	items := []T201InboxItem{{ID: "msg_1"}, {ID: "msg_456"}}
	if got := T201FindInboxItem(items, "msg_456"); got == nil || got.ID != "msg_456" {
		t.Fatalf("T201FindInboxItem = %+v", got)
	}
	if got := T201FindInboxItem(items, "missing"); got != nil {
		t.Fatalf("missing id = %+v, want nil", got)
	}
	if got := T201FindInboxItem(items, ""); got != nil {
		t.Fatalf("empty id = %+v, want nil", got)
	}
}

func TestSteerPendingPatchesFoundItemT201(t *testing.T) {
	var calls [][]string
	a := &T201API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[1] == "GET" {
			return []byte(`{"data":[{"id":"msg_456","delivery":"queue","text":"Recheck CI"}]}`), nil
		}
		return nil, nil // 204 has no body
	}}
	if err := a.SteerPending(context.Background(), "ses_test", "msg_456"); err != nil {
		t.Fatalf("SteerPending: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (GET then PATCH)", len(calls))
	}
	patch := calls[1]
	if patch[0] != "api" || patch[1] != "PATCH" || patch[2] != "/api/session/ses_test/inbox/msg_456" {
		t.Fatalf("unexpected PATCH args: %q", patch)
	}
	if patch[3] != "--data" {
		t.Fatalf("missing --data: %q", patch)
	}
	var sent struct {
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(patch[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != T201deliverySteer {
		t.Fatalf("delivery = %q, want steer", sent.Delivery)
	}
}

func TestSteerPendingRejectsMissingItemT201(t *testing.T) {
	patched := false
	a := &T201API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] == "GET" {
			return []byte(`{"data":[]}`), nil
		}
		patched = true
		return nil, nil
	}}
	err := a.SteerPending(context.Background(), "ses_test", "msg_456")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
	if patched {
		t.Fatal("PATCH must not run for a missing item")
	}
}

func TestQueuePendingRestoresQueueWithoutTextT201(t *testing.T) {
	var patch []string
	a := &T201API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		patch = append([]string(nil), args...)
		return nil, nil
	}}
	if err := a.QueuePending(context.Background(), "ses_test", "msg_456"); err != nil {
		t.Fatalf("QueuePending: %v", err)
	}
	if patch[1] != "PATCH" || patch[2] != "/api/session/ses_test/inbox/msg_456" {
		t.Fatalf("unexpected PATCH args: %q", patch)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(patch[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent["delivery"] != T201deliveryQueue || len(sent) != 1 {
		t.Fatalf("payload = %v, want only delivery=queue", sent)
	}
}

func TestSetDeliveryValidatesInputT201(t *testing.T) {
	called := false
	a := &T201API{Runner: func(context.Context, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}}
	cases := []struct{ session, id, delivery string }{
		{"not-a-session", "msg_1", T201deliverySteer},
		{"ses_test", "", T201deliverySteer},
		{"ses_test", "msg_1", "unknown"},
	}
	for _, tc := range cases {
		if err := a.SetDelivery(context.Background(), tc.session, tc.id, tc.delivery); err == nil {
			t.Fatalf("SetDelivery(%q, %q, %q) must fail", tc.session, tc.id, tc.delivery)
		}
	}
	if called {
		t.Fatal("Runner must not be called for invalid input")
	}
}

func TestSetDeliveryPropagatesTransportErrorT201(t *testing.T) {
	a := &T201API{Runner: func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("api: 404 not found")
	}}
	err := a.SetDelivery(context.Background(), "ses_test", "msg_1", T201deliverySteer)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want wrapped 404", err)
	}
}

// ===== T3.202 steer/queue и interrupt (#287) =====

func TestHooksApplyOnlyToPromptT202(t *testing.T) {
	cases := map[T202InputKind]bool{
		T202InputPrompt:    true,
		T202InputSynthetic: false,
		T202InputShell:     false,
		T202InputCompact:   false,
		T202InputMove:      false,
	}
	for kind, want := range cases {
		if got := T202HooksApply(kind); got != want {
			t.Errorf("T202HooksApply(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestDefaultDeliveryT202(t *testing.T) {
	for _, kind := range []T202InputKind{T202InputPrompt, T202InputSynthetic, T202InputCompact} {
		if got := T202DefaultDelivery(kind); got != T202DeliverySteer {
			t.Errorf("T202DefaultDelivery(%q) = %q, want steer", kind, got)
		}
	}
}

func TestPlanAdmissionSteerAtStepBoundaryT202(t *testing.T) {
	active := T202PlanAdmission(T202DeliverySteer, T202ExecActive)
	if !active.SchedulesLoop || !active.AtStepBoundary || active.Parked {
		t.Fatalf("steer/active = %+v, want scheduled at step boundary", active)
	}

	idle := T202PlanAdmission(T202DeliverySteer, T202ExecIdle)
	if !idle.SchedulesLoop || !idle.IdlePriority || idle.AtStepBoundary || idle.Parked {
		t.Fatalf("steer/idle = %+v, want scheduled idle with priority", idle)
	}
}

func TestPlanAdmissionQueueAlwaysParksT202(t *testing.T) {
	for _, state := range []T202ExecutionState{T202ExecIdle, T202ExecActive} {
		a := T202PlanAdmission(T202DeliveryQueue, state)
		if a.SchedulesLoop || !a.Parked || a.AtStepBoundary {
			t.Fatalf("queue/%s = %+v, want parked", state, a)
		}
	}
}

func TestQueueStaysParkedAfterInterruptT202(t *testing.T) {
	for _, resume := range []bool{true, false} {
		q := T202AfterInterrupt(T202DeliveryQueue, resume)
		if !q.Parked || q.SchedulesLoop {
			t.Fatalf("queue after interrupt resume=%v = %+v, want parked", resume, q)
		}
		s := T202AfterInterrupt(T202DeliverySteer, resume)
		if !s.AtStepBoundary || s.Parked {
			t.Fatalf("steer after interrupt resume=%v = %+v, want step boundary", resume, s)
		}
	}
}

func TestNextInputSteersWinAtIdleT202(t *testing.T) {
	pending := []T202PendingInput{
		{ID: "q1", Mode: T202DeliveryQueue},
		{ID: "s2", Mode: T202DeliverySteer},
		{ID: "q3", Mode: T202DeliveryQueue},
	}
	got, ok := T202NextInput(pending)
	if !ok || got.ID != "s2" {
		t.Fatalf("T202NextInput = %+v, %v; want steer s2", got, ok)
	}

	queued := []T202PendingInput{{ID: "q1", Mode: T202DeliveryQueue}, {ID: "q2", Mode: T202DeliveryQueue}}
	got, ok = T202NextInput(queued)
	if !ok || got.ID != "q1" {
		t.Fatalf("T202NextInput(queued) = %+v, %v; want first queued", got, ok)
	}

	if _, ok := T202NextInput(nil); ok {
		t.Fatal("T202NextInput(nil) must report no input")
	}
}

func Test409IsDuplicateIDNotBusySessionT202(t *testing.T) {
	if got := T202ClassifyHTTP(409); got != T202StatusDuplicateID {
		t.Fatalf("T202ClassifyHTTP(409) = %q, want duplicate-id", got)
	}
	if got := T202ClassifyHTTP(200); got != T202StatusAdmitted {
		t.Fatalf("T202ClassifyHTTP(200) = %q, want admitted", got)
	}
	if got := T202ClassifyHTTP(500); got != T202StatusError {
		t.Fatalf("T202ClassifyHTTP(500) = %q, want error", got)
	}
	if T202BusySession(T202StatusDuplicateID) || T202BusySession(T202StatusAdmitted) || T202BusySession(T202StatusError) {
		t.Fatal("no status may be read as a busy session")
	}
}

func T202captureAPI(t *testing.T, response string) (*T202API, *[]string) {
	t.Helper()
	got := new([]string)
	api := &T202API{T202Runner: func(_ context.Context, args ...string) ([]byte, error) {
		*got = args
		return []byte(response), nil
	}}
	return api, got
}

func TestPromptSendsExplicitDeliveryModeT202(t *testing.T) {
	for _, mode := range []T202DeliveryMode{T202DeliverySteer, T202DeliveryQueue} {
		api, got := T202captureAPI(t, `{"data":{"id":"msg_1"}}`)
		id, err := api.Prompt(context.Background(), "ses_test", "wake", mode)
		if err != nil || id != "msg_1" {
			t.Fatalf("Prompt(%q) = %q, %v", mode, id, err)
		}
		args := *got
		if len(args) != 5 || args[0] != "api" || args[1] != "POST" ||
			args[2] != "/api/session/ses_test/prompt" || args[3] != "--data" {
			t.Fatalf("unexpected api call for %q: %q", mode, args)
		}
		var sent struct {
			Text     string `json:"text"`
			Delivery string `json:"delivery"`
		}
		if err := json.Unmarshal([]byte(args[4]), &sent); err != nil {
			t.Fatalf("decode --data: %v", err)
		}
		if sent.Delivery != string(mode) || sent.Text != "wake" {
			t.Fatalf("body for %q = %+v", mode, sent)
		}
	}
}

// A wake usually reaches a subscriber whose last turn already succeeded. The
// prompt must ask the host to resume that session, not only steer a new turn.
func TestPromptResumesCompletedSessionT202(t *testing.T) {
	var got []string
	api := &T202API{T202Runner: func(_ context.Context, args ...string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte(`{"data":{"id":"msg_resumed","sessionID":"ses_done"}}`), nil
	}}
	const text = `<beacon-event>{"watchID":"w","obligationID":"o","revision":1,"verdict":"ready"}</beacon-event>`
	id, err := api.Prompt(context.Background(), "ses_done", text, T202DeliverySteer)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if id != "msg_resumed" {
		t.Fatalf("id = %q, want msg_resumed", id)
	}
	if len(got) != 5 || got[0] != "api" || got[1] != "POST" ||
		got[2] != "/api/session/ses_done/prompt" || got[3] != "--data" {
		t.Fatalf("api call = %q", got)
	}
	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
		Resume   *bool  `json:"resume"`
	}
	if err := json.Unmarshal([]byte(got[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Resume == nil || !*sent.Resume {
		t.Fatalf("resume = %v, want true in %s", sent.Resume, got[4])
	}
	if sent.Delivery != "steer" {
		t.Fatalf("delivery = %q, want steer", sent.Delivery)
	}
	if sent.Text != text {
		t.Fatalf("text = %q, want %q", sent.Text, text)
	}
}

func TestPromptRejectsInvalidSessionIDWithoutRunnerT202(t *testing.T) {
	called := false
	api := &T202API{T202Runner: func(context.Context, ...string) ([]byte, error) {
		called = true
		return []byte(`{"data":{"id":"msg_1"}}`), nil
	}}
	if _, err := api.Prompt(context.Background(), "not-a-session", "wake", T202DeliverySteer); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	if called {
		t.Fatal("runner must not be called for an invalid session id")
	}
}

func TestParkSendsQueueDeliveryT202(t *testing.T) {
	api, got := T202captureAPI(t, `{"data":{"id":"msg_parked"}}`)
	id, err := api.Park(context.Background(), "ses_test", "park")
	if err != nil || id != "msg_parked" {
		t.Fatalf("Park = %q, %v", id, err)
	}
	args := *got
	var sent struct {
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(args[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "queue" {
		t.Fatalf("Park delivery = %q, want queue", sent.Delivery)
	}
}

func TestInterruptResumeQueryT202(t *testing.T) {
	for _, resume := range []bool{true, false} {
		api, got := T202captureAPI(t, `{}`)
		if err := api.Interrupt(context.Background(), "ses_test", resume); err != nil {
			t.Fatalf("Interrupt(%v): %v", resume, err)
		}
		args := *got
		want := "/api/session/ses_test/interrupt?resume=" + map[bool]string{true: "true", false: "false"}[resume]
		if len(args) != 3 || args[0] != "api" || args[1] != "POST" || args[2] != want {
			t.Fatalf("Interrupt(%v) args = %q, want %q", resume, args, want)
		}
	}
}

func TestPromotePatchesInboxToSteerT202(t *testing.T) {
	api, got := T202captureAPI(t, `{}`)
	if err := api.Promote(context.Background(), "ses_test", "msg_parked"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	args := *got
	if len(args) != 5 || args[0] != "api" || args[1] != "PATCH" ||
		args[2] != "/api/session/ses_test/inbox/msg_parked" || args[3] != "--data" {
		t.Fatalf("unexpected promote call: %q", args)
	}
	if !strings.Contains(args[4], `"delivery":"steer"`) {
		t.Fatalf("promote body = %q, want steer", args[4])
	}
}

func TestEnvelopeWhitelistsFieldsT202(t *testing.T) {
	env := T202Envelope(T202Pointer{WatchID: "w-1", ObligationID: "o-1", Revision: 7, Verdict: "ready"})
	if !strings.HasPrefix(env, "<beacon-event>") || !strings.HasSuffix(env, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", env)
	}
	for _, want := range []string{`"watchID":"w-1"`, `"obligationID":"o-1"`, `"revision":7`, `"verdict":"ready"`} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %s: %s", want, env)
		}
	}
}

func TestPromptRejectsBadSessionAndMissingAckT202(t *testing.T) {
	api := &T202API{T202Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{}}`), nil
	}}
	if _, err := api.Prompt(context.Background(), "not-a-session", "x", T202DeliverySteer); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	if _, err := api.Prompt(context.Background(), "ses_test", "x", T202DeliverySteer); err == nil {
		t.Fatal("ack without message id must be rejected")
	}
}

// ===== T3.203 CLI opencode api (#285) =====

func TestEnvelopeWhitelistsFieldsT203(t *testing.T) {
	env := T203Envelope(domain.Pointer{WatchID: "watch-1", ObligationID: "obl-1", Revision: 7})
	if !strings.HasPrefix(env, "<beacon-event>") || !strings.HasSuffix(env, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", env)
	}
	for _, want := range []string{`"watchID":"watch-1"`, `"obligationID":"obl-1"`, `"revision":7`} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %q: %s", want, env)
		}
	}
	if strings.Contains(env, "verdict") {
		t.Errorf("envelope must not carry verdict detail: %s", env)
	}
}

func TestPromptSyntaxesEquivalentT203(t *testing.T) {
	c := &T203CLI{}
	text := T203Envelope(domain.Pointer{WatchID: "w", ObligationID: "o", Revision: 1})

	rpc := c.PromptArgs(T203PromptRPC, "ses_test", text)
	path := c.PromptArgs(T203PromptPath, "ses_test", text)

	if rpc[1] != "session.prompt" || rpc[2] != "--param" || rpc[3] != "sessionID=ses_test" {
		t.Fatalf("rpc args = %q", rpc)
	}
	if path[1] != "POST" || path[2] != "/api/session/ses_test/prompt" {
		t.Fatalf("path args = %q", path)
	}
	if rpc[len(rpc)-2] != "--data" || path[len(path)-2] != "--data" {
		t.Fatalf("both syntaxes must carry --data: rpc=%q path=%q", rpc, path)
	}
	if rpc[len(rpc)-1] != path[len(path)-1] {
		t.Fatalf("--data differs:\nrpc  = %s\npath = %s", rpc[len(rpc)-1], path[len(path)-1])
	}

	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(rpc[len(rpc)-1]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "steer" || sent.Text != text {
		t.Fatalf("prompt body = %+v", sent)
	}
}

func TestServerFlagOnEveryCommandT203(t *testing.T) {
	c := &T203CLI{Server: "http://127.0.0.1:4096"}
	for name, args := range map[string][]string{
		"openapi": c.OpenAPIArgs(),
		"rpc":     c.PromptArgs(T203PromptRPC, "ses_test", "text"),
		"path":    c.PromptArgs(T203PromptPath, "ses_test", "text"),
		"events":  c.EventArgs("session.execution.started"),
	} {
		if len(args) < 4 || args[1] != "--server" || args[2] != c.Server {
			t.Errorf("%s args missing --server URL: %q", name, args)
		}
	}
}

func TestPromptBothSyntaxesReturnSameAdmissionT203(t *testing.T) {
	var calls [][]string
	c := &T203CLI{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte(`{"data":{"id":"msg_1","sessionID":"ses_test"}}`), nil
	}}
	for _, syntax := range []T203PromptSyntax{T203PromptRPC, T203PromptPath} {
		id, err := c.Prompt(context.Background(), syntax, "ses_test", "text")
		if err != nil {
			t.Fatalf("Prompt(%d): %v", syntax, err)
		}
		if id != "msg_1" {
			t.Fatalf("Prompt(%d) id = %q", syntax, id)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("runner calls = %d", len(calls))
	}
	if !reflect.DeepEqual(calls[0][len(calls[0])-1], calls[1][len(calls[1])-1]) {
		t.Fatalf("--data differs between syntaxes: %q vs %q", calls[0], calls[1])
	}
}

func TestPromptRejectsBadSessionAndMissingAckT203(t *testing.T) {
	c := &T203CLI{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{}}`), nil
	}}
	if _, err := c.Prompt(context.Background(), T203PromptPath, "not-a-session", "text"); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	if _, err := c.Prompt(context.Background(), T203PromptPath, "ses_test", "text"); err == nil {
		t.Fatal("ack without message id must be rejected")
	}
}

func TestPromptPropagatesRunnerErrorT203(t *testing.T) {
	c := &T203CLI{Runner: func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("exit 1")
	}}
	if _, err := c.Prompt(context.Background(), T203PromptRPC, "ses_test", "text"); err == nil {
		t.Fatal("runner error must be returned")
	}
}

func TestFramesParseAndDropPastEventsT203(t *testing.T) {
	raw := ": heartbeat\n" +
		"event: session.execution.started\n" +
		"data: {\"past\":true}\n\n" +
		"event: server.connected\n" +
		"data: {}\n\n" +
		"event: session.inbox.delivered\n" +
		"data: {\"id\":\"msg_1\"}\n\n" +
		"event: session.execution.started\n" +
		"data: {\"sessionID\":\"ses_test\"}"

	all := T203Frames([]byte(raw))
	if len(all) != 4 {
		t.Fatalf("frames = %d, want 4: %+v", len(all), all)
	}

	events := T203Events(all)
	if len(events) != 3 {
		t.Fatalf("events after connect = %d, want 3: %+v", len(events), events)
	}
	if events[0].Event != T203connectedEvent {
		t.Fatalf("first event = %q, want %q", events[0].Event, T203connectedEvent)
	}
	if events[1].Event != "session.inbox.delivered" {
		t.Fatalf("second event = %q", events[1].Event)
	}
	if !strings.Contains(events[2].Data, "ses_test") {
		t.Fatalf("last frame lost data: %q", events[2].Data)
	}
}

func TestSubscribeRequiresConnectionFrameT203(t *testing.T) {
	bad := &T203CLI{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte("event: session.execution.started\ndata: {}\n\n"), nil
	}}
	if _, err := bad.Subscribe(context.Background()); err == nil {
		t.Fatal("stream without server.connected must be rejected")
	}

	good := &T203CLI{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		if got := args[len(args)-1]; !strings.Contains(got, "/api/event?types=") {
			t.Fatalf("event args = %q", args)
		}
		return []byte("event: server.connected\ndata: {}\n\n"), nil
	}}
	frames, err := good.Subscribe(context.Background(), "session.execution.started")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if len(frames) != 1 || frames[0].Event != T203connectedEvent {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestDeliverUsesEnvelopeT203(t *testing.T) {
	var args []string
	c := &T203CLI{Runner: func(_ context.Context, a ...string) ([]byte, error) {
		args = a
		return []byte(`{"data":{"id":"msg_9"}}`), nil
	}}
	p := domain.Pointer{WatchID: "w", ObligationID: "o", Revision: 3}
	if err := c.Deliver(context.Background(), "ses_test", p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(args[len(args)-1]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "steer" || sent.Text != T203Envelope(p) {
		t.Fatalf("delivery = %+v, want envelope %q", sent, T203Envelope(p))
	}
}

// ===== T3.204 негативный контроль через steer PATCH (#290) =====

// T204controlHost is the fake OpenCode host for the T3.204 negative control. It
// emits session events only in response to real T204API calls: a queue prompt
// enqueues, and a PATCH to steer starts execution and then goes idle. A
// coincidental execution.started would have to be written into this host by
// hand, which is what TestVerifySteerCausalityRejectsCoincidentalStartT204 does.
type T204controlHost struct {
	sessionID      string
	events         []T204SessionEvent
	inboxID        string
	promptDelivery string
	promptText     string
	patches        int
}

func T204newControlHost(sessionID string) *T204controlHost {
	return &T204controlHost{sessionID: sessionID}
}

func (h *T204controlHost) runner(_ context.Context, args ...string) ([]byte, error) {
	if len(args) < 3 || args[0] != "api" {
		return nil, fmt.Errorf("unexpected runner call: %q", args)
	}
	switch args[1] {
	case "POST":
		switch {
		case strings.HasSuffix(args[2], "/prompt"):
			data, err := T204dataArg(args)
			if err != nil {
				return nil, err
			}
			var body struct {
				Text     string `json:"text"`
				Delivery string `json:"delivery"`
			}
			if err := json.Unmarshal([]byte(data), &body); err != nil {
				return nil, err
			}
			h.promptDelivery = body.Delivery
			h.promptText = body.Text
			h.inboxID = "msg_1"
			h.events = append(h.events, T204SessionEvent{Type: T204EventInboxEnqueued, SessionID: h.sessionID})
			return []byte(`{"data":{"id":"msg_1"}}`), nil
		case strings.HasSuffix(args[2], "/interrupt"):
			return []byte(`{"interrupted":false}`), nil
		}
	case "GET":
		switch args[2] {
		case "/api/session/" + h.sessionID + "/inbox":
			return []byte(`{"data":[{"id":"msg_1","delivery":"queue","text":"parked task"}]}`), nil
		case "/api/session/active":
			return []byte(`{"data":[]}`), nil
		}
	case "PATCH":
		data, err := T204dataArg(args)
		if err != nil {
			return nil, err
		}
		var body struct {
			Delivery string `json:"delivery"`
		}
		if err := json.Unmarshal([]byte(data), &body); err != nil {
			return nil, err
		}
		h.patches++
		if body.Delivery == T204deliverySteer {
			h.events = append(h.events,
				T204SessionEvent{Type: T204EventDeliveryChanged, SessionID: h.sessionID},
				T204SessionEvent{Type: T204EventExecutionStarted, SessionID: h.sessionID},
				T204SessionEvent{Type: T204EventIdle, SessionID: h.sessionID},
			)
		}
		return nil, nil // 204 has no body
	}
	return nil, fmt.Errorf("unexpected api call: %q", args)
}

func T204dataArg(args []string) (string, error) {
	if len(args) < 5 || args[3] != "--data" {
		return "", fmt.Errorf("missing --data in %q", args)
	}
	return args[4], nil
}

func T204hasEvent(events []T204SessionEvent, eventType, sessionID string) bool {
	for _, event := range events {
		if event.Type == eventType && event.SessionID == sessionID {
			return true
		}
	}
	return false
}

func T204testPointer() T204Pointer {
	return T204Pointer{WatchID: "watch-1", ObligationID: "obl-1", Revision: 3, Verdict: "ready"}
}

// The first half of the control: a queue prompt without PATCH leaves the
// session idle. There is no execution.started, interrupt reports false and the
// session is absent from /api/session/active.
func TestParkedQueuePromptDoesNotStartExecutionT204(t *testing.T) {
	const sessionID = "ses_test"
	host := T204newControlHost(sessionID)
	a := &T204API{Runner: host.runner}
	ctx := context.Background()

	inboxID, err := a.Park(ctx, sessionID, T204testPointer())
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if inboxID != "msg_1" {
		t.Fatalf("inboxID = %q, want msg_1", inboxID)
	}
	if host.promptDelivery != T204deliveryQueue {
		t.Fatalf("prompt delivery = %q, want queue", host.promptDelivery)
	}
	if !strings.HasPrefix(host.promptText, "<beacon-event>") ||
		!strings.HasSuffix(host.promptText, "</beacon-event>") {
		t.Fatalf("prompt text is not an envelope: %q", host.promptText)
	}
	if T204hasEvent(host.events, T204EventExecutionStarted, sessionID) {
		t.Fatal("queue prompt started execution without a steer switch")
	}
	if host.patches != 0 {
		t.Fatalf("patches = %d, want 0 before the steer switch", host.patches)
	}

	interrupted, err := a.Interrupt(ctx, sessionID)
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if interrupted {
		t.Fatal("interrupt = true on an idle session, want false")
	}
	active, err := a.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active sessions = %q, want none for an idle session", active)
	}
	if err := T204VerifySteerCausality(host.events, sessionID); err == nil {
		t.Fatal("queue-only event log passed the causality check")
	}
}

// The second half of the control: the same parked task starts only after the
// PATCH to steer, and the event order is enqueued, changed, started, idle.
func TestSteerAfterParkStartsExecutionT204(t *testing.T) {
	const sessionID = "ses_test"
	host := T204newControlHost(sessionID)
	a := &T204API{Runner: host.runner}
	ctx := context.Background()

	inboxID, err := a.Park(ctx, sessionID, T204testPointer())
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if T204hasEvent(host.events, T204EventExecutionStarted, sessionID) {
		t.Fatal("execution started before the steer switch")
	}

	if err := a.SteerPending(ctx, sessionID, inboxID); err != nil {
		t.Fatalf("SteerPending: %v", err)
	}
	if host.patches != 1 {
		t.Fatalf("patches = %d, want 1", host.patches)
	}
	want := []string{
		T204EventInboxEnqueued,
		T204EventDeliveryChanged,
		T204EventExecutionStarted,
		T204EventIdle,
	}
	if len(host.events) != len(want) {
		t.Fatalf("events = %+v, want %d events", host.events, len(want))
	}
	for i, eventType := range want {
		if host.events[i].Type != eventType || host.events[i].SessionID != sessionID {
			t.Fatalf("events[%d] = %+v, want %s for %s", i, host.events[i], eventType, sessionID)
		}
	}
	if err := T204VerifySteerCausality(host.events, sessionID); err != nil {
		t.Fatalf("T204VerifySteerCausality: %v", err)
	}
}

// The control of the control: a log with a coincidental execution.started
// before the steer switch must be rejected. Without this, the positive test
// would pass even if the host started sessions for unrelated activity.
func TestVerifySteerCausalityRejectsCoincidentalStartT204(t *testing.T) {
	const sessionID = "ses_test"
	cases := []struct {
		name   string
		events []T204SessionEvent
		want   string
	}{
		{
			name: "execution started before steer",
			events: []T204SessionEvent{
				{Type: T204EventInboxEnqueued, SessionID: sessionID},
				{Type: T204EventExecutionStarted, SessionID: sessionID},
				{Type: T204EventDeliveryChanged, SessionID: sessionID},
				{Type: T204EventExecutionStarted, SessionID: sessionID},
				{Type: T204EventIdle, SessionID: sessionID},
			},
			want: "before steer",
		},
		{
			name: "steer without execution",
			events: []T204SessionEvent{
				{Type: T204EventInboxEnqueued, SessionID: sessionID},
				{Type: T204EventDeliveryChanged, SessionID: sessionID},
				{Type: T204EventIdle, SessionID: sessionID},
			},
			want: "after steer",
		},
		{
			name: "execution without idle",
			events: []T204SessionEvent{
				{Type: T204EventInboxEnqueued, SessionID: sessionID},
				{Type: T204EventDeliveryChanged, SessionID: sessionID},
				{Type: T204EventExecutionStarted, SessionID: sessionID},
			},
			want: "no session.idle",
		},
		{
			name: "only another session changed delivery",
			events: []T204SessionEvent{
				{Type: T204EventDeliveryChanged, SessionID: "ses_other"},
				{Type: T204EventExecutionStarted, SessionID: "ses_other"},
				{Type: T204EventIdle, SessionID: "ses_other"},
			},
			want: "no session.inbox.delivery.changed for ses_test",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := T204VerifySteerCausality(tc.events, sessionID)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("T204VerifySteerCausality = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

// ===== T3.205 сравнение wake-механизмов (#291) =====

var T205wantMechanisms = []T205Mechanism{T205MechanismPi, T205MechanismKilo, T205MechanismCursor}
var T205wantAxes = []T205Axis{T205AxisTruth, T205AxisDedup, T205AxisBusy, T205AxisRestart, T205AxisDelivery}

func TestMatrixCoversEveryAxisForEveryMechanismT205(t *testing.T) {
	seen := make(map[T205Mechanism]map[T205Axis]int)
	for _, m := range T205wantMechanisms {
		seen[m] = make(map[T205Axis]int)
		for _, a := range T205wantAxes {
			seen[m][a] = 0
		}
	}
	for _, r := range T205Matrix {
		if _, ok := seen[r.T205Mechanism]; !ok {
			t.Fatalf("unknown mechanism %q in matrix", r.T205Mechanism)
		}
		if _, ok := seen[r.T205Mechanism][r.T205Axis]; !ok {
			t.Fatalf("unknown axis %q in matrix", r.T205Axis)
		}
		seen[r.T205Mechanism][r.T205Axis]++
	}
	for _, m := range T205wantMechanisms {
		for _, a := range T205wantAxes {
			if got := seen[m][a]; got != 1 {
				t.Fatalf("cell (%s, %s) occurs %d times, want 1", m, a, got)
			}
		}
	}
	if len(T205Matrix) != 15 {
		t.Fatalf("matrix has %d rows, want 15", len(T205Matrix))
	}
}

func TestMatrixRowsAreGroundedT205(t *testing.T) {
	known := map[string]bool{
		"wake-pi.md":        true,
		"wake-kilo-code.md": true,
		"wake-cursor.md":    true,
	}
	for _, r := range T205Matrix {
		if strings.TrimSpace(r.Fact) == "" {
			t.Fatalf("T205row (%s, %s) has no fact", r.T205Mechanism, r.T205Axis)
		}
		if !known[r.Source] {
			t.Fatalf("T205row (%s, %s) cites unknown source %q", r.T205Mechanism, r.T205Axis, r.Source)
		}
	}
}

func TestSourceAnchorsT205(t *testing.T) {
	want := make(map[string]string)
	want["pi-original-url-404"] = "github.com/mariozechner/pi-coding-agent"
	want["pi-repository"] = "https://github.com/earendil-works/pi"
	want["kilo-commit"] = "bd8b98aeab348c08221a836ba091b855a62814e9"
	want["cursor-api-split"] = "v0/v1"

	if len(T205SourceAnchors) != len(want) {
		t.Fatalf("anchors = %d, want %d", len(T205SourceAnchors), len(want))
	}
	for _, a := range T205SourceAnchors {
		v, ok := want[a.ID]
		if !ok {
			t.Fatalf("unexpected T205anchor %q", a.ID)
		}
		if a.Value != v {
			t.Fatalf("T205anchor %q = %q, want %q", a.ID, a.Value, v)
		}
	}
}

func TestCommonPrimitivesT205(t *testing.T) {
	want := make(map[T205Primitive]bool)
	want[T205PrimitiveDeliveryID] = true
	want[T205PrimitiveEventsHint] = true
	want[T205PrimitiveStateTruth] = true
	want[T205PrimitiveLeaseRelease] = true

	if len(T205Primitives) != len(want) {
		t.Fatalf("primitives = %d, want %d", len(T205Primitives), len(want))
	}
	known := map[string]bool{
		"wake-pi.md":        true,
		"wake-kilo-code.md": true,
		"wake-cursor.md":    true,
	}
	seen := make(map[T205Primitive]bool)
	for _, p := range T205Primitives {
		if seen[p.T205Primitive] {
			t.Fatalf("duplicate T205primitive %q", p.T205Primitive)
		}
		seen[p.T205Primitive] = true
		if !want[p.T205Primitive] {
			t.Fatalf("unexpected T205primitive %q", p.T205Primitive)
		}
		if strings.TrimSpace(p.Summary) == "" {
			t.Fatalf("T205primitive %q has no summary", p.T205Primitive)
		}
		if len(p.Reports) == 0 {
			t.Fatalf("T205primitive %q cites no report", p.T205Primitive)
		}
		for _, r := range p.Reports {
			if !known[r] {
				t.Fatalf("T205primitive %q cites unknown report %q", p.T205Primitive, r)
			}
		}
	}
}

func TestNonMechanismsAreTheRejectedOnesT205(t *testing.T) {
	want := []string{
		"a built-in sleep scheduler in the Pi coding-agent core",
		"automatic events.subscribe reconnect as a recovery path",
		"FINISHED, IDLE or agent_end as final readiness",
	}
	if len(T205NonMechanisms) != len(want) {
		t.Fatalf("non-mechanisms = %d, want %d", len(T205NonMechanisms), len(want))
	}
	set := make(map[string]bool)
	for _, n := range T205NonMechanisms {
		set[n] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Fatalf("missing rejected behaviour %q", w)
		}
	}
}

func TestOverviewContainsEveryCellT205(t *testing.T) {
	out := T205Overview()
	if !strings.Contains(out, "| T205Axis | Pi | Kilo Code | Cursor |") {
		t.Fatal("overview has no header")
	}
	for _, m := range T205wantMechanisms {
		for _, a := range T205wantAxes {
			fact := T205FactFor(m, a)
			if fact == "" {
				t.Fatalf("missing fact for (%s, %s)", m, a)
			}
			if !strings.Contains(out, fact) {
				t.Fatalf("overview omits fact for (%s, %s)", m, a)
			}
		}
	}
}

// ===== T3.206 deliveryId и журнал (#288) =====

func T206testRequest(id, key string, body []byte) T206WakeRequest {
	return T206WakeRequest{
		DeliveryID:     id,
		IdempotencyKey: key,
		Target:         "ses_test",
		Kind:           T206KindSteer,
		Body:           body,
	}
}

func TestWakeRequestFromWebhookT206(t *testing.T) {
	h := http.Header{}
	h.Set(T206HeaderWebhookID, "wh_123")
	h.Set(T206HeaderIdempotencyKey, "key-1")
	req, err := T206WakeRequestFromWebhook(h, "ses_test", T206KindSteer, []byte(`{"kind":"done"}`))
	if err != nil {
		t.Fatalf("T206WakeRequestFromWebhook: %v", err)
	}
	if req.DeliveryID != "wh_123" || req.IdempotencyKey != "key-1" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Target != "ses_test" || req.Kind != T206KindSteer || req.At.IsZero() {
		t.Fatalf("unexpected request fields: %+v", req)
	}
}

func TestWakeRequestFromWebhookRequiresIDT206(t *testing.T) {
	_, err := T206WakeRequestFromWebhook(http.Header{}, "ses_test", T206KindSteer, nil)
	if !errors.Is(err, T206ErrDeliveryIDRequired) {
		t.Fatalf("err = %v, want T206ErrDeliveryIDRequired", err)
	}
}

func TestDuplicateDeliveryStartsNoSecondJobT206(t *testing.T) {
	var calls int32
	d := &T206Deliverer{
		Journal: T206NewDeliveryJournal(),
		T206Transport: func(context.Context, T206WakeRequest) (T206DeliveryStatus, error) {
			atomic.AddInt32(&calls, 1)
			return T206StatusStarted, nil
		},
	}
	req := T206testRequest("wh_1", "", []byte("body"))
	first, err := d.Deliver(context.Background(), req)
	if err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	if first.Status != T206StatusStarted || first.Coalesced {
		t.Fatalf("first = %+v, want started", first)
	}
	second, err := d.Deliver(context.Background(), req)
	if err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if second.Status != T206StatusDuplicate || !second.Coalesced {
		t.Fatalf("second = %+v, want duplicate/coalesced", second)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("transport calls = %d, want 1 (duplicate must not start a second job)", got)
	}
	if d.Journal.Len() != 1 {
		t.Fatalf("journal len = %d, want 1", d.Journal.Len())
	}
}

func TestIdempotencyKeyConflictRejectedT206(t *testing.T) {
	var calls int32
	d := &T206Deliverer{
		Journal: T206NewDeliveryJournal(),
		T206Transport: func(context.Context, T206WakeRequest) (T206DeliveryStatus, error) {
			atomic.AddInt32(&calls, 1)
			return T206StatusDelivered, nil
		},
	}
	if _, err := d.Deliver(context.Background(), T206testRequest("wh_1", "key-1", []byte("body-A"))); err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	_, err := d.Deliver(context.Background(), T206testRequest("wh_2", "key-1", []byte("body-B")))
	if !errors.Is(err, T206ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want T206ErrIdempotencyConflict", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestIdempotencyKeySameBodyIsDuplicateT206(t *testing.T) {
	var calls int32
	d := &T206Deliverer{
		Journal: T206NewDeliveryJournal(),
		T206Transport: func(context.Context, T206WakeRequest) (T206DeliveryStatus, error) {
			atomic.AddInt32(&calls, 1)
			return T206StatusDelivered, nil
		},
	}
	first, _ := d.Deliver(context.Background(), T206testRequest("wh_1", "key-1", []byte("body")))
	second, err := d.Deliver(context.Background(), T206testRequest("wh_2", "key-1", []byte("body")))
	if err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if second.Status != T206StatusDuplicate || !second.Coalesced {
		t.Fatalf("second = %+v, want duplicate", second)
	}
	if second.DeliveryID != first.DeliveryID {
		t.Fatalf("duplicate deliveryId = %q, want %q", second.DeliveryID, first.DeliveryID)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestDeliveryIDReuseWithDifferentBodyRejectedT206(t *testing.T) {
	d := &T206Deliverer{
		Journal:   T206NewDeliveryJournal(),
		T206Transport: func(context.Context, T206WakeRequest) (T206DeliveryStatus, error) { return T206StatusDelivered, nil },
	}
	if _, err := d.Deliver(context.Background(), T206testRequest("wh_1", "", []byte("A"))); err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	_, err := d.Deliver(context.Background(), T206testRequest("wh_1", "", []byte("B")))
	if !errors.Is(err, T206ErrDeliveryIDReuse) {
		t.Fatalf("err = %v, want T206ErrDeliveryIDReuse", err)
	}
}

func TestKeyFixedBeforeExternalCallT206(t *testing.T) {
	d := &T206Deliverer{Journal: T206NewDeliveryJournal()}
	d.T206Transport = func(_ context.Context, req T206WakeRequest) (T206DeliveryStatus, error) {
		got, ok := d.Journal.Get(req.DeliveryID)
		if !ok {
			return T206StatusStarted, errors.New("journal entry missing before external call")
		}
		if got.Status != T206StatusQueued {
			return T206StatusStarted, errors.New("journal status not staged before external call")
		}
		return T206StatusDelivered, nil
	}
	res, err := d.Deliver(context.Background(), T206testRequest("wh_1", "key-1", []byte("body")))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != T206StatusDelivered {
		t.Fatalf("status = %q, want delivered", res.Status)
	}
	after, _ := d.Journal.Get("wh_1")
	if after.Status != T206StatusDelivered {
		t.Fatalf("journal status after = %q, want delivered", after.Status)
	}
}

func TestConcurrentDuplicatesStartOneJobT206(t *testing.T) {
	var calls int32
	d := &T206Deliverer{
		Journal: T206NewDeliveryJournal(),
		T206Transport: func(context.Context, T206WakeRequest) (T206DeliveryStatus, error) {
			atomic.AddInt32(&calls, 1)
			return T206StatusStarted, nil
		},
	}
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.Deliver(context.Background(), T206testRequest("wh_1", "", []byte("body"))); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Deliver: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("transport calls = %d, want 1 under concurrency", got)
	}
}

func TestMissingDeliveryIDRejectedT206(t *testing.T) {
	d := &T206Deliverer{
		Journal:   T206NewDeliveryJournal(),
		T206Transport: func(context.Context, T206WakeRequest) (T206DeliveryStatus, error) { return T206StatusStarted, nil },
	}
	_, err := d.Deliver(context.Background(), T206testRequest("", "", nil))
	if !errors.Is(err, T206ErrDeliveryIDRequired) {
		t.Fatalf("err = %v, want T206ErrDeliveryIDRequired", err)
	}
}

// ===== T3.207 приём 202 и вердикт (#283) =====

// T207waitState polls until the delivery reaches want or the deadline passes.
func T207waitState(t *testing.T, d *T207Deliverer, id T207DeliveryId, want T207Verdict) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d.State(id) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state of %s = %q, want %q", id, d.State(id), want)
}

// T207waitEvent waits for an event of the given type carrying id.
func T207waitEvent(t *testing.T, d *T207Deliverer, typ T207EventType, id T207DeliveryId) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case e := <-d.Events():
			if e.Type == typ && e.T207DeliveryId == id {
				return
			}
		case <-deadline:
			t.Fatalf("no %s event for %s", typ, id)
		}
	}
}

// TestAcceptReturns202BeforeWorkT207 proves the call does not wait for the
// background job. The work blocks until the test releases it, yet Accept must
// have returned with 202 already.
func TestAcceptReturns202BeforeWorkT207(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	work := func(ctx context.Context, req T207Request, id T207DeliveryId) {
		close(started)
		<-release
	}
	d := T207New(work)
	req := T207Request{Target: "session-1", Payload: "obligation-1@7", T207Mode: T207Hosted}

	done := make(chan T207Accepted, 1)
	go func() { done <- d.Accept(context.Background(), req) }()

	var got T207Accepted
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("Accept blocked on background work")
	}
	if got.Status != T207AcceptedStatus {
		t.Errorf("status = %d, want 202", got.Status)
	}
	if got.T207DeliveryId == "" {
		t.Error("deliveryId is empty")
	}
	<-started
	close(release)
}

// TestAcceptedHasNoResultFieldT207 is the acceptance core: the 202 response has
// only status and deliveryId, so a caller cannot read finality from it.
func TestAcceptedHasNoResultFieldT207(t *testing.T) {
	d := T207New(nil)
	got := d.Accept(context.Background(), T207Request{Target: "s"})
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(fields) != 2 {
		t.Errorf("accepted fields = %v, want only status and deliveryId", fields)
	}
	for _, forbidden := range []string{"result", "verdict", "outcome", "done", "finished"} {
		if _, ok := fields[forbidden]; ok {
			t.Errorf("accepted response carries %q; 202 must not contain a result", forbidden)
		}
	}
	if !strings.Contains(string(raw), `"status":202`) {
		t.Errorf("response = %s, want status 202", raw)
	}
}

// TestWorkRunsInBackgroundAndPublishesEventsT207 proves the admitted delivery
// publishes hints after the 202 returned. The events carry the deliveryId.
func TestWorkRunsInBackgroundAndPublishesEventsT207(t *testing.T) {
	d := T207New(func(ctx context.Context, req T207Request, id T207DeliveryId) {})
	got := d.Accept(context.Background(), T207Request{Target: "session-1", T207Mode: T207Local})
	T207waitEvent(t, d, T207AcceptedEvent, got.T207DeliveryId)
	T207waitEvent(t, d, T207DeliveredEvent, got.T207DeliveryId)
}

// TestVerdictComesFromSeparateChannelT207 proves no verdict appears with the 202
// and that a decision arrives only on the verdict channel.
func TestVerdictComesFromSeparateChannelT207(t *testing.T) {
	d := T207New(nil)
	got := d.Accept(context.Background(), T207Request{Target: "s"})
	T207waitState(t, d, got.T207DeliveryId, T207Pending)
	select {
	case v := <-d.Verdicts():
		t.Fatalf("verdict %q appeared before any decision", v)
	default:
	}
	d.Decide(got.T207DeliveryId, T207Ready)
	select {
	case v := <-d.Verdicts():
		if v != T207Ready {
			t.Errorf("verdict = %q, want ready", v)
		}
	case <-time.After(time.Second):
		t.Fatal("no verdict on the verdict channel")
	}
	T207waitState(t, d, got.T207DeliveryId, T207Ready)
}

// TestFinalStatusIsNotDerivedFrom202T207 proves the 202 never completes a task.
// Only an explicit ready verdict moves the state off pending.
func TestFinalStatusIsNotDerivedFrom202T207(t *testing.T) {
	for _, mode := range []T207Mode{T207Hosted, T207Local} {
		t.Run(string(mode), func(t *testing.T) {
			d := T207New(nil)
			got := d.Accept(context.Background(), T207Request{Target: "s", T207Mode: mode})
			if v := d.State(got.T207DeliveryId); v != T207Pending {
				t.Errorf("after 202 state = %q, want pending", v)
			}
			select {
			case v := <-d.Verdicts():
				t.Fatalf("after 202 a verdict %q was published", v)
			default:
			}
			d.Decide(got.T207DeliveryId, T207Ready)
			if v := d.State(got.T207DeliveryId); v != T207Ready {
				t.Errorf("after verdict state = %q, want ready", v)
			}
		})
	}
}

// TestAgentSignalsDoNotFinishTaskT207 is the acceptance core: FINISHED and IDLE
// (Cursor) and agent_end (Pi) are not final. Only verdict=ready finishes.
func TestAgentSignalsDoNotFinishTaskT207(t *testing.T) {
	for _, mode := range []T207Mode{T207Hosted, T207Local} {
		t.Run(string(mode), func(t *testing.T) {
			d := T207New(nil)
			got := d.Accept(context.Background(), T207Request{Target: "s", T207Mode: mode})
			id := got.T207DeliveryId
			for _, signal := range []T207AgentSignal{T207Finished, T207Idle, T207AgentEnd} {
				if v := d.Observe(id, signal); v != T207Pending {
					t.Errorf("after %s state = %q, want pending", signal, v)
				}
				select {
				case v := <-d.Verdicts():
					t.Fatalf("after %s a verdict %q was published", signal, v)
				default:
				}
			}
			d.Decide(id, T207Ready)
			if v := d.State(id); v != T207Ready {
				t.Errorf("after verdict state = %q, want ready", v)
			}
		})
	}
}

// TestNonReadyVerdictKeepsTaskOpenT207 proves a verdict that is not ready does not
// finish the task either.
func TestNonReadyVerdictKeepsTaskOpenT207(t *testing.T) {
	d := T207New(nil)
	got := d.Accept(context.Background(), T207Request{Target: "s"})
	d.Decide(got.T207DeliveryId, T207Pending)
	if v := d.State(got.T207DeliveryId); v != T207Pending {
		t.Errorf("after pending verdict state = %q, want pending", v)
	}
	select {
	case v := <-d.Verdicts():
		t.Fatalf("pending verdict published %q on the decision channel", v)
	default:
	}
}

// TestHostedAndLocalBehaveTheSameT207 proves the hosting mode changes neither the
// acknowledgement nor the finality rule (ADR 0082).
func TestHostedAndLocalBehaveTheSameT207(t *testing.T) {
	run := func(mode T207Mode) (T207Accepted, T207Verdict) {
		d := T207New(nil)
		got := d.Accept(context.Background(), T207Request{Target: "s", T207Mode: mode})
		d.Decide(got.T207DeliveryId, T207Ready)
		return got, d.State(got.T207DeliveryId)
	}
	hosted, hostedState := run(T207Hosted)
	local, localState := run(T207Local)
	if hosted.Status != local.Status {
		t.Errorf("status hosted=%d local=%d, want equal", hosted.Status, local.Status)
	}
	if hostedState != localState {
		t.Errorf("state hosted=%q local=%q, want equal", hostedState, localState)
	}
	if hosted.Status != T207AcceptedStatus || hostedState != T207Ready {
		t.Errorf("unexpected result: ack=%+v state=%q", hosted, hostedState)
	}
}

// TestDuplicateDeliveryIdStartsOneJobT207 closes the idempotence loop from ADR
// 0081: a repeat with the same deliveryId reuses the acknowledgement and does
// not start a second background job.
func TestDuplicateDeliveryIdStartsOneJobT207(t *testing.T) {
	var runs atomic.Int32
	d := T207New(func(ctx context.Context, req T207Request, id T207DeliveryId) { runs.Add(1) })
	req := T207Request{Target: "s", T207DeliveryId: T207DeliveryId("dlv_fixed")}
	first := d.Accept(context.Background(), req)
	second := d.Accept(context.Background(), req)
	if first != second {
		t.Errorf("duplicate ack = %+v, want %+v", second, first)
	}
	deadline := time.Now().Add(time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() != 1 {
		t.Errorf("background runs = %d, want 1", runs.Load())
	}
}

// TestGeneratedDeliveryIdsAreDistinctT207 proves two admissions without an
// explicit id get different keys, so their verdicts cannot cross over.
func TestGeneratedDeliveryIdsAreDistinctT207(t *testing.T) {
	d := T207New(nil)
	first := d.Accept(context.Background(), T207Request{Target: "s"})
	second := d.Accept(context.Background(), T207Request{Target: "s"})
	if first.T207DeliveryId == second.T207DeliveryId {
		t.Fatalf("both admissions got deliveryId %q", first.T207DeliveryId)
	}
	d.Decide(first.T207DeliveryId, T207Ready)
	if v := d.State(first.T207DeliveryId); v != T207Ready {
		t.Errorf("first state = %q, want ready", v)
	}
	if v := d.State(second.T207DeliveryId); v != T207Pending {
		t.Errorf("second state = %q, want pending", v)
	}
}

// ===== T3.208 события подсказка, состояние истина (#284) =====

// T208sourceStub is a T208StateSource built from two functions. Each test supplies only
// the behaviour it cares about.
type T208sourceStub struct {
	list  func(ctx context.Context) (T208Snapshot, error)
	watch func(ctx context.Context, cursor string) (T208Event, error)
}

func (s T208sourceStub) List(ctx context.Context) (T208Snapshot, error) {
	if s.list == nil {
		return T208Snapshot{}, nil
	}
	return s.list(ctx)
}

func (s T208sourceStub) Watch(ctx context.Context, cursor string) (T208Event, error) {
	if s.watch == nil {
		return T208Event{}, nil
	}
	return s.watch(ctx, cursor)
}

func T208fixedList(snap T208Snapshot) func(context.Context) (T208Snapshot, error) {
	return func(context.Context) (T208Snapshot, error) { return snap, nil }
}

func TestMissedEventRecoveredByFullReadT208(t *testing.T) {
	src := T208sourceStub{list: T208fixedList(T208Snapshot{
		Revision: 1,
		Cursor:   "c1",
		Obligations: []T208Obligation{
			{ID: "ob-1", Revision: 1},
		},
	})}
	d := T208NewDeliverer(src, nil)

	// No event is ever delivered. The full read alone discovers the obligation.
	n, applied, err := d.Reconcile(context.Background())
	if err != nil || !applied || n != 1 {
		t.Fatalf("Reconcile = (%d, %v, %v), want (1, true, nil)", n, applied, err)
	}
	if !d.Has("ob-1") {
		t.Fatal("full read missed obligation ob-1")
	}
}

func TestRepeatedEventDoesNotChangeStateT208(t *testing.T) {
	src := T208sourceStub{list: T208fixedList(T208Snapshot{
		Revision:    1,
		Obligations: []T208Obligation{{ID: "ob-1", Revision: 1}},
	})}
	d := T208NewDeliverer(src, nil)
	ev := T208Event{ID: "e1", Seq: 1}

	if !d.Apply(ev) {
		t.Fatal("first Apply should accept the event")
	}
	if d.Apply(ev) {
		t.Fatal("second Apply of the same id must be a no-op")
	}
	if _, _, dirty := d.State(); !dirty {
		t.Fatal("a fresh event should set the dirty signal")
	}

	if _, _, err := d.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, dirty := d.State(); dirty {
		t.Fatal("Reconcile should clear the dirty signal")
	}

	// Replaying the event and re-listing the same revision changes nothing.
	if d.Apply(ev) {
		t.Fatal("replayed event must be dropped")
	}
	_, applied, _ := d.Reconcile(context.Background())
	if applied {
		t.Fatal("a snapshot with an unchanged revision must not be applied")
	}
	if got := d.Relists(); got != 1 {
		t.Fatalf("Relists = %d, want 1", got)
	}
	if ob := d.Obligations(); len(ob) != 1 || ob[0].ID != "ob-1" {
		t.Fatalf("state changed after replay: %+v", ob)
	}
}

func TestOldSeqDiscardedT208(t *testing.T) {
	d := T208NewDeliverer(T208sourceStub{}, nil)
	if !d.Apply(T208Event{ID: "e10", Seq: 10}) {
		t.Fatal("seq 10 should be accepted")
	}
	if d.Apply(T208Event{ID: "e5", Seq: 5}) {
		t.Fatal("older seq must be discarded")
	}
	if d.Apply(T208Event{ID: "e10b", Seq: 10}) {
		t.Fatal("equal seq must be discarded")
	}
	if !d.Apply(T208Event{ID: "e11", Seq: 11}) {
		t.Fatal("newer seq should be accepted")
	}
	if _, _, dirty := d.State(); !dirty {
		t.Fatal("accepted hints should keep the dirty signal set")
	}
}

func TestOldRevisionDiscardedT208(t *testing.T) {
	snaps := []T208Snapshot{
		{Revision: 5, Obligations: []T208Obligation{{ID: "A", Revision: 5}}},
		{Revision: 3, Obligations: []T208Obligation{{ID: "B", Revision: 3}}},
	}
	i := 0
	src := T208sourceStub{list: func(context.Context) (T208Snapshot, error) {
		s := snaps[i]
		i++
		return s, nil
	}}
	d := T208NewDeliverer(src, nil)

	if _, applied, _ := d.Reconcile(context.Background()); !applied {
		t.Fatal("first snapshot should be applied")
	}
	if _, applied, _ := d.Reconcile(context.Background()); applied {
		t.Fatal("older revision must be discarded")
	}
	if !d.Has("A") || d.Has("B") {
		t.Fatalf("old revision rolled state back: A=%v B=%v", d.Has("A"), d.Has("B"))
	}
}

func TestGoneTriggersRelistT208(t *testing.T) {
	src := T208sourceStub{
		list: T208fixedList(T208Snapshot{
			Revision:    1,
			Cursor:      "fresh-cursor",
			Obligations: []T208Obligation{{ID: "ob-1", Revision: 1}},
		}),
		watch: func(context.Context, string) (T208Event, error) {
			return T208Event{}, &T208GoneError{Status: T208httpStatusGone}
		},
	}
	d := T208NewDeliverer(src, nil)

	_, relisted, err := d.WatchOnce(context.Background())
	if err != nil || !relisted {
		t.Fatalf("WatchOnce = (_, %v, %v), want (true, nil)", relisted, err)
	}
	if !d.Has("ob-1") {
		t.Fatal("410 Gone did not recover the obligation via re-list")
	}
	if _, cursor, _ := d.State(); cursor != "fresh-cursor" {
		t.Fatalf("cursor = %q, want fresh-cursor after re-list", cursor)
	}
	if got := d.GoneRelists(); got != 1 {
		t.Fatalf("GoneRelists = %d, want 1", got)
	}
}

func TestCursorExpiredTriggersRelistT208(t *testing.T) {
	src := T208sourceStub{
		list: T208fixedList(T208Snapshot{
			Revision:    1,
			Cursor:      "c2",
			Obligations: []T208Obligation{{ID: "ob-2", Revision: 1}},
		}),
		watch: func(context.Context, string) (T208Event, error) {
			return T208Event{}, &T208GoneError{Code: T208CursorExpired}
		},
	}
	d := T208NewDeliverer(src, nil)
	if _, relisted, err := d.WatchOnce(context.Background()); err != nil || !relisted {
		t.Fatalf("WatchOnce = (_, %v, %v), want (true, nil)", relisted, err)
	}
	if !d.Has("ob-2") {
		t.Fatal("cursor_expired did not recover the obligation via re-list")
	}
}

func TestNonGoneWatchErrorDoesNotRelistT208(t *testing.T) {
	src := T208sourceStub{
		watch: func(context.Context, string) (T208Event, error) {
			return T208Event{}, errors.New("network down")
		},
	}
	d := T208NewDeliverer(src, nil)
	if _, relisted, err := d.WatchOnce(context.Background()); err == nil || relisted {
		t.Fatalf("WatchOnce = (_, %v, %v), want (false, error)", relisted, err)
	}
	if got := d.Relists(); got != 0 {
		t.Fatalf("Relists = %d, want 0 for a non-gone error", got)
	}
}

func TestCursorNotPersistedAcrossProcessesT208(t *testing.T) {
	src := T208sourceStub{list: T208fixedList(T208Snapshot{
		Revision:    7,
		Cursor:      "c7",
		Obligations: []T208Obligation{{ID: "ob-7", Revision: 7}},
	})}
	d := T208NewDeliverer(src, nil)
	if rev, cursor, _ := d.State(); rev != 0 || cursor != "" {
		t.Fatalf("fresh process state = (%d, %q), want (0, empty)", rev, cursor)
	}
	_, _, _ = d.Reconcile(context.Background())
	if _, cursor, _ := d.State(); cursor != "c7" {
		t.Fatalf("cursor = %q, want c7", cursor)
	}

	// A new T208Deliverer models a restarted process: no persisted cursor, no state
	// until it performs its own full list.
	restarted := T208NewDeliverer(src, nil)
	if rev, cursor, _ := restarted.State(); rev != 0 || cursor != "" {
		t.Fatalf("restarted process kept cursor/state: (%d, %q)", rev, cursor)
	}
	if restarted.Has("ob-7") {
		t.Fatal("restarted process must not inherit obligations")
	}
}

func TestWatchUsesProcessLocalCursorT208(t *testing.T) {
	var seen string
	src := T208sourceStub{
		list: T208fixedList(T208Snapshot{Revision: 1, Cursor: "c1"}),
		watch: func(_ context.Context, cursor string) (T208Event, error) {
			seen = cursor
			return T208Event{}, errors.New("stop")
		},
	}
	d := T208NewDeliverer(src, nil)
	_, _, _ = d.Reconcile(context.Background())
	_, _, _ = d.WatchOnce(context.Background())
	if seen != "c1" {
		t.Fatalf("Watch cursor = %q, want c1 from the full read", seen)
	}
}

func TestLostWebhookKeepsObligationT208(t *testing.T) {
	src := T208sourceStub{
		list: T208fixedList(T208Snapshot{
			Revision:    1,
			Obligations: []T208Obligation{{ID: "ob-1", Revision: 1}},
		}),
		watch: func(context.Context, string) (T208Event, error) {
			return T208Event{}, errors.New("webhook never arrived")
		},
	}
	d := T208NewDeliverer(src, nil)
	_, _, _ = d.Reconcile(context.Background())
	if !d.Has("ob-1") {
		t.Fatal("obligation missing after full read")
	}
	// The stream never delivers anything, yet the obligation survives both a
	// failed watch and a later no-op reconcile.
	_, _, _ = d.WatchOnce(context.Background())
	_, _, _ = d.Reconcile(context.Background())
	if !d.Has("ob-1") {
		t.Fatal("lost webhook lost the obligation")
	}
}

func TestEventHintAppliedByWatchOnceT208(t *testing.T) {
	ev := T208Event{ID: "e1", Seq: 1}
	src := T208sourceStub{
		list:  T208fixedList(T208Snapshot{Revision: 1, Obligations: []T208Obligation{{ID: "ob-1"}}}),
		watch: func(context.Context, string) (T208Event, error) { return ev, nil },
	}
	d := T208NewDeliverer(src, nil)

	got, relisted, err := d.WatchOnce(context.Background())
	if err != nil || relisted || got.ID != "e1" {
		t.Fatalf("WatchOnce = (%+v, %v, %v)", got, relisted, err)
	}
	if _, _, dirty := d.State(); !dirty {
		t.Fatal("a received hint should set the dirty signal")
	}
	// The hint itself carries no obligation; only the full read supplies it.
	if d.Has("ob-1") {
		t.Fatal("event must not carry state into the deliverer")
	}
	_, _, _ = d.Reconcile(context.Background())
	if !d.Has("ob-1") {
		t.Fatal("full read after hint should supply the obligation")
	}
}

func TestRunRelistsPeriodicallyWithJitterT208(t *testing.T) {
	lists := make(chan struct{}, 8)
	var jitterCalls atomic.Int64
	rev := uint64(0)
	src := T208sourceStub{
		list: func(context.Context) (T208Snapshot, error) {
			rev++
			lists <- struct{}{}
			return T208Snapshot{Revision: rev, Cursor: "c"}, nil
		},
		watch: func(context.Context, string) (T208Event, error) {
			return T208Event{}, errors.New("no stream")
		},
	}
	d := T208NewDeliverer(src, func() time.Duration {
		jitterCalls.Add(1)
		return 0
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx, time.Millisecond)
		close(done)
	}()
	<-lists // startup full read
	<-lists // one periodic full read with jitter
	cancel()
	<-done

	if jitterCalls.Load() < 1 {
		t.Fatalf("jitter was called %d times, want at least 1", jitterCalls.Load())
	}
	if d.Relists() < 2 {
		t.Fatalf("Relists = %d, want at least 2", d.Relists())
	}
}

func TestIsGoneT208(t *testing.T) {
	if !T208IsGone(&T208GoneError{Status: T208httpStatusGone}) {
		t.Fatal("410 should be gone")
	}
	if !T208IsGone(&T208GoneError{Code: T208CursorExpired}) {
		t.Fatal("cursor_expired should be gone")
	}
	if T208IsGone(&T208GoneError{Status: 500}) {
		t.Fatal("500 must not be gone")
	}
	if T208IsGone(errors.New("plain error")) {
		t.Fatal("plain error must not be gone")
	}
}

// ===== T3.209 lease obligation (#289) =====

// T209fakeClock gives the lease window deterministic boundaries.
type T209fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func T209newFakeClock() *T209fakeClock {
	return &T209fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
}

func (c *T209fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *T209fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func T209newTestManager(clock *T209fakeClock) *T209Manager {
	m := T209NewManager(1000)
	m.Now = clock.Now
	return m
}

// TestClaimRejectsRepeatAtLiveLeaseT209 covers the first card check: while a lease
// is live, a second claim is rejected and the caller can see who owns it.
func TestClaimRejectsRepeatAtLiveLeaseT209(t *testing.T) {
	clock := T209newFakeClock()
	m := T209newTestManager(clock)

	first, err := m.Claim("obl-1", "worker-a")
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if first.Outcome != T209ClaimGranted {
		t.Fatalf("first claim outcome = %q, want %q", first.Outcome, T209ClaimGranted)
	}
	if want := clock.Now().Add(time.Second); !first.T209Lease.ExpiresAt.Equal(want) {
		t.Fatalf("lease expires at %v, want %v", first.T209Lease.ExpiresAt, want)
	}

	second, err := m.Claim("obl-1", "worker-b")
	if !errors.Is(err, T209ErrLeaseLive) {
		t.Fatalf("repeat Claim error = %v, want T209ErrLeaseLive", err)
	}
	if second.Outcome != T209ClaimConflict {
		t.Fatalf("repeat claim outcome = %q, want %q", second.Outcome, T209ClaimConflict)
	}
	if second.OwnerWorkerID != "worker-a" {
		t.Fatalf("blocking owner = %q, want worker-a", second.OwnerWorkerID)
	}
	if m.T209State("obl-1") != T209StateClaimed {
		t.Fatal("repeat claim changed the obligation state")
	}
}

// TestExpiredLeaseReclaimPublishesVisibleReasonT209 covers the acceptance
// criterion: a worker that never responded leaves a visible reason, and the
// next claim recovers the obligation instead of failing silently.
func TestExpiredLeaseReclaimPublishesVisibleReasonT209(t *testing.T) {
	clock := T209newFakeClock()
	m := T209newTestManager(clock)

	if _, err := m.Claim("obl-2", "offline-worker"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	clock.Advance(1000 * time.Millisecond)

	reclaimed, err := m.Claim("obl-2", "worker-b")
	if err != nil {
		t.Fatalf("Claim after expiry: %v", err)
	}
	if reclaimed.Outcome != T209ClaimReclaimed {
		t.Fatalf("claim outcome = %q, want %q", reclaimed.Outcome, T209ClaimReclaimed)
	}

	reasons := m.Reasons()
	if len(reasons) != 1 {
		t.Fatalf("published reasons = %d, want 1", len(reasons))
	}
	if reasons[0].Kind != T209ReasonTimeout {
		t.Fatalf("reason kind = %q, want %q", reasons[0].Kind, T209ReasonTimeout)
	}
	if reasons[0].ObligationID != "obl-2" || reasons[0].WorkerID != "offline-worker" {
		t.Fatalf("reason = %+v, want obligation obl-2 and offline-worker", reasons[0])
	}
	if !strings.Contains(reasons[0].Text, "offline-worker") {
		t.Fatalf("timeout reason text %q does not name the worker", reasons[0].Text)
	}
}

// TestExpiredLeaseReadAsCreatedT209 covers the expiry rule itself: observing an
// expired lease returns the obligation to created.
func TestExpiredLeaseReadAsCreatedT209(t *testing.T) {
	clock := T209newFakeClock()
	m := T209newTestManager(clock)

	if _, err := m.Claim("obl-2b", "offline-worker"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	clock.Advance(1001 * time.Millisecond)

	if got := m.T209State("obl-2b"); got != T209StateCreated {
		t.Fatalf("state after expiry = %q, want %q", got, T209StateCreated)
	}
	if _, owned := m.LeaseOf("obl-2b"); owned {
		t.Fatal("expired lease still reported as live")
	}
	if _, err := m.Claim("obl-2b", "worker-b"); err != nil {
		t.Fatalf("Claim after expiry: %v", err)
	}
}

// TestReleaseAfterLostResponseAllowsClaimAgainT209 covers the second card check:
// a lost wake response is cleared by release, then a new claim succeeds.
func TestReleaseAfterLostResponseAllowsClaimAgainT209(t *testing.T) {
	clock := T209newFakeClock()
	m := T209newTestManager(clock)

	if _, err := m.Claim("obl-3", "worker-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Release("obl-3", "worker-a"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := m.T209State("obl-3"); got != T209StateCreated {
		t.Fatalf("state after release = %q, want %q", got, T209StateCreated)
	}
	if _, owned := m.LeaseOf("obl-3"); owned {
		t.Fatal("lease still present after release")
	}

	next, err := m.Claim("obl-3", "worker-b")
	if err != nil {
		t.Fatalf("Claim after release: %v", err)
	}
	if next.OwnerWorkerID != "worker-b" {
		t.Fatalf("new lease owner = %q, want worker-b", next.OwnerWorkerID)
	}
}

// TestReleaseRejectsForeignWorkerAndMissingLeaseT209 pins the ownership guard on
// release, which is the 409 case for another workerId.
func TestReleaseRejectsForeignWorkerAndMissingLeaseT209(t *testing.T) {
	clock := T209newFakeClock()
	m := T209newTestManager(clock)

	if err := m.Release("missing", "worker-a"); !errors.Is(err, T209ErrNoLease) {
		t.Fatalf("Release on missing obligation = %v, want T209ErrNoLease", err)
	}
	if _, err := m.Claim("obl-4", "worker-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Release("obl-4", "worker-b"); !errors.Is(err, T209ErrWrongWorker) {
		t.Fatalf("Release by other worker = %v, want T209ErrWrongWorker", err)
	}
	if got := m.T209State("obl-4"); got != T209StateClaimed {
		t.Fatalf("foreign release changed state to %q", got)
	}
}

// TestFailPublishesVisibleReasonT209 covers the third card check: fail puts the
// text in front of the user and leaves the obligation claimable again.
func TestFailPublishesVisibleReasonT209(t *testing.T) {
	clock := T209newFakeClock()
	var seen []T209Reason
	m := T209newTestManager(clock)
	m.OnReason = func(r T209Reason) { seen = append(seen, r) }

	if _, err := m.Claim("obl-5", "worker-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	const text = "wake: session did not start, press Try Again"
	if err := m.Fail("obl-5", "worker-a", text); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if len(seen) != 1 {
		t.Fatalf("sink got %d reasons, want 1", len(seen))
	}
	if seen[0].Kind != T209ReasonFail || seen[0].Text != text {
		t.Fatalf("sunk reason = %+v, want fail with %q", seen[0], text)
	}
	if seen[0].ObligationID != "obl-5" || seen[0].WorkerID != "worker-a" {
		t.Fatalf("sunk reason = %+v, want obligation and worker ids", seen[0])
	}
	if got := m.Reasons(); len(got) != 1 || got[0] != seen[0] {
		t.Fatalf("history = %+v, want the sunk reason", got)
	}
	if got := m.T209State("obl-5"); got != T209StateCreated {
		t.Fatalf("state after fail = %q, want %q", got, T209StateCreated)
	}
	if _, err := m.Claim("obl-5", "worker-b"); err != nil {
		t.Fatalf("Claim after fail: %v", err)
	}
}

// TestFailRejectsEmptyReasonAndForeignWorkerT209 keeps the visible-reason contract
// from being bypassed by an empty or unauthorised fail.
func TestFailRejectsEmptyReasonAndForeignWorkerT209(t *testing.T) {
	clock := T209newFakeClock()
	m := T209newTestManager(clock)

	if err := m.Fail("obl-6", "worker-a", ""); !errors.Is(err, T209ErrEmptyReason) {
		t.Fatalf("Fail with empty text = %v, want T209ErrEmptyReason", err)
	}
	if _, err := m.Claim("obl-6", "worker-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Fail("obl-6", "worker-b", "boom"); !errors.Is(err, T209ErrWrongWorker) {
		t.Fatalf("Fail by other worker = %v, want T209ErrWrongWorker", err)
	}
	if got := m.Reasons(); len(got) != 0 {
		t.Fatalf("published %d reasons for rejected fails, want 0", len(got))
	}
}

// TestConcurrentClaimGrantsOneLeaseT209 checks that the manager serialises claims
// on the same obligation even when callers arrive at once.
func TestConcurrentClaimGrantsOneLeaseT209(t *testing.T) {
	m := T209newTestManager(T209newFakeClock())

	const callers = 16
	start := make(chan struct{})
	results := make([]T209ClaimResult, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = m.Claim("obl-7", "worker-"+string(rune('a'+i)))
		}(i)
	}
	close(start)
	wg.Wait()

	granted := 0
	for i := range results {
		if errs[i] == nil {
			granted++
			if results[i].Outcome != T209ClaimGranted {
				t.Fatalf("successful claim outcome = %q, want %q", results[i].Outcome, T209ClaimGranted)
			}
			continue
		}
		if !errors.Is(errs[i], T209ErrLeaseLive) {
			t.Fatalf("losing claim error = %v, want T209ErrLeaseLive", errs[i])
		}
	}
	if granted != 1 {
		t.Fatalf("granted %d leases, want exactly 1", granted)
	}
}

// ===== T3.210-T3.215 =====

type t210FakeAdapter struct {
	binds    int
	delivers int
	observes int
	target   T210Target
}

func (a *t210FakeAdapter) Bind(p T210WakePointer, sessions *T211SessionRegistry) (T210Target, bool) {
	a.binds++
	if a.target != "" {
		return a.target, true
	}
	_, ok := sessions.Lookup(p.Adapter, p.Target)
	return "", ok
}

func (a *t210FakeAdapter) Deliver(p T210WakePointer, _ T210Target) (T210Receipt, error) {
	a.delivers++
	return T210Receipt{ObligationID: p.ObligationID, Attempt: p.Attempt, Delivery: p.Delivery, Outcome: T210Delivered}, nil
}

func (a *t210FakeAdapter) Observe(_ T210WakePointer, _ T210Receipt) error {
	a.observes++
	return nil
}

func TestT210BindThroughRegistryAndSkipUnknown(t *testing.T) {
	sessions := T211NewSessionRegistry()
	adapters := T211NewAdapterRegistry()
	deliveries := T211NewDeliveryRegistry()
	a := &t210FakeAdapter{}
	adapters.Register("bridge-a", a)

	unknown := T210WakePointer{Adapter: "bridge-b", ObligationID: "o-0"}
	if _, ok := T210Deliver(context.Background(), unknown, nil, sessions, adapters, deliveries); ok {
		t.Fatal("unknown adapter was delivered")
	}
	missing := T210WakePointer{Adapter: "bridge-a", Target: "t-missing", ObligationID: "o-1"}
	if _, ok := T210Deliver(context.Background(), missing, nil, sessions, adapters, deliveries); ok {
		t.Fatal("unregistered target was delivered")
	}
	if a.delivers != 0 {
		t.Fatalf("adapter delivered %d times before a registry match", a.delivers)
	}

	sessions.Add("bridge-a", "t-1", "ses_1")
	p := T210WakePointer{Adapter: "bridge-a", Target: "t-1", ObligationID: "o-2", Attempt: 3, Delivery: "steer"}
	r, ok := T210Deliver(context.Background(), p, nil, sessions, adapters, deliveries)
	if !ok || r.Outcome != T210Delivered || r.ObligationID != "o-2" {
		t.Fatalf("delivery = %+v, ok = %v", r, ok)
	}
	if a.delivers != 1 || a.observes != 2 {
		t.Fatalf("delivers = %d, observes = %d, want 1 and 2", a.delivers, a.observes)
	}
}

func TestT211RegistriesKeepTargetsOpaqueAndImportReceipts(t *testing.T) {
	sessions := T211NewSessionRegistry()
	if !sessions.AddFromMeta("bridge-a", map[string]string{"ai.opencode/sessionID": "ses_meta"}, "t-ignored") {
		t.Fatal("AddFromMeta rejected a valid _meta")
	}
	if sid, ok := sessions.Lookup("bridge-a", "ses_meta"); !ok || sid != "ses_meta" {
		t.Fatalf("_meta lookup = %q, %v", sid, ok)
	}
	if !sessions.AddFromMeta("bridge-a", map[string]string{}, "notify-target") {
		t.Fatal("AddFromMeta rejected notify.target")
	}
	if sid, ok := sessions.Lookup("bridge-a", "notify-target"); !ok || sid != "notify-target" {
		t.Fatalf("notify lookup = %q, %v", sid, ok)
	}

	adapters := T211NewAdapterRegistry()
	adapters.Register("bridge-a", &t210FakeAdapter{})
	adapters.Register("bridge-b", &t210FakeAdapter{})
	if _, ok := adapters.Get("bridge-a"); !ok {
		t.Fatal("registered adapter missing")
	}
	if _, ok := adapters.Get("bridge-c"); ok {
		t.Fatal("unknown adapter found")
	}

	d := T211NewDeliveryRegistry()
	batch := []T210Receipt{
		{ObligationID: "o-1", Attempt: 1, Outcome: T210Delivered},
		{ObligationID: "o-1", Attempt: 1, Outcome: T210Delivered},
		{ObligationID: "o-1", Attempt: 2, Outcome: T210Queued},
	}
	if got := d.ImportReceipts(batch); got != 2 {
		t.Fatalf("ImportReceipts added %d, want 2", got)
	}
	if !d.Has("o-1", 2) || d.Has("o-1", 3) {
		t.Fatal("delivery registry has wrong keys")
	}
	if r, ok := d.Get("o-1", 1); !ok || r.Outcome != T210Delivered {
		t.Fatalf("stored receipt = %+v, %v", r, ok)
	}
}

func TestT212PromptTextHasIdentifiersOnly(t *testing.T) {
	p := T210WakePointer{
		WatchID:      `watch<&>"'` + "\n\x00",
		ObligationID: "obl-9",
		Revision:     42,
		VerdictRef:   "watch-9/revision/42",
	}
	text := T212buildPromptText(p)
	if !strings.HasPrefix(text, "<beacon-event>") || !strings.HasSuffix(text, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", text)
	}
	if len(text) > 512 {
		t.Fatalf("prompt is %d bytes, want at most 512", len(text))
	}
	for _, forbidden := range []string{"checkName", "prTitle", "branch", "<watch", "\n"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("prompt contains %q: %s", forbidden, text)
		}
	}
	for _, want := range []string{`"watchID"`, `"obligationID"`, `"revision"`, `"verdictRef"`, `"obl-9"`, "42"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt missing %q: %s", want, text)
		}
	}
	if !strings.Contains(text, `\u0026lt;`) || !strings.Contains(text, `\\u0000`) {
		t.Fatalf("escapeUntrusted did not escape markup and control chars: %q", text)
	}
	long := p
	long.WatchID = strings.Repeat("w", 5000)
	if got := len(T212buildPromptText(long)); got > 512 {
		t.Fatalf("long prompt is %d bytes, want at most 512", got)
	}
}

func TestT213SpoolSourceDedupeAndCursor(t *testing.T) {
	dir := t.TempDir()
	src := T213NewSpoolSource(dir)
	event := func(id string) string {
		return `{"adapter":"bridge-a","target":"t-1","watchID":"w-1","obligationID":"` + id + `","attempt":1,"version":"v"}`
	}
	lines := strings.Join([]string{
		event("o-1"),
		`{broken json`,
		event("o-1"),
		event("o-2"),
	}, "\n") // no trailing newline: the last line is unfinished
	if err := os.WriteFile(src.Path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	p, ok := src.Next()
	if !ok || p.ObligationID != "o-1" {
		t.Fatalf("first pointer = %+v, %v", p, ok)
	}
	if err := src.Commit(p); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(src.ReceiptsDir, "o-1-1.wrote")
	info, err := os.Stat(receipt)
	if err != nil {
		t.Fatalf("receipt missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %v, want 0600", info.Mode().Perm())
	}
	c := src.Cursor()
	if c.Offset == 0 || c.Epoch == "" {
		t.Fatalf("cursor = %+v, want a read prefix and hash", c)
	}

	if next, ok := src.Next(); ok {
		t.Fatalf("duplicate and unfinished lines produced a pointer: %+v", next)
	}
	// A later complete line is still delivered after the broken ones.
	f, err := os.OpenFile(src.Path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, "\n"+event("o-3")+"\n")
	_ = f.Close()
	if next, ok := src.Next(); !ok || next.ObligationID != "o-2" {
		t.Fatalf("completed line pointer = %+v, %v", next, ok)
	}
	if err := src.Commit(T210WakePointer{ObligationID: "o-2", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if next, ok := src.Next(); !ok || next.ObligationID != "o-3" {
		t.Fatalf("appended pointer = %+v, %v", next, ok)
	}
}

func TestT214McpPullSourceUsesTwoMCPTools(t *testing.T) {
	var calls []string
	var claimArgs, receiptArgs map[string]any
	src := &T214McpPullSource{
		Adapter: "bridge-a",
		Target:  "t-mcp",
		Call: func(_ context.Context, tool string, args map[string]any) ([]byte, error) {
			calls = append(calls, tool)
			switch tool {
			case "beacon_wake_claim":
				claimArgs = args
				return []byte(`{"adapter":"bridge-a","target":"t-mcp","obligationID":"o-mcp","attempt":2,"delivery":"steer","resume":false}`), nil
			case "beacon_wake_receipt":
				receiptArgs = args
				return []byte(`{"ok":true}`), nil
			default:
				return nil, fmt.Errorf("unexpected tool %q", tool)
			}
		},
	}
	p, ok := src.Next()
	if !ok || p.ObligationID != "o-mcp" {
		t.Fatalf("claim pointer = %+v, %v", p, ok)
	}
	if err := src.Commit(p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"beacon_wake_claim", "beacon_wake_receipt"}) {
		t.Fatalf("tools = %v", calls)
	}
	if claimArgs["adapter"] != "bridge-a" || claimArgs["target"] != "t-mcp" {
		t.Fatalf("claim args = %v", claimArgs)
	}
	if receiptArgs["obligationID"] != "o-mcp" || receiptArgs["attempt"] != 2 {
		t.Fatalf("receipt args = %v", receiptArgs)
	}
	if receiptArgs["outcome"] != T210Enqueued {
		t.Fatalf("receipt outcome = %v, want %q", receiptArgs["outcome"], T210Enqueued)
	}
	if _, ok := src.Next(); ok {
		t.Fatal("second claim returned a pointer")
	}
}

type t215Doer struct {
	status int
	body   []byte
	calls  int
}

func (d *t215Doer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.body, _ = io.ReadAll(req.Body)
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(bytes.NewReader(nil)),
	}, nil
}

func TestT215SteerQueueAndResumeFalse(t *testing.T) {
	sessions := T211NewSessionRegistry()
	sessions.Add("bridge-a", "t-1", "ses_1")
	steerEvents := []T215Event{
		{Type: "session.inbox.enqueued", SessionID: "ses_1"},
		{Type: "session.inbox.delivered", SessionID: "ses_1"},
		{Type: "session.execution.started", SessionID: "ses_1"},
	}
	enqueuedEvents := []T215Event{{Type: "session.inbox.enqueued", SessionID: "ses_1"}}
	cases := []struct {
		name       string
		delivery   string
		resume     *bool
		events     []T215Event
		wantResult string
		wantResume bool
	}{
		{name: "steer", delivery: "steer", events: steerEvents, wantResult: T210Delivered},
		{name: "queue", delivery: "queue", events: enqueuedEvents, wantResult: T210Queued},
		{name: "resume false", delivery: "steer", resume: boolPtr(false), events: enqueuedEvents, wantResult: T210Enqueued, wantResume: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &t215Doer{status: http.StatusOK}
			a := &T215Adapter{BaseURL: "http://127.0.0.1:1", Doer: doer, Events: func(context.Context) ([]T215Event, error) {
				return tc.events, nil
			}}
			target, ok := a.Bind(T210WakePointer{Adapter: "bridge-a", Target: "t-1"}, sessions)
			if !ok || target != "ses_1" {
				t.Fatalf("bind = %q, %v", target, ok)
			}
			p := T210WakePointer{Adapter: "bridge-a", Target: "t-1", ObligationID: "o-1", Delivery: tc.delivery, Resume: tc.resume}
			r, err := a.Deliver(p, target)
			if err != nil {
				t.Fatal(err)
			}
			if r.Outcome != tc.wantResult {
				t.Fatalf("outcome = %q, want %q", r.Outcome, tc.wantResult)
			}
			if doer.calls != 1 {
				t.Fatalf("HTTP calls = %d, want 1", doer.calls)
			}
			var sent map[string]any
			if err := json.Unmarshal(doer.body, &sent); err != nil {
				t.Fatal(err)
			}
			if sent["delivery"] != tc.delivery {
				t.Fatalf("body delivery = %v", sent["delivery"])
			}
			if _, has := sent["resume"]; has != tc.wantResume {
				t.Fatalf("body resume presence = %v, want %v", has, tc.wantResume)
			}
			if _, has := sent["text"]; !has {
				t.Fatal("body has no prompt text")
			}
		})
	}
	if T215classify([]T215Event{{Type: "session.inbox.enqueued", SessionID: "ses_1"}}, "ses_1", "steer", true) != T210Enqueued {
		t.Fatal("resume:false path reported delivered without delivered/started events")
	}
}

func boolPtr(v bool) *bool { return &v }

