package trigger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	TriggerID  string `json:"trigger_id"`
	DeliveryID string `json:"delivery_id"`
	Status     string `json:"status"`
	Run        RunRef `json:"run"`
	Reason     string `json:"reason,omitempty"`
	ReceivedAt string `json:"received_at"`
}

func (d *Dispatcher) Dispatch(ctx context.Context, event Event) (DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	if event.DeliveryID == "" {
		event.DeliveryID = generatedDeliveryID(event)
	}
	if existing, err := d.loadReceipt(event.DeliveryID); err != nil {
		return DispatchResult{}, err
	} else if existing != nil {
		return DispatchResult{TriggerID: existing.TriggerID, DeliveryID: existing.DeliveryID, Status: existing.Status, Run: existing.Run, Duplicate: true, Reason: existing.Reason}, nil
	}
	if strings.EqualFold(definition.Policy.Concurrency, "forbid") {
		active, err := d.hasActiveRun(definition.ID)
		if err != nil {
			return DispatchResult{}, err
		}
		if active {
			result := DispatchResult{TriggerID: definition.ID, DeliveryID: event.DeliveryID, Status: "skipped", Reason: "active_run"}
			return result, d.saveReceipt(DeliveryReceipt{TriggerID: definition.ID, DeliveryID: event.DeliveryID, Status: result.Status, Reason: result.Reason, ReceivedAt: event.ReceivedAt.UTC().Format(time.RFC3339Nano)})
		}
	}
	inputs, err := ResolveInputs(definition, event)
	if err != nil {
		return DispatchResult{}, err
	}
	command := StartRunCommand{WorkflowPath: definition.Target.Workflow, Inputs: inputs, TriggerID: definition.ID, TriggerKind: event.SourceKind, DeliveryID: event.DeliveryID, ScheduledAt: event.ScheduledAt, ReceivedAt: event.ReceivedAt, IdempotencyKey: definition.ID + ":" + event.DeliveryID}
	run, err := d.Starter.Start(ctx, command)
	if err != nil {
		return DispatchResult{}, err
	}
	result := DispatchResult{TriggerID: definition.ID, DeliveryID: event.DeliveryID, Status: "queued", Run: run}
	if err := d.saveReceipt(DeliveryReceipt{TriggerID: definition.ID, DeliveryID: event.DeliveryID, Status: result.Status, Run: run, ReceivedAt: event.ReceivedAt.UTC().Format(time.RFC3339Nano)}); err != nil {
		return DispatchResult{}, err
	}
	return result, nil
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

func (d *Dispatcher) receiptPath(deliveryID string) string {
	hash := sha256.Sum256([]byte(deliveryID))
	return filepath.Join(d.Root, "trigger-deliveries", hex.EncodeToString(hash[:])+".json")
}

func (d *Dispatcher) loadReceipt(deliveryID string) (*DeliveryReceipt, error) {
	data, err := os.ReadFile(d.receiptPath(deliveryID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt DeliveryReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (d *Dispatcher) saveReceipt(receipt DeliveryReceipt) error {
	path := d.receiptPath(receipt.DeliveryID)
	if err := store.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (d *Dispatcher) hasActiveRun(triggerID string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(d.Root, "runs"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		run, err := store.LoadRun(d.Root, entry.Name())
		if err != nil {
			return false, err
		}
		if run.State.Trigger == nil || run.State.Trigger.ID != triggerID {
			continue
		}
		switch run.State.Status {
		case "pending", "queued", "running", "waiting_human":
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
