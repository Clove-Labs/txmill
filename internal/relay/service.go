package relay

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clove-labs/txmill/internal/signer"
)

var (
	ErrChainIDMismatch = errors.New("chain_id does not match server")
	ErrPastDeadline    = errors.New("deadline is in the past")
)

const (
	gasLimitBufferNumerator   = 12
	gasLimitBufferDenominator = 10
)

type Chain interface {
	ChainID() *big.Int
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)
	SuggestGasPrice(ctx context.Context) (*big.Int, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
}

type SignerPool interface {
	Load(ctx context.Context, appID string) error
	Checkout(ctx context.Context, appID string) (*signer.Signer, error)
	Release(s *signer.Signer)
}

type WebhookEnqueuer interface {
	Enqueue(ctx context.Context, requestID string) error
}

type Service struct {
	pool     *pgxpool.Pool
	chain    Chain
	pools    SignerPool
	signer   types.Signer
	webhooks WebhookEnqueuer
}

func NewService(pool *pgxpool.Pool, ch Chain, pools SignerPool) *Service {
	return &Service{
		pool:   pool,
		chain:  ch,
		pools:  pools,
		signer: types.NewEIP155Signer(ch.ChainID()),
	}
}

// SetWebhooks wires in the webhook enqueuer post-construction. Optional —
// when nil, status changes won't fan out to webhooks.
func (s *Service) SetWebhooks(w WebhookEnqueuer) {
	s.webhooks = w
}

type SubmitInput struct {
	AppID            string
	ChainID          uint64
	To               common.Address
	Data             []byte
	Value            *big.Int
	GasLimit         uint64
	Deadline         int64
	CallbackURL      string
	CallbackMetadata string
}

type SubmitResult struct {
	RequestID string
	TxHash    common.Hash
	Signer    common.Address
}

func (s *Service) Submit(ctx context.Context, in SubmitInput) (*SubmitResult, error) {
	if in.ChainID != s.chain.ChainID().Uint64() {
		return nil, ErrChainIDMismatch
	}
	if in.Deadline > 0 && time.Now().Unix() > in.Deadline {
		return nil, ErrPastDeadline
	}
	if in.Value == nil {
		in.Value = new(big.Int)
	}

	requestID, err := s.insertRequest(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("insert request: %w", err)
	}

	if err := s.pools.Load(ctx, in.AppID); err != nil {
		_ = s.markRequestStatus(ctx, requestID, "rejected")
		return nil, fmt.Errorf("load signer pool: %w", err)
	}
	sig, err := s.pools.Checkout(ctx, in.AppID)
	if err != nil {
		_ = s.markRequestStatus(ctx, requestID, "rejected")
		return nil, fmt.Errorf("checkout: %w", err)
	}
	defer s.pools.Release(sig)

	gasLimit := in.GasLimit
	if gasLimit == 0 {
		est, err := s.chain.EstimateGas(ctx, ethereum.CallMsg{
			From:  sig.Address,
			To:    &in.To,
			Value: in.Value,
			Data:  in.Data,
		})
		if err != nil {
			_ = s.markRequestStatus(ctx, requestID, "rejected")
			return nil, fmt.Errorf("estimate gas: %w", err)
		}
		gasLimit = est * gasLimitBufferNumerator / gasLimitBufferDenominator
	}

	gasPrice, err := s.chain.SuggestGasPrice(ctx)
	if err != nil {
		_ = s.markRequestStatus(ctx, requestID, "rejected")
		return nil, fmt.Errorf("suggest gas price: %w", err)
	}

	nonce := sig.UseNonce()
	tx := types.NewTransaction(nonce, in.To, in.Value, gasLimit, gasPrice, in.Data)
	signed, err := types.SignTx(tx, s.signer, sig.Key)
	if err != nil {
		sig.RewindNonce()
		return nil, fmt.Errorf("sign: %w", err)
	}

	attemptID, err := s.insertAttempt(ctx, requestID, sig.Address, nonce, signed.Hash(), gasPrice)
	if err != nil {
		sig.RewindNonce()
		return nil, fmt.Errorf("insert attempt: %w", err)
	}

	if err := s.chain.SendTransaction(ctx, signed); err != nil {
		sig.RewindNonce()
		_ = s.markAttemptStatus(ctx, attemptID, "failed")
		return nil, fmt.Errorf("submit: %w", err)
	}

	if err := s.markSubmitted(ctx, requestID, attemptID); err != nil {
		return nil, fmt.Errorf("mark submitted: %w", err)
	}
	s.fireWebhook(ctx, requestID)

	return &SubmitResult{
		RequestID: requestID,
		TxHash:    signed.Hash(),
		Signer:    sig.Address,
	}, nil
}

func (s *Service) insertRequest(ctx context.Context, in SubmitInput) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO relay_requests
		    (app_id, chain_id, to_address, call_data, value, gas_limit, deadline, callback_url, callback_metadata)
		VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0), NULLIF($8, ''), NULLIF($9, ''))
		RETURNING id::text
	`,
		in.AppID,
		in.ChainID,
		strings.ToLower(in.To.Hex()),
		in.Data,
		in.Value.String(),
		int64(in.GasLimit),
		in.Deadline,
		in.CallbackURL,
		in.CallbackMetadata,
	).Scan(&id)
	return id, err
}

func (s *Service) insertAttempt(
	ctx context.Context,
	requestID string,
	signerAddr common.Address,
	nonce uint64,
	hash common.Hash,
	gasPrice *big.Int,
) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tx_attempts (request_id, signer_address, nonce, tx_hash, gas_price)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING id::text
	`,
		requestID,
		strings.ToLower(signerAddr.Hex()),
		int64(nonce),
		strings.ToLower(hash.Hex()),
		gasPrice.String(),
	).Scan(&id)
	return id, err
}

func (s *Service) markRequestStatus(ctx context.Context, requestID, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE relay_requests SET status = $1 WHERE id = $2::uuid`, status, requestID)
	if err == nil && (status == "rejected" || status == "failed") {
		s.fireWebhook(ctx, requestID)
	}
	return err
}

func (s *Service) markAttemptStatus(ctx context.Context, attemptID, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tx_attempts SET status = $1 WHERE id = $2::uuid`, status, attemptID)
	return err
}

func (s *Service) fireWebhook(ctx context.Context, requestID string) {
	if s.webhooks == nil {
		return
	}
	if err := s.webhooks.Enqueue(ctx, requestID); err != nil {
		// Webhook enqueue failure shouldn't fail the relay path; log and move on.
		// (Caller can still poll GET /v1/relay/:id.)
		_ = err
	}
}

func (s *Service) markSubmitted(ctx context.Context, requestID, attemptID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE tx_attempts SET status = 'submitted' WHERE id = $1::uuid`, attemptID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE relay_requests SET status = 'submitted' WHERE id = $1::uuid`, requestID,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
