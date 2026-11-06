// Package coderun records runs whose steps are driven by a script instead of
// the graph engine: code orchestration. A code run is stored like any other
// run (runs/<id>/workflow.json and state.json), with workflow version "code"
// and nodes that appear as the script reaches them, so `run list`, `run get`,
// the Hub reporter and the Hub's pages all see it without knowing the
// difference. Every change happens under an OS lock on the run directory, so
// steps a script starts in parallel do not overwrite each other.
package coderun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

// Version marks a workflow recorded by a script.
const Version = "code"

// Executor names the candidate of a code step that records its own result.
const Executor = "code"

// IsCode reports whether run was recorded by a script.
func IsCode(run *model.Run) bool { return run.Workflow.Version == Version }

// ErrNotCode is returned for a run the engine drives.
var ErrNotCode = errors.New("not a code run")

// ErrFinished is returned when a step is recorded on a finished run.
var ErrFinished = errors.New("run already finished")

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Start creates a running code run named name.
func Start(root, name string, inputs map[string]string) (*model.Run, error) {
	if name == "" {
		return nil, errors.New("a code run needs a name")
	}
	if inputs == nil {
		inputs = map[string]string{}
	}
	at := now()
	id := fmt.Sprintf("run_%s_%s", time.Now().UTC().Format("20060102150405"), store.NewToken()[:16])
	dir := filepath.Join(root, "runs", id)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		return nil, err
	}
	run := &model.Run{RunID: id, RunDir: dir, Workflow: model.Workflow{Version: Version, Name: name, Nodes: []model.Node{}},
		State: model.RunState{RunID: id, WorkflowName: name, Status: "running", Inputs: inputs, Nodes: map[string]*model.NodeState{},
			Visits: map[string]int{}, CreatedAt: at, StartedAt: at}}
	if err := saveWorkflow(run); err != nil {
		return nil, err
	}
	return run, store.SaveState(run)
}

// Update loads the code run, applies change under the run's lock and saves it.
func Update(root, runID string, change func(run *model.Run) error) (*model.Run, error) {
	dir := filepath.Join(root, "runs", runID)
	release, err := store.AcquireWorkspaceLock(context.Background(), dir, "code.lock", true)
	if err != nil {
		return nil, err
	}
	defer release()
	run, err := store.LoadRun(root, runID)
	if err != nil {
		return nil, err
	}
	if !IsCode(run) {
		return nil, ErrNotCode
	}
	nodes := len(run.Workflow.Nodes)
	edges := len(run.Workflow.Edges)
	if err := change(run); err != nil {
		return nil, err
	}
	if len(run.Workflow.Nodes) != nodes || len(run.Workflow.Edges) != edges {
		if err := saveWorkflow(run); err != nil {
			return nil, err
		}
	}
	return run, store.SaveState(run)
}

// Begin marks step running. A step seen for the first time joins the graph.
// Its edges come from the steps that finished since a step last began (in
// RunState.ActiveNodes): steps a script runs one after another form a chain,
// steps it starts together share their predecessors, and the step after them
// joins them. A step seen again is a new visit.
func Begin(run *model.Run, stepID, executor string) error {
	if finished(run.State.Status) {
		return ErrFinished
	}
	if stepID == "" || stepID == "$end" {
		return fmt.Errorf("invalid step id %q", stepID)
	}
	if _, known := run.State.Nodes[stepID]; !known {
		run.Workflow.Nodes = append(run.Workflow.Nodes, model.Node{ID: stepID, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: Executor}}}})
		if run.Workflow.Entry == "" {
			run.Workflow.Entry = stepID
		}
	}
	for _, source := range sources(run, stepID) {
		if source != stepID && !hasEdge(run.Workflow, source, stepID) {
			run.Workflow.Edges = append(run.Workflow.Edges, model.Transition{From: source, To: stepID})
		}
	}
	run.State.ActiveNodes = nil
	state := run.State.Nodes[stepID]
	attempts := 0
	if state != nil {
		attempts = state.Attempts
	}
	if run.State.Visits == nil {
		run.State.Visits = map[string]int{}
	}
	run.State.Visits[stepID]++
	run.State.Nodes[stepID] = &model.NodeState{Status: "running", SelectedExecutor: executor, Attempts: attempts + 1, StartedAt: now(), Artifacts: map[string]string{}}
	run.State.ActiveNode = stepID
	return nil
}

// sources are the steps a new step follows: those finished since a step last
// began, or else the predecessors of the step that began last (the new step
// started alongside it).
func sources(run *model.Run, stepID string) []string {
	if len(run.State.ActiveNodes) > 0 {
		return run.State.ActiveNodes
	}
	latest, latestAt := "", ""
	for id, state := range run.State.Nodes {
		if id != stepID && state.StartedAt > latestAt {
			latest, latestAt = id, state.StartedAt
		}
	}
	var out []string
	for _, edge := range run.Workflow.Edges {
		if edge.To == latest {
			out = append(out, edge.From)
		}
	}
	return out
}

// Finish records the outcome of a running step. result may be nil; artifacts
// are paths relative to the run directory (stdoutLog, stderrLog, result).
func Finish(run *model.Run, stepID, status string, result map[string]any, message string, artifacts map[string]string) error {
	state := run.State.Nodes[stepID]
	if state == nil || state.Status != "running" {
		return fmt.Errorf("step %s is not running", stepID)
	}
	if status != "succeeded" && status != "failed" && status != "skipped" {
		return fmt.Errorf("a step ends succeeded, failed or skipped, not %q", status)
	}
	at := time.Now().UTC()
	state.Status, state.FinishedAt, state.Error = status, at.Format(time.RFC3339Nano), ""
	if status == "failed" {
		state.Error = message
	} else if status == "skipped" {
		state.Reason = message
	}
	if started, err := time.Parse(time.RFC3339Nano, state.StartedAt); err == nil {
		state.DurationMs = at.Sub(started).Milliseconds()
	}
	if result != nil {
		state.Result = result
	}
	if state.Artifacts == nil && len(artifacts) > 0 {
		state.Artifacts = map[string]string{} // an empty map does not survive the JSON round trip
	}
	for name, path := range artifacts {
		state.Artifacts[name] = path
		if info, err := os.Stat(filepath.Join(run.RunDir, filepath.FromSlash(path))); err == nil {
			switch name {
			case "stdoutLog":
				state.StdoutBytes = info.Size()
			case "stderrLog":
				state.StderrBytes = info.Size()
			case "result":
				state.OutputBytes = info.Size()
			}
		}
	}
	run.State.History = append(run.State.History, model.NodeVisit{NodeID: stepID, Visit: run.State.Visits[stepID], State: *state})
	if !contains(run.State.ActiveNodes, stepID) {
		run.State.ActiveNodes = append(run.State.ActiveNodes, stepID)
	}
	return nil
}

// End finishes the run. Without a status it ends failed when a step failed
// (or is still running) and succeeded otherwise. A step still running is
// marked failed: its process is gone with the script.
func End(run *model.Run, status, message string) error {
	if finished(run.State.Status) {
		return ErrFinished
	}
	failedStep := ""
	for _, node := range run.Workflow.Nodes {
		state := run.State.Nodes[node.ID]
		if state != nil && state.Status == "running" {
			_ = Finish(run, node.ID, "failed", nil, "the script ended while this step was running", nil)
		}
		if state != nil && state.Status == "failed" && failedStep == "" {
			failedStep = node.ID
		}
	}
	if status == "" {
		status = "succeeded"
		if failedStep != "" {
			status = "failed"
		}
	}
	if status != "succeeded" && status != "failed" && status != "cancelled" {
		return fmt.Errorf("a run ends succeeded, failed or cancelled, not %q", status)
	}
	if status == "succeeded" {
		for _, source := range sources(run, "$end") {
			if !hasEdge(run.Workflow, source, "$end") {
				run.Workflow.Edges = append(run.Workflow.Edges, model.Transition{From: source, To: "$end"})
			}
		}
	}
	at := time.Now().UTC()
	run.State.Status, run.State.FinishedAt, run.State.ActiveNode, run.State.ActiveNodes = status, at.Format(time.RFC3339Nano), "", nil
	if started, err := time.Parse(time.RFC3339Nano, run.State.StartedAt); err == nil {
		run.State.DurationMs = at.Sub(started).Milliseconds()
	}
	if message != "" {
		run.State.Outcome = message
	}
	return nil
}

func finished(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func hasEdge(workflow model.Workflow, from, to string) bool {
	for _, edge := range workflow.Edges {
		if edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}

func saveWorkflow(run *model.Run) error {
	return store.WriteRunWorkflow(run.RunDir, run.Workflow)
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
