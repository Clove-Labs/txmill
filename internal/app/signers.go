package app

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

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
