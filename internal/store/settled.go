package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Settled queue jobs.
//
// A job whose run finished and that has no outstanding generation is never
// claimed again, yet it used to stay in dispatch/ and every queue poll read it.
// Polls held the queue lock for time proportional to every run the workspace
// ever had (about 11 ms per poll at 800 runs, 80 ms at 5,000), so an idle
// worker burned CPU and blocked submits as history grew.
//
// Such jobs now move to dispatch/settled/. Polls read only live jobs. Readers
// that look a job up by run (wake, `run get`, creation recovery) check both
// places, and a wake moves a settled job back. The one thing a poll used to do
// for every run, finding a control published by a producer that died before
// waking the run, now happens at a worker's first poll and every
// recoveryInterval after; it only matters after a crash.

const settledDir = "settled"

// recoveryInterval spaces the full scan for orphaned controls.
const recoveryInterval = 30 * time.Second

var (
	recoveryMu   sync.Mutex
	lastRecovery = map[string]time.Time{}
)

func settledDispatchPath(root, runID string) string {
	return filepath.Join(root, "dispatch", settledDir, runID+".json")
}

// settledStatus reports whether a job is finished and owes nothing: no
// outstanding generation, no owner, and a terminal status.
func settledStatus(job DispatchJob) bool {
	if job.Generation > job.Completed || job.Owner != "" {
		return false
	}
	switch job.Status {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// settleDispatch moves job to dispatch/settled/. It writes the job's final
// state in place and then renames the file, never deletes it: on Windows a
// file deleted while another process reads it stays "delete pending", and
// every open of that name fails with access denied until the reader closes it
// (concurrent `run start` and `run get` hit exactly that). A rename takes the
// name away at once, and readers find the job under its settled name.
func settleDispatch(root string, job DispatchJob) error {
	if err := os.MkdirAll(filepath.Join(root, "dispatch", settledDir), 0o755); err != nil {
		return err
	}
	if err := SaveDispatchJob(root, job); err != nil {
		return err
	}
	return replaceStoreFile(filepath.Join(root, "dispatch", job.RunID+".json"), settledDispatchPath(root, job.RunID))
}

// unsettleDispatch moves a settled job back to the live queue, by rename for
// the same reason.
func unsettleDispatch(root, runID string) error {
	err := replaceStoreFile(settledDispatchPath(root, runID), filepath.Join(root, "dispatch", runID+".json"))
	if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// readDispatch reads a run's job, live first; settled reports which copy.
func readDispatch(root, runID string) (DispatchJob, bool, error) {
	var job DispatchJob
	err := readJSON(filepath.Join(root, "dispatch", runID+".json"), &job)
	if !os.IsNotExist(err) {
		return job, false, err
	}
	job = DispatchJob{}
	err = readJSON(settledDispatchPath(root, runID), &job)
	return job, err == nil, err
}

func settledDispatchCount(root string) (int, error) {
	entries, err := os.ReadDir(filepath.Join(root, "dispatch", settledDir))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

func recoveryDue(root string) bool {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	now := time.Now()
	if last, ok := lastRecovery[root]; ok && now.Sub(last) < recoveryInterval {
		return false
	}
	lastRecovery[root] = now
	return true
}

// recoverPublishedControls wakes settled or unqueued runs that hold a
// published control: the producer died between writing the control and
// waking the run. Live jobs are checked by every poll.
func recoverPublishedControls(root string, live []DispatchJob) ([]DispatchJob, error) {
	pending := map[string]bool{}
	for _, job := range live {
		pending[job.RunID] = true
	}
	ids, err := RunIDs(root)
	if err != nil {
		return nil, err
	}
	var woken []DispatchJob
	for _, id := range ids {
		if pending[id] || !HasControls(filepath.Join(root, "runs", id)) {
			continue
		}
		job, err := wakeRun(root, id, "recovered_control")
		if err != nil {
			return nil, err
		}
		woken = append(woken, job)
	}
	return woken, nil
}

// DispatchSettled reports whether runID's queue job settled: the run finished
// and owes nothing. A stat, so callers can skip reading the run's state.
func DispatchSettled(root, runID string) bool {
	if !validRunID(runID) {
		return false
	}
	if _, err := os.Stat(filepath.Join(root, "dispatch", runID+".json")); err == nil {
		return false // a live copy wins over a settled one
	}
	_, err := os.Stat(settledDispatchPath(root, runID))
	return err == nil
}
