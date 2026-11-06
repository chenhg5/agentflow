package trigger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/store"
)

type Dispatcher struct {
	Root    string
	Config  Config
	Starter RunStarter
	mu      sync.Mutex
}

type DeliveryReceipt struct {
	TriggerID   string           `json:"trigger_id"`
	DeliveryID  string           `json:"delivery_id"`
	Status      string           `json:"status"`
	Run         RunRef           `json:"run"`
	Reason      string           `json:"reason,omitempty"`
	ReceivedAt  string           `json:"received_at"`
	ScheduledAt string           `json:"scheduled_at,omitempty"`
	Timezone    string           `json:"schedule_timezone,omitempty"`
	CatchUp     bool             `json:"catch_up,omitempty"`
	Command     *StartRunCommand `json:"command,omitempty"`
}

func (d *Dispatcher) Dispatch(ctx context.Context, event Event) (DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	release, err := acquireWorkspaceLock(ctx, d.Root, "trigger-dispatch.lock", true)
	if err != nil {
		return DispatchResult{}, err
	}
	defer release()
	definition, ok := findDefinition(d.Config, event.TriggerID)
	if !ok {
		return DispatchResult{}, fmt.Errorf("trigger not found: %s", event.TriggerID)
	}
	if !definition.Enabled {
		return DispatchResult{}, fmt.Errorf("trigger is disabled: %s", event.TriggerID)
	}
	if definition.Source.Kind != event.SourceKind {
		return DispatchResult{}, fmt.Errorf("trigger %s expects source %s, got %s", event.TriggerID, definition.Source.Kind, event.SourceKind)
	}
	if event.ReceivedAt.IsZero() {
		event.ReceivedAt = time.Now().UTC()
	}
	if event.SourceKind == "cron" {
		event.Timezone = definition.Source.Timezone
		if event.Timezone == "" {
			event.Timezone = "UTC"
		}
		if !event.Manual {
			if event.ScheduledAt == nil {
				return DispatchResult{}, fmt.Errorf("cron occurrence requires scheduled time")
			}
			event.DeliveryID = OccurrenceID(event.TriggerID, *event.ScheduledAt)
		}
	}
	if event.DeliveryID == "" {
		event.DeliveryID = generatedDeliveryID(event)
	}
	existing, err := d.loadReceipt(event.TriggerID, event.DeliveryID)
	if err != nil {
		return DispatchResult{}, err
	}
	var receipt DeliveryReceipt
	if existing != nil {
		receipt = *existing
		// Receipt facts are immutable, including an event-bound received_at.
		event.ReceivedAt, err = time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
		if err != nil {
			return DispatchResult{}, err
		}
		if receipt.Command != nil {
			event.ScheduledAt = receipt.Command.ScheduledAt
			event.Timezone = receipt.Command.Timezone
			event.CatchUp = receipt.Command.CatchUp
		}
		if receipt.Status != "pending" {
			if receipt.Run.RunID != "" && (event.SourceKind != "cron" || event.Manual) {
				inputs, err := ResolveInputs(definition, event)
				if err != nil {
					return DispatchResult{}, err
				}
				command := StartRunCommand{WorkflowPath: definition.Target.Workflow, Inputs: inputs}
				if err := d.Starter.ValidateReplay(ctx, command, receipt.Run); err != nil {
					return DispatchResult{}, err
				}
			}
			return receipt.result(true), nil
		}
		if receipt.Command == nil {
			return DispatchResult{}, fmt.Errorf("pending receipt has no prepared command")
		}
		if event.SourceKind != "cron" || event.Manual {
			inputs, err := ResolveInputs(definition, event)
			if err != nil {
				return DispatchResult{}, err
			}
			wanted, _ := json.Marshal(inputs)
			prepared, _ := json.Marshal(receipt.Command.Inputs)
			if string(wanted) != string(prepared) {
				return DispatchResult{}, fmt.Errorf("%w: pending trigger delivery", store.ErrIdempotencyConflict)
			}
		}
	} else {
		receipt = DeliveryReceipt{TriggerID: definition.ID, DeliveryID: event.DeliveryID, ReceivedAt: event.ReceivedAt.UTC().Format(time.RFC3339Nano), Timezone: event.Timezone, CatchUp: event.CatchUp}
		if event.ScheduledAt != nil {
			receipt.ScheduledAt = event.ScheduledAt.UTC().Format(time.RFC3339Nano)
		}
		if !strings.EqualFold(definition.Policy.Concurrency, "allow") {
			active, err := d.hasActiveRun(definition.ID)
			if err != nil {
				return DispatchResult{}, err
			}
			if active {
				receipt.Status, receipt.Reason = "skipped", "active_run"
				return receipt.result(false), d.saveReceipt(receipt)
			}
		}
		inputs, err := ResolveInputs(definition, event)
		if err != nil {
			return DispatchResult{}, err
		}
		command := StartRunCommand{WorkflowPath: definition.Target.Workflow, Inputs: inputs, TriggerID: definition.ID, TriggerKind: event.SourceKind, DeliveryID: event.DeliveryID, ScheduledAt: event.ScheduledAt, ReceivedAt: event.ReceivedAt, IdempotencyKey: deliveryKey(definition.ID, event.DeliveryID), Timezone: event.Timezone, CatchUp: event.CatchUp}
		command, err = d.Starter.Prepare(ctx, command)
		if err != nil {
			return DispatchResult{}, err
		}
		receipt.Status, receipt.Command = "pending", &command
		if err := d.saveReceipt(receipt); err != nil {
			return DispatchResult{}, err
		}
		triggerBoundary("receipt_pending")
	}
	// A durable pending intent already passed concurrency policy. Recover it
	// before testing forbid against the run that this same intent created.
	run, err := d.Starter.Start(ctx, *receipt.Command)
	if err != nil {
		return DispatchResult{}, err
	}
	triggerBoundary("run_created")
	receipt.Status, receipt.Run = "queued", run
	if err := d.saveReceipt(receipt); err != nil {
		return DispatchResult{}, err
	}
	triggerBoundary("receipt_saved")
	return receipt.result(existing != nil), nil
}

func (r DeliveryReceipt) result(duplicate bool) DispatchResult {
	return DispatchResult{TriggerID: r.TriggerID, DeliveryID: r.DeliveryID, Status: r.Status, Run: r.Run, Reason: r.Reason, Duplicate: duplicate}
}

func findDefinition(config Config, id string) (Definition, bool) {
	for _, definition := range config.Triggers {
		if definition.ID == id {
			return definition, true
		}
	}
	return Definition{}, false
}

func generatedDeliveryID(event Event) string {
	hash := sha256.Sum256(append([]byte(event.TriggerID+":"+event.SourceKind+":"+event.ReceivedAt.UTC().Format(time.RFC3339Nano)), event.Payload...))
	return hex.EncodeToString(hash[:])
}

func deliveryKey(triggerID, deliveryID string) string {
	encoded, _ := json.Marshal([]string{triggerID, deliveryID})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func (d *Dispatcher) receiptPath(triggerID, deliveryID string) string {
	return filepath.Join(d.Root, "trigger-deliveries", deliveryKey(triggerID, deliveryID)+".json")
}

func (d *Dispatcher) loadReceipt(triggerID, deliveryID string) (*DeliveryReceipt, error) {
	var receipt DeliveryReceipt
	if err := store.ReadAtomicJSON(d.receiptPath(triggerID, deliveryID), &receipt); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if receipt.TriggerID != triggerID || receipt.DeliveryID != deliveryID || (receipt.Status != "pending" && receipt.Status != "queued" && receipt.Status != "skipped") || (receipt.Status == "queued" && receipt.Run.RunID == "") {
		return nil, fmt.Errorf("invalid trigger delivery receipt")
	}
	return &receipt, nil
}

func (d *Dispatcher) saveReceipt(receipt DeliveryReceipt) error {
	return store.WriteAtomicJSON(d.receiptPath(receipt.TriggerID, receipt.DeliveryID), receipt)
}

func DeliveryStatus(root string) ([]DeliveryReceipt, error) {
	paths, err := filepath.Glob(filepath.Join(root, "trigger-deliveries", "*.json"))
	if err != nil {
		return nil, err
	}
	out := []DeliveryReceipt{}
	for _, path := range paths {
		var receipt DeliveryReceipt
		if err := store.ReadAtomicJSON(path, &receipt); err != nil {
			return nil, err
		}
		out = append(out, receipt)
	}
	return out, nil
}

// DeliveryView is a delivery as `trigger status` shows it. Receipts are
// written once, when the delivery creates its run, so their run status is
// the one at that moment; the view reads the run's current state instead.
type DeliveryView struct {
	DeliveryReceipt
	// RunStatus is the run's current status, "missing" when its run
	// directory is gone, or empty when the delivery created no run.
	RunStatus string `json:"run_status,omitempty"`
}

// DeliveryStatusView lists deliveries newest first. Status reads "delivered"
// once a run exists (a receipt stores that as "queued", meaning handed to the
// run queue, which says nothing about the run now), and Run.Status and
// RunStatus carry the run's current status.
func DeliveryStatusView(root string) ([]DeliveryView, error) {
	receipts, err := DeliveryStatus(root)
	if err != nil {
		return nil, err
	}
	views := make([]DeliveryView, 0, len(receipts))
	for _, receipt := range receipts {
		view := DeliveryView{DeliveryReceipt: receipt}
		if receipt.Run.RunID != "" {
			if receipt.Status == "queued" {
				view.Status = "delivered"
			}
			run, err := store.LoadRun(root, receipt.Run.RunID)
			switch {
			case err == nil:
				view.RunStatus = run.State.Status
			case os.IsNotExist(err) || errors.Is(err, os.ErrNotExist):
				view.RunStatus = "missing"
			default:
				view.RunStatus = "unreadable"
			}
			view.Run.Status = view.RunStatus
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].ReceivedAt > views[j].ReceivedAt })
	return views, nil
}

// hasActiveRun reports whether triggerID has a delivery still being created
// or a run not finished. Every run a trigger starts goes through a delivery
// receipt, so only the runs those receipts name are read, and only their
// status: reading every run in the workspace made each dispatch slower as
// history grew.
func (d *Dispatcher) hasActiveRun(triggerID string) (bool, error) {
	receipts, err := DeliveryStatus(d.Root)
	if err != nil {
		return false, err
	}
	for _, receipt := range receipts {
		if receipt.TriggerID != triggerID {
			continue
		}
		if receipt.Status == "pending" {
			return true, nil
		}
		if receipt.Run.RunID == "" || store.DispatchSettled(d.Root, receipt.Run.RunID) {
			continue // no run, or its queue job settled: finished
		}
		run, err := store.LoadRunSummary(d.Root, receipt.Run.RunID)
		if errors.Is(err, os.ErrNotExist) {
			continue // the run was deleted: nothing of it is active
		}
		if err != nil {
			return false, err
		}
		if run.Trigger != nil && run.Trigger.ID != triggerID {
			continue
		}
		switch run.Status {
		case "pending", "queued", "running", "waiting_input", "waiting_resource":
			return true, nil
		}
	}
	return false, nil
}

func (d *Dispatcher) Trigger(id string) (Definition, error) {
	definition, ok := findDefinition(d.Config, id)
	if !ok {
		return Definition{}, fmt.Errorf("trigger not found: %s", id)
	}
	return definition, nil
}
