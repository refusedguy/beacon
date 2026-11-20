package wake

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

type countingDoer struct {
	calls int
}

func (d *countingDoer) Do(*http.Request) (*http.Response, error) {
	d.calls++
	return nil, errors.New("transport must not be called")
}

func TestTelegramConfigFromEnv(t *testing.T) {
	cfg := TelegramConfigFromEnv(func(key string) string {
		switch key {
		case TelegramTokenEnv:
			return "  test-token  "
		case TelegramChatEnv:
			return "  42  "
		case TelegramAPIBaseEnv:
			return "  http://example.test/  "
		}
		return ""
	})
	if cfg.Token != "test-token" || cfg.ChatID != "42" || cfg.APIBase != "http://example.test/" {
		t.Fatalf("config = %+v, want trimmed env values", cfg)
	}
}

func TestTelegramNotifySinkFailOpenWithoutConfig(t *testing.T) {
	doer := &countingDoer{}
	sink := NewTelegramNotifySink(TelegramConfig{Doer: doer})
	ctx := context.Background()

	if err := sink.Notify(ctx, Notification{Kind: NotifyBlockerOpened, WatchID: "w1"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := sink.SendText(ctx, "42", "hello"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	updates, err := sink.Updates(ctx, 0)
	if err != nil {
		t.Fatalf("Updates: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("updates = %d, want 0", len(updates))
	}
	if doer.calls != 0 {
		t.Fatalf("transport calls = %d, want 0", doer.calls)
	}
}

func TestTelegramNotifySinkSendsFormattedAndRedactedText(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()

	sink := NewTelegramNotifySink(TelegramConfig{
		Token:   "test-token",
		ChatID:  "42",
		APIBase: srv.URL,
		Doer:    srv.Client(),
	})
	n := Notification{
		Kind:     NotifyEscalateHuman,
		WatchID:  "watch-1",
		PR:       7,
		Revision: 3,
		Detail:   "ghp_abcdefghijklmnopqrstuvwxyz0123456789 Authorization: Bearer topsecret",
	}
	if err := sink.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/bottest-token/sendMessage" {
		t.Fatalf("path = %q, want sendMessage path", gotPath)
	}
	var payload struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if payload.ChatID != "42" {
		t.Fatalf("chat_id = %q, want 42", payload.ChatID)
	}
	if !strings.Contains(payload.Text, "[escalate_human] watch=watch-1 pr=7 revision=3") {
		t.Fatalf("text = %q, want formatted notification", payload.Text)
	}
	if strings.Contains(payload.Text, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") ||
		strings.Contains(payload.Text, "topsecret") {
		t.Fatalf("text kept a secret: %q", payload.Text)
	}
	if !strings.Contains(payload.Text, "[REDACTED]") {
		t.Fatalf("text = %q, want redaction marker", payload.Text)
	}
}

func TestTelegramNotifySinkPollsUpdatesWithOffset(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":[`+
			`{"update_id":5,"message":{"message_id":1,"text":"/approve watch-1","chat":{"id":987}}},`+
			`{"update_id":6,"message":{"message_id":2,"text":"hello","chat":{"id":987}}}]}`)
	}))
	defer srv.Close()

	sink := NewTelegramNotifySink(TelegramConfig{Token: "test-token", APIBase: srv.URL, Doer: srv.Client()})
	updates, err := sink.Updates(context.Background(), 5)
	if err != nil {
		t.Fatalf("Updates: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/bottest-token/getUpdates" {
		t.Fatalf("path = %q, want getUpdates path", gotPath)
	}
	if !strings.Contains(string(gotBody), `"offset":5`) {
		t.Fatalf("body = %s, want offset 5", gotBody)
	}
	if len(updates) != 2 {
		t.Fatalf("updates = %d, want 2", len(updates))
	}
	if updates[0].UpdateID != 5 || updates[0].Message.Text != "/approve watch-1" ||
		updates[0].Message.Chat.ID.String() != "987" {
		t.Fatalf("first update = %+v", updates[0])
	}
	if updates[1].UpdateID != 6 {
		t.Fatalf("second update id = %d, want 6", updates[1].UpdateID)
	}
}

func TestTelegramNotifySinkReportsRefusalWithoutLeakingToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"description":"Bad Request: token test-token rejected"}`)
	}))
	defer srv.Close()

	sink := NewTelegramNotifySink(TelegramConfig{Token: "test-token", ChatID: "42", APIBase: srv.URL, Doer: srv.Client()})
	err := sink.SendText(context.Background(), "42", "hello")
	if err == nil {
		t.Fatal("refused request must fail")
	}
	if !strings.Contains(err.Error(), "Bad Request") {
		t.Fatalf("err = %v, want refusal detail", err)
	}
	if strings.Contains(err.Error(), "test-token") {
		t.Fatalf("err leaked the token: %v", err)
	}
}

func TestParseBotCommand(t *testing.T) {
	cases := []struct {
		text    string
		command BotCommand
		target  string
		note    string
		ok      bool
	}{
		{"/status", BotStatus, "", "", true},
		{"/status@MyBot", BotStatus, "", "", true},
		{"  /inspect watch-1  ", BotInspect, "watch-1", "", true},
		{"/approve watch-1 спасибо за работу", BotApprove, "watch-1", "спасибо за работу", true},
		{"/unblock blocker-9", BotUnblock, "blocker-9", "", true},
		{"/PAUSE watch-2", BotPause, "watch-2", "", true},
		{"hello", "", "", "", false},
		{"/unknown watch-1", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseBotCommand(tc.text)
		if ok != tc.ok || got.Command != tc.command || got.Target != tc.target || got.Note != tc.note {
			t.Errorf("ParseBotCommand(%q) = (%q, %q, %q, %t), want (%q, %q, %q, %t)",
				tc.text, got.Command, got.Target, got.Note, ok, tc.command, tc.target, tc.note, tc.ok)
		}
	}
}

func TestNotificationFromEventOnlyMapsHumanAttention(t *testing.T) {
	cases := []struct {
		event  string
		fields observe.Fields
		kind   NotificationKind
		ok     bool
	}{
		{"block_escalated", observe.Fields{observe.FieldWatchID: "w1", observe.FieldReason: "escalate_human"}, NotifyEscalateHuman, true},
		{"block_escalated", observe.Fields{observe.FieldWatchID: "w1", observe.FieldReason: "fix_agent"}, "", false},
		{"executor_orphaned", observe.Fields{observe.FieldWatchID: "w2", "subscriberID": "ses_1"}, NotifyExecutorOrphaned, true},
		{"blocker_opened", observe.Fields{observe.FieldWatchID: "w3", observe.FieldBlockerID: "b1"}, NotifyBlockerOpened, true},
		{"cycle", observe.Fields{observe.FieldWatchID: "w4"}, "", false},
	}
	for _, tc := range cases {
		got, ok := NotificationFromEvent(tc.event, tc.fields)
		if ok != tc.ok || got.Kind != tc.kind {
			t.Errorf("NotificationFromEvent(%q) = (%q, %t), want (%q, %t)", tc.event, got.Kind, ok, tc.kind, tc.ok)
		}
	}
}

type fakeNotificationSink struct {
	notifications []Notification
	err           error
}

func (f *fakeNotificationSink) Notify(_ context.Context, n Notification) error {
	f.notifications = append(f.notifications, n)
	return f.err
}

type captureObserveSink struct {
	events []string
}

func (s *captureObserveSink) Log(_ observe.Level, event string, _ observe.Fields) {
	s.events = append(s.events, event)
}

func (s *captureObserveSink) Inc(string, string) {}

func TestEventNotifySinkForwardsAttentionAndKeepsOriginalLog(t *testing.T) {
	previous := &captureObserveSink{}
	notifier := &fakeNotificationSink{}
	sink := NewEventNotifySink(previous, notifier)

	sink.Log(observe.LevelInfo, "executor_orphaned", observe.Fields{observe.FieldWatchID: "w1"})
	sink.Log(observe.LevelInfo, "cycle", observe.Fields{})
	sink.Log(observe.LevelInfo, "blocker_opened", observe.Fields{observe.FieldWatchID: "w2"})

	if len(notifier.notifications) != 2 {
		t.Fatalf("notifications = %d, want 2", len(notifier.notifications))
	}
	if notifier.notifications[0].Kind != NotifyExecutorOrphaned ||
		notifier.notifications[1].Kind != NotifyBlockerOpened {
		t.Fatalf("kinds = %q, %q", notifier.notifications[0].Kind, notifier.notifications[1].Kind)
	}
	if len(previous.events) != 3 || previous.events[0] != "executor_orphaned" || previous.events[1] != "cycle" {
		t.Fatalf("previous events = %v, want every original event", previous.events)
	}
}

func TestEventNotifySinkLogsDeliveryFailure(t *testing.T) {
	previous := &captureObserveSink{}
	notifier := &fakeNotificationSink{err: errors.New("send failed")}
	sink := NewEventNotifySink(previous, notifier)

	sink.Log(observe.LevelInfo, "blocker_opened", observe.Fields{observe.FieldWatchID: "w1"})

	if len(previous.events) != 2 || previous.events[1] != "telegram_notify_failed" {
		t.Fatalf("events = %v, want the failure recorded on the previous sink", previous.events)
	}
}
