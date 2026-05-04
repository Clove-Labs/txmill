package signer

import (
	"crypto/ecdsa"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
)

type Signer struct {
	AppID   string
	Address common.Address
	Key     *ecdsa.PrivateKey
	nonce   atomic.Uint64
}

func (s *Signer) Nonce() uint64 {
	return s.nonce.Load()
}

func (s *Signer) UseNonce() uint64 {
	return s.nonce.Add(1) - 1
}

func (s *Signer) ResetNonce(n uint64) {
	s.nonce.Store(n)
}
