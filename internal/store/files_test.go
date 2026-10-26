package store

import (
	"io"
	"path/filepath"
	"testing"
)

func TestAtomicCheckpointWithOpenReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeCreationJSON(path, map[string]int{"value": 1}); err != nil {
		t.Fatal(err)
	}
	reader, err := openStoreFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := writeJSON(path, map[string]int{"value": 2}); err != nil {
		t.Fatalf("open observer prevented checkpoint: %v", err)
	}
	old, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(old) != "{\"value\":1}\n" {
		t.Fatalf("observer did not retain old snapshot: %s", old)
	}
	var current map[string]int
	if err := readJSON(path, &current); err != nil || current["value"] != 2 {
		t.Fatalf("new checkpoint missing: %+v %v", current, err)
	}
	if err := writeCreationJSON(path, map[string]int{"value": 3}); err != nil {
		t.Fatalf("open observer prevented queue/journal checkpoint: %v", err)
	}
}
