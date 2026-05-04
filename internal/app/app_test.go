package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/clove-labs/txmill/internal/app"
	"github.com/clove-labs/txmill/internal/keystore"
	"github.com/clove-labs/txmill/internal/store/dbtest"
)

func TestCreateApp(t *testing.T) {
	pool := dbtest.New(t)
	ks, err := keystore.New(t.TempDir(), "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewService(pool, ks)

	res, err := svc.Create(context.Background(), app.CreateInput{
		Name:               "alpha",
		PoolSize:           5,
		DefaultCallbackURL: "https://example.com/cb",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.AppID == "" {
		t.Fatal("empty app id")
	}
	if !strings.HasPrefix(res.BearerToken, "tk_") {
		t.Fatalf("token doesn't start with tk_: %q", res.BearerToken)
	}
	if len(res.SignerAddresses) != 5 {
		t.Fatalf("got %d signers, want 5", len(res.SignerAddresses))
	}
	if res.TreasuryAddress == res.SignerAddresses[0] {
		t.Fatal("treasury address collided with first signer")
	}

	ctx := context.Background()
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM signers WHERE app_id = $1::uuid`, res.AppID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("DB has %d signers, want 5", n)
	}

	var stored []byte
	var url *string
	if err := pool.QueryRow(ctx, `
		SELECT bearer_token_hash, default_callback_url
		FROM apps WHERE id = $1::uuid
	`, res.AppID).Scan(&stored, &url); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(res.BearerToken))
	if !bytes.Equal(stored, want[:]) {
		t.Fatal("stored bearer_token_hash != sha256(token)")
	}
	if url == nil || *url != "https://example.com/cb" {
		t.Fatalf("default_callback_url = %v, want set", url)
	}
}

func TestCreateApp_Validation(t *testing.T) {
	pool := dbtest.New(t)
	ks, err := keystore.New(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewService(pool, ks)

	cases := []struct {
		name string
		in   app.CreateInput
	}{
		{"empty name", app.CreateInput{Name: "  ", PoolSize: 1}},
		{"zero pool", app.CreateInput{Name: "x", PoolSize: 0}},
		{"too big pool", app.CreateInput{Name: "x", PoolSize: 10000}},
		{"refill <= min", app.CreateInput{
			Name: "x", PoolSize: 1,
			SignerMinBalance: "100", SignerRefillAmount: "100",
		}},
		{"refill < min", app.CreateInput{
			Name: "x", PoolSize: 1,
			SignerMinBalance: "100", SignerRefillAmount: "50",
		}},
		{"min set, refill missing", app.CreateInput{
			Name: "x", PoolSize: 1,
			SignerMinBalance: "100",
		}},
		{"refill set, min missing", app.CreateInput{
			Name: "x", PoolSize: 1,
			SignerRefillAmount: "100",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(context.Background(), tc.in); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
