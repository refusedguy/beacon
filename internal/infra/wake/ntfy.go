package wake

import (
	"context"
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
	valid bool
}

// normalizeNtfyBase validates the endpoint before it can become a request
// target. Only http and https URLs with a public host are accepted. An empty
// value uses the public server. The bool reports whether the value is usable.
func normalizeNtfyBase(raw string) (string, bool) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return defaultNtfyURL, true
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := u.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil &&
		(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return "", false
	}
	return base, true
}

// validNtfyTopic accepts the characters ntfy uses in a topic: letters, digits,
// dash, underscore and dot. Anything else would change the request path.
func validNtfyTopic(topic string) bool {
	if topic == "" {
		return false
	}
	for _, r := range topic {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func warnNtfyConfig(name, value string) {
	observe.Event(observe.LevelWarn, "config.invalid", observe.Fields{
		"env":   name,
		"value": value,
	})
}

// NewNtfyNotifySink builds a sink. A missing URL falls back to the public ntfy
// server; a missing doer gets a bounded client so a hung endpoint cannot block
// the reconcile cycle. An invalid URL or topic is logged and leaves the sink
// unconfigured, so Notify and Send fail open and no request is made.
func NewNtfyNotifySink(cfg NtfyConfig) *NtfyNotifySink {
	base, validURL := normalizeNtfyBase(cfg.URL)
	topic := strings.TrimSpace(cfg.Topic)
	validTopic := validNtfyTopic(topic)
	if strings.TrimSpace(cfg.URL) != "" && !validURL {
		warnNtfyConfig(NtfyURLEnv, cfg.URL)
	}
	if topic != "" && !validTopic {
		warnNtfyConfig(NtfyTopicEnv, cfg.Topic)
	}
	doer := cfg.Doer
	if doer == nil {
		doer = &http.Client{Timeout: ntfyRequestTimeout}
	}
	return &NtfyNotifySink{
		base:  base,
		topic: topic,
		doer:  doer,
		valid: validURL && validTopic,
	}
}

// Configured reports whether a destination topic is set on a valid sink. An
// unconfigured sink is fail-open: callers get no error and no request is made.
func (s *NtfyNotifySink) Configured() bool {
	return s != nil && s.valid && s.topic != ""
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
		return s.transportError("request", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	resp, err := s.doer.Do(req)
	if err != nil {
		return s.transportError("transport", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, ntfyMaxRespBytes))
	if err != nil {
		return s.transportError("read", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("wake: ntfy: status %d: %s", resp.StatusCode, s.sanitize(string(raw)))
	}
	return nil
}

// transportError keeps the old error text and carries the cause sentinels in a
// redacted chain. The original error value stays out of Error and Unwrap.
func (s *NtfyNotifySink) transportError(stage string, cause error) error {
	message := "wake: ntfy: " + stage + ": " + s.sanitize(cause.Error())
	return newRedactedTransportError(message, s.sanitize, nil, cause)
}

func (s *NtfyNotifySink) sanitize(raw string) string {
	return string(observe.Redact([]byte(raw)))
}
