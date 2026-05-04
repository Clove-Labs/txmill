package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/clove-labs/txmill/internal/app"
)

type createAppRequest struct {
	Name               string `json:"name"`
	PoolSize           int    `json:"pool_size"`
	DefaultCallbackURL string `json:"default_callback_url,omitempty"`
}

type createAppResponse struct {
	AppID                 string   `json:"app_id"`
	BearerToken           string   `json:"bearer_token"`
	DefaultCallbackSecret string   `json:"default_callback_secret,omitempty"`
	TreasuryAddress       string   `json:"treasury_address"`
	SignerAddresses       []string `json:"signer_addresses"`
}

func (h *Handlers) createApp(c echo.Context) error {
	var req createAppRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json body")
	}
	res, err := h.Apps.Create(c.Request().Context(), app.CreateInput{
		Name:               strings.TrimSpace(req.Name),
		PoolSize:           req.PoolSize,
		DefaultCallbackURL: strings.TrimSpace(req.DefaultCallbackURL),
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	signers := make([]string, len(res.SignerAddresses))
	for i, a := range res.SignerAddresses {
		signers[i] = a.Hex()
	}
	return c.JSON(http.StatusCreated, createAppResponse{
		AppID:                 res.AppID,
		BearerToken:           res.BearerToken,
		DefaultCallbackSecret: res.DefaultCallbackSecret,
		TreasuryAddress:       res.TreasuryAddress.Hex(),
		SignerAddresses:       signers,
	})
}
