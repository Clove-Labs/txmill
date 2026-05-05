package treasury

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sweepGasLimit = 21000

var ErrAppNotFound = errors.New("app not found")

type Chain interface {
	ChainID() *big.Int
	BalanceAt(ctx context.Context, addr common.Address) (*big.Int, error)
	PendingNonceAt(ctx context.Context, addr common.Address) (uint64, error)
	SuggestGasPrice(ctx context.Context) (*big.Int, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
}

type Keystore interface {
	Load(addr common.Address) (*ecdsa.PrivateKey, error)
}

type Service struct {
	pool     *pgxpool.Pool
	chain    Chain
	keystore Keystore
	signer   types.Signer
}

func NewService(pool *pgxpool.Pool, chain Chain, keystore Keystore) *Service {
	return &Service{
		pool:     pool,
		chain:    chain,
		keystore: keystore,
		signer:   types.NewEIP155Signer(chain.ChainID()),
	}
}

type SweepResult struct {
	TxHash   common.Hash
	From     common.Address
	To       common.Address
	ValueWei *big.Int
}

// Sweep sends `value` wei from the app's treasury to `to`. If `value` is nil,
// the entire treasury balance minus the gas cost is sent (leaves treasury at
// ~0 wei).
func (s *Service) Sweep(
	ctx context.Context,
	appID string,
	to common.Address,
	value *big.Int,
) (*SweepResult, error) {
	var treasuryHex string
	err := s.pool.QueryRow(ctx,
		`SELECT treasury_address FROM apps WHERE id = $1::uuid`, appID,
	).Scan(&treasuryHex)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAppNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("treasury lookup: %w", err)
	}
	from := common.HexToAddress(treasuryHex)

	priv, err := s.keystore.Load(from)
	if err != nil {
		return nil, fmt.Errorf("load treasury key: %w", err)
	}

	nonce, err := s.chain.PendingNonceAt(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("treasury nonce: %w", err)
	}
	gasPrice, err := s.chain.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("gas price: %w", err)
	}

	send := value
	if send == nil {
		bal, err := s.chain.BalanceAt(ctx, from)
		if err != nil {
			return nil, fmt.Errorf("treasury balance: %w", err)
		}
		gasCost := new(big.Int).Mul(gasPrice, big.NewInt(sweepGasLimit))
		send = new(big.Int).Sub(bal, gasCost)
		if send.Sign() <= 0 {
			return nil, fmt.Errorf("treasury balance %s wei does not cover gas %s wei",
				bal.String(), gasCost.String())
		}
	}

	tx := types.NewTransaction(nonce, to, send, sweepGasLimit, gasPrice, nil)
	signed, err := types.SignTx(tx, s.signer, priv)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	if err := s.chain.SendTransaction(ctx, signed); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	return &SweepResult{
		TxHash:   signed.Hash(),
		From:     from,
		To:       to,
		ValueWei: send,
	}, nil
}
