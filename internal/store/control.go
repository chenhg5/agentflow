package store

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

const controlDir = "control"

// EnqueueControl records a request for the run's executor. Requests are
// written atomically so an executor never reads a partial file.
func EnqueueControl(runDir string, control model.Control) error {
	if control.At == "" {
		control.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	name := time.Now().UTC().Format("20060102T150405.000000000") + "-" + randomSuffix() + ".json"
	return writeJSON(filepath.Join(runDir, controlDir, name), control)
}

// QueuedControl pairs a control with the queue file that holds it so the
// caller can acknowledge delivery only after its durable state checkpoint.
type QueuedControl struct {
	Name    string
	Control model.Control
}

// PeekControls returns queued requests in arrival order without removing
// them. Acknowledging a request is a separate step, so a failed state write
// leaves the request available for retry.
func PeekControls(runDir string) ([]QueuedControl, error) {
	directory := filepath.Join(runDir, controlDir)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	controls := make([]QueuedControl, 0, len(names))
	for _, name := range names {
		var control model.Control
		if err := readJSON(filepath.Join(directory, name), &control); err != nil {
			return controls, err
		}
		controls = append(controls, QueuedControl{Name: name, Control: control})
	}
	return controls, nil
}

// AcknowledgeControl removes a delivered request after its effects are
// durable.
func AcknowledgeControl(runDir, name string) error {
	err := os.Remove(filepath.Join(runDir, controlDir, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// HasControls reports whether requests are waiting for an executor.
func HasControls(runDir string) bool {
	entries, err := os.ReadDir(filepath.Join(runDir, controlDir))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			return true
		}
	}
	return false
}
