package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/chenhg5/agentflow/internal/queue"
)

func runWorker(parsed parsedArgs, root, cwd string) error {
	if len(parsed.Positionals) > 0 {
		return usageError("worker takes no positional arguments", "Use --concurrency <count>.", "worker_arguments")
	}
	if parsed.DryRun {
		return usageError("worker does not support --dry-run", "Inspect with run queue.", "worker_arguments")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	worker, err := queue.Start(ctx, root, cwd, parsed.workerConcurrency(), true)
	if errors.Is(err, queue.ErrWorkerBusy) {
		return conflictError(err.Error(), "Use the worker already serving this workspace.", parsed.Format)
	}
	if err != nil {
		return internalError(err)
	}
	fmt.Fprintf(os.Stderr, "agentflow worker ready: concurrency=%d workspace=%s\n", parsed.workerConcurrency(), root)
	return worker.Wait()
}
