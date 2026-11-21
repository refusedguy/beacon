package wake

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// ntfy is an optional transport, like Telegram. Nothing in the cycle requires
// it: an unconfigured sink drops notifications without an error, so a local
// install keeps working exactly as before.
const (
	NtfyURLEnv   = "BEACON_NTFY_URL"
	NtfyTopicEnv = "BEACON_NTFY_TOPIC"

	defaultNtfyURL     = "https://ntfy.sh"
	ntfyRequestTimeout = 5 * time.Second
	ntfyMaxRespBytes   = 1 << 20
)

// NtfyConfig is the boundary between the process environment and the transport.
// Topic is the destination; URL lets an operator use a self-hosted instance and
// lets a test point the sink at an httptest server.
type NtfyConfig struct {
	URL   string
	Topic string
	Doer  NtfyDoer
}

// NtfyDoer is the single HTTP seam. *http.Client implements it and tests pass a
// fake or an httptest client.
type NtfyDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// NtfyConfigFromEnv reads the optional ntfy settings. A nil lookup uses
// os.Getenv; a caller can pass its own map in tests. An empty result is a valid
// config: the sink is simply not configured.
func NtfyConfigFromEnv(lookup func(string) string) NtfyConfig {
	if lookup == nil {
		lookup = os.Getenv
	}
	return NtfyConfig{
		URL:   strings.TrimSpace(lookup(NtfyURLEnv)),
		Topic: strings.TrimSpace(lookup(NtfyTopicEnv)),
	}
}

// NtfyNotifySink sends human-attention notifications to an ntfy topic. All HTTP
// lives here; the CLI only holds this type behind NotificationSink.
type NtfyNotifySink struct {
	base  string
	topic string
	doer  NtfyDoer
}

// NewNtfyNotifySink builds a sink. A missing URL falls back to the public ntfy
// server; a missing doer gets a bounded client so a hung endpoint cannot block
// the reconcile cycle.
func NewNtfyNotifySink(cfg NtfyConfig) *NtfyNotifySink {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		base = defaultNtfyURL
	}
	doer := cfg.Doer
	if doer == nil {
		doer = &http.Client{Timeout: ntfyRequestTimeout}
	}
	return &NtfyNotifySink{
		base:  base,
		topic: strings.TrimSpace(cfg.Topic),
		doer:  doer,
	}
}

// Configured reports whether a destination topic is set. An unconfigured sink
// is fail-open: callers get no error and no request is made.
func (s *NtfyNotifySink) Configured() bool {
	return s != nil && s.topic != ""
}

// Notify implements NotificationSink. The message is redacted before it is
// sent, so a secret in a failed-check name never reaches the topic.
func (s *NtfyNotifySink) Notify(ctx context.Context, n Notification) error {
	if !s.Configured() {
		return nil
	}
	return s.Send(ctx, FormatNotification(n))
}

// Send posts one plain-text message to the configured topic. An unconfigured
// sink is a no-op.
func (s *NtfyNotifySink) Send(ctx context.Context, text string) error {
	if !s.Configured() {
		return nil
	}
	clean := string(observe.Redact([]byte(text)))
	endpoint := s.base + "/" + url.PathEscape(s.topic)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(clean))
	if err != nil {
		return fmt.Errorf("wake: ntfy: request: %s", s.sanitize(err.Error()))
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	resp, err := s.doer.Do(req)
	if err != nil {
		return fmt.Errorf("wake: ntfy: transport: %s", s.sanitize(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, ntfyMaxRespBytes))
	if err != nil {
		return fmt.Errorf("wake: ntfy: read: %s", s.sanitize(err.Error()))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("wake: ntfy: status %d: %s", resp.StatusCode, s.sanitize(string(raw)))
	}
	return nil
}

func (s *NtfyNotifySink) sanitize(raw string) string {
	return string(observe.Redact([]byte(raw)))
}
