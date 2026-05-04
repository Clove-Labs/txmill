package alert_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clove-labs/txmill/internal/alert"
)

func TestWebhook_Notify(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	a := alert.Alert{Key: "k", Level: alert.LevelWarn, Title: "t", Message: "m",
		Tags: map[string]string{"app_id": "x"}}
	if err := alert.NewWebhook(srv.URL).Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "t" || got["message"] != "m" {
		t.Fatalf("body = %v", got)
	}
	if got["level"] != "warn" {
		t.Fatalf("level = %v", got["level"])
	}
}

func TestWebhook_NotifyErrorOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	if err := alert.NewWebhook(srv.URL).Notify(context.Background(), alert.Alert{}); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestTelegram_Notify(t *testing.T) {
	var receivedForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		receivedForm = r.PostForm
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := alert.NewTelegram("BOTTOKEN", "CHATID")
	tg.SetAPIBase(srv.URL)

	if err := tg.Notify(context.Background(), alert.Alert{
		Key: "k", Level: alert.LevelWarn, Title: "Treasury low", Message: "balance: 0",
		Tags: map[string]string{"app_id": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if receivedForm.Get("chat_id") != "CHATID" {
		t.Fatalf("chat_id = %q", receivedForm.Get("chat_id"))
	}
	text := receivedForm.Get("text")
	if !strings.Contains(text, "[WARN]") || !strings.Contains(text, "Treasury low") || !strings.Contains(text, "balance: 0") {
		t.Fatalf("text missing fields: %q", text)
	}
}

type counter struct {
	calls atomic.Int32
	err   error
}

func (c *counter) Notify(context.Context, alert.Alert) error {
	c.calls.Add(1)
	return c.err
}

func TestMulti_FansOut(t *testing.T) {
	a := &counter{}
	b := &counter{}
	if err := (alert.Multi{a, b}).Notify(context.Background(), alert.Alert{}); err != nil {
		t.Fatal(err)
	}
	if a.calls.Load() != 1 || b.calls.Load() != 1 {
		t.Fatalf("a=%d b=%d, want 1/1", a.calls.Load(), b.calls.Load())
	}
}

func TestMulti_AggregatesErrors(t *testing.T) {
	a := &counter{err: errors.New("fail")}
	b := &counter{}
	err := (alert.Multi{a, b}).Notify(context.Background(), alert.Alert{})
	if err == nil || !strings.Contains(err.Error(), "fail") {
		t.Fatalf("err = %v", err)
	}
	if b.calls.Load() != 1 {
		t.Fatal("b not called despite a's failure")
	}
}

func TestThrottled_SuppressesWithinInterval(t *testing.T) {
	c := &counter{}
	th := alert.NewThrottled(c, time.Hour)
	a := alert.Alert{Key: "same"}
	for i := 0; i < 5; i++ {
		_ = th.Notify(context.Background(), a)
	}
	if c.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", c.calls.Load())
	}
}

func TestThrottled_DistinctKeysAllPassThrough(t *testing.T) {
	c := &counter{}
	th := alert.NewThrottled(c, time.Hour)
	for i := 0; i < 5; i++ {
		_ = th.Notify(context.Background(), alert.Alert{Key: string(rune('a' + i))})
	}
	if c.calls.Load() != 5 {
		t.Fatalf("calls = %d, want 5", c.calls.Load())
	}
}
