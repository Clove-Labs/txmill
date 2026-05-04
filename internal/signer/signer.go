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

// RewindNonce undoes the most recent UseNonce. The pool serializes access to
// each Signer (one checkout at a time), so it is safe to call between
// checkout and release when a submit fails before the chain consumed the
// reserved nonce.
func (s *Signer) RewindNonce() {
	s.nonce.Add(^uint64(0))
}

func (s *Signer) ResetNonce(n uint64) {
	s.nonce.Store(n)
}
