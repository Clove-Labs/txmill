package relay_test

import (
	"context"
	"strings"
	"testing"

	"github.com/clove-labs/txmill/internal/relay"
	"github.com/clove-labs/txmill/internal/store/dbtest"
)

func TestRecover_Empty(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	r, err := relay.Recover(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if r.InflightAttempts != 0 {
		t.Fatalf("InflightAttempts = %d, want 0", r.InflightAttempts)
	}
	if r.PendingWebhookDeliveries != 0 {
		t.Fatalf("PendingWebhookDeliveries = %d, want 0", r.PendingWebhookDeliveries)
	}
	if len(r.StuckRequests) != 0 {
		t.Fatalf("StuckRequests = %d, want 0", len(r.StuckRequests))
	}
}

func TestRecover_CountsInflight(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	appID := mustInsertApp(t, ctx, pool)
	req1 := mustInsertRequest(t, ctx, pool, appID,
		"0x1111111111111111111111111111111111111111", []byte{}, "0")
	req2 := mustInsertRequest(t, ctx, pool, appID,
		"0x2222222222222222222222222222222222222222", []byte{}, "0")
	_ = mustInsertAttempt(t, ctx, pool, req1, "0x"+strings.Repeat("aa", 32))
	_ = mustInsertAttempt(t, ctx, pool, req2, "0x"+strings.Repeat("bb", 32))

	// Mark one attempt confirmed (terminal — should NOT count).
	if _, err := pool.Exec(ctx, `UPDATE tx_attempts SET status='confirmed' WHERE tx_hash=$1`,
		"0x"+strings.Repeat("aa", 32)); err != nil {
		t.Fatal(err)
	}

	r, err := relay.Recover(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if r.InflightAttempts != 1 {
		t.Fatalf("InflightAttempts = %d, want 1", r.InflightAttempts)
	}
}

func TestRecover_StuckRequests(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	appID := mustInsertApp(t, ctx, pool)

	// Two requests with no attempts. mustInsertRequest defaults status='submitted',
	// so flip them to 'pending' to simulate "crashed before submit".
	stuck1 := mustInsertRequest(t, ctx, pool, appID,
		"0x1111111111111111111111111111111111111111", []byte{}, "0")
	stuck2 := mustInsertRequest(t, ctx, pool, appID,
		"0x2222222222222222222222222222222222222222", []byte{}, "0")
	if _, err := pool.Exec(ctx, `
		UPDATE relay_requests SET status='pending' WHERE id IN ($1::uuid, $2::uuid)
	`, stuck1, stuck2); err != nil {
		t.Fatal(err)
	}

	// One pending request WITH an attempt — not stuck.
	notStuck := mustInsertRequest(t, ctx, pool, appID,
		"0x3333333333333333333333333333333333333333", []byte{}, "0")
	if _, err := pool.Exec(ctx, `UPDATE relay_requests SET status='pending' WHERE id=$1::uuid`, notStuck); err != nil {
		t.Fatal(err)
	}
	_ = mustInsertAttempt(t, ctx, pool, notStuck, "0x"+strings.Repeat("cc", 32))

	r, err := relay.Recover(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.StuckRequests) != 2 {
		t.Fatalf("StuckRequests = %d, want 2", len(r.StuckRequests))
	}
	got := map[string]bool{r.StuckRequests[0].RequestID: true, r.StuckRequests[1].RequestID: true}
	if !got[stuck1] || !got[stuck2] {
		t.Fatalf("missing stuck ids: got %v, want %s/%s", got, stuck1, stuck2)
	}
}

func TestRecover_PendingWebhooks(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	appID := mustInsertApp(t, ctx, pool)
	requestID := mustInsertRequest(t, ctx, pool, appID,
		"0x4444444444444444444444444444444444444444", []byte{}, "0")

	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (request_id, url, payload, signature)
		VALUES ($1::uuid, 'https://x', '\x01', 'sha256=abc'),
		       ($1::uuid, 'https://x', '\x02', 'sha256=abc')
	`, requestID); err != nil {
		t.Fatal(err)
	}
	// Mark one as already delivered — should NOT count.
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_deliveries SET status='delivered' WHERE payload='\x01'
	`); err != nil {
		t.Fatal(err)
	}

	r, err := relay.Recover(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if r.PendingWebhookDeliveries != 1 {
		t.Fatalf("PendingWebhookDeliveries = %d, want 1", r.PendingWebhookDeliveries)
	}
}
