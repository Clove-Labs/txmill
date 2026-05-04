package gas

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clove-labs/txmill/internal/alert"
)

// recentTopupWindow defines how long a previous top-up suppresses another
// for the same signer. Long enough to ride out mempool latency on Sonic.
const recentTopupWindow = 60 * time.Second

const topupGasLimit = 21000

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

type Worker struct {
	pool     *pgxpool.Pool
	chain    Chain
	keystore Keystore
	notifier alert.Notifier
	interval time.Duration
	logger   *slog.Logger
	signer   types.Signer
}

func NewWorker(pool *pgxpool.Pool, chain Chain, keystore Keystore, notifier alert.Notifier, interval time.Duration, logger *slog.Logger) *Worker {
	if notifier == nil {
		notifier = alert.Discard{}
	}
	return &Worker{
		pool:     pool,
		chain:    chain,
		keystore: keystore,
		notifier: notifier,
		interval: interval,
		logger:   logger,
		signer:   types.NewEIP155Signer(chain.ChainID()),
	}
}

func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("gas worker starting", "interval", w.interval)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("gas worker stopped")
			return
		case <-t.C:
			if err := w.tick(ctx); err != nil {
				w.logger.Error("gas tick failed", "err", err)
			}
		}
	}
}

func (w *Worker) Tick(ctx context.Context) error {
	return w.tick(ctx)
}

type appPolicy struct {
	id                 string
	treasuryAddress    common.Address
	signerMinBalance   *big.Int
	signerRefillAmount *big.Int
	treasuryMinBalance *big.Int
}

func (w *Worker) tick(ctx context.Context) error {
	rows, err := w.pool.Query(ctx, `
		SELECT id::text, treasury_address,
		       signer_min_balance::text, signer_refill_amount::text, treasury_min_balance::text
		FROM apps
		WHERE signer_min_balance > 0 AND signer_refill_amount > 0
	`)
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	apps := make([]appPolicy, 0)
	for rows.Next() {
		var p appPolicy
		var treasuryHex, sMin, sRefill, tMin string
		if err := rows.Scan(&p.id, &treasuryHex, &sMin, &sRefill, &tMin); err != nil {
			rows.Close()
			return err
		}
		p.treasuryAddress = common.HexToAddress(treasuryHex)
		p.signerMinBalance, _ = new(big.Int).SetString(sMin, 10)
		p.signerRefillAmount, _ = new(big.Int).SetString(sRefill, 10)
		p.treasuryMinBalance, _ = new(big.Int).SetString(tMin, 10)
		apps = append(apps, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range apps {
		if err := w.processApp(ctx, p); err != nil {
			w.logger.Error("process app", "app_id", p.id, "err", err)
		}
	}
	return nil
}

func (w *Worker) processApp(ctx context.Context, p appPolicy) error {
	tBal, err := w.chain.BalanceAt(ctx, p.treasuryAddress)
	if err != nil {
		return fmt.Errorf("treasury balance: %w", err)
	}
	if p.treasuryMinBalance.Sign() > 0 && tBal.Cmp(p.treasuryMinBalance) < 0 {
		w.logger.Warn("treasury balance below threshold",
			"app_id", p.id,
			"treasury", p.treasuryAddress.Hex(),
			"balance", tBal.String(),
			"min", p.treasuryMinBalance.String(),
		)
		if err := w.notifier.Notify(ctx, alert.Alert{
			Key:     "treasury_low:" + p.id,
			Level:   alert.LevelWarn,
			Title:   "Treasury balance low",
			Message: fmt.Sprintf("balance %s wei (min %s wei)", tBal.String(), p.treasuryMinBalance.String()),
			Tags: map[string]string{
				"app_id":   p.id,
				"treasury": p.treasuryAddress.Hex(),
			},
		}); err != nil {
			w.logger.Error("alert dispatch", "key", "treasury_low", "err", err)
		}
	}

	signers, err := w.listSigners(ctx, p.id)
	if err != nil {
		return fmt.Errorf("list signers: %w", err)
	}
	if len(signers) == 0 {
		return nil
	}

	priv, err := w.keystore.Load(p.treasuryAddress)
	if err != nil {
		return fmt.Errorf("load treasury key: %w", err)
	}

	nonce, err := w.chain.PendingNonceAt(ctx, p.treasuryAddress)
	if err != nil {
		return fmt.Errorf("treasury nonce: %w", err)
	}
	gasPrice, err := w.chain.SuggestGasPrice(ctx)
	if err != nil {
		return fmt.Errorf("gas price: %w", err)
	}

	for _, s := range signers {
		recent, err := w.hasRecentTopup(ctx, s)
		if err != nil {
			w.logger.Error("recent top-up check", "signer", s.Hex(), "err", err)
			continue
		}
		if recent {
			continue
		}
		bal, err := w.chain.BalanceAt(ctx, s)
		if err != nil {
			w.logger.Error("signer balance", "signer", s.Hex(), "err", err)
			continue
		}
		if bal.Cmp(p.signerMinBalance) >= 0 {
			continue
		}
		if err := w.refill(ctx, p, priv, nonce, gasPrice, s); err != nil {
			w.logger.Error("refill failed", "signer", s.Hex(), "err", err)
			continue
		}
		nonce++
	}
	return nil
}

func (w *Worker) listSigners(ctx context.Context, appID string) ([]common.Address, error) {
	rows, err := w.pool.Query(ctx,
		`SELECT address FROM signers WHERE app_id = $1::uuid ORDER BY created_at, address`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]common.Address, 0)
	for rows.Next() {
		var hex string
		if err := rows.Scan(&hex); err != nil {
			return nil, err
		}
		out = append(out, common.HexToAddress(hex))
	}
	return out, rows.Err()
}

func (w *Worker) hasRecentTopup(ctx context.Context, signer common.Address) (bool, error) {
	var n int
	err := w.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM gas_attempts
		WHERE signer_address = $1
		  AND status IN ('submitted', 'confirmed')
		  AND submitted_at > NOW() - $2::interval
	`, strings.ToLower(signer.Hex()), fmt.Sprintf("%d milliseconds", recentTopupWindow.Milliseconds())).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (w *Worker) refill(
	ctx context.Context,
	p appPolicy,
	treasuryKey *ecdsa.PrivateKey,
	nonce uint64,
	gasPrice *big.Int,
	signer common.Address,
) error {
	tx := types.NewTransaction(nonce, signer, p.signerRefillAmount, topupGasLimit, gasPrice, nil)
	signed, err := types.SignTx(tx, w.signer, treasuryKey)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	if err := w.chain.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO gas_attempts
		    (app_id, signer_address, nonce, tx_hash, amount, gas_price, status)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, 'submitted')
	`,
		p.id,
		strings.ToLower(signer.Hex()),
		int64(nonce),
		strings.ToLower(signed.Hash().Hex()),
		p.signerRefillAmount.String(),
		gasPrice.String(),
	); err != nil {
		// Tx is already on chain; row insert failure is ugly but recoverable.
		w.logger.Warn("gas_attempts insert after send", "tx_hash", signed.Hash().Hex(), "err", err)
	}
	w.logger.Info("topped up signer",
		"app_id", p.id,
		"signer", signer.Hex(),
		"amount", p.signerRefillAmount.String(),
		"tx_hash", signed.Hash().Hex(),
	)
	return nil
}
