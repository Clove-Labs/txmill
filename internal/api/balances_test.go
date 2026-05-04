package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/clove-labs/txmill/internal/api"
	"github.com/clove-labs/txmill/internal/app"
	"github.com/clove-labs/txmill/internal/keystore"
	"github.com/clove-labs/txmill/internal/store/dbtest"
)

type stubChainBalances struct {
	balances map[common.Address]*big.Int
	calls    atomic.Int32
}

func (s *stubChainBalances) BalanceAt(_ context.Context, addr common.Address) (*big.Int, error) {
	s.calls.Add(1)
	if b, ok := s.balances[addr]; ok {
		return new(big.Int).Set(b), nil
	}
	return new(big.Int), nil
}

func TestGetBalances_EndToEnd(t *testing.T) {
	pool := dbtest.New(t)
	ks, err := keystore.New(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewService(pool, ks)

	res, err := svc.Create(context.Background(), app.CreateInput{Name: "bal-test", PoolSize: 4})
	if err != nil {
		t.Fatal(err)
	}

	balances := map[common.Address]*big.Int{
		res.TreasuryAddress: big.NewInt(123_000_000_000_000_000),
	}
	for i, s := range res.SignerAddresses {
		balances[s] = big.NewInt(int64(i+1) * 10_000_000_000)
	}
	chain := &stubChainBalances{balances: balances}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	e := api.NewRouter(logger, &api.Handlers{Apps: svc, Chain: chain})

	t.Run("missing token returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/apps/"+res.AppID+"/balances", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("wrong app id returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/apps/00000000-0000-0000-0000-000000000000/balances", nil)
		req.Header.Set("Authorization", "Bearer "+res.BearerToken)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("valid request returns full balance set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/apps/"+res.AppID+"/balances", nil)
		req.Header.Set("Authorization", "Bearer "+res.BearerToken)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var got struct {
			Treasury struct {
				Address    string `json:"address"`
				BalanceWei string `json:"balance_wei"`
			} `json:"treasury"`
			Signers []struct {
				Address    string  `json:"address"`
				BalanceWei string  `json:"balance_wei"`
				LastUsedAt *string `json:"last_used_at"`
			} `json:"signers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}

		if !strings.EqualFold(got.Treasury.Address, res.TreasuryAddress.Hex()) {
			t.Fatalf("treasury address = %q, want %q", got.Treasury.Address, res.TreasuryAddress.Hex())
		}
		if got.Treasury.BalanceWei != "123000000000000000" {
			t.Fatalf("treasury balance = %q, want 123000000000000000", got.Treasury.BalanceWei)
		}
		if len(got.Signers) != 4 {
			t.Fatalf("got %d signers, want 4", len(got.Signers))
		}

		// Check at least one signer's balance maps correctly.
		want := make(map[string]string, len(res.SignerAddresses))
		for i, s := range res.SignerAddresses {
			want[strings.ToLower(s.Hex())] = big.NewInt(int64(i+1) * 10_000_000_000).String()
		}
		for _, s := range got.Signers {
			if got, ok := want[strings.ToLower(s.Address)]; ok {
				if s.BalanceWei != got {
					t.Fatalf("balance for %s = %s, want %s", s.Address, s.BalanceWei, got)
				}
			}
		}

		if chain.calls.Load() != 5 {
			t.Fatalf("BalanceAt called %d times, want 5 (1 treasury + 4 signers)", chain.calls.Load())
		}
	})
}
