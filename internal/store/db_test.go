package store_test

import (
	"context"
	"testing"

	"github.com/clove-labs/txmill/internal/store/dbtest"
)

func TestPing(t *testing.T) {
	pool := dbtest.New(t)
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d, want 1", n)
	}
}
