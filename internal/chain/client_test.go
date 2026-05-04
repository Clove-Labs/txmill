package chain_test

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/clove-labs/txmill/internal/chain"
)

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mockRPC(t *testing.T, results map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("mock read: %v", err)
		}
		var req rpcReq
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("mock decode: %v (body=%s)", err, body)
		}
		resp := rpcResp{JSONRPC: "2.0", ID: req.ID}
		if v, ok := results[req.Method]; ok {
			resp.Result = v
		} else {
			resp.Error = &rpcErr{Code: -32601, Message: "method not handled: " + req.Method}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestDial_ChainIDMatch(t *testing.T) {
	srv := mockRPC(t, map[string]any{"eth_chainId": "0x92"})
	defer srv.Close()

	c, err := chain.Dial(context.Background(), srv.URL, 146)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c.ChainID().Cmp(big.NewInt(146)) != 0 {
		t.Fatalf("ChainID() = %s, want 146", c.ChainID())
	}
}

func TestDial_ChainIDMismatch(t *testing.T) {
	srv := mockRPC(t, map[string]any{"eth_chainId": "0x1"})
	defer srv.Close()

	if _, err := chain.Dial(context.Background(), srv.URL, 146); err == nil {
		t.Fatal("expected chain id mismatch error")
	}
}

func TestBalanceAt(t *testing.T) {
	srv := mockRPC(t, map[string]any{
		"eth_chainId":    "0x92",
		"eth_getBalance": "0xde0b6b3a7640000",
	})
	defer srv.Close()

	c, err := chain.Dial(context.Background(), srv.URL, 146)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	bal, err := c.BalanceAt(context.Background(), common.Address{})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := new(big.Int).SetString("1000000000000000000", 10)
	if bal.Cmp(want) != 0 {
		t.Fatalf("BalanceAt = %s, want %s", bal, want)
	}
}

func TestPendingNonceAt(t *testing.T) {
	srv := mockRPC(t, map[string]any{
		"eth_chainId":             "0x92",
		"eth_getTransactionCount": "0x2a",
	})
	defer srv.Close()

	c, err := chain.Dial(context.Background(), srv.URL, 146)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	n, err := c.PendingNonceAt(context.Background(), common.Address{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Fatalf("PendingNonceAt = %d, want 42", n)
	}
}

func TestSuggestGasPrice(t *testing.T) {
	srv := mockRPC(t, map[string]any{
		"eth_chainId":  "0x92",
		"eth_gasPrice": "0x3b9aca00",
	})
	defer srv.Close()

	c, err := chain.Dial(context.Background(), srv.URL, 146)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	gp, err := c.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gp.Cmp(big.NewInt(1_000_000_000)) != 0 {
		t.Fatalf("SuggestGasPrice = %s, want 1000000000", gp)
	}
}

func TestSendTransaction_PassesThroughError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := rpcResp{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "eth_chainId":
			resp.Result = "0x92"
		case "eth_sendRawTransaction":
			resp.Error = &rpcErr{Code: -32000, Message: "nonce too low"}
		default:
			resp.Error = &rpcErr{Code: -32601, Message: "unhandled"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, err := chain.Dial(context.Background(), srv.URL, 146)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	tx := types.NewTransaction(0, common.Address{}, big.NewInt(0), 21000, big.NewInt(1), nil)
	signed, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(146)), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SendTransaction(context.Background(), signed); err == nil {
		t.Fatal("expected error from RPC")
	}
}
