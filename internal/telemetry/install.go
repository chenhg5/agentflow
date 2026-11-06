package telemetry

import (
	"context"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

// Install starts reporting the runs under root when a Hub is configured.
// It returns nil, and changes nothing, when reporting is off.
func Install(root string) (*Reporter, func(), error) {
	config, err := LoadConfig(root)
	if err != nil || !config.Enabled() {
		return nil, func() {}, err
	}
	reporter := NewReporter(NewOutbox(root), NewHTTPSender(config), SourceFor(config, root))
	remove := store.OnSave(func(run *model.Run) {
		_ = reporter.Record(run) // the run must not fail because a report could not be written
	})
	return reporter, remove, nil
}

// FlushQuietly delivers what it can within timeout and keeps the rest.
func (r *Reporter) FlushQuietly(timeout time.Duration) {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = r.Flush(ctx)
}

// Every flushes on an interval until ctx is done, so long runs and servers
// show up on the Hub while they work. A flush in progress is abandoned when
// ctx ends; whatever it did not deliver stays in the outbox.
func (r *Reporter) Every(ctx context.Context, interval time.Duration) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			flush, cancel := context.WithTimeout(ctx, interval)
			_, _ = r.Flush(flush)
			cancel()
		}
	}
}

// Start installs reporting for a command: it records every saved run and
// flushes in the background. stop flushes once more, briefly, and uninstalls.
// When no Hub is configured both are no-ops.
func Start(root string, interval, finalFlush time.Duration) (stop func()) {
	reporter, remove, err := Install(root)
	if err != nil || reporter == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		reporter.Every(ctx, interval)
	}()
	return func() {
		cancel()
		<-done
		remove()
		reporter.FlushQuietly(finalFlush)
	}
}
