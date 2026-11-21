package wake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// Telegram is an optional transport. Nothing in the cycle requires it: an
// unconfigured sink drops notifications and update polls without an error, so a
// local install keeps working exactly as before.
const (
	TelegramTokenEnv   = "BEACON_TELEGRAM_TOKEN"
	TelegramChatEnv    = "BEACON_TELEGRAM_CHAT"
	TelegramAPIBaseEnv = "BEACON_TELEGRAM_API"

	telegramAPIHost        = "api.telegram.org"
	defaultTelegramAPIBase = "https://api.telegram.org"
	telegramRequestTimeout = 5 * time.Second
	telegramMaxRespBytes   = 1 << 20
)

// ErrTelegramTransport marks a failed Telegram request. Callers use errors.Is
// to detect transport failures. The original cause stays unwrapped because its
// text can carry the token from the request URL.
var ErrTelegramTransport = errors.New("wake: telegram transport failure")

// TelegramConfig is the boundary between the process environment and the
// transport. Token and ChatID are secrets and never appear in a log field;
// APIBase exists so a test can point the sink at an httptest server, and is
// checked against an allowlist before a request is built.
type TelegramConfig struct {
	Token   string
	ChatID  string
	APIBase string
	Doer    TelegramDoer
}

// TelegramDoer is the single HTTP seam. *http.Client implements it and tests
// pass a fake or an httptest client.
type TelegramDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// TelegramConfigFromEnv reads the optional Telegram settings. A nil lookup uses
// os.Getenv; a caller can pass its own map in tests. An empty result is a valid
// config: the sink is simply not configured.
func TelegramConfigFromEnv(lookup func(string) string) TelegramConfig {
	if lookup == nil {
		lookup = os.Getenv
	}
	return TelegramConfig{
		Token:   strings.TrimSpace(lookup(TelegramTokenEnv)),
		ChatID:  strings.TrimSpace(lookup(TelegramChatEnv)),
		APIBase: strings.TrimSpace(lookup(TelegramAPIBaseEnv)),
	}
}

// TelegramNotifySink sends human-attention notifications and polls bot commands
// through the Telegram Bot API. All HTTP lives here; the CLI only holds this
// type behind a port.
type TelegramNotifySink struct {
	cfg          TelegramConfig
	base         string
	doer         TelegramDoer
	baseRejected bool
}

// NewTelegramNotifySink builds a sink. A missing API base falls back to the
// public Bot API; a missing doer gets a bounded client so a hung endpoint can
// never block the reconcile cycle. A base naming any host other than the
// public API or loopback is refused: the token travels in the URL path, so
// sending it to an address injected through the environment would leak it.
func NewTelegramNotifySink(cfg TelegramConfig) *TelegramNotifySink {
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	rejected := false
	switch {
	case base == "":
		base = defaultTelegramAPIBase
	case telegramAPIBaseAllowed(base):
		// Keep the explicit base: loopback in tests, or the public API.
	default:
		base = defaultTelegramAPIBase
		rejected = true
	}
	doer := cfg.Doer
	if doer == nil {
		doer = &http.Client{Timeout: telegramRequestTimeout}
	}
	return &TelegramNotifySink{cfg: cfg, base: base, doer: doer, baseRejected: rejected}
}

// telegramAPIBaseAllowed reports whether raw may receive a bot token. Only the
// public Bot API over https and loopback hosts for tests are allowed. Parsing
// the hostname also rejects lookalikes such as api.telegram.org.evil.test and
// userinfo tricks such as https://api.telegram.org@evil.test.
func telegramAPIBaseAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "":
		return false
	case host == telegramAPIHost:
		return strings.EqualFold(u.Scheme, "https")
	default:
		return isLoopbackHost(host)
	}
}

// isLoopbackHost recognizes the loopback names used by httptest and local
// development. net.ParseIP covers every address in 127.0.0.0/8 and ::1.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Configured reports whether proactive notifications have both a token and a
// destination chat. An unconfigured sink is fail-open: callers get no error and
// no request is made.
func (s *TelegramNotifySink) Configured() bool {
	return s != nil && !s.baseRejected && s.tokenConfigured() && strings.TrimSpace(s.cfg.ChatID) != ""
}

func (s *TelegramNotifySink) tokenConfigured() bool {
	return s != nil && strings.TrimSpace(s.cfg.Token) != ""
}

// Notify implements NotificationSink. The message is redacted before it is
// marshalled, so a secret in a failed-check name never reaches Telegram.
func (s *TelegramNotifySink) Notify(ctx context.Context, n Notification) error {
	if !s.Configured() {
		return nil
	}
	return s.SendText(ctx, s.cfg.ChatID, FormatNotification(n))
}

// SendText posts one message to chatID. It is the reply path for bot commands
// and the send path for notifications. An absent token or chat is a no-op.
func (s *TelegramNotifySink) SendText(ctx context.Context, chatID, text string) error {
	if !s.tokenConfigured() || strings.TrimSpace(chatID) == "" {
		return nil
	}
	clean := string(observe.Redact([]byte(text)))
	var result json.RawMessage
	return s.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     clean,
		"disable_web_page_preview": true,
	}, &result)
}

// TelegramChat is the chat a command came from. Telegram sends a numeric id;
// json.Number keeps it exact without a float conversion.
type TelegramChat struct {
	ID json.Number `json:"id"`
}

// TelegramMessage is the only part of an update the bot reads.
type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	Text      string       `json:"text"`
	Chat      TelegramChat `json:"chat"`
}

// TelegramUpdate is one getUpdates entry. UpdateID is the cursor the caller
// persists between ticks.
type TelegramUpdate struct {
	UpdateID int64           `json:"update_id"`
	Message  TelegramMessage `json:"message"`
}

// Updates polls getUpdates from offset. With no token it returns no updates and
// no error, which keeps the long-running loop quiet on an unconfigured host.
func (s *TelegramNotifySink) Updates(ctx context.Context, offset int64) ([]TelegramUpdate, error) {
	if !s.tokenConfigured() {
		return nil, nil
	}
	var result []TelegramUpdate
	if err := s.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 0}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

type telegramResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

// call posts one Bot API method and decodes its envelope. Request and response
// errors are sanitized: the token lives in the URL and must never reach a log
// or an error string an operator copies into a ticket. A rejected API base
// stops the call before a request reaches the transport.
func (s *TelegramNotifySink) call(ctx context.Context, method string, payload any, out any) error {
	endpoint := s.base + "/bot" + s.cfg.Token + "/" + method
	if s.baseRejected {
		return fmt.Errorf("wake: telegram %s: transport: %w: %s",
			method, ErrTelegramTransport, s.sanitize("refused endpoint "+endpoint))
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("wake: telegram %s: encode: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("wake: telegram %s: request: %w: %s", method, ErrTelegramTransport, s.sanitize(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.doer.Do(req)
	if err != nil {
		return fmt.Errorf("wake: telegram %s: transport: %w: %s", method, ErrTelegramTransport, s.sanitize(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, telegramMaxRespBytes))
	if err != nil {
		return fmt.Errorf("wake: telegram %s: read: %w: %s", method, ErrTelegramTransport, s.sanitize(err.Error()))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("wake: telegram %s: status %d: %s", method, resp.StatusCode, s.sanitize(string(raw)))
	}
	if err := decodeTelegramResult(raw, out); err != nil {
		return fmt.Errorf("wake: telegram %s: %w: %s", method, ErrTelegramTransport, s.sanitize(err.Error()))
	}
	return nil
}

// decodeTelegramResult accepts both shapes the Bot API and mocks return: the
// documented envelope {"ok":true,"result":...} and a bare array. A refusal is
// {"ok":false,"description":...}. Decoding the shape is a boundary concern, so
// it lives here rather than in each method.
func decodeTelegramResult(raw []byte, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		if out != nil {
			return json.Unmarshal(trimmed, out)
		}
		return nil
	}
	var envelope telegramResponse
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if !envelope.OK {
		detail := strings.TrimSpace(envelope.Description)
		if detail == "" {
			detail = "telegram refused the request"
		}
		return errors.New(detail)
	}
	if out != nil && len(envelope.Result) != 0 && string(envelope.Result) != "null" {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

func (s *TelegramNotifySink) sanitize(raw string) string {
	if s == nil {
		return raw
	}
	out := raw
	if token := strings.TrimSpace(s.cfg.Token); token != "" {
		out = strings.ReplaceAll(out, token, "[REDACTED]")
	}
	return string(observe.Redact([]byte(out)))
}

// BotCommand is the closed set of commands the Telegram bot accepts. Commands
// are lowercased so a user spelling is normalized before dispatch.
type BotCommand string

const (
	BotStatus  BotCommand = "/status"
	BotInspect BotCommand = "/inspect"
	BotApprove BotCommand = "/approve"
	BotUnblock BotCommand = "/unblock"
	BotPause   BotCommand = "/pause"
)

// ParsedCommand is one bot command. Target is the watchID or blockerID, Note is
// the operator's optional trailing text.
type ParsedCommand struct {
	Command BotCommand
	Target  string
	Note    string
}

var botCommands = map[string]BotCommand{
	"/status":  BotStatus,
	"/inspect": BotInspect,
	"/approve": BotApprove,
	"/unblock": BotUnblock,
	"/pause":   BotPause,
}

// ParseBotCommand maps raw chat text onto a command. A non-command message and
// an unknown command are both rejected; the caller decides whether to answer.
// A "/status@MyBot" mention keeps working in a group chat.
func ParseBotCommand(text string) (ParsedCommand, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ParsedCommand{}, false
	}
	word := fields[0]
	if at := strings.Index(word, "@"); at > 0 {
		word = word[:at]
	}
	cmd, ok := botCommands[strings.ToLower(word)]
	if !ok {
		return ParsedCommand{}, false
	}
	out := ParsedCommand{Command: cmd}
	if len(fields) > 1 {
		out.Target = strings.TrimSpace(fields[1])
	}
	if len(fields) > 2 {
		out.Note = strings.TrimSpace(strings.Join(fields[2:], " "))
	}
	return out, true
}

// NotificationFromEvent maps the three structured observe events onto a
// Notification. The mapping is pure so the bridge can be tested without a
// Telegram server. A block_escalated event qualifies only when its reason is
// escalate_human: other block reasons are not a human-escalation notice.
func NotificationFromEvent(event string, fields observe.Fields) (Notification, bool) {
	if fields == nil {
		fields = observe.Fields{}
	}
	n := Notification{
		WatchID:  fieldString(fields, observe.FieldWatchID),
		PR:       fieldInt(fields, observe.FieldPR),
		Revision: fieldUint(fields, observe.FieldRevision),
	}
	switch event {
	case "block_escalated":
		if fieldString(fields, observe.FieldReason) != string(NotifyEscalateHuman) {
			return Notification{}, false
		}
		n.Kind = NotifyEscalateHuman
		n.Detail = "reason=" + string(NotifyEscalateHuman)
	case "executor_orphaned":
		n.Kind = NotifyExecutorOrphaned
		n.Detail = "subscriber=" + fieldString(fields, "subscriberID") +
			", session=" + fieldString(fields, "sessionID")
	case "blocker_opened":
		n.Kind = NotifyBlockerOpened
		n.Detail = "blocker=" + fieldString(fields, observe.FieldBlockerID) +
			", repo=" + fieldString(fields, "repo") +
			", baseRef=" + fieldString(fields, "baseRef")
	default:
		return Notification{}, false
	}
	return n, true
}

func fieldString(fields observe.Fields, key string) string {
	v, ok := fields[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return fmt.Sprint(v)
}

func fieldInt(fields observe.Fields, key string) int {
	switch v := fields[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	case string:
		var n int
		_, _ = fmt.Sscan(v, &n)
		return n
	default:
		return 0
	}
}

func fieldUint(fields observe.Fields, key string) uint64 {
	switch v := fields[key].(type) {
	case uint64:
		return v
	case int:
		if v > 0 {
			return uint64(v)
		}
	case int64:
		if v > 0 {
			return uint64(v)
		}
	case string:
		var n uint64
		_, _ = fmt.Sscan(v, &n)
		return n
	}
	return 0
}

// forwardNotification is shared by the two observe decorators below. It logs
// through the previous sink, never through observe.Default, so a notify failure
// cannot recurse into this decorator.
func forwardNotification(previous observe.Sink, notify NotificationSink, event string, fields observe.Fields) {
	n, ok := NotificationFromEvent(event, fields)
	if !ok || notify == nil {
		return
	}
	if err := notify.Notify(context.Background(), n); err != nil {
		previous.Log(observe.LevelWarn, "telegram_notify_failed", observe.Fields{
			observe.FieldEvent:   event,
			observe.FieldWatchID: n.WatchID,
			observe.FieldError:   err.Error(),
		})
		return
	}
	previous.Log(observe.LevelDebug, "telegram_notify_sent", observe.Fields{
		observe.FieldEvent:   event,
		observe.FieldWatchID: n.WatchID,
	})
}

// EventNotifySink tees structured observe events to a NotificationSink. The
// previous sink keeps receiving every event, so logs and counters are unchanged.
type EventNotifySink struct {
	observe.Sink
	Notify NotificationSink
}

// NewEventNotifySink wraps prev for a process that has no typed Recorder sink.
func NewEventNotifySink(prev observe.Sink, notify NotificationSink) *EventNotifySink {
	if prev == nil {
		prev = observe.Default()
	}
	return &EventNotifySink{Sink: prev, Notify: notify}
}

// Log forwards the original event, then the notification when the event is one
// of the three human-attention transitions.
func (s *EventNotifySink) Log(level observe.Level, event string, fields observe.Fields) {
	s.Sink.Log(level, event, fields)
	forwardNotification(s.Sink, s.Notify, event, fields)
}

// RecorderEventNotifySink is the typed variant. Embedding *observe.Recorder
// promotes its unexported histogram method, so observe.Observe keeps working
// after the decorator is installed. A plain observe.Sink cannot do that.
type RecorderEventNotifySink struct {
	*observe.Recorder
	Notify NotificationSink
}

// Log forwards the original event, then the notification.
func (s *RecorderEventNotifySink) Log(level observe.Level, event string, fields observe.Fields) {
	s.Recorder.Log(level, event, fields)
	forwardNotification(s.Recorder, s.Notify, event, fields)
}
