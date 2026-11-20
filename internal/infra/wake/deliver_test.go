package wake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// recordedRequest is the part of an outgoing request a test cares about.
type recordedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// fakeDoer replaces the HTTP transport. No real server is started.
type fakeDoer struct {
	mu    sync.Mutex
	calls []recordedRequest
	fn    func(*http.Request) (*http.Response, error)
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, recordedRequest{
		method: req.Method,
		path:   req.URL.EscapedPath(),
		header: req.Header.Clone(),
		body:   body,
	})
	f.mu.Unlock()
	if f.fn == nil {
		return nil, errors.New("fakeDoer: fn не задан")
	}
	return f.fn(req)
}

func (f *fakeDoer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDoer) snapshot() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.calls...)
}

func fakeResponse(body string, code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     fmt.Sprintf("%d test", code),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func newTestDeliverer(t *testing.T, doer Doer) *Deliverer {
	t.Helper()
	d, err := New("http://127.0.0.1:49374/", "pw", doer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestDeliverPostsSteerPrompt(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{"data":{"id":"inbox_1","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := newTestDeliverer(t, doer)

	got, err := d.Deliver(context.Background(), Request{
		SessionID:  "ses_1",
		Text:       "новый факт: обязательство T3.200",
		DeliveryID: "obligation-1",
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got.ID != "inbox_1" || got.Delivery != DeliverySteer || got.Duplicate {
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
	user, pass, ok := basicAuth(call.header)
	if !ok || user != "opencode" || pass != "pw" {
		t.Fatalf("неверный basic auth: user=%q pass=%q ok=%v", user, pass, ok)
	}
	var payload struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal(call.body, &payload); err != nil {
		t.Fatalf("неверный JSON запроса: %v", err)
	}
	if payload.Text != "новый факт: обязательство T3.200" || payload.Delivery != "steer" {
		t.Fatalf("неверное тело запроса: %+v", payload)
	}
}

func basicAuth(header http.Header) (string, string, bool) {
	req := &http.Request{Header: header}
	return req.BasicAuth()
}

func TestDeliverDuplicateIDReturnsOriginalAdmission(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{"data":{"id":"inbox_1","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := newTestDeliverer(t, doer)
	req := Request{SessionID: "ses_1", Text: "факт", DeliveryID: "obligation-1"}

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
	if second.ID != first.ID || second.Delivery != first.Delivery {
		t.Fatalf("повтор изменил admitting: first=%+v second=%+v", first, second)
	}
	if n := doer.count(); n != 1 {
		t.Fatalf("ожидался 1 POST, получено %d: вторая задача была бы создана", n)
	}
}

func TestDeliverDerivedIDCollapsesRepeats(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{"data":{"id":"inbox_2","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := newTestDeliverer(t, doer)
	req := Request{SessionID: "ses_2", Text: "тот же текст"}

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

func TestDeliverDifferentTextCreatesNewTask(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{"data":{"id":"inbox_3","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := newTestDeliverer(t, doer)

	if _, err := d.Deliver(context.Background(), Request{SessionID: "ses_3", Text: "факт один"}); err != nil {
		t.Fatalf("первый Deliver: %v", err)
	}
	if _, err := d.Deliver(context.Background(), Request{SessionID: "ses_3", Text: "факт два"}); err != nil {
		t.Fatalf("второй Deliver: %v", err)
	}
	if n := doer.count(); n != 2 {
		t.Fatalf("ожидалось 2 POST для разных текстов, получено %d", n)
	}
}

func TestDeliver409ReturnsOriginalAdmission(t *testing.T) {
	calls := 0
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return fakeResponse(`{"data":{"id":"inbox_4","delivery":"steer"}}`, http.StatusOK), nil
		}
		return fakeResponse(`{"data":{"id":"inbox_4","delivery":"steer"}}`, http.StatusConflict), nil
	}}
	d := newTestDeliverer(t, doer)

	first, err := d.Deliver(context.Background(), Request{SessionID: "ses_4", Text: "факт", DeliveryID: "obligation-4"})
	if err != nil {
		t.Fatalf("первый Deliver: %v", err)
	}
	// A fresh deliverer simulates a restart: no local journal, server answers 409.
	restarted := newTestDeliverer(t, doer)
	second, err := restarted.Deliver(context.Background(), Request{SessionID: "ses_4", Text: "факт", DeliveryID: "obligation-4"})
	if err != nil {
		t.Fatalf("второй Deliver после рестарта: %v", err)
	}
	if !second.Duplicate || second.ID != first.ID {
		t.Fatalf("409 не вернул исходное admit: first=%+v second=%+v", first, second)
	}
}

func TestDeliverRejectsMissingDataID(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{"data":{"delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := newTestDeliverer(t, doer)

	if _, err := d.Deliver(context.Background(), Request{SessionID: "ses_5", Text: "факт"}); err == nil {
		t.Fatal("ожидалась ошибка при отсутствии data.id")
	}
}

func TestDeliverRejectsWrongDelivery(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{"data":{"id":"inbox_6","delivery":"queue"}}`, http.StatusOK), nil
	}}
	d := newTestDeliverer(t, doer)

	if _, err := d.Deliver(context.Background(), Request{SessionID: "ses_6", Text: "факт"}); err == nil {
		t.Fatal("ожидалась ошибка при data.delivery != steer")
	}
}

func TestDeliverHTTPErrors(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
				return fakeResponse(`{"message":"нет сессии"}`, code), nil
			}}
			d := newTestDeliverer(t, doer)
			if _, err := d.Deliver(context.Background(), Request{SessionID: "ses_7", Text: "факт"}); err == nil {
				t.Fatal("ожидалась ошибка для неуспешного статуса")
			}
			// A failed admission is not journalled: a later retry must reach the server.
			doer.fn = func(*http.Request) (*http.Response, error) {
				return fakeResponse(`{"data":{"id":"inbox_7","delivery":"steer"}}`, http.StatusOK), nil
			}
			if _, err := d.Deliver(context.Background(), Request{SessionID: "ses_7", Text: "факт"}); err != nil {
				t.Fatalf("повтор после ошибки: %v", err)
			}
			if n := doer.count(); n != 2 {
				t.Fatalf("ожидалось 2 POST, получено %d", n)
			}
		})
	}
}

func TestDeliverValidatesRequest(t *testing.T) {
	doer := &fakeDoer{}
	d := newTestDeliverer(t, doer)

	cases := []Request{
		{SessionID: "", Text: "факт"},
		{SessionID: " ", Text: "факт"},
		{SessionID: "ses_8", Text: ""},
		{SessionID: "ses_8", Text: "факт", Delivery: "unknown"},
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

func TestDeliverEscapesSessionID(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{"data":{"id":"inbox_9","delivery":"steer"}}`, http.StatusOK), nil
	}}
	d := newTestDeliverer(t, doer)

	if _, err := d.Deliver(context.Background(), Request{SessionID: "ses 9", Text: "факт"}); err != nil {
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

func TestNewRejectsBadURL(t *testing.T) {
	for _, raw := range []string{"", "ftp://127.0.0.1:1", "http://", "  "} {
		if _, err := New(raw, "pw", &fakeDoer{}); err == nil {
			t.Fatalf("ожидалась ошибка для url %q", raw)
		}
	}
}

func TestParseAdmissionBareObject(t *testing.T) {
	got := parseAdmission([]byte(`{"id":"inbox_10","delivery":"steer"}`))
	if got.ID != "inbox_10" || got.Delivery != DeliverySteer {
		t.Fatalf("bare object не разобран: %+v", got)
	}
}
