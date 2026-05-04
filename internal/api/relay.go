package api

import (
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	"github.com/clove-labs/txmill/internal/relay"
)

const maxCallbackMetadataLen = 1024

type relayRequest struct {
	ChainID          uint64 `json:"chain_id"`
	To               string `json:"to"`
	Data             string `json:"data"`
	Value            string `json:"value,omitempty"`
	GasLimit         uint64 `json:"gas_limit,omitempty"`
	Deadline         int64  `json:"deadline,omitempty"`
	CallbackURL      string `json:"callback_url,omitempty"`
	CallbackMetadata string `json:"callback_metadata,omitempty"`
}

type relayResponse struct {
	RequestID string `json:"request_id"`
	TxHash    string `json:"tx_hash"`
	Signer    string `json:"signer"`
}

func (h *Handlers) postRelay(c echo.Context) error {
	var req relayRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json body")
	}

	if !common.IsHexAddress(req.To) {
		return echo.NewHTTPError(http.StatusBadRequest, "to is not a valid address")
	}
	data, err := decodeHexBytes(req.Data)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "data: "+err.Error())
	}
	value := new(big.Int)
	if req.Value != "" {
		v, ok := new(big.Int).SetString(req.Value, 10)
		if !ok || v.Sign() < 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "value must be a non-negative decimal string")
		}
		value = v
	}
	if len(req.CallbackMetadata) > maxCallbackMetadataLen {
		return echo.NewHTTPError(http.StatusBadRequest, "callback_metadata exceeds limit")
	}

	res, err := h.Relay.Submit(c.Request().Context(), relay.SubmitInput{
		AppID:            AppID(c),
		ChainID:          req.ChainID,
		To:               common.HexToAddress(req.To),
		Data:             data,
		Value:            value,
		GasLimit:         req.GasLimit,
		Deadline:         req.Deadline,
		CallbackURL:      strings.TrimSpace(req.CallbackURL),
		CallbackMetadata: req.CallbackMetadata,
	})
	if err != nil {
		switch {
		case errors.Is(err, relay.ErrChainIDMismatch),
			errors.Is(err, relay.ErrPastDeadline):
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		default:
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
	}

	return c.JSON(http.StatusAccepted, relayResponse{
		RequestID: res.RequestID,
		TxHash:    res.TxHash.Hex(),
		Signer:    res.Signer.Hex(),
	})
}

func decodeHexBytes(s string) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return []byte{}, nil
	}
	return hex.DecodeString(s)
}
