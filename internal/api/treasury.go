package api

import (
	"errors"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	"github.com/clove-labs/txmill/internal/treasury"
)

type sweepRequest struct {
	To    string `json:"to"`
	Value string `json:"value,omitempty"`
}

type sweepResponse struct {
	TxHash   string `json:"tx_hash"`
	From     string `json:"from"`
	To       string `json:"to"`
	ValueWei string `json:"value_wei"`
}

func (h *Handlers) sweepTreasury(c echo.Context) error {
	if c.Param("id") != AppID(c) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if h.Treasury == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "treasury service not configured")
	}

	var req sweepRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json body")
	}
	if !common.IsHexAddress(req.To) {
		return echo.NewHTTPError(http.StatusBadRequest, "to is not a valid address")
	}

	var value *big.Int
	if req.Value != "" {
		v, ok := new(big.Int).SetString(req.Value, 10)
		if !ok || v.Sign() <= 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "value must be a positive decimal string")
		}
		value = v
	}

	res, err := h.Treasury.Sweep(c.Request().Context(), AppID(c), common.HexToAddress(req.To), value)
	if err != nil {
		if errors.Is(err, treasury.ErrAppNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "not found")
		}
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}

	return c.JSON(http.StatusAccepted, sweepResponse{
		TxHash:   res.TxHash.Hex(),
		From:     res.From.Hex(),
		To:       res.To.Hex(),
		ValueWei: res.ValueWei.String(),
	})
}
