package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	minPoolSize    = 1
	maxPoolSize    = 1024
	bearerTokenLen = 32
	bearerTokenTag = "tk_"
)

type Keystore interface {
	Generate() (common.Address, error)
}

type Service struct {
	pool *pgxpool.Pool
	ks   Keystore
}

func NewService(pool *pgxpool.Pool, ks Keystore) *Service {
	return &Service{pool: pool, ks: ks}
}

type CreateInput struct {
	Name               string
	PoolSize           int
	DefaultCallbackURL string
}

type CreateResult struct {
	AppID                 string
	BearerToken           string
	DefaultCallbackSecret string
	TreasuryAddress       common.Address
	SignerAddresses       []common.Address
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*CreateResult, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if in.PoolSize < minPoolSize || in.PoolSize > maxPoolSize {
		return nil, fmt.Errorf("pool_size must be between %d and %d", minPoolSize, maxPoolSize)
	}

	treasury, err := s.ks.Generate()
	if err != nil {
		return nil, fmt.Errorf("generate treasury: %w", err)
	}
	signers := make([]common.Address, in.PoolSize)
	for i := range signers {
		addr, err := s.ks.Generate()
		if err != nil {
			return nil, fmt.Errorf("generate signer %d: %w", i, err)
		}
		signers[i] = addr
	}

	token, hash, err := newBearerToken()
	if err != nil {
		return nil, err
	}

	var callbackSecret string
	if in.DefaultCallbackURL != "" {
		callbackSecret, err = newCallbackSecret()
		if err != nil {
			return nil, err
		}
	}

	appID, err := s.insert(ctx, name, in.PoolSize, in.DefaultCallbackURL, callbackSecret, treasury, signers, hash)
	if err != nil {
		return nil, fmt.Errorf("insert: %w", err)
	}

	return &CreateResult{
		AppID:                 appID,
		BearerToken:           token,
		DefaultCallbackSecret: callbackSecret,
		TreasuryAddress:       treasury,
		SignerAddresses:       signers,
	}, nil
}

func (s *Service) insert(
	ctx context.Context,
	name string,
	poolSize int,
	callbackURL string,
	callbackSecret string,
	treasury common.Address,
	signers []common.Address,
	tokenHash []byte,
) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var appID string
	err = tx.QueryRow(ctx, `
		INSERT INTO apps (name, treasury_address, bearer_token_hash, pool_size,
		                  default_callback_url, default_callback_secret)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''))
		RETURNING id::text
	`, name, lowerHex(treasury), tokenHash, poolSize, callbackURL, callbackSecret).Scan(&appID)
	if err != nil {
		return "", err
	}

	rows := make([][]any, len(signers))
	for i, addr := range signers {
		rows[i] = []any{lowerHex(addr), appID}
	}
	if _, err := tx.CopyFrom(ctx,
		pgx.Identifier{"signers"},
		[]string{"address", "app_id"},
		pgx.CopyFromRows(rows),
	); err != nil {
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return appID, nil
}

func newBearerToken() (token string, hash []byte, err error) {
	raw := make([]byte, bearerTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("token rand: %w", err)
	}
	token = bearerTokenTag + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func newCallbackSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("callback secret rand: %w", err)
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func lowerHex(a common.Address) string {
	return strings.ToLower(a.Hex())
}
