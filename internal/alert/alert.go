package alert

import "context"

type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

type Alert struct {
	// Key dedupes alerts within a throttle window. Pick something stable for
	// the same condition (e.g. "treasury_low:<app_id>"), so repeated firings
	// don't spam.
	Key     string
	Level   Level
	Title   string
	Message string
	Tags    map[string]string
}

type Notifier interface {
	Notify(ctx context.Context, a Alert) error
}

// Discard is a no-op Notifier — useful when no transports are configured.
type Discard struct{}

func (Discard) Notify(context.Context, Alert) error { return nil }
