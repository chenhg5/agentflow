package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

var ErrResourceUnavailable = errors.New("resources are unavailable")

type ResourceDefinition struct {
	Capacity int `json:"capacity"`
}
type ResourceConfig struct {
	Resources map[string]ResourceDefinition `json:"resources"`
}
type ResourceClaim struct {
	ID         string                  `json:"id"`
	OwnerPID   int                     `json:"ownerPid"`
	ProcessPID int                     `json:"processPid,omitempty"`
	RunID      string                  `json:"runId"`
	NodeID     string                  `json:"nodeId"`
	Attempt    int                     `json:"attempt"`
	Token      string                  `json:"token"`
	Requests   []model.ResourceRequest `json:"requests"`
	Status     string                  `json:"status"`
	CreatedAt  string                  `json:"createdAt"`
	UpdatedAt  string                  `json:"updatedAt"`
	Reason     string                  `json:"reason,omitempty"`
}
type ResourceSnapshot struct {
	Resources map[string]ResourceDefinition `json:"resources"`
	Used      map[string]int                `json:"used"`
	Claims    []ResourceClaim               `json:"claims"`
}
type Reservation struct {
	Root         string
	Claim        ResourceClaim
	releaseOwner func()
	mu           sync.Mutex
	unsafe       bool
}

func LoadResources(root string) (ResourceConfig, error) {
	var cfg ResourceConfig
	file, err := openStoreFile(filepath.Join(root, "resources.json"))
	if os.IsNotExist(err) {
		cfg.Resources = map[string]ResourceDefinition{}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cfg, fmt.Errorf("resources.json must contain one JSON object")
	}
	if cfg.Resources == nil {
		return cfg, fmt.Errorf("resources.json requires a resources object")
	}
	for key, r := range cfg.Resources {
		if strings.TrimSpace(key) == "" || r.Capacity < 1 {
			return cfg, fmt.Errorf("invalid resource %q: capacity must be positive", key)
		}
	}
	return cfg, nil
}
func validateResourceRequests(cfg ResourceConfig, requests []model.ResourceRequest) error {
	seen := map[string]bool{}
	for _, r := range requests {
		capacity, exists := cfg.Resources[r.Key]
		if !exists {
			return fmt.Errorf("unknown resource %q", r.Key)
		}
		if seen[r.Key] || r.Units < 1 || r.Units > capacity.Capacity {
			return fmt.Errorf("invalid units or duplicate request for resource %q", r.Key)
		}
		seen[r.Key] = true
	}
	return nil
}
func ValidateResources(root string, flow model.Workflow) error {
	cfg, err := LoadResources(root)
	if err != nil {
		return err
	}
	for _, node := range flow.Nodes {
		if err := validateResourceRequests(cfg, node.Resources); err != nil {
			return fmt.Errorf("node %s: %w", node.ID, err)
		}
	}
	return nil
}
func resourceOwnerLock(ctx context.Context, root, id string) (func(), error) {
	directory := filepath.Join(root, "resource-state", "owners")
	if err := EnsureDir(directory); err != nil {
		return nil, err
	}
	return AcquireWorkspaceLock(ctx, directory, id+".lock", false)
}
func resourceClaimPath(root, id string) string {
	return filepath.Join(root, "resource-state", "claims", id+".json")
}
func saveResourceClaim(root string, claim ResourceClaim) error {
	return writeCreationJSON(resourceClaimPath(root, claim.ID), claim)
}
func resourceEvent(root string, claim ResourceClaim, kind string) {
	_ = AppendEvent(filepath.Join(root, "runs", claim.RunID), map[string]any{"type": "resource." + kind, "claim": claim.ID, "nodeId": claim.NodeID, "attempt": claim.Attempt, "token": claim.Token, "requests": claim.Requests, "processPid": claim.ProcessPID, "reason": claim.Reason})
}

// Called under resources.lock. A lost owner is quarantined, never reclaimed by
// TTL or PID guessing: a child process may still be using the resource.
func resourceSnapshotLocked(ctx context.Context, root string) (ResourceSnapshot, error) {
	cfg, err := LoadResources(root)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	snapshot := ResourceSnapshot{Resources: cfg.Resources, Used: map[string]int{}, Claims: []ResourceClaim{}}
	paths, err := filepath.Glob(filepath.Join(root, "resource-state", "claims", "*.json"))
	if err != nil {
		return snapshot, err
	}
	for _, path := range paths {
		var claim ResourceClaim
		if err := readJSON(path, &claim); err != nil {
			return snapshot, err
		}
		seen := map[string]bool{}
		for _, request := range claim.Requests {
			if strings.TrimSpace(request.Key) == "" || request.Units < 1 || seen[request.Key] {
				return snapshot, fmt.Errorf("invalid persisted resource claim %s", path)
			}
			seen[request.Key] = true
		}
		if len(claim.Requests) == 0 || claim.Token == "" || claim.Attempt < 1 || !validRunID(claim.RunID) {
			return snapshot, fmt.Errorf("incomplete resource claim %s", path)
		}
		if !validClaimID(claim.ID) {
			return snapshot, fmt.Errorf("invalid resource claim %s", path)
		}
		if claim.Status == "active" {
			release, err := resourceOwnerLock(ctx, root, claim.ID)
			if err == nil {
				release()
				claim.Status = "orphaned"
				claim.Reason = "owner_lost"
				claim.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
				if err := saveResourceClaim(root, claim); err != nil {
					return snapshot, err
				}
				resourceEvent(root, claim, "orphaned")
			} else if !errors.Is(err, ErrWorkspaceBusy) {
				return snapshot, err
			}
		}
		switch claim.Status {
		case "active", "orphaned":
			for _, request := range claim.Requests {
				if snapshot.Used[request.Key] > int(^uint(0)>>1)-request.Units {
					return snapshot, fmt.Errorf("resource usage overflow")
				}
				snapshot.Used[request.Key] += request.Units
			}
		case "released":
		default:
			return snapshot, fmt.Errorf("invalid resource claim status %q", claim.Status)
		}
		snapshot.Claims = append(snapshot.Claims, claim)
	}
	return snapshot, nil
}
func ResourceStatus(ctx context.Context, root string) (ResourceSnapshot, error) {
	release, err := AcquireWorkspaceLock(ctx, root, "resources.lock", true)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	defer release()
	return resourceSnapshotLocked(ctx, root)
}
func resourceAvailable(snapshot ResourceSnapshot, requests []model.ResourceRequest) bool {
	for _, r := range requests {
		if snapshot.Used[r.Key] > snapshot.Resources[r.Key].Capacity || r.Units > snapshot.Resources[r.Key].Capacity-snapshot.Used[r.Key] {
			return false
		}
	}
	return true
}
func ResourcesReady(ctx context.Context, root string, requests []model.ResourceRequest) (bool, error) {
	if len(requests) == 0 {
		return true, nil
	}
	release, err := AcquireWorkspaceLock(ctx, root, "resources.lock", true)
	if err != nil {
		return false, err
	}
	defer release()
	snapshot, err := resourceSnapshotLocked(ctx, root)
	if err != nil {
		return false, err
	}
	if err := validateResourceRequests(ResourceConfig{Resources: snapshot.Resources}, requests); err != nil {
		return false, err
	}
	return resourceAvailable(snapshot, requests), nil
}

// One transaction reserves every requested resource or none. Keys never become
// filenames and have no meaning to the engine beyond identity and capacity.
func AcquireResources(ctx context.Context, root string, claim ResourceClaim) (*Reservation, error) {
	if len(claim.Requests) == 0 {
		return nil, nil
	}
	release, err := AcquireWorkspaceLock(ctx, root, "resources.lock", true)
	if err != nil {
		return nil, err
	}
	defer release()
	snapshot, err := resourceSnapshotLocked(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := validateResourceRequests(ResourceConfig{Resources: snapshot.Resources}, claim.Requests); err != nil {
		return nil, err
	}
	if !resourceAvailable(snapshot, claim.Requests) {
		return nil, ErrResourceUnavailable
	}
	claim.ID = "claim_" + NewToken()
	claim.OwnerPID = os.Getpid()
	claim.Status = "active"
	claim.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	claim.UpdatedAt = claim.CreatedAt
	owner, err := resourceOwnerLock(ctx, root, claim.ID)
	if err != nil {
		return nil, err
	}
	if err := saveResourceClaim(root, claim); err != nil {
		owner()
		return nil, err
	}
	resourceEvent(root, claim, "acquired")
	return &Reservation{Root: root, Claim: claim, releaseOwner: owner}, nil
}
func (r *Reservation) Started(pid int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Claim.ProcessPID = pid
	r.Claim.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	release, err := AcquireWorkspaceLock(context.Background(), r.Root, "resources.lock", true)
	if err != nil {
		r.unsafe = true
		return err
	}
	defer release()
	if err := saveResourceClaim(r.Root, r.Claim); err != nil {
		r.unsafe = true
		return err
	}
	resourceEvent(r.Root, r.Claim, "started")
	return nil
}
func (r *Reservation) Release() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.releaseOwner == nil {
		return nil
	}
	defer func() { r.releaseOwner(); r.releaseOwner = nil }()
	release, err := AcquireWorkspaceLock(context.Background(), r.Root, "resources.lock", true)
	if err != nil {
		return err
	}
	defer release()
	r.Claim.Status = "released"
	r.Claim.Reason = "attempt_exited"
	if r.unsafe {
		r.Claim.Status = "orphaned"
		r.Claim.Reason = "process_registration_failed"
	}
	r.Claim.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveResourceClaim(r.Root, r.Claim); err != nil {
		return err
	}
	resourceEvent(r.Root, r.Claim, r.Claim.Status)
	return nil
}
func validClaimID(id string) bool {
	if len(id) != 38 || !strings.HasPrefix(id, "claim_") {
		return false
	}
	_, err := hex.DecodeString(id[6:])
	return err == nil
}

// Explicit cleanup is restricted to an orphan with no live recorded process.
// confirmStopped acknowledges that its entire external execution scope, including
// descendants, has been stopped; it is not permission to kill arbitrary processes.
func ResolveResource(ctx context.Context, root, id, reason string, confirmStopped bool) error {
	if !validClaimID(id) || !confirmStopped || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("an orphan claim ID, --confirm-stopped and a cleanup reason are required")
	}
	release, err := AcquireWorkspaceLock(ctx, root, "resources.lock", true)
	if err != nil {
		return err
	}
	defer release()
	snapshot, err := resourceSnapshotLocked(ctx, root)
	if err != nil {
		return err
	}
	for _, claim := range snapshot.Claims {
		if claim.ID == id {
			if claim.Status != "orphaned" {
				return fmt.Errorf("only orphaned resource claims can be resolved")
			}
			if claim.ProcessPID > 0 && processAlive(claim.ProcessPID) {
				return fmt.Errorf("recorded process %d is still alive; stop and verify its execution scope first", claim.ProcessPID)
			}
			claim.Status = "released"
			claim.Reason = "explicit_cleanup: " + reason
			claim.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err := saveResourceClaim(root, claim); err != nil {
				return err
			}
			resourceEvent(root, claim, "resolved")
			return nil
		}
	}
	return fmt.Errorf("resource claim not found")
}

func ResourceRunReady(ctx context.Context, root, id string) (bool, error) {
	run, err := LoadRun(root, id)
	if err != nil {
		return false, err
	}
	for _, state := range run.State.Nodes {
		if state.Status == "waiting_resource" {
			ready, err := ResourcesReady(ctx, root, state.ResourceWait)
			if err != nil {
				return false, err
			}
			if ready {
				return true, nil
			}
		}
	}
	return false, nil
}
