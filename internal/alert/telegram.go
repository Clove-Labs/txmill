package alert

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Telegram struct {
	apiBase  string
	botToken string
	chatID   string
	client   *http.Client
}

func NewTelegram(botToken, chatID string) *Telegram {
	return &Telegram{
		apiBase:  "https://api.telegram.org",
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// SetAPIBase overrides the Telegram API endpoint (test-only).
func (t *Telegram) SetAPIBase(base string) { t.apiBase = base }

func (t *Telegram) Notify(ctx context.Context, a Alert) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s\n%s", strings.ToUpper(string(a.Level)), a.Title, a.Message)
	for k, v := range a.Tags {
		fmt.Fprintf(&b, "\n%s: %s", k, v)
	}

	form := url.Values{
		"chat_id": {t.chatID},
		"text":    {b.String()},
	}
	apiURL := fmt.Sprintf("%s/bot%s/sendMessage", t.apiBase, t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}
