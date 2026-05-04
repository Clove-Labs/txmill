package gas_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clove-labs/txmill/internal/gas"
	"github.com/clove-labs/txmill/internal/store/dbtest"
)

type stubChain struct {
	chainID  *big.Int
	balances map[common.Address]*big.Int
	nonce    uint64
	gasPrice *big.Int
	sentTxs  []*types.Transaction
	sendErr  error
	mu       atomic.Int64
}

func (s *stubChain) ChainID() *big.Int { return s.chainID }
func (s *stubChain) BalanceAt(_ context.Context, addr common.Address) (*big.Int, error) {
	if b, ok := s.balances[addr]; ok {
		return new(big.Int).Set(b), nil
	}
	return new(big.Int), nil
}
func (s *stubChain) PendingNonceAt(_ context.Context, _ common.Address) (uint64, error) {
	return s.nonce, nil
}
func (s *stubChain) SuggestGasPrice(_ context.Context) (*big.Int, error) {
	return new(big.Int).Set(s.gasPrice), nil
}
func (s *stubChain) SendTransaction(_ context.Context, tx *types.Transaction) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.mu.Add(1)
	s.sentTxs = append(s.sentTxs, tx)
	return nil
}

type stubKeystore struct {
	key *ecdsa.PrivateKey
}

func (s *stubKeystore) Load(_ common.Address) (*ecdsa.PrivateKey, error) {
	if s.key == nil {
		return nil, errors.New("no key")
	}
	return s.key, nil
}

func mustInsertAppForGas(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	treasury common.Address, signerMin, refill, treasuryMin string,
) string {
	t.Helper()
	hash := sha256.Sum256([]byte("gas-token-" + t.Name()))
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO apps (name, treasury_address, bearer_token_hash, pool_size,
		                  signer_min_balance, signer_refill_amount, treasury_min_balance)
		VALUES ($1, $2, $3, 1, $4, $5, $6)
		RETURNING id::text
	`, "gas-test", strings.ToLower(treasury.Hex()), hash[:], signerMin, refill, treasuryMin).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustInsertSigner(t *testing.T, ctx context.Context, pool *pgxpool.Pool, appID string, signer common.Address) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO signers (address, app_id) VALUES ($1, $2::uuid)
	`, strings.ToLower(signer.Hex()), appID); err != nil {
		t.Fatal(err)
	}
}

func TestTick_NoOptedInApps(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	chain := &stubChain{chainID: big.NewInt(146), gasPrice: big.NewInt(1e9)}
	w := gas.NewWorker(pool, chain, &stubKeystore{}, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if chain.mu.Load() != 0 {
		t.Fatalf("sent %d txs, want 0", chain.mu.Load())
	}
}

func TestTick_RefillsBelowThresholdSigner(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	treasuryKey, _ := crypto.GenerateKey()
	treasuryAddr := crypto.PubkeyToAddress(treasuryKey.PublicKey)

	signerLow := common.HexToAddress("0x" + strings.Repeat("01", 20))
	signerHigh := common.HexToAddress("0x" + strings.Repeat("02", 20))

	appID := mustInsertAppForGas(t, ctx, pool, treasuryAddr,
		"100", "1000", "0")
	mustInsertSigner(t, ctx, pool, appID, signerLow)
	mustInsertSigner(t, ctx, pool, appID, signerHigh)

	chain := &stubChain{
		chainID:  big.NewInt(146),
		gasPrice: big.NewInt(1e9),
		nonce:    7,
		balances: map[common.Address]*big.Int{
			treasuryAddr: big.NewInt(10_000),
			signerLow:    big.NewInt(50),  // below threshold (100)
			signerHigh:   big.NewInt(500), // above threshold
		},
	}
	w := gas.NewWorker(pool, chain, &stubKeystore{key: treasuryKey}, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if chain.mu.Load() != 1 {
		t.Fatalf("sent %d txs, want 1", chain.mu.Load())
	}
	tx := chain.sentTxs[0]
	if tx.Nonce() != 7 {
		t.Fatalf("nonce = %d, want 7", tx.Nonce())
	}
	if *tx.To() != signerLow {
		t.Fatalf("to = %s, want %s", tx.To().Hex(), signerLow.Hex())
	}
	if tx.Value().Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("value = %s, want 1000", tx.Value())
	}

	var rowCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM gas_attempts WHERE signer_address=$1`,
		strings.ToLower(signerLow.Hex())).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("gas_attempts rows = %d, want 1", rowCount)
	}
}

func TestTick_SuppressesRecentDuplicate(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	treasuryKey, _ := crypto.GenerateKey()
	treasuryAddr := crypto.PubkeyToAddress(treasuryKey.PublicKey)
	signer := common.HexToAddress("0x" + strings.Repeat("03", 20))

	appID := mustInsertAppForGas(t, ctx, pool, treasuryAddr, "100", "1000", "0")
	mustInsertSigner(t, ctx, pool, appID, signer)

	// Pre-existing recent top-up — worker should skip.
	if _, err := pool.Exec(ctx, `
		INSERT INTO gas_attempts (app_id, signer_address, nonce, tx_hash, amount, gas_price, status)
		VALUES ($1::uuid, $2, 0, $3, 1000, 1000000000, 'submitted')
	`, appID, strings.ToLower(signer.Hex()),
		"0x"+strings.Repeat("aa", 32)); err != nil {
		t.Fatal(err)
	}

	chain := &stubChain{
		chainID:  big.NewInt(146),
		gasPrice: big.NewInt(1e9),
		nonce:    1,
		balances: map[common.Address]*big.Int{
			treasuryAddr: big.NewInt(10_000),
			signer:       big.NewInt(50),
		},
	}
	w := gas.NewWorker(pool, chain, &stubKeystore{key: treasuryKey}, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if chain.mu.Load() != 0 {
		t.Fatalf("sent %d txs, want 0 (suppressed)", chain.mu.Load())
	}
}

func TestTick_TreasuryLowAlertsButStillRefills(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	treasuryKey, _ := crypto.GenerateKey()
	treasuryAddr := crypto.PubkeyToAddress(treasuryKey.PublicKey)
	signer := common.HexToAddress("0x" + strings.Repeat("04", 20))

	appID := mustInsertAppForGas(t, ctx, pool, treasuryAddr, "100", "1000", "5000")
	mustInsertSigner(t, ctx, pool, appID, signer)

	chain := &stubChain{
		chainID:  big.NewInt(146),
		gasPrice: big.NewInt(1e9),
		nonce:    0,
		balances: map[common.Address]*big.Int{
			treasuryAddr: big.NewInt(2000), // below treasury_min_balance (5000) — alert
			signer:       big.NewInt(50),   // below signer_min_balance (100) — refill anyway
		},
	}
	w := gas.NewWorker(pool, chain, &stubKeystore{key: treasuryKey}, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if chain.mu.Load() != 1 {
		t.Fatalf("sent %d txs, want 1", chain.mu.Load())
	}
}
