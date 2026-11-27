// Package notify sends operator notifications.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
	"github.com/refusedguy/beacon/pkg/core/redact"
)

// Telegram sends notifications through the Telegram Bot API.
type Telegram struct {
	Token    string
	ChatID   string
	HTTP     *http.Client
	endpoint string
}

// NewTelegram builds a notifier for a bot token and chat. The endpoint is
// built once here because the token and its PathEscape cannot change on the
// send path.
func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{
		Token:    token,
		ChatID:   chatID,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
		endpoint: "https://api.telegram.org/bot" + url.PathEscape(token) + "/sendMessage",
	}
}

// Enabled reports whether a token and chat id are configured.
func (t *Telegram) Enabled() bool { return t.Token != "" && t.ChatID != "" }

// Notify sends n as a Telegram message.
func (t *Telegram) Notify(ctx context.Context, n core.Notification) error {
	if !t.Enabled() {
		return nil
	}
	text := redact.Redact(fmt.Sprintf("beacon [%s] %s: %s", n.RunID, n.Subject, n.Body))
	payload, _ := json.Marshal(map[string]string{
		"chat_id": t.ChatID,
		"text":    text,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram status %d", resp.StatusCode)
	}
	return nil
}
