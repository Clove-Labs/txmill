package relay_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/clove-labs/txmill/internal/app"
	"github.com/clove-labs/txmill/internal/chain"
	"github.com/clove-labs/txmill/internal/keystore"
	"github.com/clove-labs/txmill/internal/relay"
	"github.com/clove-labs/txmill/internal/signer"
	"github.com/clove-labs/txmill/internal/store/dbtest"
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

type mockChain struct {
	chainID  string
	gasPrice string
	estimate string
	nonce    string

	server      *httptest.Server
	sentRawTxs  atomic.Int32
	estimateHit atomic.Int32
	gasPriceHit atomic.Int32
}

func newMockChain(t *testing.T) *mockChain {
	t.Helper()
	m := &mockChain{
		chainID:  "0x92",
		gasPrice: "0x3b9aca00",
		estimate: "0x5208",
		nonce:    "0x0",
	}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcReq
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("mock decode: %v", err)
		}
		resp := rpcResp{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "eth_chainId":
			resp.Result = m.chainID
		case "eth_getTransactionCount":
			resp.Result = m.nonce
		case "eth_estimateGas":
			m.estimateHit.Add(1)
			resp.Result = m.estimate
		case "eth_gasPrice":
			m.gasPriceHit.Add(1)
			resp.Result = m.gasPrice
		case "eth_sendRawTransaction":
			m.sentRawTxs.Add(1)
			resp.Result = "0x" + strings.Repeat("ab", 32)
		default:
			resp.Error = &rpcErr{Code: -32601, Message: "unhandled: " + req.Method}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(m.server.Close)
	return m
}

func setup(t *testing.T) (*relay.Service, string, common.Address) {
	t.Helper()
	pool := dbtest.New(t)
	ks, err := keystore.New(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	mock := newMockChain(t)
	chClient, err := chain.Dial(context.Background(), mock.server.URL, 146)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chClient.Close)

	appSvc := app.NewService(pool, ks)
	res, err := appSvc.Create(context.Background(), app.CreateInput{
		Name:     "relay-test",
		PoolSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	sp := signer.NewPool(ks, chClient, appSvc)
	svc := relay.NewService(pool, chClient, sp)
	return svc, res.AppID, res.SignerAddresses[0]
}

func TestSubmit_HappyPath(t *testing.T) {
	svc, appID, _ := setup(t)

	calldata, _ := hex.DecodeString("a9059cbb")
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")

	res, err := svc.Submit(context.Background(), relay.SubmitInput{
		AppID:   appID,
		ChainID: 146,
		To:      to,
		Data:    calldata,
		Value:   big.NewInt(0),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.RequestID == "" {
		t.Fatal("empty request_id")
	}
	if (res.TxHash == common.Hash{}) {
		t.Fatal("empty tx_hash")
	}
	if (res.Signer == common.Address{}) {
		t.Fatal("empty signer")
	}
}

func TestSubmit_ChainIDMismatch(t *testing.T) {
	svc, appID, _ := setup(t)
	_, err := svc.Submit(context.Background(), relay.SubmitInput{
		AppID:   appID,
		ChainID: 1, // wrong
		To:      common.Address{1},
		Data:    nil,
		Value:   big.NewInt(0),
	})
	if err != relay.ErrChainIDMismatch {
		t.Fatalf("err = %v, want ErrChainIDMismatch", err)
	}
}

func TestSubmit_PastDeadline(t *testing.T) {
	svc, appID, _ := setup(t)
	_, err := svc.Submit(context.Background(), relay.SubmitInput{
		AppID:    appID,
		ChainID:  146,
		To:       common.Address{1},
		Data:     nil,
		Value:    big.NewInt(0),
		Deadline: 1, // very old
	})
	if err != relay.ErrPastDeadline {
		t.Fatalf("err = %v, want ErrPastDeadline", err)
	}
}
