package alert

import (
	"context"
	"errors"
)

// Multi fans an alert out to every contained Notifier. Per-transport errors
// are aggregated; partial failure does not block other transports.
type Multi []Notifier

func (m Multi) Notify(ctx context.Context, a Alert) error {
	var errs []error
	for _, n := range m {
		if err := n.Notify(ctx, a); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
