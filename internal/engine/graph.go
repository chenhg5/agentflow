package engine

import (
	"fmt"
	"sort"

	"github.com/chenhg5/agentflow/internal/model"
)

// readiness decides what to do with a pending node from its dependencies and
// runIf policy. It returns (true, "") to run the node, (false, reason) to skip
// it, and (false, "") to keep waiting.
//
//	success (default)  run when every dependency succeeded
//	failure            run when an upstream node failed
//	always             run once every dependency has finished
func readiness(run *model.Run, node model.Node) (bool, string) {
	failed, skipped := false, false
	for _, dependencyID := range node.Needs {
		switch run.State.Nodes[dependencyID].Status {
		case "pending", "running", "waiting_human":
			return false, ""
		case "failed":
			failed = true
		case "skipped":
			skipped = true
		}
	}
	switch node.RunIf {
	case "always":
		return true, ""
	case "failure":
		if upstreamFailed(run, node.ID, map[string]bool{}) {
			return true, ""
		}
		return false, "no_upstream_failure"
	default:
		if failed {
			return false, "dependency_failed"
		}
		if skipped {
			return false, "dependency_skipped"
		}
		return true, ""
	}
}

func upstreamFailed(run *model.Run, nodeID string, seen map[string]bool) bool {
	for _, dependencyID := range nodeByID(run.Workflow, nodeID).Needs {
		if seen[dependencyID] {
			continue
		}
		seen[dependencyID] = true
		if run.State.Nodes[dependencyID].Status == "failed" || upstreamFailed(run, dependencyID, seen) {
			return true
		}
	}
	return false
}

// RetryPlan lists the nodes a retry resets: the given nodes, or every failed
// node when none are given, plus everything downstream of them.
func RetryPlan(run *model.Run, from []string) ([]string, error) {
	targets := from
	if len(targets) == 0 {
		for _, node := range run.Workflow.Nodes {
			if run.State.Nodes[node.ID].Status == "failed" {
				targets = append(targets, node.ID)
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("run %s has no failed nodes to retry", run.RunID)
		}
	}
	reset := map[string]bool{}
	for _, id := range targets {
		if _, ok := run.State.Nodes[id]; !ok {
			return nil, fmt.Errorf("node %s does not exist in run %s", id, run.RunID)
		}
		reset[id] = true
	}
	for changed := true; changed; {
		changed = false
		for _, node := range run.Workflow.Nodes {
			if reset[node.ID] {
				continue
			}
			for _, dependencyID := range node.Needs {
				if reset[dependencyID] {
					reset[node.ID] = true
					changed = true
					break
				}
			}
		}
	}
	ids := make([]string, 0, len(reset))
	for id := range reset {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func nodeByID(workflow model.Workflow, id string) model.Node {
	for _, node := range workflow.Nodes {
		if node.ID == id {
			return node
		}
	}
	return model.Node{ID: id}
}
