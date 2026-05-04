package chain

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

type Client struct {
	rpc     *ethclient.Client
	chainID *big.Int
}

func Dial(ctx context.Context, rpcURL string, expectedChainID uint64) (*Client, error) {
	rpc, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("chain: dial: %w", err)
	}
	got, err := rpc.ChainID(ctx)
	if err != nil {
		rpc.Close()
		return nil, fmt.Errorf("chain: chain id: %w", err)
	}
	if got.Cmp(new(big.Int).SetUint64(expectedChainID)) != 0 {
		rpc.Close()
		return nil, fmt.Errorf("chain: chain id mismatch: rpc=%s want=%d", got, expectedChainID)
	}
	return &Client{rpc: rpc, chainID: got}, nil
}

func (c *Client) Close() {
	c.rpc.Close()
}

func (c *Client) ChainID() *big.Int {
	return new(big.Int).Set(c.chainID)
}

func (c *Client) BalanceAt(ctx context.Context, addr common.Address) (*big.Int, error) {
	return c.rpc.BalanceAt(ctx, addr, nil)
}

func (c *Client) PendingNonceAt(ctx context.Context, addr common.Address) (uint64, error) {
	return c.rpc.PendingNonceAt(ctx, addr)
}

func (c *Client) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	return c.rpc.SuggestGasPrice(ctx)
}

func (c *Client) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	return c.rpc.EstimateGas(ctx, msg)
}

func (c *Client) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	return c.rpc.SendTransaction(ctx, tx)
}

func (c *Client) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	return c.rpc.TransactionReceipt(ctx, hash)
}
