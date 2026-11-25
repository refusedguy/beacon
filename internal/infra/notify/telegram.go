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

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

// Telegram sends notifications through the Telegram Bot API.
type Telegram struct {
	Token  string
	ChatID string
	HTTP   *http.Client
}

// NewTelegram builds a notifier for a bot token and chat.
func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{Token: token, ChatID: chatID, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Enabled reports whether a token and chat id are configured.
func (t *Telegram) Enabled() bool { return t.Token != "" && t.ChatID != "" }

// Notify sends n as a Telegram message.
func (t *Telegram) Notify(ctx context.Context, n core.Notification) error {
	if !t.Enabled() {
		return nil
	}
	text := shared.Redact(fmt.Sprintf("beacon [%s] %s: %s", n.RunID, n.Subject, n.Body))
	payload, _ := json.Marshal(map[string]string{
		"chat_id": t.ChatID,
		"text":    text,
	})
	endpoint := "https://api.telegram.org/bot" + url.PathEscape(t.Token) + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
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
