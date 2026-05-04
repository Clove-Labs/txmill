package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clove-labs/txmill/internal/api"
	"github.com/clove-labs/txmill/internal/app"
	"github.com/clove-labs/txmill/internal/keystore"
	"github.com/clove-labs/txmill/internal/store/dbtest"
)

func TestListSigners_EndToEnd(t *testing.T) {
	pool := dbtest.New(t)
	ks, err := keystore.New(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewService(pool, ks)

	res, err := svc.Create(context.Background(), app.CreateInput{
		Name:     "tester",
		PoolSize: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	e := api.NewRouter(logger, &api.Handlers{Apps: svc})

	t.Run("missing token returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/apps/"+res.AppID+"/signers", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("wrong app id returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/apps/00000000-0000-0000-0000-000000000000/signers", nil)
		req.Header.Set("Authorization", "Bearer "+res.BearerToken)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("valid request returns full signer list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/apps/"+res.AppID+"/signers", nil)
		req.Header.Set("Authorization", "Bearer "+res.BearerToken)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var got struct {
			AppID           string   `json:"app_id"`
			SignerAddresses []string `json:"signer_addresses"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.AppID != res.AppID {
			t.Fatalf("app_id = %q, want %q", got.AppID, res.AppID)
		}
		if len(got.SignerAddresses) != 4 {
			t.Fatalf("got %d signers, want 4", len(got.SignerAddresses))
		}

		want := make(map[string]bool, len(res.SignerAddresses))
		for _, a := range res.SignerAddresses {
			want[a.Hex()] = true
		}
		for _, a := range got.SignerAddresses {
			if !want[a] {
				t.Fatalf("unexpected signer %s", a)
			}
		}
	})
}
