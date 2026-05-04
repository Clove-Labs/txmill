package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/clove-labs/txmill/internal/app"
)

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*app.Identity, error)
}

const (
	ctxKeyAppID    = "txmill.app_id"
	ctxKeyPoolSize = "txmill.pool_size"
)

func bearerAuth(auth Authenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			tok, err := extractBearerToken(c.Request().Header.Get("Authorization"))
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid Authorization header")
			}
			id, err := auth.Authenticate(c.Request().Context(), tok)
			if err != nil {
				if errors.Is(err, app.ErrInvalidToken) {
					return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
				}
				return err
			}
			if id.Disabled {
				return echo.NewHTTPError(http.StatusForbidden, "app is disabled")
			}
			c.Set(ctxKeyAppID, id.AppID)
			c.Set(ctxKeyPoolSize, id.PoolSize)
			return next(c)
		}
	}
}

func extractBearerToken(authHeader string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return "", errors.New("not a bearer token")
	}
	tok := strings.TrimPrefix(authHeader, prefix)
	if tok == "" {
		return "", errors.New("empty token")
	}
	return tok, nil
}

func AppID(c echo.Context) string {
	v, _ := c.Get(ctxKeyAppID).(string)
	return v
}

func PoolSize(c echo.Context) int {
	v, _ := c.Get(ctxKeyPoolSize).(int)
	return v
}
