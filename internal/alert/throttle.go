package alert

import (
	"context"
	"sync"
	"time"
)

// Throttled wraps a Notifier and suppresses repeat alerts whose Key was
// already sent within the configured interval. State is in-memory; on
// process restart the throttle resets and the next firing goes through.
type Throttled struct {
	inner    Notifier
	interval time.Duration

	mu   sync.Mutex
	sent map[string]time.Time
	now  func() time.Time
}

func NewThrottled(inner Notifier, interval time.Duration) *Throttled {
	return &Throttled{
		inner:    inner,
		interval: interval,
		sent:     make(map[string]time.Time),
		now:      time.Now,
	}
}

func (t *Throttled) Notify(ctx context.Context, a Alert) error {
	t.mu.Lock()
	now := t.now()
	if last, ok := t.sent[a.Key]; ok && now.Sub(last) < t.interval {
		t.mu.Unlock()
		return nil
	}
	t.sent[a.Key] = now
	t.mu.Unlock()
	return t.inner.Notify(ctx, a)
}
