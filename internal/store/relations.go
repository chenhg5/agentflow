package store

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chenhg5/agentflow/internal/model"
)

func requestForRun(run *model.Run) CreateRequest {
	return CreateRequest{Workflow: run.Workflow, Inputs: run.State.Inputs, ParentRunID: run.State.ParentRunID, ParentNodeID: run.State.ParentNodeID, Correlation: run.State.Correlation}
}

func validRunID(id string) bool {
	return strings.HasPrefix(id, "run_") && filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}

func validateParent(root string, request CreateRequest) error {
	if request.ParentRunID == "" {
		if request.ParentNodeID != "" {
			return fmt.Errorf("parentNodeId requires parentRunId")
		}
		return nil
	}
	if !validRunID(request.ParentRunID) {
		return fmt.Errorf("parent must be a run ID in the same workspace")
	}
	parent, err := LoadRun(root, request.ParentRunID)
	if err != nil {
		return fmt.Errorf("parent run must exist in this workspace: %w", err)
	}
	if request.ParentNodeID != "" {
		found := false
		for _, node := range parent.Workflow.Nodes {
			if node.ID == request.ParentNodeID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("parent node %q does not exist", request.ParentNodeID)
		}
	}

	// Relations are immutable and point only to existing runs, so creation cannot
	// introduce a cycle. Also reject a corrupted pre-existing ancestry chain.
	seen := map[string]bool{}
	for parent != nil {
		if seen[parent.RunID] {
			return fmt.Errorf("parent ancestry contains a cycle")
		}
		seen[parent.RunID] = true
		if parent.State.ParentRunID == "" {
			break
		}
		if !validRunID(parent.State.ParentRunID) {
			return fmt.Errorf("invalid parent ancestry")
		}
		parent, err = LoadRun(root, parent.State.ParentRunID)
		if err != nil {
			return err
		}
	}
	return nil
}

// Children returns direct children. No state or cancellation is propagated.
func Children(root, parent string) ([]*model.Run, map[string]int, error) {
	ids, err := RunIDs(root)
	if err != nil {
		return nil, nil, err
	}
	children := []*model.Run{}
	counts := map[string]int{}
	for _, id := range ids {
		run, err := LoadRun(root, id)
		if err != nil {
			return nil, nil, err
		}
		if run.State.ParentRunID == parent {
			children = append(children, run)
			counts[run.State.Status]++
		}
	}
	return children, counts, nil
}
