package treasury_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clove-labs/txmill/internal/store/dbtest"
	"github.com/clove-labs/txmill/internal/treasury"
)

type stubChain struct {
	chainID  *big.Int
	balance  *big.Int
	nonce    uint64
	gasPrice *big.Int
	sentTxs  []*types.Transaction
	sendErr  error
	sent     atomic.Int32
}

func (s *stubChain) ChainID() *big.Int { return s.chainID }
func (s *stubChain) BalanceAt(_ context.Context, _ common.Address) (*big.Int, error) {
	return new(big.Int).Set(s.balance), nil
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
	s.sent.Add(1)
	s.sentTxs = append(s.sentTxs, tx)
	return nil
}

type stubKeystore struct{ key *ecdsa.PrivateKey }

func (s *stubKeystore) Load(_ common.Address) (*ecdsa.PrivateKey, error) {
	if s.key == nil {
		return nil, errors.New("no key")
	}
	return s.key, nil
}

func mustInsertAppForSweep(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, treasuryAddr common.Address,
) string {
	t.Helper()
	hash := sha256.Sum256([]byte("sweep-token-" + t.Name()))
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO apps (name, treasury_address, bearer_token_hash, pool_size)
		VALUES ($1, $2, $3, 1)
		RETURNING id::text
	`, "sweep-test", strings.ToLower(treasuryAddr.Hex()), hash[:]).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSweep_FullBalanceMinusGas(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	appID := mustInsertAppForSweep(t, ctx, pool, addr)

	chain := &stubChain{
		chainID:  big.NewInt(146),
		balance:  big.NewInt(10_000_000),
		nonce:    3,
		gasPrice: big.NewInt(100), // gas cost = 100 * 21000 = 2_100_000
	}
	svc := treasury.NewService(pool, chain, &stubKeystore{key: key})

	to := common.HexToAddress("0x" + strings.Repeat("aa", 20))
	res, err := svc.Sweep(ctx, appID, to, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := big.NewInt(10_000_000 - 2_100_000)
	if res.ValueWei.Cmp(want) != 0 {
		t.Fatalf("value = %s, want %s", res.ValueWei, want)
	}
	if chain.sent.Load() != 1 {
		t.Fatalf("sent %d txs, want 1", chain.sent.Load())
	}
	tx := chain.sentTxs[0]
	if tx.Nonce() != 3 {
		t.Fatalf("nonce = %d, want 3", tx.Nonce())
	}
	if *tx.To() != to {
		t.Fatalf("to = %s, want %s", tx.To().Hex(), to.Hex())
	}
}

func TestSweep_ExplicitValue(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	appID := mustInsertAppForSweep(t, ctx, pool, addr)

	chain := &stubChain{
		chainID:  big.NewInt(146),
		balance:  big.NewInt(99_999_999), // ignored when value is explicit
		nonce:    0,
		gasPrice: big.NewInt(1),
	}
	svc := treasury.NewService(pool, chain, &stubKeystore{key: key})

	to := common.HexToAddress("0x" + strings.Repeat("bb", 20))
	res, err := svc.Sweep(ctx, appID, to, big.NewInt(123))
	if err != nil {
		t.Fatal(err)
	}
	if res.ValueWei.Cmp(big.NewInt(123)) != 0 {
		t.Fatalf("value = %s, want 123", res.ValueWei)
	}
}

func TestSweep_BalanceUnderGas_Fails(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	appID := mustInsertAppForSweep(t, ctx, pool, addr)

	chain := &stubChain{
		chainID:  big.NewInt(146),
		balance:  big.NewInt(100), // way under gas cost
		nonce:    0,
		gasPrice: big.NewInt(1_000_000_000),
	}
	svc := treasury.NewService(pool, chain, &stubKeystore{key: key})

	if _, err := svc.Sweep(ctx, appID, common.Address{1}, nil); err == nil {
		t.Fatal("expected error when balance under gas cost")
	}
	if chain.sent.Load() != 0 {
		t.Fatal("tx was sent despite insufficient balance")
	}
}

func TestSweep_UnknownApp(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	key, _ := crypto.GenerateKey()
	chain := &stubChain{chainID: big.NewInt(146), balance: big.NewInt(1), nonce: 0, gasPrice: big.NewInt(1)}
	svc := treasury.NewService(pool, chain, &stubKeystore{key: key})

	_, err := svc.Sweep(ctx, "00000000-0000-0000-0000-000000000000", common.Address{1}, big.NewInt(1))
	if !errors.Is(err, treasury.ErrAppNotFound) {
		t.Fatalf("err = %v, want ErrAppNotFound", err)
	}
}
