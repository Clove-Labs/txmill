package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Webhook struct {
	url    string
	client *http.Client
}

func NewWebhook(url string) *Webhook {
	return &Webhook{url: url, client: &http.Client{Timeout: 10 * time.Second}}
}

func (w *Webhook) Notify(ctx context.Context, a Alert) error {
	body, err := json.Marshal(map[string]any{
		"key":     a.Key,
		"level":   a.Level,
		"title":   a.Title,
		"message": a.Message,
		"tags":    a.Tags,
		"ts":      time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("alert webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}
