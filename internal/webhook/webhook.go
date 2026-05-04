package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BackoffSchedule is the gap between successive attempts. Once attempts has
// reached len(BackoffSchedule), the delivery is marked dead.
var BackoffSchedule = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	5 * time.Minute,
	30 * time.Minute,
}

type StatusFetcher interface {
	GetRaw(ctx context.Context, requestID string) (payload []byte, callbackURL, callbackSecret string, err error)
}

type Service struct {
	pool   *pgxpool.Pool
	status StatusFetcher
	logger *slog.Logger
}

func NewService(pool *pgxpool.Pool, status StatusFetcher, logger *slog.Logger) *Service {
	return &Service{pool: pool, status: status, logger: logger}
}

// Enqueue snapshots the current status of requestID and (if a callback URL is
// configured for this request or its app) inserts a webhook_deliveries row
// ready for the worker to send. No-op when no URL is configured.
func (s *Service) Enqueue(ctx context.Context, requestID string) error {
	payload, url, secret, err := s.status.GetRaw(ctx, requestID)
	if err != nil {
		return fmt.Errorf("webhook: snapshot status: %w", err)
	}
	if url == "" || secret == "" {
		return nil
	}
	sig := sign(secret, payload)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (request_id, url, payload, signature)
		VALUES ($1::uuid, $2, $3, $4)
	`, requestID, url, payload, sig)
	if err != nil {
		return fmt.Errorf("webhook: insert delivery: %w", err)
	}
	return nil
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type Worker struct {
	pool      *pgxpool.Pool
	http      *http.Client
	interval  time.Duration
	batchSize int
	logger    *slog.Logger
}

func NewWorker(pool *pgxpool.Pool, interval time.Duration, batchSize int, logger *slog.Logger) *Worker {
	return &Worker{
		pool:      pool,
		http:      &http.Client{Timeout: 10 * time.Second},
		interval:  interval,
		batchSize: batchSize,
		logger:    logger,
	}
}

func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("webhook worker starting", "interval", w.interval, "batch_size", w.batchSize)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("webhook worker stopped")
			return
		case <-t.C:
			n, err := w.tick(ctx)
			if err != nil {
				w.logger.Error("webhook tick failed", "err", err)
			} else if n > 0 {
				w.logger.Debug("webhook tick", "processed", n)
			}
		}
	}
}

// Tick processes one batch of due deliveries. Exposed for tests / ops.
func (w *Worker) Tick(ctx context.Context) (int, error) {
	return w.tick(ctx)
}

type pendingDelivery struct {
	id        string
	url       string
	payload   []byte
	signature string
	attempt   int
}

func (w *Worker) tick(ctx context.Context) (int, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT id::text, url, payload, signature, attempt
		FROM webhook_deliveries
		WHERE status = 'pending' AND next_attempt_at <= NOW()
		ORDER BY next_attempt_at
		LIMIT $1
	`, w.batchSize)
	if err != nil {
		return 0, err
	}
	deliveries := make([]pendingDelivery, 0)
	for rows.Next() {
		var d pendingDelivery
		if err := rows.Scan(&d.id, &d.url, &d.payload, &d.signature, &d.attempt); err != nil {
			rows.Close()
			return 0, err
		}
		deliveries = append(deliveries, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	processed := 0
	for _, d := range deliveries {
		if err := w.deliver(ctx, d); err != nil {
			w.logger.Warn("webhook deliver", "delivery_id", d.id, "attempt", d.attempt, "err", err)
		}
		processed++
	}
	return processed, nil
}

func (w *Worker) deliver(ctx context.Context, d pendingDelivery) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(d.payload))
	if err != nil {
		return w.recordFailure(ctx, d, 0, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Txmill-Signature", d.signature)
	req.Header.Set("X-Txmill-Delivery", d.id)
	req.Header.Set("X-Txmill-Attempt", fmt.Sprintf("%d", d.attempt))

	resp, err := w.http.Do(req)
	if err != nil {
		return w.recordFailure(ctx, d, 0, err.Error())
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, err := w.pool.Exec(ctx, `
			UPDATE webhook_deliveries
			SET status = 'delivered',
			    response_code = $1,
			    last_error = NULL,
			    attempt = attempt + 1
			WHERE id = $2::uuid
		`, resp.StatusCode, d.id)
		return err
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return w.recordFailure(ctx, d, resp.StatusCode, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)))
}

func (w *Worker) recordFailure(ctx context.Context, d pendingDelivery, code int, msg string) error {
	nextAttempt := d.attempt + 1
	if nextAttempt >= len(BackoffSchedule)+1 {
		_, err := w.pool.Exec(ctx, `
			UPDATE webhook_deliveries
			SET status = 'dead', response_code = NULLIF($1, 0),
			    last_error = $2, attempt = $3
			WHERE id = $4::uuid
		`, code, msg, nextAttempt, d.id)
		return err
	}
	next := time.Now().Add(BackoffSchedule[d.attempt])
	_, err := w.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET response_code   = NULLIF($1, 0),
		    last_error      = $2,
		    attempt         = $3,
		    next_attempt_at = $4
		WHERE id = $5::uuid
	`, code, msg, nextAttempt, next, d.id)
	return err
}
