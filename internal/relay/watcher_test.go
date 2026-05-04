package relay_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clove-labs/txmill/internal/relay"
	"github.com/clove-labs/txmill/internal/store/dbtest"
)

type stubReceiptChain struct {
	receipts map[string]*types.Receipt
	notFound map[string]bool
	callErr  string
	callHits int
}

func (s *stubReceiptChain) TransactionReceipt(_ context.Context, hash common.Hash) (*types.Receipt, error) {
	key := strings.ToLower(hash.Hex())
	if s.notFound[key] {
		return nil, ethereum.NotFound
	}
	r, ok := s.receipts[key]
	if !ok {
		return nil, ethereum.NotFound
	}
	return r, nil
}

func (s *stubReceiptChain) CallContract(_ context.Context, _ ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	s.callHits++
	if s.callErr != "" {
		return nil, errors.New(s.callErr)
	}
	return nil, nil
}

func mustInsertApp(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	hash := sha256.Sum256([]byte("test-token-" + t.Name()))
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO apps (name, treasury_address, bearer_token_hash, pool_size)
		VALUES ($1, $2, $3, 1)
		RETURNING id::text
	`, "watcher-test", "0x"+strings.Repeat("aa", 20), hash[:]).Scan(&id)
	if err != nil {
		t.Fatalf("insert app: %v", err)
	}
	return id
}

func mustInsertRequest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, appID, to string, data []byte, value string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO relay_requests (app_id, chain_id, to_address, call_data, value, status)
		VALUES ($1::uuid, 146, $2, $3, $4, 'submitted')
		RETURNING id::text
	`, appID, to, data, value).Scan(&id)
	if err != nil {
		t.Fatalf("insert request: %v", err)
	}
	return id
}

func mustInsertAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestID, txHash string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO tx_attempts (request_id, signer_address, nonce, tx_hash, gas_price, status)
		VALUES ($1::uuid, $2, 0, $3, 1000000000, 'submitted')
		RETURNING id::text
	`, requestID, "0x"+strings.Repeat("bb", 20), txHash).Scan(&id)
	if err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	return id
}

func newWatcher(chain relay.ReceiptChain, pool *pgxpool.Pool) *relay.Watcher {
	return relay.NewWatcher(pool, chain, time.Second, 50, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestWatcher_Confirmed(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	appID := mustInsertApp(t, ctx, pool)
	requestID := mustInsertRequest(t, ctx, pool, appID,
		"0x1111111111111111111111111111111111111111",
		[]byte{0xa9, 0x05, 0x9c, 0xbb}, "0")
	txHash := "0x" + strings.Repeat("ab", 32)
	attemptID := mustInsertAttempt(t, ctx, pool, requestID, txHash)

	chain := &stubReceiptChain{
		receipts: map[string]*types.Receipt{
			txHash: {
				Status:            types.ReceiptStatusSuccessful,
				BlockNumber:       big.NewInt(123),
				GasUsed:           21000,
				EffectiveGasPrice: big.NewInt(1_000_000_000),
				Logs:              []*types.Log{},
			},
		},
	}

	if _, err := newWatcher(chain, pool).Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var aStatus, rStatus, egp string
	var blockNumber, gasUsed int64
	if err := pool.QueryRow(ctx, `
		SELECT a.status, a.block_number, a.gas_used, a.effective_gas_price::text,
		       (SELECT status FROM relay_requests WHERE id = a.request_id)
		FROM tx_attempts a WHERE id = $1::uuid
	`, attemptID).Scan(&aStatus, &blockNumber, &gasUsed, &egp, &rStatus); err != nil {
		t.Fatal(err)
	}

	if aStatus != "confirmed" {
		t.Fatalf("attempt status = %q, want confirmed", aStatus)
	}
	if rStatus != "confirmed" {
		t.Fatalf("request status = %q, want confirmed", rStatus)
	}
	if blockNumber != 123 {
		t.Fatalf("block_number = %d, want 123", blockNumber)
	}
	if gasUsed != 21000 {
		t.Fatalf("gas_used = %d, want 21000", gasUsed)
	}
	if egp != "1000000000" {
		t.Fatalf("effective_gas_price = %q, want 1000000000", egp)
	}
}

func TestWatcher_Reverted_WithReason(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	appID := mustInsertApp(t, ctx, pool)
	requestID := mustInsertRequest(t, ctx, pool, appID,
		"0x2222222222222222222222222222222222222222",
		[]byte{0xde, 0xad, 0xbe, 0xef}, "0")
	txHash := "0x" + strings.Repeat("cd", 32)
	attemptID := mustInsertAttempt(t, ctx, pool, requestID, txHash)

	chain := &stubReceiptChain{
		receipts: map[string]*types.Receipt{
			txHash: {
				Status:            types.ReceiptStatusFailed,
				BlockNumber:       big.NewInt(456),
				GasUsed:           50000,
				EffectiveGasPrice: big.NewInt(2_000_000_000),
				Logs:              []*types.Log{},
			},
		},
		callErr: "execution reverted: ERC20: insufficient allowance",
	}

	if _, err := newWatcher(chain, pool).Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var aStatus, rStatus string
	var revertReason *string
	if err := pool.QueryRow(ctx, `
		SELECT a.status, a.revert_reason,
		       (SELECT status FROM relay_requests WHERE id = a.request_id)
		FROM tx_attempts a WHERE id = $1::uuid
	`, attemptID).Scan(&aStatus, &revertReason, &rStatus); err != nil {
		t.Fatal(err)
	}
	if aStatus != "reverted" {
		t.Fatalf("attempt status = %q, want reverted", aStatus)
	}
	if rStatus != "reverted" {
		t.Fatalf("request status = %q, want reverted", rStatus)
	}
	if revertReason == nil || *revertReason != "ERC20: insufficient allowance" {
		t.Fatalf("revert_reason = %v, want 'ERC20: insufficient allowance'", revertReason)
	}
	if chain.callHits != 1 {
		t.Fatalf("CallContract called %d times, want 1", chain.callHits)
	}
}

func TestWatcher_NotYetMined_NoChange(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	appID := mustInsertApp(t, ctx, pool)
	requestID := mustInsertRequest(t, ctx, pool, appID,
		"0x3333333333333333333333333333333333333333", []byte{}, "0")
	txHash := "0x" + strings.Repeat("ef", 32)
	attemptID := mustInsertAttempt(t, ctx, pool, requestID, txHash)

	chain := &stubReceiptChain{
		notFound: map[string]bool{txHash: true},
	}

	if _, err := newWatcher(chain, pool).Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var aStatus, rStatus string
	if err := pool.QueryRow(ctx, `
		SELECT a.status, (SELECT status FROM relay_requests WHERE id = a.request_id)
		FROM tx_attempts a WHERE id = $1::uuid
	`, attemptID).Scan(&aStatus, &rStatus); err != nil {
		t.Fatal(err)
	}
	if aStatus != "submitted" {
		t.Fatalf("attempt status = %q, want submitted (unchanged)", aStatus)
	}
	if rStatus != "submitted" {
		t.Fatalf("request status = %q, want submitted (unchanged)", rStatus)
	}
}
