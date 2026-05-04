package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type signersResponse struct {
	AppID           string   `json:"app_id"`
	SignerAddresses []string `json:"signer_addresses"`
}

func (h *Handlers) listSigners(c echo.Context) error {
	callerID := AppID(c)
	if c.Param("id") != callerID {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	addrs, err := h.Apps.ListSigners(c.Request().Context(), callerID)
	if err != nil {
		return err
	}
	hex := make([]string, len(addrs))
	for i, a := range addrs {
		hex[i] = a.Hex()
	}
	return c.JSON(http.StatusOK, signersResponse{
		AppID:           callerID,
		SignerAddresses: hex,
	})
}
