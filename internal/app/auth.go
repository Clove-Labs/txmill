package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrInvalidToken = errors.New("invalid token")

type Identity struct {
	AppID    string
	PoolSize int
	Disabled bool
}

func (s *Service) Authenticate(ctx context.Context, bearerToken string) (*Identity, error) {
	if bearerToken == "" {
		return nil, ErrInvalidToken
	}
	sum := sha256.Sum256([]byte(bearerToken))
	var id Identity
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, pool_size, disabled
		FROM apps
		WHERE bearer_token_hash = $1
	`, sum[:]).Scan(&id.AppID, &id.PoolSize, &id.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}
	return &id, nil
}
