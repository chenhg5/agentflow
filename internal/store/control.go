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

// TakeControls returns queued requests in arrival order and removes them.
func TakeControls(runDir string) ([]model.Control, error) {
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
	controls := make([]model.Control, 0, len(names))
	for _, name := range names {
		path := filepath.Join(directory, name)
		var control model.Control
		if err := readJSON(path, &control); err != nil {
			return controls, err
		}
		if err := os.Remove(path); err != nil {
			return controls, err
		}
		controls = append(controls, control)
	}
	return controls, nil
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
