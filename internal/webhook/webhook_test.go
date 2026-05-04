package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clove-labs/txmill/internal/store/dbtest"
	"github.com/clove-labs/txmill/internal/webhook"
)

type stubStatus struct {
	payload []byte
	url     string
	secret  string
}

func (s stubStatus) GetRaw(_ context.Context, _ string) ([]byte, string, string, error) {
	return s.payload, s.url, s.secret, nil
}

func mustInsertAppForWebhook(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	hash := sha256.Sum256([]byte("token-" + t.Name()))
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO apps (name, treasury_address, bearer_token_hash, pool_size)
		VALUES ($1, $2, $3, 1)
		RETURNING id::text
	`, "wh-test", "0x"+strings.Repeat("aa", 20), hash[:]).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustInsertRequestForWebhook(t *testing.T, ctx context.Context, pool *pgxpool.Pool, appID string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO relay_requests (app_id, chain_id, to_address, call_data, value, status)
		VALUES ($1::uuid, 146, $2, '\x', 0, 'submitted')
		RETURNING id::text
	`, appID, "0x"+strings.Repeat("bb", 20)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEnqueue_NoURL_IsNoop(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	appID := mustInsertAppForWebhook(t, ctx, pool)
	requestID := mustInsertRequestForWebhook(t, ctx, pool, appID)

	svc := webhook.NewService(pool, stubStatus{
		payload: []byte(`{"x":1}`), url: "", secret: "whsec_x",
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := svc.Enqueue(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM webhook_deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected no rows when URL empty, got %d", n)
	}
}

func TestEnqueue_NoSecret_IsNoop(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	appID := mustInsertAppForWebhook(t, ctx, pool)
	requestID := mustInsertRequestForWebhook(t, ctx, pool, appID)

	svc := webhook.NewService(pool, stubStatus{
		payload: []byte(`{"x":1}`), url: "https://x", secret: "",
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := svc.Enqueue(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM webhook_deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected no rows when secret empty, got %d", n)
	}
}

func TestEnqueueAndDeliver_Success(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	appID := mustInsertAppForWebhook(t, ctx, pool)
	requestID := mustInsertRequestForWebhook(t, ctx, pool, appID)

	payload := []byte(`{"status":"confirmed"}`)
	secret := "whsec_test"

	var receivedSig, receivedDelivery, receivedAttempt string
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Txmill-Signature")
		receivedDelivery = r.Header.Get("X-Txmill-Delivery")
		receivedAttempt = r.Header.Get("X-Txmill-Attempt")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	svc := webhook.NewService(pool, stubStatus{payload: payload, url: srv.URL, secret: secret},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := svc.Enqueue(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	worker := webhook.NewWorker(pool, time.Second, 50, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if _, err := worker.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if string(receivedBody) != string(payload) {
		t.Fatalf("body = %s, want %s", receivedBody, payload)
	}

	expected := computeHMAC(secret, payload)
	if receivedSig != expected {
		t.Fatalf("sig header = %q, want %q", receivedSig, expected)
	}
	if receivedDelivery == "" {
		t.Fatal("X-Txmill-Delivery missing")
	}
	if receivedAttempt != "0" {
		t.Fatalf("X-Txmill-Attempt = %q, want 0", receivedAttempt)
	}

	var status string
	var responseCode int
	if err := pool.QueryRow(ctx, `SELECT status, response_code FROM webhook_deliveries`).Scan(&status, &responseCode); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("status = %q, want delivered", status)
	}
	if responseCode != 200 {
		t.Fatalf("response_code = %d, want 200", responseCode)
	}
}

func TestDeliver_Failure_RetryWithBackoff(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	appID := mustInsertAppForWebhook(t, ctx, pool)
	requestID := mustInsertRequestForWebhook(t, ctx, pool, appID)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("oops"))
	}))
	defer srv.Close()

	svc := webhook.NewService(pool, stubStatus{
		payload: []byte(`{}`), url: srv.URL, secret: "x",
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := svc.Enqueue(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	worker := webhook.NewWorker(pool, time.Second, 50, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if _, err := worker.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	var status string
	var attempt int
	var lastError *string
	var nextAt time.Time
	now := time.Now()
	if err := pool.QueryRow(ctx, `
		SELECT status, attempt, last_error, next_attempt_at FROM webhook_deliveries
	`).Scan(&status, &attempt, &lastError, &nextAt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending (still retrying)", status)
	}
	if attempt != 1 {
		t.Fatalf("attempt = %d, want 1", attempt)
	}
	if lastError == nil || !strings.Contains(*lastError, "HTTP 500") {
		t.Fatalf("last_error = %v, want HTTP 500", lastError)
	}
	if !nextAt.After(now) {
		t.Fatalf("next_attempt_at = %v, want after now (%v)", nextAt, now)
	}
}

func TestDeliver_ExhaustsRetries_MarksDead(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	appID := mustInsertAppForWebhook(t, ctx, pool)
	requestID := mustInsertRequestForWebhook(t, ctx, pool, appID)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	svc := webhook.NewService(pool, stubStatus{
		payload: []byte(`{}`), url: srv.URL, secret: "x",
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := svc.Enqueue(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	worker := webhook.NewWorker(pool, time.Second, 50, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	maxAttempts := len(webhook.BackoffSchedule) + 1
	for i := 0; i < maxAttempts; i++ {
		// Force the row due so the worker picks it up each iteration.
		if _, err := pool.Exec(ctx, `UPDATE webhook_deliveries SET next_attempt_at = NOW()`); err != nil {
			t.Fatal(err)
		}
		if _, err := worker.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}

	var status string
	var attempt int
	if err := pool.QueryRow(ctx, `SELECT status, attempt FROM webhook_deliveries`).Scan(&status, &attempt); err != nil {
		t.Fatal(err)
	}
	if status != "dead" {
		t.Fatalf("status = %q, want dead", status)
	}
	if attempt != maxAttempts {
		t.Fatalf("attempt = %d, want %d", attempt, maxAttempts)
	}
}

func computeHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
