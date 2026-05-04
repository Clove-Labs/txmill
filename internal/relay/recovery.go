package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RecoveryReport describes the state the process is resuming on startup.
// Counts > 0 are not errors — they're work the watcher / webhook worker
// will pick up on their next tick.
type RecoveryReport struct {
	InflightAttempts         int
	PendingWebhookDeliveries int
	StuckRequests            []StuckRequest
}

type StuckRequest struct {
	RequestID string
	AppID     string
	CreatedAt time.Time
}

// Recover scans the database for work in progress and returns a snapshot
// for the operator to log/inspect. It does NOT mutate state — the receipt
// watcher and webhook worker resume on their own ticks.
func Recover(ctx context.Context, pool *pgxpool.Pool) (*RecoveryReport, error) {
	r := &RecoveryReport{}

	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM tx_attempts WHERE status IN ('submitting', 'submitted')
	`).Scan(&r.InflightAttempts); err != nil {
		return nil, fmt.Errorf("count inflight attempts: %w", err)
	}

	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'pending'
	`).Scan(&r.PendingWebhookDeliveries); err != nil {
		return nil, fmt.Errorf("count webhook deliveries: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT r.id::text, r.app_id::text, r.created_at
		FROM relay_requests r
		WHERE r.status = 'pending'
		  AND NOT EXISTS (SELECT 1 FROM tx_attempts a WHERE a.request_id = r.id)
		ORDER BY r.created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("scan stuck requests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s StuckRequest
		if err := rows.Scan(&s.RequestID, &s.AppID, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan stuck request row: %w", err)
		}
		r.StuckRequests = append(r.StuckRequests, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return r, nil
}
