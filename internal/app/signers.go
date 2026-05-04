package app

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type SignerInfo struct {
	Address    common.Address
	LastUsedAt *time.Time
}

type AppAccounts struct {
	Treasury common.Address
	Signers  []SignerInfo
}

func (s *Service) Accounts(ctx context.Context, appID string) (*AppAccounts, error) {
	out := &AppAccounts{}
	var treasuryHex string
	if err := s.pool.QueryRow(ctx,
		`SELECT treasury_address FROM apps WHERE id = $1::uuid`, appID,
	).Scan(&treasuryHex); err != nil {
		return nil, fmt.Errorf("treasury: %w", err)
	}
	out.Treasury = common.HexToAddress(treasuryHex)

	rows, err := s.pool.Query(ctx, `
		SELECT address, last_used_at FROM signers
		WHERE app_id = $1::uuid
		ORDER BY created_at, address
	`, appID)
	if err != nil {
		return nil, fmt.Errorf("signers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hex string
		var lastUsed *time.Time
		if err := rows.Scan(&hex, &lastUsed); err != nil {
			return nil, fmt.Errorf("signer scan: %w", err)
		}
		out.Signers = append(out.Signers, SignerInfo{
			Address:    common.HexToAddress(hex),
			LastUsedAt: lastUsed,
		})
	}
	return out, rows.Err()
}

func (s *Service) ListSigners(ctx context.Context, appID string) ([]common.Address, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT address FROM signers
		WHERE app_id = $1::uuid
		ORDER BY created_at, address
	`, appID)
	if err != nil {
		return nil, fmt.Errorf("list signers: %w", err)
	}
	defer rows.Close()

	var addrs []common.Address
	for rows.Next() {
		var hex string
		if err := rows.Scan(&hex); err != nil {
			return nil, fmt.Errorf("list signers scan: %w", err)
		}
		addrs = append(addrs, common.HexToAddress(hex))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list signers rows: %w", err)
	}
	return addrs, nil
}
