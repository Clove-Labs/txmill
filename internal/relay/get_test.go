package relay_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/clove-labs/txmill/internal/relay"
	"github.com/clove-labs/txmill/internal/store/dbtest"
)

type stubChainID struct{}

func (stubChainID) ChainID() *big.Int { return big.NewInt(146) }
func (stubChainID) EstimateGas(context.Context, ethereum.CallMsg) (uint64, error) {
	panic("not used")
}
func (stubChainID) SuggestGasPrice(context.Context) (*big.Int, error) { panic("not used") }
func (stubChainID) SendTransaction(context.Context, *types.Transaction) error {
	panic("not used")
}

func TestGet_HappyPath(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	appID := mustInsertApp(t, ctx, pool)
	requestID := mustInsertRequest(t, ctx, pool, appID,
		"0x1234567890123456789012345678901234567890",
		[]byte{0xde, 0xad}, "0")
	txHash := "0x" + strings.Repeat("11", 32)
	_ = mustInsertAttempt(t, ctx, pool, requestID, txHash)

	// Set the attempt to a confirmed state with receipt fields populated.
	if _, err := pool.Exec(ctx, `
		UPDATE tx_attempts
		SET status='confirmed', block_number=42, gas_used=21000,
		    effective_gas_price=55000000000,
		    logs='[]'::jsonb
		WHERE tx_hash=$1
	`, txHash); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE relay_requests SET status='confirmed' WHERE id=$1::uuid`, requestID); err != nil {
		t.Fatal(err)
	}

	svc := relay.NewService(pool, stubChainID{}, nil)
	got, err := svc.Get(ctx, requestID, appID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "confirmed" {
		t.Fatalf("status = %q, want confirmed", got.Status)
	}
	if got.TxHash != txHash {
		t.Fatalf("tx_hash = %q, want %q", got.TxHash, txHash)
	}
	if got.BlockNumber == nil || *got.BlockNumber != 42 {
		t.Fatalf("block_number = %v, want 42", got.BlockNumber)
	}
	if got.GasUsed == nil || *got.GasUsed != 21000 {
		t.Fatalf("gas_used = %v, want 21000", got.GasUsed)
	}
	if got.EffectiveGasPrice != "55000000000" {
		t.Fatalf("effective_gas_price = %q", got.EffectiveGasPrice)
	}
	if string(got.Logs) != "[]" {
		t.Fatalf("logs = %q, want []", string(got.Logs))
	}
	// JSON shape sanity: round-trip through encoding/json.
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"status":"confirmed"`) {
		t.Fatalf("marshalled payload missing status: %s", body)
	}
	if !strings.Contains(string(body), `"updated_at":`) {
		t.Fatalf("marshalled payload missing updated_at: %s", body)
	}
}

func TestGet_OtherAppReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	appID := mustInsertApp(t, ctx, pool)
	requestID := mustInsertRequest(t, ctx, pool, appID,
		"0x1234567890123456789012345678901234567890", []byte{}, "0")
	_ = mustInsertAttempt(t, ctx, pool, requestID, "0x"+strings.Repeat("22", 32))

	svc := relay.NewService(pool, stubChainID{}, nil)

	otherApp := "00000000-0000-0000-0000-000000000000"
	if _, err := svc.Get(ctx, requestID, otherApp); !errors.Is(err, relay.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_PendingNoAttempt(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	appID := mustInsertApp(t, ctx, pool)
	// Insert a request with status='pending' (the default after #9 validation passes
	// but before any attempt is recorded). Force status back to 'pending' since
	// mustInsertRequest uses 'submitted'.
	requestID := mustInsertRequest(t, ctx, pool, appID,
		"0x1234567890123456789012345678901234567890", []byte{}, "0")
	if _, err := pool.Exec(ctx, `UPDATE relay_requests SET status='pending' WHERE id=$1::uuid`, requestID); err != nil {
		t.Fatal(err)
	}

	svc := relay.NewService(pool, stubChainID{}, nil)
	got, err := svc.Get(ctx, requestID, appID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" {
		t.Fatalf("status = %q, want pending", got.Status)
	}
	if got.TxHash != "" {
		t.Fatalf("tx_hash = %q, want empty", got.TxHash)
	}
	if got.BlockNumber != nil {
		t.Fatalf("block_number = %v, want nil", got.BlockNumber)
	}
}
