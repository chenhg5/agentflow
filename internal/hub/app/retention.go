package app

import (
	"context"
	"log"
	"time"
)

// Retention removes finished runs older than Keep. Runs still going or
// waiting for a person are kept however old they are.
type Retention struct {
	Store Store
	Keep  time.Duration
	Now   func() time.Time
	Logf  func(format string, args ...any)
}

// Prune removes what is past the retention period once.
func (r Retention) Prune(ctx context.Context) (int, error) {
	if r.Keep <= 0 {
		return 0, nil
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	return r.Store.DeleteFinishedBefore(ctx, now().Add(-r.Keep))
}

// Every prunes now and then on each tick until ctx is done.
func (r Retention) Every(ctx context.Context, interval time.Duration) {
	if r.Keep <= 0 {
		return
	}
	logf := r.Logf
	if logf == nil {
		logf = log.Printf
	}
	prune := func() {
		if removed, err := r.Prune(ctx); err != nil {
			logf("hub retention: %v", err)
		} else if removed > 0 {
			logf("hub retention: removed %d finished runs older than %s", removed, r.Keep)
		}
	}
	prune()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}
