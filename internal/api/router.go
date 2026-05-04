package api

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func NewRouter(logger *slog.Logger, h *Handlers) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(middleware.Recover())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:  true,
		LogURI:     true,
		LogMethod:  true,
		LogLatency: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			logger.LogAttrs(c.Request().Context(), slog.LevelInfo, "request",
				slog.String("method", v.Method),
				slog.String("uri", v.URI),
				slog.Int("status", v.Status),
				slog.Duration("latency", v.Latency),
			)
			return nil
		},
	}))

	e.GET("/health", health)

	v1 := e.Group("/v1")
	if h != nil && h.Apps != nil {
		v1.POST("/apps", h.createApp)

		protected := v1.Group("", bearerAuth(h.Apps))
		protected.GET("/apps/:id/signers", h.listSigners)
		protected.GET("/apps/:id/balances", h.getBalances)
		if h.Relay != nil {
			protected.POST("/relay", h.postRelay)
			protected.GET("/relay/:request_id", h.getRelay)
		}
	}

	return e
}

func health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}
