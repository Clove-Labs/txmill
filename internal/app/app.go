package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
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
	// Gas top-up policy. All three must be set together (or all zero = disabled).
	SignerMinBalance   string // uint256 decimal
	SignerRefillAmount string // uint256 decimal
	TreasuryMinBalance string // uint256 decimal
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
	if err := validateGasPolicy(in.SignerMinBalance, in.SignerRefillAmount); err != nil {
		return nil, err
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

	appID, err := s.insert(ctx, name, in.PoolSize, in.DefaultCallbackURL, callbackSecret,
		treasury, signers, hash,
		nonEmptyOrZero(in.SignerMinBalance),
		nonEmptyOrZero(in.SignerRefillAmount),
		nonEmptyOrZero(in.TreasuryMinBalance),
	)
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
	signerMinBalance, signerRefillAmount, treasuryMinBalance string,
) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var appID string
	err = tx.QueryRow(ctx, `
		INSERT INTO apps (name, treasury_address, bearer_token_hash, pool_size,
		                  default_callback_url, default_callback_secret,
		                  signer_min_balance, signer_refill_amount, treasury_min_balance)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8, $9)
		RETURNING id::text
	`, name, lowerHex(treasury), tokenHash, poolSize,
		callbackURL, callbackSecret,
		signerMinBalance, signerRefillAmount, treasuryMinBalance,
	).Scan(&appID)
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

func nonEmptyOrZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// validateGasPolicy enforces:
//   - both fields zero → gas worker skips this app (OK).
//   - both fields non-zero → signer_refill_amount must exceed signer_min_balance,
//     otherwise the worker would refill on every dedupe-window tick forever
//     (refill leaves the signer still below threshold).
//   - one field zero, other non-zero → invalid (forgot to set both).
func validateGasPolicy(signerMin, signerRefill string) error {
	min := parseUintOrZero(signerMin)
	refill := parseUintOrZero(signerRefill)

	if min.Sign() == 0 && refill.Sign() == 0 {
		return nil
	}
	if min.Sign() == 0 || refill.Sign() == 0 {
		return errors.New("signer_min_balance and signer_refill_amount must be set together (or both zero)")
	}
	if refill.Cmp(min) <= 0 {
		return errors.New("signer_refill_amount must be greater than signer_min_balance")
	}
	return nil
}

func parseUintOrZero(s string) *big.Int {
	if s == "" {
		return new(big.Int)
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.Sign() < 0 {
		return new(big.Int)
	}
	return v
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
