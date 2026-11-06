package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/hubapi"
	"github.com/chenhg5/agentflow/internal/model"
)

// Reporter records run snapshots into the outbox and flushes them to a Hub.
type Reporter struct {
	outbox *Outbox
	sender Sender
	source hubapi.Source
	now    func() time.Time
	mu     sync.Mutex // one flush at a time

	// commands and apply are set when this runner accepts commands from the
	// Hub (see Config.AcceptCommands) and the command can act on them.
	commands CommandSource
	apply    Applier
}

// CommandSource is where a runner pulls the Hub's commands from.
type CommandSource interface {
	Commands(ctx context.Context, machine string) ([]hubapi.Command, error)
	Answer(ctx context.Context, machine, id string, result hubapi.CommandResult) error
}

// Applier carries out one command against the local runs. The CLI provides
// it, so this package never depends on the engine's control flow.
type Applier func(ctx context.Context, command hubapi.Command) error

// Poll fetches pending commands and applies each one, answering the Hub with
// the outcome. Errors reaching the Hub are ignored: the next poll retries.
func (r *Reporter) Poll(ctx context.Context) {
	if r == nil || r.commands == nil || r.apply == nil {
		return
	}
	commands, err := r.commands.Commands(ctx, r.source.Machine)
	if err != nil {
		return
	}
	for _, command := range commands {
		result := hubapi.CommandResult{Status: "applied"}
		if err := r.apply(ctx, command); err != nil {
			result = hubapi.CommandResult{Status: "rejected", Error: clip(Redact(err.Error()), maxErrorRunes)}
		}
		_ = r.commands.Answer(ctx, r.source.Machine, command.ID, result)
	}
}

// NewReporter wires the reporting pieces together.
func NewReporter(outbox *Outbox, sender Sender, source hubapi.Source) *Reporter {
	return &Reporter{outbox: outbox, sender: sender, source: source, now: time.Now}
}

// Record stores the latest snapshot of run. It is cheap and never touches the network.
func (r *Reporter) Record(run *model.Run) error {
	return r.outbox.Put(FromRun(run, r.source, r.now().UTC()))
}

// FlushResult says what a flush delivered.
type FlushResult struct {
	Sent     int      `json:"sent"`
	Stale    int      `json:"stale"`
	Rejected []string `json:"rejected,omitempty"`
	Pending  int      `json:"pending"`
}

// Flush delivers pending reports in batches. Reports the Hub rejected as
// invalid are dropped so one bad report cannot block the rest; on a network or
// server error everything stays in the outbox for the next flush.
func (r *Reporter) Flush(ctx context.Context) (FlushResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total FlushResult
	pending, err := r.outbox.Pending()
	if err != nil {
		return total, err
	}
	if len(pending) == 0 {
		return total, nil // keep the last status: nothing was attempted
	}
	for start := 0; start < len(pending); start += hubapi.MaxBatch {
		end := min(start+hubapi.MaxBatch, len(pending))
		batch := pending[start:end]
		result, err := r.sender.Send(ctx, batch)
		if err != nil {
			total.Pending = len(pending) - start
			r.writeStatus(total, err)
			return total, err
		}
		for _, report := range batch {
			if err := r.outbox.Done(report); err != nil {
				return total, err
			}
		}
		total.Sent += result.Accepted
		total.Stale += result.Stale
		total.Rejected = append(total.Rejected, result.Rejected...)
	}
	r.writeStatus(total, nil)
	return total, nil
}

// Status is the outcome of the last flush, kept for `agentflow hub status`.
type Status struct {
	At     string      `json:"at"`
	Result FlushResult `json:"result"`
	Error  string      `json:"error,omitempty"`
}

const statusFile = ".status.json"

func (r *Reporter) writeStatus(result FlushResult, err error) {
	status := Status{At: r.now().UTC().Format(time.RFC3339), Result: result}
	if err != nil {
		status.Error = err.Error()
	}
	data, _ := json.Marshal(status)
	_ = os.MkdirAll(r.outbox.Dir(), 0o755)
	_ = os.WriteFile(filepath.Join(r.outbox.Dir(), statusFile), data, 0o600)
}

// LastStatus reads the outcome of the previous flush, if any.
func LastStatus(outbox *Outbox) (*Status, error) {
	data, err := os.ReadFile(filepath.Join(outbox.Dir(), statusFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var status Status
	return &status, json.Unmarshal(data, &status)
}
