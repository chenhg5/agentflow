package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

var ErrIdempotencyConflict = errors.New("idempotency key belongs to a different request")

type CreateRequest struct {
	Workflow       model.Workflow     `json:"workflow"`
	Inputs         map[string]string  `json:"inputs"`
	IdempotencyKey string             `json:"idempotencyKey,omitempty"`
	Trigger        *model.TriggerInfo `json:"trigger,omitempty"`
	ParentRunID    string             `json:"parentRunId,omitempty"`
	ParentNodeID   string             `json:"parentNodeId,omitempty"`
	Correlation    map[string]string  `json:"correlation,omitempty"`
	Queued         bool               `json:"queued,omitempty"`
}

type CreateResult struct {
	Run     *model.Run
	Created bool
}

type creationRecord struct {
	Request   CreateRequest `json:"request"`
	Digest    string        `json:"digest"`
	RunID     string        `json:"runId"`
	CreatedAt string        `json:"createdAt"`
	Published bool          `json:"published"`
}

// DispatchJob is the persistent execution outbox projection. The initial copy
// is published with the run, before any worker may execute it.
type DispatchJob struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Generation uint64 `json:"generation"`
	Completed  uint64 `json:"completed"`
	Claimed    uint64 `json:"claimed,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Reason     string `json:"reason,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Tests use this private hook in isolated processes to interrupt exact commit
// boundaries. Production does not read an environment variable or enable it.
var creationCheckpoint func(string)

func creationBoundary(stage string) {
	if creationCheckpoint != nil {
		creationCheckpoint(stage)
	}
}

// CreateOrGetRun serializes only creation. It never holds the creation lock
// while executing a node. The journal fixes the run ID before publishing any
// snapshot, and a directory rename makes a complete run visible atomically.
func CreateOrGetRun(ctx context.Context, root string, request CreateRequest) (CreateResult, error) {
	if request.Workflow.Version != "3" || request.Workflow.Entry == "" {
		return CreateResult{}, fmt.Errorf("only version 3 edge workflows can start")
	}
	release, err := acquireCreationLock(ctx, root)
	if err != nil {
		return CreateResult{}, err
	}
	defer release()
	if err := validateParent(root, request); err != nil {
		return CreateResult{}, err
	}
	digest, err := creationDigest(request)
	if err != nil {
		return CreateResult{}, err
	}
	runID := "run_" + time.Now().UTC().Format("20060102150405") + "_" + randomSuffix()
	identity := request.IdempotencyKey
	if identity == "" {
		identity = runID
	}
	kind := "key"
	if request.IdempotencyKey == "" {
		kind = "unkeyed"
	}
	keyData, _ := json.Marshal([]string{request.Workflow.Name, kind, identity})
	hash := sha256.Sum256(keyData)
	key := hex.EncodeToString(hash[:])
	path := filepath.Join(root, "runs", "requests", key+".json")
	var record creationRecord
	err = readJSON(path, &record)
	if err == nil {
		if record.Digest != digest {
			return CreateResult{}, fmt.Errorf("%w: run %s", ErrIdempotencyConflict, record.RunID)
		}
	} else if !os.IsNotExist(err) {
		return CreateResult{}, err
	} else {
		// Existing run state remains authoritative when a request has not yet
		// acquired an index (including runs created before this index existed).
		if request.IdempotencyKey != "" {
			existing, findErr := FindRunByIdempotency(root, request.Workflow.Name, request.IdempotencyKey)
			if findErr != nil {
				return CreateResult{}, findErr
			}
			if existing != nil {
				oldDigest, err := creationDigest(requestForRun(existing))
				if err != nil {
					return CreateResult{}, err
				}
				if oldDigest != digest {
					return CreateResult{}, fmt.Errorf("%w: run %s", ErrIdempotencyConflict, existing.RunID)
				}
				return CreateResult{Run: existing}, nil
			}
		}
		record = creationRecord{Request: request, Digest: digest, RunID: runID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := writeCreationJSON(path, record); err != nil {
			return CreateResult{}, err
		}
		creationBoundary("reserved")
	}
	return finishCreation(root, path, record)
}

func creationDigest(request CreateRequest) (string, error) {
	inputs := request.Inputs
	if len(inputs) == 0 {
		inputs = map[string]string{}
	}
	data, err := json.Marshal(struct {
		Workflow     model.Workflow    `json:"workflow"`
		Inputs       map[string]string `json:"inputs"`
		ParentRunID  string            `json:"parentRunId,omitempty"`
		ParentNodeID string            `json:"parentNodeId,omitempty"`
		Correlation  map[string]string `json:"correlation,omitempty"`
	}{request.Workflow, inputs, request.ParentRunID, request.ParentNodeID, request.Correlation})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func ValidateRunRequest(root, runID string, request CreateRequest) error {
	run, err := LoadRun(root, runID)
	if err != nil {
		return err
	}
	want, err := creationDigest(request)
	if err != nil {
		return err
	}
	actual, err := creationDigest(requestForRun(run))
	if err != nil {
		return err
	}
	if actual != want {
		return fmt.Errorf("%w: run %s", ErrIdempotencyConflict, runID)
	}
	return nil
}

func finishCreation(root, path string, record creationRecord) (CreateResult, error) {
	if record.RunID == "" || filepath.Base(record.RunID) != record.RunID || strings.ContainsAny(record.RunID, `/\`) || !strings.HasPrefix(record.RunID, "run_") {
		return CreateResult{}, fmt.Errorf("invalid run creation journal: %s", path)
	}
	runDir := filepath.Join(root, "runs", record.RunID)
	_, statErr := os.Stat(runDir)
	created := os.IsNotExist(statErr)
	if statErr != nil && !created {
		return CreateResult{}, statErr
	}
	if created {
		if record.Published {
			return CreateResult{}, fmt.Errorf("committed run %s is missing; refusing to recreate it", record.RunID)
		}
		stage := path + ".staging"
		if err := os.RemoveAll(stage); err != nil {
			return CreateResult{}, err
		}
		if err := EnsureDir(filepath.Join(stage, "artifacts")); err != nil {
			return CreateResult{}, err
		}
		run := initialCreationRun(stage, record)
		for name, value := range map[string]any{"workflow.json": run.Workflow, "state.json": run.State} {
			if err := writeCreationJSON(filepath.Join(stage, name), value); err != nil {
				return CreateResult{}, err
			}
		}
		var events bytes.Buffer
		encoder := json.NewEncoder(&events)
		if err := encoder.Encode(map[string]any{"type": "run.created", "runId": run.RunID, "inputs": run.State.Inputs, "at": record.CreatedAt}); err != nil {
			return CreateResult{}, err
		}
		if record.Request.Queued {
			event := map[string]any{"type": "run.queued", "runId": run.RunID, "at": record.CreatedAt}
			if record.Request.Trigger != nil {
				event["triggerId"] = record.Request.Trigger.ID
				event["deliveryId"] = record.Request.Trigger.DeliveryID
			}
			if err := encoder.Encode(event); err != nil {
				return CreateResult{}, err
			}
		}
		if err := writeCreationBytes(filepath.Join(stage, "events.jsonl"), events.Bytes()); err != nil {
			return CreateResult{}, err
		}
		if record.Request.Queued {
			job := DispatchJob{RunID: record.RunID, Status: "queued", Generation: 1, Reason: "created", CreatedAt: record.CreatedAt, UpdatedAt: record.CreatedAt}
			if err := writeCreationJSON(filepath.Join(stage, "dispatch.json"), job); err != nil {
				return CreateResult{}, err
			}
		}
		creationBoundary("snapshot")
		if err := os.Rename(stage, runDir); err != nil {
			return CreateResult{}, err
		}
		if err := syncCreationDirectory(filepath.Dir(runDir)); err != nil {
			return CreateResult{}, err
		}
		creationBoundary("published")
	}
	if !record.Published {
		record.Published = true
		if err := writeCreationJSON(path, record); err != nil {
			return CreateResult{}, err
		}
	}
	if record.Request.Queued {
		if err := recoverCreationDispatch(root, runDir, record.RunID); err != nil {
			return CreateResult{}, err
		}
		creationBoundary("queued")
	}
	run, err := LoadRun(root, record.RunID)
	return CreateResult{Run: run, Created: created}, err
}

func initialCreationRun(runDir string, record creationRecord) *model.Run {
	r := record.Request
	nodes := map[string]*model.NodeState{}
	for _, node := range r.Workflow.Nodes {
		nodes[node.ID] = &model.NodeState{Status: "pending", Artifacts: map[string]string{}}
	}
	input := map[string]any{}
	for k, v := range r.Inputs {
		input[k] = v
	}
	nodes[r.Workflow.Entry].Input = input
	status := "pending"
	if r.Queued {
		status = "queued"
	}
	return &model.Run{RunID: record.RunID, RunDir: runDir, Workflow: r.Workflow, State: model.RunState{
		RunID: record.RunID, WorkflowName: r.Workflow.Name, WorkflowDigest: model.Digest(r.Workflow), Status: status,
		ParentRunID: r.ParentRunID, ParentNodeID: r.ParentNodeID, Correlation: r.Correlation,
		Inputs: r.Inputs, IdempotencyKey: r.IdempotencyKey, Trigger: r.Trigger, Nodes: nodes,
		CreatedAt: record.CreatedAt, UpdatedAt: record.CreatedAt, ActiveNode: r.Workflow.Entry,
		Visits: map[string]int{r.Workflow.Entry: 1}, Traversals: map[string]int{},
	}}
}

func recoverCreationDispatch(root, runDir, runID string) error {
	release, err := AcquireWorkspaceLock(context.Background(), root, "queue.lock", true)
	if err != nil {
		return err
	}
	defer release()
	path := filepath.Join(root, "dispatch", runID+".json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var job DispatchJob
	if err := readJSON(filepath.Join(runDir, "dispatch.json"), &job); err != nil {
		return err
	}
	return writeCreationJSON(path, job)
}

func SaveDispatchJob(root string, job DispatchJob) error {
	if filepath.Base(job.RunID) != job.RunID || !strings.HasPrefix(job.RunID, "run_") {
		return fmt.Errorf("invalid dispatch run ID")
	}
	return writeCreationJSON(filepath.Join(root, "dispatch", job.RunID+".json"), job)
}

// RecoverRunCreations completes interrupted publication and execution outboxes.
// It does not execute a node or turn terminal runs back into pending runs.
func RecoverRunCreations(ctx context.Context, root string) error {
	release, err := acquireCreationLock(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	paths, err := filepath.Glob(filepath.Join(root, "runs", "requests", "*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		var record creationRecord
		if err := readJSON(path, &record); err != nil {
			return err
		}
		if _, err := finishCreation(root, path, record); err != nil {
			return err
		}
	}
	return nil
}

func writeCreationJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeCreationBytes(path, append(data, '\n'))
}

func writeCreationBytes(path string, data []byte) error {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".create-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := replaceStoreFile(name, path); err != nil {
		return err
	}
	return syncCreationDirectory(filepath.Dir(path))
}

func acquireCreationLock(ctx context.Context, root string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := EnsureDir(filepath.Join(root, "runs", "requests")); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, "runs", "creation.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := tryCreationLock(file); err == nil {
			return func() { _ = file.Close() }, nil
		} else if !creationLockBusy(err) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
