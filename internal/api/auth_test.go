package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/clove-labs/txmill/internal/app"
)

type stubAuth struct {
	id  *app.Identity
	err error
}

func (s stubAuth) Authenticate(_ context.Context, _ string) (*app.Identity, error) {
	return s.id, s.err
}

func runMiddleware(t *testing.T, header string, auth Authenticator) (int, string) {
	t.Helper()
	e := echo.New()
	e.Use(bearerAuth(auth))
	e.GET("/x", func(c echo.Context) error {
		return c.String(http.StatusOK, AppID(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestBearerAuth_NoHeader(t *testing.T) {
	code, _ := runMiddleware(t, "", stubAuth{})
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestBearerAuth_NotBearer(t *testing.T) {
	code, _ := runMiddleware(t, "Basic xyz", stubAuth{})
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestBearerAuth_EmptyToken(t *testing.T) {
	code, _ := runMiddleware(t, "Bearer ", stubAuth{})
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestBearerAuth_InvalidToken(t *testing.T) {
	code, _ := runMiddleware(t, "Bearer tk_bad", stubAuth{err: app.ErrInvalidToken})
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestBearerAuth_Disabled(t *testing.T) {
	code, _ := runMiddleware(t, "Bearer tk_x", stubAuth{
		id: &app.Identity{AppID: "abc", PoolSize: 5, Disabled: true},
	})
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
}

func TestBearerAuth_OK(t *testing.T) {
	code, body := runMiddleware(t, "Bearer tk_x", stubAuth{
		id: &app.Identity{AppID: "abc", PoolSize: 5},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body != "abc" {
		t.Fatalf("body = %q, want %q", body, "abc")
	}
}

func TestBearerAuth_BackendError(t *testing.T) {
	code, _ := runMiddleware(t, "Bearer tk_x", stubAuth{err: errors.New("db down")})
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
}
