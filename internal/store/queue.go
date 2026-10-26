package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type WorkerInfo struct {
	Owner       string `json:"owner"`
	Concurrency int    `json:"concurrency"`
	Heartbeat   string `json:"heartbeat"`
}

func SaveWorkerInfo(root string, info WorkerInfo) error {
	return writeCreationJSON(filepath.Join(root, "worker.json"), info)
}

type QueueConfig struct {
	Version     int `json:"version"`
	Concurrency int `json:"concurrency"`
}

func QueueManaged(root string) (bool, error) {
	var cfg QueueConfig
	err := readJSON(filepath.Join(root, "queue.json"), &cfg)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if cfg.Version != 1 || cfg.Concurrency < 1 {
		return false, fmt.Errorf("invalid queue.json")
	}
	return true, nil
}

func ConfigureQueue(root string, concurrency int) error {
	if concurrency < 1 {
		return fmt.Errorf("concurrency must be positive")
	}
	return writeCreationJSON(filepath.Join(root, "queue.json"), QueueConfig{Version: 1, Concurrency: concurrency})
}

// EnableQueue opts the workspace into durable service admission even while its
// worker is offline. Only a service owner may change its concurrency setting.
func EnableQueue(ctx context.Context, root string) error {
	release, err := AcquireWorkspaceLock(ctx, root, "queue.lock", true)
	if err != nil {
		return err
	}
	defer release()
	managed, err := QueueManaged(root)
	if err != nil || managed {
		return err
	}
	return ConfigureQueue(root, 1)
}

func DispatchJobs(root string) ([]DispatchJob, error) {
	paths, err := filepath.Glob(filepath.Join(root, "dispatch", "*.json"))
	if err != nil {
		return nil, err
	}
	jobs := make([]DispatchJob, 0, len(paths))
	for _, path := range paths {
		var job DispatchJob
		if err := readJSON(path, &job); err != nil {
			return nil, err
		}
		if !validRunID(job.RunID) || job.Generation == 0 {
			return nil, fmt.Errorf("invalid queue job %s", path)
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].UpdatedAt == jobs[j].UpdatedAt {
			return jobs[i].RunID < jobs[j].RunID
		}
		return jobs[i].UpdatedAt < jobs[j].UpdatedAt
	})
	return jobs, nil
}

// WakeRun durably requests another admission. Generation is independent of
// completion so a submit racing with worker completion cannot be overwritten.
func WakeRun(ctx context.Context, root, runID, reason string) (DispatchJob, error) {
	release, err := AcquireWorkspaceLock(ctx, root, "queue.lock", true)
	if err != nil {
		return DispatchJob{}, err
	}
	defer release()
	return wakeRun(root, runID, reason)
}

func wakeRun(root, runID, reason string) (DispatchJob, error) {
	if !validRunID(runID) {
		return DispatchJob{}, fmt.Errorf("invalid run ID")
	}
	run, err := LoadRun(root, runID)
	if err != nil {
		return DispatchJob{}, err
	}
	var job DispatchJob
	err = readJSON(filepath.Join(root, "dispatch", runID+".json"), &job)
	if os.IsNotExist(err) {
		job = DispatchJob{RunID: runID, CreatedAt: run.State.CreatedAt}
	} else if err != nil {
		return job, err
	}
	job.Generation++
	job.Reason = reason
	job.Error = ""
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if job.Owner == "" {
		job.Status = "queued"
	}
	return job, SaveDispatchJob(root, job)
}

func ClaimDispatch(ctx context.Context, root, owner string, active map[string]bool, target string) (*DispatchJob, error) {
	release, err := AcquireWorkspaceLock(ctx, root, "queue.lock", true)
	if err != nil {
		return nil, err
	}
	defer release()
	jobs, err := DispatchJobs(root)
	if err != nil {
		return nil, err
	}
	indexed := map[string]bool{}
	for _, job := range jobs {
		indexed[job.RunID] = true
	}
	ids, err := RunIDs(root)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !indexed[id] && HasControls(filepath.Join(root, "runs", id)) {
			job, err := wakeRun(root, id, "recovered_control")
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, job)
		}
	}
	for _, job := range jobs {
		if active[job.RunID] || (target != "" && target != job.RunID) {
			continue
		}
		// A control can survive a crash between inbox publication and WakeRun.
		// Check even acknowledged jobs on every poll, not just at service startup.
		if job.Generation == job.Completed && HasControls(filepath.Join(root, "runs", job.RunID)) {
			job.Generation++
			job.Reason = "recovered_control"
		}
		if job.Generation <= job.Completed {
			continue
		}
		if job.Owner != "" && job.Owner != owner {
			job.Reason = "recovered_worker"
		}
		job.Owner = owner
		job.Claimed = job.Generation
		job.Status = "running"
		job.Error = ""
		job.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := SaveDispatchJob(root, job); err != nil {
			return nil, err
		}
		return &job, nil
	}
	return nil, nil
}

func FinishDispatch(ctx context.Context, root string, claim DispatchJob, status, message string) error {
	release, err := AcquireWorkspaceLock(ctx, root, "queue.lock", true)
	if err != nil {
		return err
	}
	defer release()
	var job DispatchJob
	if err := readJSON(filepath.Join(root, "dispatch", claim.RunID+".json"), &job); err != nil {
		return err
	}
	if job.Owner != claim.Owner || job.Claimed != claim.Claimed {
		return fmt.Errorf("dispatch claim changed")
	}
	job.Completed = claim.Claimed
	job.Owner = ""
	job.Claimed = 0
	job.Status = status
	job.Error = message
	if job.Generation > job.Completed {
		job.Status = "queued"
	}
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return SaveDispatchJob(root, job)
}

func LoadDispatch(root, runID string) (DispatchJob, error) {
	if !validRunID(runID) {
		return DispatchJob{}, fmt.Errorf("invalid run ID")
	}
	var job DispatchJob
	err := readJSON(filepath.Join(root, "dispatch", runID+".json"), &job)
	return job, err
}

func QueueSnapshot(root string) (map[string]any, error) {
	jobs, err := DispatchJobs(root)
	if err != nil {
		return nil, err
	}
	managed, err := QueueManaged(root)
	if err != nil {
		return nil, err
	}
	release, lockErr := AcquireWorkspaceLock(context.Background(), root, "worker.lock", false)
	online := false
	if lockErr == nil {
		release()
	} else if !errors.Is(lockErr, ErrWorkspaceBusy) {
		return nil, lockErr
	} else {
		online = true
	}
	var info WorkerInfo
	infoErr := readJSON(filepath.Join(root, "worker.json"), &info)
	if infoErr != nil && !os.IsNotExist(infoErr) {
		return nil, infoErr
	}
	counts := map[string]int{"queued": 0, "active": 0, "waiting": 0, "terminal": 0}
	for _, job := range jobs {
		switch {
		case job.Status == "running" && online && job.Owner == info.Owner:
			counts["active"]++
		case job.Generation > job.Completed:
			counts["queued"]++
		case job.Status == "waiting_input":
			counts["waiting"]++
		default:
			counts["terminal"]++
		}
	}
	result := map[string]any{"worker": info, "managed": managed, "worker_online": online, "counts": counts, "jobs": jobs}
	if managed {
		var cfg QueueConfig
		if err := readJSON(filepath.Join(root, "queue.json"), &cfg); err != nil {
			return nil, err
		}
		result["concurrency"] = cfg.Concurrency
	}
	return result, nil
}
