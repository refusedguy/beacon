package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/refusedguy/beacon/internal/app"
)

type Telegram struct {
	Token  string
	ChatID string
	HTTP   *http.Client
}

func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{Token: token, ChatID: chatID, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (t *Telegram) Enabled() bool { return t.Token != "" && t.ChatID != "" }

func (t *Telegram) Notify(ctx context.Context, n app.Notification) error {
	if !t.Enabled() {
		return nil
	}
	text := Redact(fmt.Sprintf("beacon [%s] %s: %s", n.RunID, n.Subject, n.Body))
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
