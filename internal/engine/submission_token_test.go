package engine

import (
	"github.com/chenhg5/agentflow/internal/model"
	"testing"
)

func TestSubmissionRejectsStaleInvocationAndTerminalRun(t *testing.T) {
	for _, test := range []struct{ name, status, token string }{
		{"stale visit", "waiting_input", "old-token"},
		{"cancelled", "cancelled", "new-token"},
		{"failed", "failed", "new-token"},
		{"finished", "succeeded", "new-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := &model.NodeState{Status: "waiting_input", LeaseToken: "new-token"}
			run := &model.Run{State: model.RunState{Status: test.status, Nodes: map[string]*model.NodeState{"gate": node}}}
			reason, err := applySubmission(run, model.Control{Kind: ControlSubmit, NodeID: "gate", Token: test.token, Output: map[string]any{"score": 8}})
			if err != nil || reason == "" || node.Status != "waiting_input" || node.Result != nil {
				t.Fatalf("stale submission mutated state: %q %v %#v", reason, err, node)
			}
		})
	}
}
