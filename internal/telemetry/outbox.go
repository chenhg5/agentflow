package telemetry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/chenhg5/agentflow/internal/hubapi"
)

// Outbox keeps undelivered reports on disk. Every snapshot gets its own file,
// <run-id>.<seq>.json, written under a unique temporary name and renamed into
// place, so concurrent writers (the engine, a background flush, another
// agentflow process acting on the same run) never overwrite or delete a newer
// snapshot. Older snapshots of a run are pruned as newer ones arrive, so the
// outbox stays at about one file per run waiting for delivery.
type Outbox struct {
	dir string
}

// NewOutbox uses <root>/outbox.
func NewOutbox(root string) *Outbox { return &Outbox{dir: filepath.Join(root, "outbox")} }

// Dir is where pending reports live.
func (o *Outbox) Dir() string { return o.dir }

type entry struct {
	run  string
	seq  int64
	path string
}

// Put stores report and prunes older snapshots of the same run.
func (o *Outbox) Put(report hubapi.RunReport) error {
	if err := os.MkdirAll(o.dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(o.dir, ".put-*.tmp")
	if err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		os.Remove(temporary.Name())
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporary.Name())
		return err
	}
	name := runName(report.RunID)
	if err := os.Rename(temporary.Name(), filepath.Join(o.dir, name+"."+strconv.FormatInt(report.Seq, 10)+".json")); err != nil {
		os.Remove(temporary.Name())
		return err
	}
	o.prune(name, report.Seq-1)
	return nil
}

// Pending returns the newest waiting report of each run, oldest first.
func (o *Outbox) Pending() ([]hubapi.RunReport, error) {
	entries, err := o.entries()
	if err != nil {
		return nil, err
	}
	newest := map[string]entry{}
	for _, item := range entries {
		if current, ok := newest[item.run]; !ok || item.seq > current.seq {
			newest[item.run] = item
		}
	}
	reports := []hubapi.RunReport{}
	for _, item := range newest {
		report, err := readReport(item.path)
		if err != nil {
			continue // removed meanwhile, or not ours: the next Pending sees the state
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Seq < reports[j].Seq })
	return reports, nil
}

// Done removes a delivered report and anything older of the same run. A newer
// snapshot written meanwhile has a higher seq and stays.
func (o *Outbox) Done(report hubapi.RunReport) error {
	return o.prune(runName(report.RunID), report.Seq)
}

// prune removes the snapshots of run with seq <= upTo.
func (o *Outbox) prune(run string, upTo int64) error {
	entries, err := o.entries()
	if err != nil {
		return err
	}
	var failed error
	for _, item := range entries {
		if item.run == run && item.seq <= upTo {
			if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				failed = err // e.g. Windows: another process has it open; retried on the next flush
			}
		}
	}
	return failed
}

func (o *Outbox) entries() ([]entry, error) {
	files, err := os.ReadDir(o.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(files))
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		stem := strings.TrimSuffix(name, ".json")
		dot := strings.LastIndexByte(stem, '.')
		if dot <= 0 {
			continue
		}
		seq, err := strconv.ParseInt(stem[dot+1:], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, entry{run: stem[:dot], seq: seq, path: filepath.Join(o.dir, name)})
	}
	return out, nil
}

// runName keeps a run id from escaping the outbox directory.
func runName(runID string) string {
	name := filepath.Base(runID)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "_"
	}
	return name
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
