package signer

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

var ErrAppNotLoaded = errors.New("signer pool: app not loaded")

type Keystore interface {
	Load(addr common.Address) (*ecdsa.PrivateKey, error)
}

type ChainNoncer interface {
	PendingNonceAt(ctx context.Context, addr common.Address) (uint64, error)
}

type SignerLister interface {
	ListSigners(ctx context.Context, appID string) ([]common.Address, error)
}

type Pool struct {
	keystore Keystore
	chain    ChainNoncer
	lister   SignerLister

	mu   sync.Mutex
	apps map[string]*appPool
}

type appPool struct {
	signers []*Signer
	idle    chan *Signer
}

func NewPool(ks Keystore, ch ChainNoncer, lister SignerLister) *Pool {
	return &Pool{
		keystore: ks,
		chain:    ch,
		lister:   lister,
		apps:     make(map[string]*appPool),
	}
}

// Load brings an app's signer pool into memory: lists signer addresses,
// decrypts each private key from the keystore, and hydrates each signer's
// nonce from the chain. Idempotent — a no-op if already loaded.
func (p *Pool) Load(ctx context.Context, appID string) error {
	p.mu.Lock()
	if _, ok := p.apps[appID]; ok {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	addrs, err := p.lister.ListSigners(ctx, appID)
	if err != nil {
		return fmt.Errorf("list signers: %w", err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("no signers for app %s", appID)
	}

	signers := make([]*Signer, 0, len(addrs))
	idle := make(chan *Signer, len(addrs))
	for _, addr := range addrs {
		key, err := p.keystore.Load(addr)
		if err != nil {
			return fmt.Errorf("load key %s: %w", addr.Hex(), err)
		}
		nonce, err := p.chain.PendingNonceAt(ctx, addr)
		if err != nil {
			return fmt.Errorf("nonce %s: %w", addr.Hex(), err)
		}
		s := &Signer{AppID: appID, Address: addr, Key: key}
		s.nonce.Store(nonce)
		signers = append(signers, s)
		idle <- s
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.apps[appID]; ok {
		return nil
	}
	p.apps[appID] = &appPool{signers: signers, idle: idle}
	return nil
}

// Checkout returns an idle signer for the app. Blocks until one is available
// or ctx is done. The caller must Release the signer when finished.
func (p *Pool) Checkout(ctx context.Context, appID string) (*Signer, error) {
	p.mu.Lock()
	ap, ok := p.apps[appID]
	p.mu.Unlock()
	if !ok {
		return nil, ErrAppNotLoaded
	}
	select {
	case s := <-ap.idle:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Pool) Release(s *Signer) {
	p.mu.Lock()
	ap, ok := p.apps[s.AppID]
	p.mu.Unlock()
	if !ok {
		return
	}
	ap.idle <- s
}

func (p *Pool) Size(appID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	ap, ok := p.apps[appID]
	if !ok {
		return 0
	}
	return len(ap.signers)
}
