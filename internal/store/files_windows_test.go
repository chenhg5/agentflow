//go:build windows

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointWaitsForExternalWindowsReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeCreationJSON(path, map[string]int{"value": 1}); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- writeCreationJSON(path, map[string]int{"value": 2}) }()
	time.Sleep(50 * time.Millisecond)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("checkpoint did not recover after external reader closed")
	}
}

func TestWindowsCheckpointPathLengths(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 96; i++ {
		path := filepath.Join(root, strings.Repeat("字", i)+"-🙂.json")
		for value := 1; value <= 2; value++ {
			if err := writeCreationJSON(path, map[string]int{"value": value}); err != nil {
				t.Fatalf("path length %d: %v", i, err)
			}
			var got map[string]int
			if err := readJSON(path, &got); err != nil || got["value"] != value {
				t.Fatalf("path length %d: %+v %v", i, got, err)
			}
		}
	}
}
