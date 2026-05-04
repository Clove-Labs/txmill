package chain_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/clove-labs/txmill/internal/chain"
)

// TestLive_Sonic hits the real Sonic mainnet RPC. Opt in via env var:
//
//	TXMILL_LIVE_RPC_TEST=1 go test -run TestLive_Sonic -v ./internal/chain/...
//
// Skipped by default so unit-test runs stay hermetic.
func TestLive_Sonic(t *testing.T) {
	if os.Getenv("TXMILL_LIVE_RPC_TEST") != "1" {
		t.Skip("set TXMILL_LIVE_RPC_TEST=1 to enable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := os.Getenv("TXMILL_RPC_URL")
	if url == "" {
		url = "https://rpc.soniclabs.com"
	}

	c, err := chain.Dial(ctx, url, 146)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	t.Logf("chain_id=%s", c.ChainID())

	gp, err := c.SuggestGasPrice(ctx)
	if err != nil {
		t.Fatalf("gas price: %v", err)
	}
	t.Logf("gas_price=%s wei", gp)

	addr := common.HexToAddress("0x0000000000000000000000000000000000000000")
	bal, err := c.BalanceAt(ctx, addr)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	t.Logf("balance(0x0000…)=%s wei", bal)

	nonce, err := c.PendingNonceAt(ctx, addr)
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	t.Logf("pending_nonce(0x0000…)=%d", nonce)
}
