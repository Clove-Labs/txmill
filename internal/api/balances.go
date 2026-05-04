package api

import (
	"context"
	"math/big"
	"net/http"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"
)

type ChainBalances interface {
	BalanceAt(ctx context.Context, addr common.Address) (*big.Int, error)
}

type balancesAccount struct {
	Address    string `json:"address"`
	BalanceWei string `json:"balance_wei"`
}

type balancesSigner struct {
	Address    string  `json:"address"`
	BalanceWei string  `json:"balance_wei"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
}

type balancesResponse struct {
	Treasury balancesAccount  `json:"treasury"`
	Signers  []balancesSigner `json:"signers"`
}

func (h *Handlers) getBalances(c echo.Context) error {
	if c.Param("id") != AppID(c) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if h.Chain == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "chain client not configured")
	}

	ctx := c.Request().Context()
	accounts, err := h.Apps.Accounts(ctx, AppID(c))
	if err != nil {
		return err
	}

	addrs := make([]common.Address, 0, 1+len(accounts.Signers))
	addrs = append(addrs, accounts.Treasury)
	for _, s := range accounts.Signers {
		addrs = append(addrs, s.Address)
	}

	balances := make([]*big.Int, len(addrs))
	errs := make([]error, len(addrs))
	var wg sync.WaitGroup
	for i, a := range addrs {
		wg.Add(1)
		go func(i int, a common.Address) {
			defer wg.Done()
			bal, err := h.Chain.BalanceAt(ctx, a)
			balances[i], errs[i] = bal, err
		}(i, a)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "balance fetch: "+err.Error())
		}
	}

	resp := balancesResponse{
		Treasury: balancesAccount{
			Address:    accounts.Treasury.Hex(),
			BalanceWei: balances[0].String(),
		},
		Signers: make([]balancesSigner, len(accounts.Signers)),
	}
	for i, s := range accounts.Signers {
		var lastUsed *string
		if s.LastUsedAt != nil {
			ts := s.LastUsedAt.UTC().Format("2006-01-02T15:04:05.000000Z")
			lastUsed = &ts
		}
		resp.Signers[i] = balancesSigner{
			Address:    s.Address.Hex(),
			BalanceWei: balances[i+1].String(),
			LastUsedAt: lastUsed,
		}
	}
	return c.JSON(http.StatusOK, resp)
}
