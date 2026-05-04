package signer_test

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/clove-labs/txmill/internal/signer"
)

type stubKeystore struct {
	keys map[common.Address]*ecdsa.PrivateKey
}

func (s *stubKeystore) Load(addr common.Address) (*ecdsa.PrivateKey, error) {
	k, ok := s.keys[addr]
	if !ok {
		return nil, errors.New("no key")
	}
	return k, nil
}

type stubChain struct {
	startNonce uint64
}

func (s *stubChain) PendingNonceAt(_ context.Context, _ common.Address) (uint64, error) {
	return s.startNonce, nil
}

type stubLister struct {
	addrs []common.Address
}

func (s *stubLister) ListSigners(_ context.Context, _ string) ([]common.Address, error) {
	return s.addrs, nil
}

func newPool(t *testing.T, n int, startNonce uint64) (*signer.Pool, []common.Address) {
	t.Helper()
	addrs := make([]common.Address, n)
	keys := make(map[common.Address]*ecdsa.PrivateKey, n)
	for i := 0; i < n; i++ {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		a := crypto.PubkeyToAddress(k.PublicKey)
		addrs[i] = a
		keys[a] = k
	}
	pool := signer.NewPool(
		&stubKeystore{keys: keys},
		&stubChain{startNonce: startNonce},
		&stubLister{addrs: addrs},
	)
	if err := pool.Load(context.Background(), "app-1"); err != nil {
		t.Fatal(err)
	}
	return pool, addrs
}

func TestLoadIdempotent(t *testing.T) {
	pool, _ := newPool(t, 3, 5)
	if err := pool.Load(context.Background(), "app-1"); err != nil {
		t.Fatal(err)
	}
	if pool.Size("app-1") != 3 {
		t.Fatalf("size = %d, want 3", pool.Size("app-1"))
	}
}

func TestCheckoutReleaseRoundtrip(t *testing.T) {
	pool, addrs := newPool(t, 3, 0)
	ctx := context.Background()

	seen := make(map[common.Address]bool, 3)
	checked := make([]*signer.Signer, 0, 3)
	for i := 0; i < 3; i++ {
		s, err := pool.Checkout(ctx, "app-1")
		if err != nil {
			t.Fatal(err)
		}
		if seen[s.Address] {
			t.Fatalf("checked out duplicate signer %s", s.Address.Hex())
		}
		seen[s.Address] = true
		checked = append(checked, s)
	}
	if len(seen) != 3 {
		t.Fatalf("got %d distinct, want 3", len(seen))
	}
	want := make(map[common.Address]bool, 3)
	for _, a := range addrs {
		want[a] = true
	}
	for a := range seen {
		if !want[a] {
			t.Fatalf("unexpected address %s", a.Hex())
		}
	}

	for _, s := range checked {
		pool.Release(s)
	}
	// Now we should be able to check out 3 again.
	for i := 0; i < 3; i++ {
		s, err := pool.Checkout(ctx, "app-1")
		if err != nil {
			t.Fatal(err)
		}
		pool.Release(s)
	}
}

func TestCheckoutBlocksUntilRelease(t *testing.T) {
	pool, _ := newPool(t, 1, 0)

	first, err := pool.Checkout(context.Background(), "app-1")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		s, err := pool.Checkout(context.Background(), "app-1")
		if err != nil {
			t.Errorf("second checkout: %v", err)
		}
		_ = s
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("second checkout returned before release")
	case <-time.After(50 * time.Millisecond):
	}

	pool.Release(first)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second checkout did not unblock after release")
	}
}

func TestCheckoutRespectsContext(t *testing.T) {
	pool, _ := newPool(t, 1, 0)
	first, err := pool.Checkout(context.Background(), "app-1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release(first)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := pool.Checkout(ctx, "app-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestCheckoutUnknownApp(t *testing.T) {
	pool, _ := newPool(t, 1, 0)
	if _, err := pool.Checkout(context.Background(), "missing"); !errors.Is(err, signer.ErrAppNotLoaded) {
		t.Fatalf("err = %v, want ErrAppNotLoaded", err)
	}
}

func TestNonceMonotonic(t *testing.T) {
	pool, _ := newPool(t, 1, 100)
	s, err := pool.Checkout(context.Background(), "app-1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release(s)

	for want := uint64(100); want < 110; want++ {
		got := s.UseNonce()
		if got != want {
			t.Fatalf("UseNonce = %d, want %d", got, want)
		}
	}
}

func TestRewindNonce(t *testing.T) {
	pool, _ := newPool(t, 1, 50)
	s, err := pool.Checkout(context.Background(), "app-1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release(s)

	if got := s.UseNonce(); got != 50 {
		t.Fatalf("first UseNonce = %d, want 50", got)
	}
	s.RewindNonce()
	if got := s.Nonce(); got != 50 {
		t.Fatalf("after rewind, Nonce() = %d, want 50", got)
	}
	if got := s.UseNonce(); got != 50 {
		t.Fatalf("second UseNonce = %d, want 50 (rewound)", got)
	}

	// Use, use, rewind once → next is the second one.
	_ = s.UseNonce() // 51
	_ = s.UseNonce() // 52
	s.RewindNonce()
	if got := s.UseNonce(); got != 52 {
		t.Fatalf("after rewind from 53, UseNonce = %d, want 52", got)
	}
}

func TestPoolSizeHonoredUnderContention(t *testing.T) {
	const poolSize = 4
	const workers = 32
	const ops = 200

	pool, _ := newPool(t, poolSize, 0)

	var inflight atomic.Int32
	var maxSeen atomic.Int32
	var totalOps atomic.Int32

	bumpMax := func(cur int32) {
		for {
			old := maxSeen.Load()
			if cur <= old || maxSeen.CompareAndSwap(old, cur) {
				return
			}
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				s, err := pool.Checkout(context.Background(), "app-1")
				if err != nil {
					t.Errorf("checkout: %v", err)
					return
				}
				cur := inflight.Add(1)
				bumpMax(cur)
				_ = s.UseNonce()
				time.Sleep(50 * time.Microsecond)
				inflight.Add(-1)
				pool.Release(s)
				totalOps.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := totalOps.Load(); got != int32(workers*ops) {
		t.Fatalf("totalOps = %d, want %d", got, workers*ops)
	}
	if max := maxSeen.Load(); max > int32(poolSize) {
		t.Fatalf("max in-flight = %d, exceeds pool size %d", max, poolSize)
	}
	t.Logf("pool=%d workers=%d ops_per_worker=%d max_inflight=%d", poolSize, workers, ops, maxSeen.Load())
}

func TestLoadFailsWithNoSigners(t *testing.T) {
	pool := signer.NewPool(
		&stubKeystore{},
		&stubChain{},
		&stubLister{addrs: nil},
	)
	if err := pool.Load(context.Background(), "empty"); err == nil {
		t.Fatal("expected error when no signers")
	} else if !strings.Contains(err.Error(), "no signers") {
		t.Fatalf("err = %v, want 'no signers' substring", err)
	}
}
