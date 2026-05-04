package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("relay request not found")

type Status struct {
	RequestID         string          `json:"request_id"`
	Status            string          `json:"status"`
	TxHash            string          `json:"tx_hash,omitempty"`
	Signer            string          `json:"signer,omitempty"`
	BlockNumber       *int64          `json:"block_number,omitempty"`
	GasUsed           *int64          `json:"gas_used,omitempty"`
	EffectiveGasPrice string          `json:"effective_gas_price,omitempty"`
	RevertReason      string          `json:"revert_reason,omitempty"`
	Logs              json.RawMessage `json:"logs,omitempty"`
	CallbackMetadata  string          `json:"callback_metadata,omitempty"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// GetRaw returns the JSON payload bytes for a request along with the
// effective callback URL (request override → app default) and the app's
// callback secret. URL or secret may be empty if either is unset.
func (s *Service) GetRaw(ctx context.Context, requestID string) (payload []byte, url string, secret string, err error) {
	var (
		out               Status
		txHash            *string
		signer            *string
		blockNumber       *int64
		gasUsed           *int64
		effectiveGasPrice *string
		revertReason      *string
		logsJSON          []byte
		callbackMetadata  *string
		effectiveURL      string
		effectiveSecret   string
	)
	err = s.pool.QueryRow(ctx, `
		SELECT
		    r.id::text,
		    r.status,
		    r.callback_metadata,
		    r.updated_at,
		    a.signer_address,
		    a.tx_hash,
		    a.block_number,
		    a.gas_used,
		    a.effective_gas_price::text,
		    a.revert_reason,
		    a.logs,
		    COALESCE(NULLIF(r.callback_url, ''), apps.default_callback_url, '') AS effective_url,
		    COALESCE(apps.default_callback_secret, '') AS effective_secret
		FROM relay_requests r
		JOIN apps ON apps.id = r.app_id
		LEFT JOIN LATERAL (
		    SELECT signer_address, tx_hash, block_number, gas_used,
		           effective_gas_price, revert_reason, logs
		    FROM tx_attempts
		    WHERE request_id = r.id
		    ORDER BY submitted_at DESC
		    LIMIT 1
		) a ON true
		WHERE r.id = $1::uuid
	`, requestID).Scan(
		&out.RequestID,
		&out.Status,
		&callbackMetadata,
		&out.UpdatedAt,
		&signer,
		&txHash,
		&blockNumber,
		&gasUsed,
		&effectiveGasPrice,
		&revertReason,
		&logsJSON,
		&effectiveURL,
		&effectiveSecret,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "", ErrNotFound
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("get raw: %w", err)
	}

	if txHash != nil {
		out.TxHash = *txHash
	}
	if signer != nil {
		out.Signer = *signer
	}
	out.BlockNumber = blockNumber
	out.GasUsed = gasUsed
	if effectiveGasPrice != nil {
		out.EffectiveGasPrice = *effectiveGasPrice
	}
	if revertReason != nil {
		out.RevertReason = *revertReason
	}
	if callbackMetadata != nil {
		out.CallbackMetadata = *callbackMetadata
	}
	if len(logsJSON) > 0 {
		out.Logs = json.RawMessage(logsJSON)
	}
	body, err := json.Marshal(&out)
	if err != nil {
		return nil, "", "", fmt.Errorf("get raw marshal: %w", err)
	}
	return body, effectiveURL, effectiveSecret, nil
}

func (s *Service) Get(ctx context.Context, requestID, appID string) (*Status, error) {
	var (
		out               Status
		txHash            *string
		signer            *string
		blockNumber       *int64
		gasUsed           *int64
		effectiveGasPrice *string
		revertReason      *string
		logsJSON          []byte
		callbackMetadata  *string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT
		    r.id::text,
		    r.status,
		    r.callback_metadata,
		    r.updated_at,
		    a.signer_address,
		    a.tx_hash,
		    a.block_number,
		    a.gas_used,
		    a.effective_gas_price::text,
		    a.revert_reason,
		    a.logs
		FROM relay_requests r
		LEFT JOIN LATERAL (
		    SELECT signer_address, tx_hash, block_number, gas_used,
		           effective_gas_price, revert_reason, logs
		    FROM tx_attempts
		    WHERE request_id = r.id
		    ORDER BY submitted_at DESC
		    LIMIT 1
		) a ON true
		WHERE r.id = $1::uuid AND r.app_id = $2::uuid
	`, requestID, appID).Scan(
		&out.RequestID,
		&out.Status,
		&callbackMetadata,
		&out.UpdatedAt,
		&signer,
		&txHash,
		&blockNumber,
		&gasUsed,
		&effectiveGasPrice,
		&revertReason,
		&logsJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get relay: %w", err)
	}

	if txHash != nil {
		out.TxHash = *txHash
	}
	if signer != nil {
		out.Signer = *signer
	}
	out.BlockNumber = blockNumber
	out.GasUsed = gasUsed
	if effectiveGasPrice != nil {
		out.EffectiveGasPrice = *effectiveGasPrice
	}
	if revertReason != nil {
		out.RevertReason = *revertReason
	}
	if callbackMetadata != nil {
		out.CallbackMetadata = *callbackMetadata
	}
	if len(logsJSON) > 0 {
		out.Logs = json.RawMessage(logsJSON)
	}
	return &out, nil
}
