package telemetry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/agentflow/internal/hubapi"
)

// Outbox keeps the latest undelivered report of each run on disk, one file per
// run. Writing a newer report replaces the older one, so the outbox never grows
// beyond one file per run that is waiting for delivery.
type Outbox struct {
	dir string
}

// NewOutbox uses <root>/outbox.
func NewOutbox(root string) *Outbox { return &Outbox{dir: filepath.Join(root, "outbox")} }

// Dir is where pending reports live.
func (o *Outbox) Dir() string { return o.dir }

// Put stores report unless a newer report of the same run is already waiting.
func (o *Outbox) Put(report hubapi.RunReport) error {
	if err := os.MkdirAll(o.dir, 0o755); err != nil {
		return err
	}
	file := o.file(report.RunID)
	if current, err := readReport(file); err == nil && current.Seq > report.Seq {
		return nil
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	temporary := file + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, file)
}

// Pending returns waiting reports, oldest first.
func (o *Outbox) Pending() ([]hubapi.RunReport, error) {
	entries, err := os.ReadDir(o.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	reports := []hubapi.RunReport{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		report, err := readReport(filepath.Join(o.dir, entry.Name()))
		if err != nil {
			continue // a half-written or foreign file: the next Put replaces it
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Seq < reports[j].Seq })
	return reports, nil
}

// Done removes a delivered report, unless a newer one arrived meanwhile.
func (o *Outbox) Done(report hubapi.RunReport) error {
	file := o.file(report.RunID)
	current, err := readReport(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil && current.Seq > report.Seq {
		return nil
	}
	return os.Remove(file)
}

func (o *Outbox) file(runID string) string {
	return filepath.Join(o.dir, filepath.Base(runID)+".json")
}

func readReport(file string) (hubapi.RunReport, error) {
	var report hubapi.RunReport
	data, err := os.ReadFile(file)
	if err != nil {
		return report, err
	}
	err = json.Unmarshal(data, &report)
	return report, err
}
