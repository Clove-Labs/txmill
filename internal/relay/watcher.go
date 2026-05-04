package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReceiptChain interface {
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

type Watcher struct {
	pool      *pgxpool.Pool
	chain     ReceiptChain
	interval  time.Duration
	batchSize int
	logger    *slog.Logger
}

func NewWatcher(pool *pgxpool.Pool, chain ReceiptChain, interval time.Duration, batchSize int, logger *slog.Logger) *Watcher {
	return &Watcher{
		pool:      pool,
		chain:     chain,
		interval:  interval,
		batchSize: batchSize,
		logger:    logger,
	}
}

func (w *Watcher) Run(ctx context.Context) {
	w.logger.Info("receipt watcher starting", "interval", w.interval, "batch_size", w.batchSize)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("receipt watcher stopped")
			return
		case <-t.C:
			n, err := w.tick(ctx)
			if err != nil {
				w.logger.Error("watcher tick failed", "err", err)
			} else if n > 0 {
				w.logger.Debug("watcher tick", "processed", n)
			}
		}
	}
}

type pendingAttempt struct {
	attemptID    string
	requestID    string
	signerHex    string
	txHash       string
	requestTo    string
	requestData  []byte
	requestValue string
}

// Tick processes one batch of pending attempts. Exposed for tests and ops tooling.
func (w *Watcher) Tick(ctx context.Context) (int, error) {
	return w.tick(ctx)
}

func (w *Watcher) tick(ctx context.Context) (int, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT
		  a.id::text, a.request_id::text, a.signer_address, a.tx_hash,
		  r.to_address, r.call_data, r.value::text
		FROM tx_attempts a
		JOIN relay_requests r ON r.id = a.request_id
		WHERE a.status IN ('submitting', 'submitted')
		ORDER BY a.submitted_at
		LIMIT $1
	`, w.batchSize)
	if err != nil {
		return 0, err
	}
	pending := make([]pendingAttempt, 0)
	for rows.Next() {
		var p pendingAttempt
		if err := rows.Scan(&p.attemptID, &p.requestID, &p.signerHex, &p.txHash, &p.requestTo, &p.requestData, &p.requestValue); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	processed := 0
	for _, p := range pending {
		if err := w.process(ctx, p); err != nil {
			w.logger.Error("process attempt", "tx_hash", p.txHash, "err", err)
			continue
		}
		processed++
	}
	return processed, nil
}

func (w *Watcher) process(ctx context.Context, p pendingAttempt) error {
	receipt, err := w.chain.TransactionReceipt(ctx, common.HexToHash(p.txHash))
	if errors.Is(err, ethereum.NotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	logsJSON, err := json.Marshal(receipt.Logs)
	if err != nil {
		return err
	}

	attemptStatus := "confirmed"
	requestStatus := "confirmed"
	revertReason := ""
	if receipt.Status == types.ReceiptStatusFailed {
		attemptStatus = "reverted"
		requestStatus = "reverted"
		revertReason = w.fetchRevertReason(ctx, p, receipt.BlockNumber)
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE tx_attempts
		SET status = $1,
		    block_number = $2,
		    gas_used = $3,
		    effective_gas_price = $4,
		    logs = $5,
		    revert_reason = NULLIF($6, '')
		WHERE id = $7::uuid
	`,
		attemptStatus,
		receipt.BlockNumber.Int64(),
		int64(receipt.GasUsed),
		effectiveGasPriceString(receipt),
		logsJSON,
		revertReason,
		p.attemptID,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE relay_requests SET status = $1 WHERE id = $2::uuid
	`, requestStatus, p.requestID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (w *Watcher) fetchRevertReason(ctx context.Context, p pendingAttempt, blockNumber *big.Int) string {
	value, _ := new(big.Int).SetString(p.requestValue, 10)
	to := common.HexToAddress(p.requestTo)
	from := common.HexToAddress(p.signerHex)
	msg := ethereum.CallMsg{
		From:  from,
		To:    &to,
		Value: value,
		Data:  p.requestData,
	}
	// Replay against state just before the reverted block.
	atBlock := new(big.Int).Sub(blockNumber, big.NewInt(1))
	if _, err := w.chain.CallContract(ctx, msg, atBlock); err != nil {
		return cleanRevertString(err.Error())
	}
	return ""
}

func cleanRevertString(s string) string {
	const prefix = "execution reverted: "
	if i := strings.Index(s, prefix); i >= 0 {
		return strings.TrimSpace(s[i+len(prefix):])
	}
	return strings.TrimSpace(s)
}

func effectiveGasPriceString(r *types.Receipt) string {
	if r.EffectiveGasPrice == nil {
		return "0"
	}
	return r.EffectiveGasPrice.String()
}
